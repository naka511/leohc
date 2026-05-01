package handler

import (
	"log"
	"strings"
	"time"
)

const autoRefreshSweepInterval = time.Minute

// StartTokenAutoRefreshLoop starts the background Leonardo token refresh sweep.
func (s *Server) StartTokenAutoRefreshLoop() {
	if s == nil || s.TokenMgr == nil {
		return
	}

	s.autoRefreshMu.Lock()
	if s.autoRefreshLoopStarted {
		s.autoRefreshMu.Unlock()
		return
	}
	s.autoRefreshLoopStarted = true
	if s.autoRefreshRun == nil {
		s.autoRefreshRun = make(map[string]time.Time)
	}
	if s.autoRefreshBusy == nil {
		s.autoRefreshBusy = make(map[string]bool)
	}
	s.autoRefreshMu.Unlock()

	go func() {
		log.Printf("[token] auto-refresh loop started")
		s.runTokenAutoRefreshSweep()

		ticker := time.NewTicker(autoRefreshSweepInterval)
		defer ticker.Stop()

		for range ticker.C {
			s.runTokenAutoRefreshSweep()
		}
	}()
}

func (s *Server) runTokenAutoRefreshSweep() {
	if s == nil || s.TokenMgr == nil || s.LeonardoClient == nil {
		return
	}

	s.autoRefreshMu.Lock()
	if s.autoRefreshSweepRunning {
		s.autoRefreshMu.Unlock()
		return
	}
	s.autoRefreshSweepRunning = true
	s.autoRefreshMu.Unlock()

	defer func() {
		s.autoRefreshMu.Lock()
		s.autoRefreshSweepRunning = false
		s.autoRefreshMu.Unlock()
	}()

	tokens := s.TokenMgr.ListFull()
	if len(tokens) == 0 {
		return
	}

	interval := s.tokenAutoRefreshInterval()
	now := time.Now()

	for _, item := range tokens {
		tokenID := strings.TrimSpace(toString(item["id"]))
		if tokenID == "" {
			continue
		}
		if strings.ToLower(strings.TrimSpace(toString(item["platform"]))) != "leonardo" {
			continue
		}
		if !toBool(item["auto_refresh"]) {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(toString(item["status"])))
		if status == "disabled" || status == "exhausted" {
			continue
		}

		if !s.shouldRunTokenAutoRefresh(tokenID, now, interval) {
			continue
		}
		s.refreshLeonardoTokenByID(tokenID)
	}
}

func (s *Server) triggerTokenAutoRefresh(tokenID string) {
	tokenID = strings.TrimSpace(tokenID)
	if tokenID == "" || s == nil || s.LeonardoClient == nil {
		return
	}
	go s.refreshLeonardoTokenByID(tokenID)
}

func (s *Server) refreshLeonardoTokenByID(tokenID string) {
	if s == nil || s.TokenMgr == nil || s.LeonardoClient == nil {
		return
	}

	tokenID = strings.TrimSpace(tokenID)
	if tokenID == "" {
		return
	}

	now := time.Now()
	if !s.beginTokenAutoRefresh(tokenID, now) {
		return
	}
	defer s.finishTokenAutoRefresh(tokenID, now)

	info := s.TokenMgr.GetByID(tokenID)
	if info == nil {
		return
	}

	status := strings.ToLower(strings.TrimSpace(toString(info["status"])))
	if status == "disabled" || status == "exhausted" {
		return
	}

	rawToken := strings.TrimSpace(toString(info["value"]))
	if rawToken == "" {
		return
	}

	session, credits, err := s.validateLeonardoToken(tokenID, rawToken)
	if err != nil {
		if shouldMarkTokenInvalidOnRefreshError(err) {
			if setErr := s.TokenMgr.SetStatus(tokenID, "invalid"); setErr != nil {
				log.Printf("[token] auto-refresh failed to mark token invalid for %s: %v", tokenID, setErr)
			}
		}
		log.Printf("[token] auto-refresh failed for %s: %v", tokenID, err)
		return
	}
	if session == nil {
		return
	}

	if err := s.TokenMgr.SetStatus(tokenID, "active"); err != nil {
		log.Printf("[token] auto-refresh failed to set active for %s: %v", tokenID, err)
	}
	if err := s.TokenMgr.UpdateAccountInfo(tokenID, session.HasuraUserID, session.Email); err != nil {
		log.Printf("[token] auto-refresh failed to update account info for %s: %v", tokenID, err)
	}
	if err := s.TokenMgr.UpdateExpiry(tokenID, float64(session.JWTExpiry.Unix())); err != nil {
		log.Printf("[token] auto-refresh failed to update expiry for %s: %v", tokenID, err)
	}
	if credits != nil {
		totalCredits := float64(credits.SubscriptionTokens + credits.PaidTokens + credits.RolloverTokens)
		if err := s.TokenMgr.UpdateCredits(tokenID, float64(credits.TotalTokens), totalCredits); err != nil {
			log.Printf("[token] auto-refresh failed to update credits for %s: %v", tokenID, err)
		}
	}

	log.Printf("[token] auto-refresh completed for %s (%s)", tokenID, session.Email)
}

func (s *Server) tokenAutoRefreshInterval() time.Duration {
	hours := 15
	if s != nil && s.Config != nil {
		hours = s.Config.GetInt("refresh_interval_hours", 15)
	}
	if hours < 1 {
		hours = 1
	}
	if hours > 24 {
		hours = 24
	}
	return time.Duration(hours) * time.Hour
}

func (s *Server) shouldRunTokenAutoRefresh(tokenID string, now time.Time, interval time.Duration) bool {
	s.autoRefreshMu.Lock()
	defer s.autoRefreshMu.Unlock()

	if s.autoRefreshRun == nil {
		s.autoRefreshRun = make(map[string]time.Time)
	}
	if last, ok := s.autoRefreshRun[tokenID]; ok && !last.IsZero() && now.Sub(last) < interval {
		return false
	}
	return true
}

func (s *Server) beginTokenAutoRefresh(tokenID string, now time.Time) bool {
	s.autoRefreshMu.Lock()
	defer s.autoRefreshMu.Unlock()

	if s.autoRefreshBusy == nil {
		s.autoRefreshBusy = make(map[string]bool)
	}
	if s.autoRefreshRun == nil {
		s.autoRefreshRun = make(map[string]time.Time)
	}
	if s.autoRefreshBusy[tokenID] {
		return false
	}
	s.autoRefreshBusy[tokenID] = true
	s.autoRefreshRun[tokenID] = now
	return true
}

func (s *Server) finishTokenAutoRefresh(tokenID string, ts time.Time) {
	s.autoRefreshMu.Lock()
	defer s.autoRefreshMu.Unlock()

	if s.autoRefreshBusy != nil {
		delete(s.autoRefreshBusy, tokenID)
	}
	if s.autoRefreshRun == nil {
		s.autoRefreshRun = make(map[string]time.Time)
	}
	s.autoRefreshRun[tokenID] = ts
}

func shouldMarkTokenInvalidOnRefreshError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "get-session returned 401") ||
		strings.Contains(msg, "get-session returned 403") ||
		strings.Contains(msg, "graphql returned 401") ||
		strings.Contains(msg, "graphql returned 403") ||
		strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "forbidden")
}

func toBool(v interface{}) bool {
	switch val := v.(type) {
	case bool:
		return val
	case float64:
		return val != 0
	case int:
		return val != 0
	case string:
		s := strings.ToLower(strings.TrimSpace(val))
		return s == "true" || s == "1" || s == "yes" || s == "on"
	default:
		return false
	}
}

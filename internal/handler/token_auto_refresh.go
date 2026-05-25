package handler

import (
	"log"
	"strings"
	"sync"
	"time"
)

const autoRefreshRetryCooldown = time.Minute

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
		for {
			s.runTokenAutoRefreshSweep()
			time.Sleep(s.tokenAutoRefreshSweepInterval())
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

	threshold := s.tokenAutoRefreshThreshold()
	maxConcurrency := s.tokenAutoRefreshMaxConcurrency()
	now := time.Now()
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrency)

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

		if !s.shouldRunTokenAutoRefresh(item, tokenID, now, threshold) {
			continue
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			s.refreshLeonardoTokenByID(id)
		}(tokenID)
	}
	wg.Wait()
}

func (s *Server) triggerTokenAutoRefresh(tokenID string) {
	tokenID = strings.TrimSpace(tokenID)
	if tokenID == "" || s == nil || s.LeonardoClient == nil {
		return
	}
	go s.refreshLeonardoTokenByID(tokenID)
}

func (s *Server) triggerTokenAutoRefreshBatch(tokenIDs []string) {
	if len(tokenIDs) == 0 || s == nil || s.LeonardoClient == nil {
		return
	}

	seen := make(map[string]struct{}, len(tokenIDs))
	ids := make([]string, 0, len(tokenIDs))
	for _, id := range tokenIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return
	}

	maxConcurrency := s.tokenImportRefreshConcurrency()
	go func() {
		log.Printf("[token] queued background refresh for %d imported token(s), concurrency=%d", len(ids), maxConcurrency)
		var wg sync.WaitGroup
		sem := make(chan struct{}, maxConcurrency)
		for _, id := range ids {
			sem <- struct{}{}
			wg.Add(1)
			go func(tokenID string) {
				defer wg.Done()
				defer func() { <-sem }()
				s.refreshLeonardoTokenByID(tokenID)
			}(id)
		}
		wg.Wait()
		log.Printf("[token] completed background refresh queue for %d imported token(s)", len(ids))
	}()
}

func (s *Server) tokenImportRefreshConcurrency() int {
	maxConcurrency := s.tokenAutoRefreshMaxConcurrency()
	if maxConcurrency > 5 {
		maxConcurrency = 5
	}
	if maxConcurrency < 1 {
		maxConcurrency = 1
	}
	return maxConcurrency
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
	if status == "disabled" || status == "exhausted" || status == "abnormal" {
		return
	}

	rawToken := strings.TrimSpace(toString(info["value"]))
	if rawToken == "" {
		return
	}

	session, credits, err := s.validateLeonardoToken(tokenID, rawToken)
	if err != nil {
		if shouldMarkTokenAbnormalOnRefreshError(err) {
			s.markTokenAbnormalAndDisableAutoRefresh(tokenID, err.Error())
		} else if shouldMarkTokenInvalidOnRefreshError(err) {
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

func (s *Server) tokenAutoRefreshThreshold() time.Duration {
	minutes := 10
	if s != nil && s.Config != nil {
		minutes = s.Config.GetInt("refresh_interval_minutes", 0)
	}
	if minutes < 1 {
		minutes = 10
	}
	if minutes > 1440 {
		minutes = 1440
	}
	return time.Duration(minutes) * time.Minute
}

func (s *Server) tokenAutoRefreshSweepInterval() time.Duration {
	minutes := 1
	if s != nil && s.Config != nil {
		minutes = s.Config.GetInt("auto_refresh_sweep_interval_minutes", 0)
	}
	if minutes < 1 {
		minutes = 1
	}
	if minutes > 1440 {
		minutes = 1440
	}
	return time.Duration(minutes) * time.Minute
}

func (s *Server) tokenAutoRefreshMaxConcurrency() int {
	maxConcurrency := 5
	if s != nil && s.Config != nil {
		maxConcurrency = s.Config.GetInt("auto_refresh_max_concurrency", 0)
	}
	if maxConcurrency < 1 {
		maxConcurrency = 5
	}
	if maxConcurrency > 50 {
		maxConcurrency = 50
	}
	return maxConcurrency
}

func (s *Server) shouldRunTokenAutoRefresh(item map[string]interface{}, tokenID string, now time.Time, threshold time.Duration) bool {
	if expiresAt := toFloat64(item["expires_at"]); expiresAt > 0 {
		expiry := time.Unix(int64(expiresAt), 0)
		if expiry.After(now.Add(threshold)) {
			return false
		}
	}

	s.autoRefreshMu.Lock()
	defer s.autoRefreshMu.Unlock()

	if s.autoRefreshRun == nil {
		s.autoRefreshRun = make(map[string]time.Time)
	}
	if last, ok := s.autoRefreshRun[tokenID]; ok && !last.IsZero() && now.Sub(last) < autoRefreshRetryCooldown {
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

func shouldMarkTokenAbnormalOnRefreshError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no jwt found in session response") ||
		strings.Contains(msg, "body keys: [session user]")
}

func (s *Server) markTokenAbnormalAndDisableAutoRefresh(tokenID, reason string) {
	if s == nil || s.TokenMgr == nil {
		return
	}
	if err := s.TokenMgr.SetStatus(tokenID, "abnormal"); err != nil {
		log.Printf("[token] failed to mark token abnormal for %s: %v", tokenID, err)
	}
	if err := s.TokenMgr.SetAutoRefresh(tokenID, false); err != nil {
		log.Printf("[token] failed to disable auto-refresh for abnormal token %s: %v", tokenID, err)
	}
	log.Printf("[token] marked token abnormal and disabled auto-refresh for %s: %s", tokenID, reason)
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

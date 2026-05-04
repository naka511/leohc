package handler

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	pathpkg "path"
	"strconv"
	"strings"
	"time"

	"leo-go/internal/provider/leonardo"
	"leo-go/internal/reqlog"
)

// HandleAuthLogin handles POST /api/v1/auth/login.
func (s *Server) HandleAuthLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"detail": "invalid body"})
		return
	}
	expectedUser := s.Config.GetString("admin_username", "admin")
	expectedPass := s.Config.GetString("admin_password", "admin")
	if body.Username != expectedUser || body.Password != expectedPass {
		writeJSON(w, 401, map[string]string{"detail": "invalid credentials"})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "admin_session",
		Value:    s.Config.GetString("admin_session_secret", "leo-go-session"),
		Path:     "/",
		MaxAge:   86400,
		HttpOnly: true,
	})
	writeJSON(w, 200, map[string]interface{}{"ok": true, "message": "login successful"})
}

// HandleAuthMe handles GET /api/v1/auth/me.
func (s *Server) HandleAuthMe(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}
	writeJSON(w, 200, map[string]interface{}{
		"ok":       true,
		"username": s.Config.GetString("admin_username", "admin"),
	})
}

func (s *Server) requireAdmin(r *http.Request) error {
	cookie, err := r.Cookie("admin_session")
	if err != nil || cookie.Value != s.Config.GetString("admin_session_secret", "leo-go-session") {
		return fmt.Errorf("unauthorized")
	}
	return nil
}

// HandleTokenList handles GET /api/v1/tokens — paginated with summary.
func (s *Server) HandleTokenList(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}
	allTokens := s.TokenMgr.List()
	if allTokens == nil {
		allTokens = []map[string]interface{}{}
	}

	// Parse filters
	statusFilter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))
	creditsFilter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("credits")))

	// Filter
	filtered := allTokens
	if statusFilter != "" {
		var tmp []map[string]interface{}
		for _, t := range filtered {
			ts := strings.ToLower(fmt.Sprintf("%v", t["status"]))
			if ts == statusFilter {
				tmp = append(tmp, t)
			}
		}
		filtered = tmp
	}
	_ = creditsFilter // Credits filter can be added later

	// Stats from all tokens
	stats := s.TokenMgr.Stats()

	// Pagination
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}

	total := len(filtered)
	totalPages := int(math.Ceil(float64(total) / float64(pageSize)))
	if totalPages < 1 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * pageSize
	end := start + pageSize
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}
	pageTokens := filtered[start:end]
	if pageTokens == nil {
		pageTokens = []map[string]interface{}{}
	}

	writeJSON(w, 200, map[string]interface{}{
		"tokens": pageTokens,
		"summary": map[string]interface{}{
			"total":    stats["total"],
			"active":   stats["active"],
			"filtered": total,
		},
		"pagination": map[string]interface{}{
			"page":        page,
			"page_size":   pageSize,
			"total":       total,
			"total_pages": totalPages,
		},
	})
}

// HandleTokenAdd handles POST /api/v1/tokens.
func (s *Server) HandleTokenAdd(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}
	var body struct {
		Token        string `json:"token"`
		Platform     string `json:"platform"`
		TokenType    string `json:"token_type"`
		AccountName  string `json:"account_name"`
		AccountEmail string `json:"account_email"`
		Source       string `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"detail": "invalid body"})
		return
	}
	if strings.TrimSpace(body.Token) == "" {
		writeJSON(w, 400, map[string]string{"detail": "token is required"})
		return
	}
	platform := strings.ToLower(strings.TrimSpace(body.Platform))
	if platform == "" {
		platform = "leonardo"
	}
	if platform != "leonardo" {
		writeJSON(w, 400, map[string]string{"detail": "unsupported platform; only leonardo is available"})
		return
	}
	tokenType := body.TokenType
	if tokenType == "" {
		tokenType = "session_token"
	}

	// For Leonardo tokens, save first then try to validate in background
	if platform == "leonardo" {
		if body.Source == "" {
			body.Source = "manual"
		}
		info, duplicate, addErr := s.TokenMgr.Add(body.Token, platform, tokenType, body.AccountName, body.AccountEmail, body.Source)
		if addErr != nil {
			writeJSON(w, 500, map[string]string{"detail": addErr.Error()})
			return
		}
		// Validation must not block import, especially in Docker where outbound
		// connectivity may differ from the host machine.
		leoInfo := map[string]interface{}{
			"status": "queued_for_validation",
			"hint":   "Token saved. Validation will continue in the background.",
		}
		if s.LeonardoClient != nil {
			if tokenID, _ := info["id"].(string); tokenID != "" {
				go s.validateLeonardoTokenAsync(tokenID, body.Token)
			}
		} else {
			leoInfo["status"] = "saved_without_validation"
			leoInfo["hint"] = "Token saved without Leonardo validation. Refresh later when available."
		}
		if false && s.LeonardoClient != nil {
			session, credits, err := s.LeonardoClient.ValidateToken(body.Token)
			if err == nil && session != nil {
				tokenID, _ := info["id"].(string)
				if tokenID != "" {
					s.TokenMgr.UpdateAccountInfo(tokenID, session.HasuraUserID, session.Email)
					if credits != nil {
						s.TokenMgr.UpdateCredits(tokenID, float64(credits.TotalTokens), float64(credits.SubscriptionTokens+credits.PaidTokens+credits.RolloverTokens))
						s.TokenMgr.UpdateExpiry(tokenID, float64(session.JWTExpiry.Unix()))
					}
				}
				leoInfo = map[string]interface{}{
					"status":     "validated",
					"email":      session.Email,
					"user_id":    session.HasuraUserID,
					"plan":       credits.Plan,
					"credits":    credits.TotalTokens,
					"jwt_expiry": session.JWTExpiry.Format(time.RFC3339),
				}
			} else {
				leoInfo["error"] = err.Error()
				leoInfo["hint"] = "Token已保存，请稍后点击「刷新Token」获取账号信息"
			}
		}
		writeJSON(w, 200, map[string]interface{}{
			"ok": true, "token": info, "duplicate": duplicate,
			"added": boolToInt(!duplicate), "duplicates": boolToInt(duplicate), "failed": 0,
			"added_count": boolToInt(!duplicate), "duplicate_count": boolToInt(duplicate), "failed_count": 0,
			"leonardo": leoInfo,
		})
		return
	}

	info, duplicate, err := s.TokenMgr.Add(body.Token, platform, tokenType, body.AccountName, body.AccountEmail, body.Source)
	if err != nil {
		writeJSON(w, 500, map[string]string{"detail": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]interface{}{
		"ok": true, "token": info, "duplicate": duplicate,
		"added": boolToInt(!duplicate), "duplicates": boolToInt(duplicate), "failed": 0,
		"added_count": boolToInt(!duplicate), "duplicate_count": boolToInt(duplicate), "failed_count": 0,
	})
}

// HandleLeonardoValidate handles POST /api/v1/leonardo/validate.
func (s *Server) HandleLeonardoValidate(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}
	if s.LeonardoClient == nil {
		writeJSON(w, 500, map[string]string{"detail": "Leonardo client not initialized"})
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"detail": "invalid body"})
		return
	}
	session, credits, err := s.validateLeonardoToken(body.Token, body.Token)
	if err != nil {
		writeJSON(w, 400, map[string]interface{}{
			"ok": false, "detail": err.Error(),
		})
		return
	}
	result := map[string]interface{}{
		"ok": true,
		"session": map[string]interface{}{
			"email":          session.Email,
			"cognito_sub":    session.CognitoSub,
			"hasura_user_id": session.HasuraUserID,
			"jwt_valid":      session.IsJWTValid(),
			"jwt_remaining":  session.GetJWTRemainingSeconds(),
			"jwt_expiry":     session.JWTExpiry.Format(time.RFC3339),
		},
	}
	if credits != nil {
		result["credits"] = map[string]interface{}{
			"paid_tokens":         credits.PaidTokens,
			"subscription_tokens": credits.SubscriptionTokens,
			"rollover_tokens":     credits.RolloverTokens,
			"total_tokens":        credits.TotalTokens,
			"plan":                credits.Plan,
			"renewal_date":        credits.TokenRenewalDate,
		}
	}
	writeJSON(w, 200, result)
}

// HandleLeonardoCredits handles GET /api/v1/leonardo/credits?token_id=xxx.
func (s *Server) HandleLeonardoCredits(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}
	if s.LeonardoClient == nil {
		writeJSON(w, 500, map[string]string{"detail": "Leonardo client not initialized"})
		return
	}
	tokenID := r.URL.Query().Get("token_id")
	if tokenID == "" {
		writeJSON(w, 400, map[string]string{"detail": "token_id required"})
		return
	}
	tokenInfo := s.TokenMgr.GetByID(tokenID)
	if tokenInfo == nil {
		writeJSON(w, 404, map[string]string{"detail": "token not found"})
		return
	}
	tokenValue, _ := tokenInfo["value"].(string)
	session, credits, err := s.validateLeonardoToken(tokenID, tokenValue)
	if err != nil {
		writeJSON(w, 400, map[string]interface{}{
			"ok": false, "detail": err.Error(),
		})
		return
	}
	writeJSON(w, 200, map[string]interface{}{
		"ok":       true,
		"token_id": tokenID,
		"email":    session.Email,
		"plan":     credits.Plan,
		"credits": map[string]interface{}{
			"paid_tokens":         credits.PaidTokens,
			"subscription_tokens": credits.SubscriptionTokens,
			"rollover_tokens":     credits.RolloverTokens,
			"total_tokens":        credits.TotalTokens,
			"renewal_date":        credits.TokenRenewalDate,
		},
		"jwt_remaining_seconds": session.GetJWTRemainingSeconds(),
	})
}

// HandleTokenBatchAdd handles POST /api/v1/tokens/batch.
func (s *Server) HandleTokenBatchAdd(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}
	var body struct {
		Tokens []struct {
			Token        string `json:"token"`
			Platform     string `json:"platform"`
			AccountName  string `json:"account_name"`
			AccountEmail string `json:"account_email"`
			Source       string `json:"source"`
		} `json:"tokens"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"detail": "invalid body"})
		return
	}
	added, duplicates, failed := 0, 0, 0
	for _, t := range body.Tokens {
		platform := strings.ToLower(strings.TrimSpace(t.Platform))
		if platform == "" {
			platform = "leonardo"
		}
		if platform != "leonardo" {
			failed++
			continue
		}
		tokenType := "session_token"
		_, dup, err := s.TokenMgr.Add(t.Token, platform, tokenType, t.AccountName, t.AccountEmail, t.Source)
		if err != nil {
			failed++
			continue
		}
		if dup {
			duplicates++
		} else {
			added++
		}
	}
	writeJSON(w, 200, map[string]interface{}{
		"ok":    true,
		"added": added, "duplicates": duplicates, "failed": failed,
		"added_count": added, "duplicate_count": duplicates, "failed_count": failed,
	})
}

func (s *Server) validateLeonardoTokenAsync(tokenID, rawToken string) {
	if s.LeonardoClient == nil || tokenID == "" || strings.TrimSpace(rawToken) == "" {
		return
	}

	session, credits, err := s.validateLeonardoToken(tokenID, rawToken)
	if err != nil {
		log.Printf("[token] leonardo validation skipped for %s: %v", tokenID, err)
		return
	}
	if session == nil {
		return
	}

	if err := s.TokenMgr.UpdateAccountInfo(tokenID, session.HasuraUserID, session.Email); err != nil {
		log.Printf("[token] failed to update Leonardo account info for %s: %v", tokenID, err)
	}
	if credits != nil {
		totalCredits := float64(credits.SubscriptionTokens + credits.PaidTokens + credits.RolloverTokens)
		if err := s.TokenMgr.UpdateCredits(tokenID, float64(credits.TotalTokens), totalCredits); err != nil {
			log.Printf("[token] failed to update Leonardo credits for %s: %v", tokenID, err)
		}
	}
	if err := s.TokenMgr.UpdateExpiry(tokenID, float64(session.JWTExpiry.Unix())); err != nil {
		log.Printf("[token] failed to update Leonardo expiry for %s: %v", tokenID, err)
	}

	log.Printf("[token] leonardo validation completed for %s (%s)", tokenID, session.Email)
}

func (s *Server) validateLeonardoToken(tokenID, rawToken string) (*leonardo.TokenSession, *leonardo.Credits, error) {
	if s.LeonardoClient == nil {
		return nil, nil, fmt.Errorf("Leonardo client not initialized")
	}
	session := s.getOrCreateLeonardoSession(tokenID, rawToken)
	if session == nil {
		return nil, nil, fmt.Errorf("token value is required")
	}
	if s.shouldForceJWTRefreshOnValidation() {
		if err := s.LeonardoClient.RefreshSession(session); err != nil {
			return session, nil, fmt.Errorf("token validation failed: %w", err)
		}
	}
	credits, err := s.LeonardoClient.QueryCredits(session)
	if err != nil {
		return session, nil, fmt.Errorf("token validation failed: %w", err)
	}
	return session, credits, nil
}

func (s *Server) shouldForceJWTRefreshOnValidation() bool {
	if s == nil || s.Config == nil {
		return false
	}
	return s.Config.GetInt("jwt_refresh_margin_minutes", 5) <= 0
}

func (s *Server) getOrCreateLeonardoSession(tokenID, rawToken string) *leonardo.TokenSession {
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		return nil
	}

	key := strings.TrimSpace(tokenID)
	if key == "" {
		key = "raw:" + rawToken
	} else {
		key = "id:" + key
	}

	s.leoSessionMu.Lock()
	defer s.leoSessionMu.Unlock()

	if s.leoSessions == nil {
		s.leoSessions = make(map[string]*leonardo.TokenSession)
	}

	if session, ok := s.leoSessions[key]; ok {
		if strings.TrimSpace(session.FullCookie) != rawToken {
			session.FullCookie = rawToken
			session.JWT = ""
			session.JWTExpiry = time.Time{}
			session.CognitoSub = ""
			session.HasuraUserID = ""
			session.Email = ""
			session.Plan = ""
		}
		return session
	}

	session := &leonardo.TokenSession{FullCookie: rawToken}
	s.leoSessions[key] = session
	return session
}

// HandleTokenDelete handles DELETE /api/v1/tokens/{id}.
func (s *Server) HandleTokenDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}
	tokenID := extractPathParam(r.URL.Path, "/api/v1/tokens/")
	if tokenID == "" {
		writeJSON(w, 400, map[string]string{"detail": "token id required"})
		return
	}
	if err := s.TokenMgr.Remove(tokenID); err != nil {
		writeJSON(w, 404, map[string]string{"detail": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

// HandleTokenStatus handles PUT /api/v1/tokens/{id}/status?status=xxx.
func (s *Server) HandleTokenStatus(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}
	path := r.URL.Path
	// Extract token ID from /api/v1/tokens/{id}/status
	trimmed := strings.TrimPrefix(path, "/api/v1/tokens/")
	parts := strings.SplitN(trimmed, "/", 2)
	tokenID := parts[0]

	status := r.URL.Query().Get("status")
	if status == "" {
		var body struct {
			Status string `json:"status"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		status = body.Status
	}
	if status == "" {
		writeJSON(w, 400, map[string]string{"detail": "status required"})
		return
	}
	if err := s.TokenMgr.SetStatus(tokenID, status); err != nil {
		writeJSON(w, 404, map[string]string{"detail": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

// HandleDeleteBatch handles POST /api/v1/tokens/delete-batch.
func (s *Server) HandleDeleteBatch(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"detail": "invalid body"})
		return
	}
	deletedIDs := []string{}
	missing := 0
	for _, id := range body.IDs {
		if s.TokenMgr.Remove(id) == nil {
			deletedIDs = append(deletedIDs, id)
		} else {
			missing++
		}
	}
	writeJSON(w, 200, map[string]interface{}{
		"ok":            true,
		"deleted":       len(deletedIDs),
		"deleted_count": len(deletedIDs),
		"deleted_ids":   deletedIDs,
		"missing_count": missing,
	})
}

// HandleTokenStatusBatch handles POST /api/v1/tokens/status-batch.
func (s *Server) HandleTokenStatusBatch(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}
	var body struct {
		IDs    []string `json:"ids"`
		Status string   `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"detail": "invalid body"})
		return
	}
	status := strings.ToLower(strings.TrimSpace(body.Status))
	if status != "active" && status != "disabled" {
		writeJSON(w, 400, map[string]string{"detail": "status must be active or disabled"})
		return
	}

	updated, missing, failed := 0, 0, 0
	for _, id := range body.IDs {
		id = strings.TrimSpace(id)
		if id == "" {
			failed++
			continue
		}
		if s.TokenMgr.GetByID(id) == nil {
			missing++
			continue
		}
		if err := s.TokenMgr.SetStatus(id, status); err != nil {
			failed++
			continue
		}
		updated++
	}
	writeJSON(w, 200, map[string]interface{}{
		"ok":            true,
		"status":        status,
		"updated_count": updated,
		"missing_count": missing,
		"failed_count":  failed,
	})
}

// HandleTokenAutoRefreshBatch handles POST /api/v1/tokens/auto-refresh-batch.
func (s *Server) HandleTokenAutoRefreshBatch(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}
	var body struct {
		IDs     []string `json:"ids"`
		Enabled bool     `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"detail": "invalid body"})
		return
	}

	updated, missing, failed, skipped := 0, 0, 0, 0
	for _, id := range body.IDs {
		id = strings.TrimSpace(id)
		if id == "" {
			failed++
			continue
		}
		info := s.TokenMgr.GetByID(id)
		if info == nil {
			missing++
			continue
		}
		platform := strings.ToLower(strings.TrimSpace(toString(info["platform"])))
		if platform != "" && platform != "leonardo" {
			skipped++
			continue
		}
		if err := s.TokenMgr.SetAutoRefresh(id, body.Enabled); err != nil {
			failed++
			continue
		}
		updated++
		if body.Enabled {
			s.triggerTokenAutoRefresh(id)
		}
	}
	writeJSON(w, 200, map[string]interface{}{
		"ok":            true,
		"enabled":       body.Enabled,
		"updated_count": updated,
		"missing_count": missing,
		"failed_count":  failed,
		"skipped_count": skipped,
	})
}

// HandleTokenRefreshBatch handles POST /api/v1/tokens/refresh-batch.
func (s *Server) HandleTokenRefreshBatch(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"detail": "invalid body"})
		return
	}

	refreshed, skipped, missing, failed := 0, 0, 0, 0
	items := make([]map[string]interface{}, 0, len(body.IDs))
	for _, id := range body.IDs {
		id = strings.TrimSpace(id)
		item := map[string]interface{}{"id": id}
		info := s.TokenMgr.GetByID(id)
		if info == nil {
			missing++
			item["status"] = "missing"
			items = append(items, item)
			continue
		}
		platform := strings.ToLower(strings.TrimSpace(toString(info["platform"])))
		tokenValue := strings.TrimSpace(toString(info["value"]))
		if platform != "leonardo" || tokenValue == "" || s.LeonardoClient == nil {
			skipped++
			item["status"] = "skipped"
			items = append(items, item)
			continue
		}
		session, credits, err := s.validateLeonardoToken(id, tokenValue)
		if err != nil {
			failed++
			item["status"] = "failed"
			item["error"] = err.Error()
			items = append(items, item)
			continue
		}
		s.TokenMgr.SetStatus(id, "active")
		if credits != nil {
			s.TokenMgr.UpdateCredits(id, float64(credits.TotalTokens), float64(credits.SubscriptionTokens+credits.PaidTokens+credits.RolloverTokens))
			item["credits"] = credits.TotalTokens
		}
		s.TokenMgr.UpdateExpiry(id, float64(session.JWTExpiry.Unix()))
		s.TokenMgr.UpdateAccountInfo(id, session.HasuraUserID, session.Email)
		refreshed++
		item["status"] = "success"
		item["email"] = session.Email
		item["jwt_remaining"] = session.GetJWTRemainingSeconds()
		items = append(items, item)
	}
	writeJSON(w, 200, map[string]interface{}{
		"ok":              true,
		"refreshed_count": refreshed,
		"success_count":   refreshed,
		"skipped_count":   skipped,
		"missing_count":   missing,
		"failed_count":    failed,
		"items":           items,
	})
}

// HandleCheckInvalidTokensBatch handles POST /api/v1/tokens/check-invalid-batch.
func (s *Server) HandleCheckInvalidTokensBatch(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"detail": "invalid body"})
		return
	}

	valid, invalid, changed, abnormal, abnormalChanged, skipped, failed, disabledAutoRefresh := 0, 0, 0, 0, 0, 0, 0, 0
	items := make([]map[string]interface{}, 0, len(body.IDs))
	for _, id := range body.IDs {
		id = strings.TrimSpace(id)
		item := map[string]interface{}{"id": id}
		info := s.TokenMgr.GetByID(id)
		if info == nil {
			skipped++
			item["status"] = "missing"
			items = append(items, item)
			continue
		}
		platform := strings.ToLower(strings.TrimSpace(toString(info["platform"])))
		tokenValue := strings.TrimSpace(toString(info["value"]))
		if platform != "leonardo" || tokenValue == "" || s.LeonardoClient == nil {
			skipped++
			item["status"] = "skipped"
			items = append(items, item)
			continue
		}
		session, credits, err := s.validateLeonardoToken(id, tokenValue)
		if err != nil {
			errMsg := err.Error()
			item["error"] = errMsg
			switch {
			case isInvalidLeonardoTokenError(err):
				invalid++
				if strings.ToLower(strings.TrimSpace(toString(info["status"]))) != "invalid" {
					changed++
				}
				s.TokenMgr.SetStatus(id, "invalid")
				item["status"] = "invalid"
			case isAbnormalLeonardoTokenError(err):
				abnormal++
				if strings.ToLower(strings.TrimSpace(toString(info["status"]))) != "abnormal" {
					abnormalChanged++
				}
				s.TokenMgr.SetStatus(id, "abnormal")
				if s.TokenMgr.SetAutoRefresh(id, false) == nil {
					disabledAutoRefresh++
				}
				item["status"] = "abnormal"
			default:
				failed++
				item["status"] = "failed"
			}
			items = append(items, item)
			continue
		}
		s.TokenMgr.SetStatus(id, "active")
		if credits != nil {
			s.TokenMgr.UpdateCredits(id, float64(credits.TotalTokens), float64(credits.SubscriptionTokens+credits.PaidTokens+credits.RolloverTokens))
			item["credits"] = credits.TotalTokens
		}
		s.TokenMgr.UpdateExpiry(id, float64(session.JWTExpiry.Unix()))
		s.TokenMgr.UpdateAccountInfo(id, session.HasuraUserID, session.Email)
		valid++
		item["status"] = "valid"
		item["email"] = session.Email
		items = append(items, item)
	}
	writeJSON(w, 200, map[string]interface{}{
		"ok":                          true,
		"valid_count":                 valid,
		"invalid_count":               invalid,
		"changed_count":               changed,
		"abnormal_count":              abnormal,
		"abnormal_changed_count":      abnormalChanged,
		"skipped_count":               skipped,
		"failed_count":                failed,
		"disabled_auto_refresh_count": disabledAutoRefresh,
		"items":                       items,
	})
}

// HandleTokenExport handles POST /api/v1/tokens/export.
func (s *Server) HandleTokenExport(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}
	tokens := s.TokenMgr.ListFull()
	writeJSON(w, 200, map[string]interface{}{"ok": true, "tokens": tokens, "count": len(tokens)})
}

// HandleAdminConfig handles GET/PUT /api/v1/config.
func (s *Server) HandleAdminConfig(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}
	if r.Method == "GET" {
		all := s.Config.GetAll()
		// Mask sensitive values
		if _, ok := all["admin_password"]; ok {
			all["admin_password"] = "***"
		}
		writeJSON(w, 200, all)
		return
	}
	// PUT/POST
	var updates map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		writeJSON(w, 400, map[string]string{"detail": "invalid body"})
		return
	}
	if rawPwd, ok := updates["admin_password"].(string); ok && strings.TrimSpace(rawPwd) == "***" {
		updates["admin_password"] = s.Config.GetString("admin_password", "admin")
	}
	for k, v := range updates {
		s.Config.Set(k, v)
	}
	if err := s.Config.Save(); err != nil {
		writeJSON(w, 500, map[string]string{"detail": "failed to save config"})
		return
	}
	s.reloadRuntimeClients()
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

// HandleProxyTest handles POST /api/v1/proxy/test using the current form values.
func (s *Server) HandleProxyTest(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}

	var body struct {
		UseProxy         bool   `json:"use_proxy"`
		Proxy            string `json:"proxy"`
		ResourceUseProxy bool   `json:"resource_use_proxy"`
		ResourceProxy    string `json:"resource_proxy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"detail": "invalid body"})
		return
	}

	basicProxy := strings.TrimSpace(body.Proxy)
	resourceProxy := strings.TrimSpace(body.ResourceProxy)
	if body.UseProxy && basicProxy == "" {
		writeJSON(w, 400, map[string]string{"detail": "proxy is required when basic proxy is enabled"})
		return
	}
	if body.ResourceUseProxy && resourceProxy == "" {
		writeJSON(w, 400, map[string]string{"detail": "resource_proxy is required when resource proxy is enabled"})
		return
	}

	result := map[string]interface{}{
		"connectivity": map[string]interface{}{
			"basic":    runHTTPProxyConnectivityTest(body.UseProxy, basicProxy, leonardo.SessionURL),
			"resource": runHTTPProxyConnectivityTest(body.ResourceUseProxy, resourceProxy, "https://app.leonardo.ai/"),
		},
		"business": map[string]interface{}{
			"basic": s.runLeonardoProxyBusinessTest(body.UseProxy, basicProxy),
		},
	}
	writeJSON(w, 200, result)
}

// HandleHealth handles GET /health.
func (s *Server) HandleHealth(w http.ResponseWriter, r *http.Request) {
	stats := s.TokenMgr.Stats()
	writeJSON(w, 200, map[string]interface{}{
		"status":  "ok",
		"version": "1.0.0-go",
		"tokens":  stats,
		"uptime":  time.Since(startTime).String(),
	})
}

// HandleAdminStats handles GET /api/v1/stats.
func (s *Server) HandleAdminStats(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}
	stats := s.TokenMgr.Stats()
	stats["uptime"] = time.Since(startTime).String()
	writeJSON(w, 200, stats)
}

// HandleLogs handles GET/DELETE /api/v1/logs.
func (s *Server) HandleLogs(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}
	if r.Method == "DELETE" {
		cleared := 0
		if s.ReqLog != nil {
			cleared = s.ReqLog.Clear()
		}
		writeJSON(w, 200, map[string]interface{}{"ok": true, "cleared": cleared})
		return
	}

	if s.ReqLog == nil {
		writeJSON(w, 200, map[string]interface{}{
			"logs": []interface{}{}, "total": 0, "page": 1, "total_pages": 1,
		})
		return
	}

	page := 1
	pageSize := 50
	if p := r.URL.Query().Get("page"); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 {
			page = v
		}
	}
	if ps := r.URL.Query().Get("page_size"); ps != "" {
		if v, err := strconv.Atoi(ps); err == nil && v > 0 {
			pageSize = v
		}
	}
	failedOnly := r.URL.Query().Get("failed_only") == "true" || r.URL.Query().Get("failed_only") == "1"

	entries, curPage, totalPages := s.ReqLog.List(page, pageSize, failedOnly)

	// Convert to interface slice
	var logs []interface{}
	for _, e := range entries {
		logs = append(logs, e)
	}
	if logs == nil {
		logs = []interface{}{}
	}

	writeJSON(w, 200, map[string]interface{}{
		"logs":        logs,
		"total":       len(logs),
		"page":        curPage,
		"total_pages": totalPages,
	})
}

// HandleLogsRunning handles GET /api/v1/logs/running.
func (s *Server) HandleLogsRunning(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}

	var items []interface{}
	if s.ReqLog != nil {
		for _, e := range s.ReqLog.Running() {
			items = append(items, e)
		}
	}
	if items == nil {
		items = []interface{}{}
	}

	writeJSON(w, 200, map[string]interface{}{
		"items": items,
		"total": len(items),
	})
}

// HandleLogsStats handles GET /api/v1/logs/stats.
func (s *Server) HandleLogsStats(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}

	rangeStr := r.URL.Query().Get("range")
	if rangeStr == "" {
		rangeStr = "today"
	}

	if s.ReqLog == nil {
		writeJSON(w, 200, map[string]interface{}{
			"generated_images": 0, "generated_videos": 0,
			"total_requests": 0, "failed_requests": 0,
			"end_ts": float64(time.Now().Unix()),
		})
		return
	}

	writeJSON(w, 200, s.ReqLog.Stats(rangeStr))
}

// HandleTokenRefresh handles POST /api/v1/tokens/{id}/refresh.
func (s *Server) HandleTokenRefresh(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}
	// Extract token ID from path: /api/v1/tokens/{id}/refresh
	path := r.URL.Path
	trimmed := strings.TrimPrefix(path, "/api/v1/tokens/")
	parts := strings.SplitN(trimmed, "/", 2)
	tokenID := parts[0]

	tokenInfo := s.TokenMgr.GetByID(tokenID)
	if tokenInfo == nil {
		writeJSON(w, 404, map[string]string{"detail": "token not found"})
		return
	}
	platform, _ := tokenInfo["platform"].(string)
	tokenValue, _ := tokenInfo["value"].(string)

	if platform == "leonardo" && s.LeonardoClient != nil {
		session, credits, err := s.validateLeonardoToken(tokenID, tokenValue)
		if err != nil {
			writeJSON(w, statusForLeonardoRefreshError(err), map[string]interface{}{
				"ok": false, "detail": "Token刷新失败: " + err.Error(),
			})
			return
		}
		// Update token info in the pool
		s.TokenMgr.SetStatus(tokenID, "active")
		if credits != nil {
			s.TokenMgr.UpdateCredits(tokenID, float64(credits.TotalTokens), float64(credits.SubscriptionTokens+credits.PaidTokens+credits.RolloverTokens))
		}
		s.TokenMgr.UpdateExpiry(tokenID, float64(session.JWTExpiry.Unix()))
		s.TokenMgr.UpdateAccountInfo(tokenID, session.HasuraUserID, session.Email)

		result := map[string]interface{}{
			"ok":            true,
			"email":         session.Email,
			"jwt_remaining": session.GetJWTRemainingSeconds(),
		}
		if credits != nil {
			result["credits_available"] = credits.TotalTokens
			result["plan"] = credits.Plan
		}
		writeJSON(w, 200, result)
		return
	}

	// For non-Leonardo tokens
	writeJSON(w, 200, map[string]interface{}{"ok": true, "message": "token refresh not supported for this platform"})
}

// HandleTokenAutoRefresh handles PUT /api/v1/tokens/{id}/auto-refresh?enabled=true|false.
func (s *Server) HandleTokenAutoRefresh(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}
	path := r.URL.Path
	trimmed := strings.TrimPrefix(path, "/api/v1/tokens/")
	parts := strings.SplitN(trimmed, "/", 2)
	tokenID := parts[0]

	enabled := r.URL.Query().Get("enabled") == "true"

	if err := s.TokenMgr.SetAutoRefresh(tokenID, enabled); err != nil {
		writeJSON(w, 404, map[string]string{"detail": err.Error()})
		return
	}
	if enabled {
		s.triggerTokenAutoRefresh(tokenID)
	}
	writeJSON(w, 200, map[string]interface{}{
		"ok":           true,
		"auto_refresh": enabled,
	})
}

// HandleStubPost returns ok for unimplemented POST endpoints.
func (s *Server) HandleStubPost(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true, "message": "not yet implemented in Go backend"})
}

type cookieImportInput struct {
	Name   string `json:"name"`
	Cookie string `json:"cookie"`
}

type cookieImportJob struct {
	ID                    string                   `json:"id"`
	Status                string                   `json:"status"`
	Total                 int                      `json:"total"`
	SuccessCount          int                      `json:"success_count"`
	ErrorCount            int                      `json:"error_count"`
	DuplicateCount        int                      `json:"duplicate_count"`
	RequestDuplicateCount int                      `json:"request_duplicate_count"`
	ListDuplicateCount    int                      `json:"list_duplicate_count"`
	OverwrittenCount      int                      `json:"overwritten_count"`
	Items                 []map[string]interface{} `json:"items"`
	BackgroundRefresh     map[string]interface{}   `json:"background_refresh"`
	Timing                map[string]interface{}   `json:"timing,omitempty"`
	StartedAt             time.Time                `json:"-"`
}

// HandleImportCookieBatch handles POST /api/v1/refresh-profiles/import-cookie-batch.
func (s *Server) HandleImportCookieBatch(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}

	var body struct {
		Items []cookieImportInput `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"detail": "invalid body"})
		return
	}

	inputs := make([]cookieImportInput, 0, len(body.Items))
	for _, item := range body.Items {
		cookie := normalizeImportedCookie(item.Cookie)
		if cookie == "" {
			continue
		}
		inputs = append(inputs, cookieImportInput{
			Name:   strings.TrimSpace(item.Name),
			Cookie: cookie,
		})
	}
	if len(inputs) == 0 {
		writeJSON(w, 400, map[string]string{"detail": "no valid cookie items found"})
		return
	}

	job := newCookieImportJob(inputs)
	s.saveCookieImportJob(job)
	go s.runCookieImportJob(job.ID, inputs)

	writeJSON(w, 200, s.snapshotCookieImportJob(job.ID))
}

// HandleImportCookieJob handles GET /api/v1/refresh-profiles/import-cookie-jobs/{id}.
func (s *Server) HandleImportCookieJob(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}

	jobID := extractPathParam(r.URL.Path, "/api/v1/refresh-profiles/import-cookie-jobs/")
	if jobID == "" {
		writeJSON(w, 400, map[string]string{"detail": "job id required"})
		return
	}

	payload := s.snapshotCookieImportJob(jobID)
	if payload == nil {
		writeJSON(w, 404, map[string]string{"detail": "job not found"})
		return
	}
	writeJSON(w, 200, payload)
}

func normalizeImportedCookie(raw string) string {
	value := strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(value), "cookie:") {
		value = strings.TrimSpace(value[7:])
	}
	return value
}

func newCookieImportJob(inputs []cookieImportInput) *cookieImportJob {
	jobID := fmt.Sprintf("cookie-%d", time.Now().UnixNano())
	items := make([]map[string]interface{}, 0, len(inputs))
	for idx, input := range inputs {
		title := strings.TrimSpace(input.Name)
		if title == "" {
			title = fmt.Sprintf("Cookie #%d", idx+1)
		}
		items = append(items, map[string]interface{}{
			"index":        idx,
			"status":       "queued",
			"profile_name": title,
			"detail":       "等待导入",
		})
	}

	return &cookieImportJob{
		ID:        jobID,
		Status:    "queued",
		Total:     len(inputs),
		Items:     items,
		StartedAt: time.Now(),
		BackgroundRefresh: map[string]interface{}{
			"job_id":          jobID,
			"total_count":     len(inputs),
			"completed_count": 0,
			"queued_count":    len(inputs),
			"running_count":   0,
			"completed":       false,
		},
	}
}

func (s *Server) saveCookieImportJob(job *cookieImportJob) {
	s.cookieImportMu.Lock()
	defer s.cookieImportMu.Unlock()
	if s.cookieImportJobs == nil {
		s.cookieImportJobs = make(map[string]*cookieImportJob)
	}
	s.cookieImportJobs[job.ID] = job
}

func (s *Server) snapshotCookieImportJob(jobID string) map[string]interface{} {
	s.cookieImportMu.Lock()
	defer s.cookieImportMu.Unlock()

	job := s.cookieImportJobs[jobID]
	if job == nil {
		return nil
	}

	items := make([]map[string]interface{}, 0, len(job.Items))
	for _, item := range job.Items {
		cloned := make(map[string]interface{}, len(item))
		for k, v := range item {
			cloned[k] = v
		}
		items = append(items, cloned)
	}

	background := make(map[string]interface{}, len(job.BackgroundRefresh))
	for k, v := range job.BackgroundRefresh {
		background[k] = v
	}

	payload := map[string]interface{}{
		"status":                  job.Status,
		"total":                   job.Total,
		"success_count":           job.SuccessCount,
		"error_count":             job.ErrorCount,
		"duplicate_count":         job.DuplicateCount,
		"request_duplicate_count": job.RequestDuplicateCount,
		"list_duplicate_count":    job.ListDuplicateCount,
		"overwritten_count":       job.OverwrittenCount,
		"items":                   items,
		"background_refresh":      background,
	}
	if len(job.Timing) > 0 {
		timing := make(map[string]interface{}, len(job.Timing))
		for k, v := range job.Timing {
			timing[k] = v
		}
		payload["timing"] = timing
	}
	return payload
}

func (s *Server) runCookieImportJob(jobID string, inputs []cookieImportInput) {
	seen := make(map[string]struct{}, len(inputs))

	for idx, input := range inputs {
		s.cookieImportMu.Lock()
		job := s.cookieImportJobs[jobID]
		if job == nil {
			s.cookieImportMu.Unlock()
			return
		}
		job.Status = "running"
		job.Items[idx]["status"] = "running"
		job.Items[idx]["detail"] = "正在导入并刷新"
		job.BackgroundRefresh["running_count"] = 1
		job.BackgroundRefresh["queued_count"] = maxInt(job.Total-idx-1, 0)
		s.cookieImportMu.Unlock()

		startedAt := time.Now()
		status := "ok"
		detail := "导入成功"
		tokenID := ""
		tokenAccountName := strings.TrimSpace(input.Name)
		tokenAccountEmail := ""

		if _, ok := seen[input.Cookie]; ok {
			status = "skipped"
			detail = "本次导入内重复，已跳过"
			s.cookieImportMu.Lock()
			job := s.cookieImportJobs[jobID]
			if job != nil {
				job.RequestDuplicateCount++
				job.DuplicateCount++
			}
			s.cookieImportMu.Unlock()
		} else {
			seen[input.Cookie] = struct{}{}
			info, duplicate, err := s.TokenMgr.Add(input.Cookie, "leonardo", "session_token", tokenAccountName, "", "cookie_import")
			if err != nil {
				status = "failed"
				detail = "导入失败: " + err.Error()
				s.cookieImportMu.Lock()
				job := s.cookieImportJobs[jobID]
				if job != nil {
					job.ErrorCount++
				}
				s.cookieImportMu.Unlock()
			} else {
				tokenID, _ = info["id"].(string)
				if duplicate {
					status = "skipped"
					detail = "Cookie 已存在于列表，已跳过"
					s.cookieImportMu.Lock()
					job := s.cookieImportJobs[jobID]
					if job != nil {
						job.ListDuplicateCount++
						job.DuplicateCount++
					}
					s.cookieImportMu.Unlock()
				} else if s.LeonardoClient == nil {
					detail = "导入成功，当前未启用 Leonardo 刷新"
					s.cookieImportMu.Lock()
					job := s.cookieImportJobs[jobID]
					if job != nil {
						job.SuccessCount++
					}
					s.cookieImportMu.Unlock()
				} else {
					session, credits, err := s.validateLeonardoToken(tokenID, input.Cookie)
					if err != nil {
						status = "failed"
						detail = "已导入，但刷新失败: " + err.Error()
						if strings.Contains(strings.ToLower(err.Error()), "invalid") ||
							strings.Contains(strings.ToLower(err.Error()), "expired") {
							_ = s.TokenMgr.SetStatus(tokenID, "invalid")
						} else {
							_ = s.TokenMgr.SetStatus(tokenID, "error")
						}
						s.cookieImportMu.Lock()
						job := s.cookieImportJobs[jobID]
						if job != nil {
							job.ErrorCount++
						}
						s.cookieImportMu.Unlock()
					} else {
						_ = s.TokenMgr.SetStatus(tokenID, "active")
						_ = s.TokenMgr.UpdateExpiry(tokenID, float64(session.JWTExpiry.Unix()))
						_ = s.TokenMgr.UpdateAccountInfo(tokenID, session.HasuraUserID, session.Email)
						tokenAccountEmail = strings.TrimSpace(session.Email)
						if tokenAccountName == "" {
							tokenAccountName = tokenAccountEmail
						}
						if credits != nil {
							totalCredits := float64(credits.SubscriptionTokens + credits.PaidTokens + credits.RolloverTokens)
							_ = s.TokenMgr.UpdateCredits(tokenID, float64(credits.TotalTokens), totalCredits)
							detail = fmt.Sprintf("导入并刷新成功，剩余积分 %d", credits.TotalTokens)
						} else {
							detail = "导入并刷新成功"
						}
						s.cookieImportMu.Lock()
						job := s.cookieImportJobs[jobID]
						if job != nil {
							job.SuccessCount++
						}
						s.cookieImportMu.Unlock()
					}
				}
			}
		}

		elapsedMs := float64(time.Since(startedAt).Milliseconds())

		s.cookieImportMu.Lock()
		job = s.cookieImportJobs[jobID]
		if job != nil {
			job.Items[idx]["status"] = status
			job.Items[idx]["detail"] = detail
			job.Items[idx]["refresh_call_ms"] = elapsedMs
			if tokenID != "" {
				job.Items[idx]["token_id"] = tokenID
				job.Items[idx]["profile_id"] = tokenID
			}
			if tokenAccountName != "" {
				job.Items[idx]["token_account_name"] = tokenAccountName
				job.Items[idx]["profile_name"] = tokenAccountName
			}
			if tokenAccountEmail != "" {
				job.Items[idx]["token_account_email"] = tokenAccountEmail
			}

			completedCount := idx + 1
			job.BackgroundRefresh["completed_count"] = completedCount
			job.BackgroundRefresh["queued_count"] = maxInt(job.Total-completedCount, 0)
			job.BackgroundRefresh["running_count"] = 0
			job.BackgroundRefresh["completed"] = completedCount >= job.Total

			switch {
			case completedCount < job.Total:
				job.Status = "running"
			case job.ErrorCount > 0 && job.SuccessCount > 0:
				job.Status = "partial"
			case job.ErrorCount > 0:
				job.Status = "failed"
			default:
				job.Status = "ok"
			}
			job.Timing = map[string]interface{}{
				"total_ms": float64(time.Since(job.StartedAt).Milliseconds()),
			}
		}
		s.cookieImportMu.Unlock()
	}
}

var startTime = time.Now()

const (
	maxRemoteImageBytes     = 20 << 20
	maxRemoteVideoBytes     = 100 << 20
	remoteImageFetchTimeout = 120 * time.Second
	initImageLookupTimeout  = 180 * time.Second
	remoteFetchMaxAttempts  = 2
)

func extractPathParam(path, prefix string) string {
	trimmed := strings.TrimPrefix(path, prefix)
	// Remove any trailing segments
	if idx := strings.Index(trimmed, "/"); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	return strings.TrimSpace(trimmed)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ──────────────────────────────────────────────────────────
// Leonardo Video Generation Handlers
// ──────────────────────────────────────────────────────────

// HandleLeonardoGenerate handles POST /api/v1/leonardo/generate.
func (s *Server) HandleLeonardoGenerate(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}
	if s.LeonardoClient == nil {
		writeJSON(w, 500, map[string]string{"detail": "Leonardo client not initialized"})
		return
	}

	var body struct {
		TokenID       string `json:"token_id"`
		Prompt        string `json:"prompt"`
		Model         string `json:"model"`
		Mode          string `json:"mode"`
		Duration      int    `json:"duration"`
		Width         int    `json:"width"`
		Height        int    `json:"height"`
		Public        *bool  `json:"public,omitempty"` // default true
		ImageGuidance []struct {
			ID       string `json:"id"`
			URL      string `json:"url"`
			Type     string `json:"type"`
			Strength string `json:"strength"`
		} `json:"image_guidance,omitempty"`
		StartFrame []struct {
			ID   string `json:"id"`
			URL  string `json:"url"`
			Type string `json:"type"`
		} `json:"start_frame,omitempty"`
		EndFrame []struct {
			ID   string `json:"id"`
			URL  string `json:"url"`
			Type string `json:"type"`
		} `json:"end_frame,omitempty"`
		VideoReference []struct {
			URL      string  `json:"url"`
			ID       string  `json:"id"`
			Type     string  `json:"type"`
			Duration float64 `json:"duration"`
		} `json:"video_reference,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"detail": "invalid request body"})
		return
	}
	if body.Prompt == "" {
		writeJSON(w, 400, map[string]string{"detail": "prompt is required"})
		return
	}

	// Get session from token pool
	session, usedTokenID := s.getLeonardoSessionForModel(body.TokenID, body.Model)
	if session == nil {
		writeJSON(w, 404, map[string]string{"detail": "Leonardo token not found or no Leonardo tokens available"})
		return
	}

	uploadCache := make(map[string]string)

	// Build image refs (multi-image reference)
	var imageRefs []leonardo.ImageRef
	for _, ig := range body.ImageGuidance {
		refType := strings.TrimSpace(ig.Type)
		if refType == "" || (strings.TrimSpace(ig.ID) == "" && strings.TrimSpace(ig.URL) != "") {
			refType = "UPLOADED"
		}
		imageID, err := s.resolveLeonardoImageID(session, ig.ID, ig.URL, uploadCache)
		if err != nil {
			writeJSON(w, 400, map[string]string{"detail": fmt.Sprintf("invalid image_guidance entry: %v", err)})
			return
		}
		imageRefs = append(imageRefs, leonardo.ImageRef{
			ID:       imageID,
			Type:     refType,
			Strength: ig.Strength,
		})
	}

	// Build start/end frame refs
	var startFrames []leonardo.FrameRef
	for _, sf := range body.StartFrame {
		refType := strings.TrimSpace(sf.Type)
		if refType == "" || (strings.TrimSpace(sf.ID) == "" && strings.TrimSpace(sf.URL) != "") {
			refType = "UPLOADED"
		}
		imageID, err := s.resolveLeonardoImageID(session, sf.ID, sf.URL, uploadCache)
		if err != nil {
			writeJSON(w, 400, map[string]string{"detail": fmt.Sprintf("invalid start_frame entry: %v", err)})
			return
		}
		startFrames = append(startFrames, leonardo.FrameRef{
			ID:   imageID,
			Type: refType,
		})
	}
	var endFrames []leonardo.FrameRef
	for _, ef := range body.EndFrame {
		refType := strings.TrimSpace(ef.Type)
		if refType == "" || (strings.TrimSpace(ef.ID) == "" && strings.TrimSpace(ef.URL) != "") {
			refType = "UPLOADED"
		}
		imageID, err := s.resolveLeonardoImageID(session, ef.ID, ef.URL, uploadCache)
		if err != nil {
			writeJSON(w, 400, map[string]string{"detail": fmt.Sprintf("invalid end_frame entry: %v", err)})
			return
		}
		endFrames = append(endFrames, leonardo.FrameRef{
			ID:   imageID,
			Type: refType,
		})
	}

	// Build video refs
	videoUploadCache := make(map[string]string)
	var videoRefs []leonardo.VideoRef
	for _, vr := range body.VideoReference {
		videoRef, err := s.resolveLeonardoVideoRef(session, vr.ID, vr.URL, vr.Duration, videoUploadCache)
		if err != nil {
			writeJSON(w, 400, map[string]string{"detail": fmt.Sprintf("invalid video_reference entry: %v", err)})
			return
		}
		refType := strings.TrimSpace(vr.Type)
		if refType == "" || (strings.TrimSpace(vr.ID) == "" && strings.TrimSpace(vr.URL) != "") {
			refType = "UPLOADED"
		}
		videoRef.Type = refType
		videoRefs = append(videoRefs, videoRef)
	}

	// Default public to true (like Leonardo web client)
	isPublic := true
	if body.Public != nil {
		isPublic = *body.Public
	}

	genReq := &leonardo.GenerateRequest{
		Model:  body.Model,
		Public: isPublic,
		Params: leonardo.GenerateParams{
			Prompt:         body.Prompt,
			Mode:           body.Mode,
			Duration:       body.Duration,
			Width:          body.Width,
			Height:         body.Height,
			MotionHasAudio: true,
			ImageRefs:      imageRefs,
			StartFrame:     startFrames,
			EndFrame:       endFrames,
			VideoRefs:      videoRefs,
		},
	}

	startTime := time.Now()
	result, err := s.LeonardoClient.Generate(session, genReq)
	elapsedSec := time.Since(startTime).Seconds()
	accountName, accountEmail := s.resolveReqLogAccount(usedTokenID, session)

	if err != nil {
		// Log failed request
		if s.ReqLog != nil {
			s.ReqLog.Add(reqlog.Entry{
				ID:           fmt.Sprintf("leo-%d", time.Now().UnixNano()),
				StatusCode:   502,
				TaskStatus:   "FAILED",
				Type:         "video",
				DurationSec:  elapsedSec,
				TokenID:      usedTokenID,
				TokenAttempt: 1,
				AccountName:  accountName,
				AccountEmail: accountEmail,
				Model:        fmt.Sprintf("%s (%dx%d %ds)", body.Model, body.Width, body.Height, body.Duration),
				Prompt:       body.Prompt,
				ErrorCode:    "502",
				ErrorMessage: fmt.Sprintf("generation failed: %v", err),
				Operation:    "leonardo.generate",
			})
		}
		writeJSON(w, 500, map[string]interface{}{
			"detail": fmt.Sprintf("generation failed: %v", err),
		})
		return
	}
	s.applyTokenCreditCost(usedTokenID, result.APICreditCost)

	// Log pending request
	if s.ReqLog != nil {
		s.ReqLog.Add(reqlog.Entry{
			ID:           fmt.Sprintf("leo-%d", time.Now().UnixNano()),
			StatusCode:   200,
			TaskStatus:   "IN_PROGRESS",
			Type:         "video",
			DurationSec:  elapsedSec,
			TokenID:      usedTokenID,
			TokenAttempt: 1,
			AccountName:  accountName,
			AccountEmail: accountEmail,
			Model:        fmt.Sprintf("%s (%dx%d %ds)", body.Model, body.Width, body.Height, body.Duration),
			Prompt:       body.Prompt,
			GenerationID: result.GenerationID,
			CreditCost:   result.APICreditCost,
			Operation:    "leonardo.generate",
		})
	}

	// Background polling goroutine to auto-update log status
	go s.pollGenerationStatus(session, result.GenerationID, usedTokenID, body.Model, startTime)

	writeJSON(w, 200, map[string]interface{}{
		"ok":            true,
		"generation_id": result.GenerationID,
		"credit_cost":   result.APICreditCost,
	})
}

// HandleLeonardoStatus handles GET /api/v1/leonardo/status?id=xxx.
func (s *Server) HandleLeonardoStatus(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}
	if s.LeonardoClient == nil {
		writeJSON(w, 500, map[string]string{"detail": "Leonardo client not initialized"})
		return
	}

	genID := r.URL.Query().Get("id")
	tokenID := r.URL.Query().Get("token_id")
	if genID == "" {
		writeJSON(w, 400, map[string]string{"detail": "id parameter required"})
		return
	}

	session, _ := s.getLeonardoSession(tokenID)
	if session == nil {
		writeJSON(w, 404, map[string]string{"detail": "Leonardo token not found"})
		return
	}

	status, err := s.LeonardoClient.PollGenerationStatus(session, genID)
	if err != nil {
		writeJSON(w, 500, map[string]interface{}{"detail": err.Error()})
		return
	}

	resp := map[string]interface{}{
		"ok":     true,
		"id":     status.ID,
		"status": status.Status,
	}

	// If complete, fetch detail with video URLs
	if status.Status == "COMPLETE" {
		detail, err := s.LeonardoClient.GetGenerationDetail(session, genID)
		if err == nil && len(detail.Images) > 0 {
			videos := make([]map[string]string, 0)
			var firstMP4, firstThumb string
			for _, img := range detail.Images {
				v := map[string]string{"id": img.ID}
				if img.MotionMP4 != "" {
					finalMP4 := img.MotionMP4
					if localURL, materializeErr := s.materializeGeneratedMedia(img.MotionMP4, genID, "video"); materializeErr == nil {
						finalMP4 = localURL
					} else {
						log.Printf("[status] failed to materialize mp4 for %s: %v", genID, materializeErr)
					}
					v["mp4_url"] = finalMP4
					if firstMP4 == "" {
						firstMP4 = finalMP4
					}
				}
				if img.MotionGIF != "" {
					v["gif_url"] = img.MotionGIF
				}
				if img.URL != "" {
					finalThumb := img.URL
					if localURL, materializeErr := s.materializeGeneratedMedia(img.URL, genID+"-thumb", "image"); materializeErr == nil {
						finalThumb = localURL
					} else {
						log.Printf("[status] failed to materialize thumbnail for %s: %v", genID, materializeErr)
					}
					v["thumbnail_url"] = finalThumb
					if firstThumb == "" {
						firstThumb = finalThumb
					}
				}
				videos = append(videos, v)
			}
			resp["videos"] = videos
			resp["prompt"] = detail.Prompt

			// Update log entry
			if s.ReqLog != nil {
				previewURL := firstMP4
				previewKind := "video"
				if previewURL == "" {
					previewURL = firstThumb
					previewKind = "image"
				}
				if previewURL != "" {
					if finalURL, materializeErr := s.materializeGeneratedMedia(previewURL, genID, previewKind); materializeErr == nil {
						previewURL = finalURL
					} else {
						log.Printf("[status] failed to materialize generated media for %s: %v", genID, materializeErr)
					}
				}
				s.ReqLog.UpdateByGenerationID(genID, "COMPLETE", 200, previewURL, previewKind, "")
			}
		}
	} else if status.Status == "FAILED" {
		if s.ReqLog != nil {
			s.ReqLog.UpdateByGenerationID(genID, "FAILED", 502, "", "", "Leonardo reported generation status FAILED")
		}
	}

	writeJSON(w, 200, resp)
}

// HandleLeonardoUploadImage handles POST /api/v1/leonardo/upload-image.
// Accepts multipart form with "file" field and optional "token_id".
// Returns the uploaded image ID for use in image_guidance.
func (s *Server) HandleLeonardoUploadImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeJSON(w, 405, map[string]string{"detail": "method not allowed"})
		return
	}
	if s.LeonardoClient == nil {
		writeJSON(w, 500, map[string]string{"detail": "Leonardo client not initialized"})
		return
	}

	// Parse multipart form (max 20MB)
	if err := r.ParseMultipartForm(20 << 20); err != nil {
		writeJSON(w, 400, map[string]string{"detail": "failed to parse form: " + err.Error()})
		return
	}

	tokenID := r.FormValue("token_id")
	session, _ := s.getLeonardoSession(tokenID)
	if session == nil {
		writeJSON(w, 404, map[string]string{"detail": "Leonardo token not found"})
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, 400, map[string]string{"detail": "file is required"})
		return
	}
	defer file.Close()

	imageData, err := io.ReadAll(file)
	if err != nil {
		writeJSON(w, 500, map[string]string{"detail": "failed to read file"})
		return
	}

	// Determine file extension
	ext := "jpg"
	if header.Filename != "" {
		parts := strings.Split(header.Filename, ".")
		if len(parts) > 1 {
			ext = parts[len(parts)-1]
		}
	}

	// Step 2: Upload to S3
	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg"
	}
	imageID, err := s.uploadLeonardoImageBytes(session, imageData, ext, contentType)
	if err != nil {
		writeJSON(w, 500, map[string]interface{}{"detail": err.Error()})
		return
	}

	writeJSON(w, 200, map[string]interface{}{
		"ok":       true,
		"image_id": imageID,
		"type":     "UPLOADED",
	})
}

func (s *Server) resolveLeonardoImageID(session *leonardo.TokenSession, id, remoteURL string, cache map[string]string) (string, error) {
	imageID := strings.TrimSpace(id)
	if imageID != "" {
		return imageID, nil
	}

	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" {
		return "", fmt.Errorf("either id or url is required")
	}
	if cache != nil {
		if cachedID, ok := cache[remoteURL]; ok && cachedID != "" {
			return cachedID, nil
		}
	}

	uploadedID, err := s.uploadLeonardoImageFromURL(session, remoteURL)
	if err != nil {
		return "", err
	}
	if cache != nil {
		cache[remoteURL] = uploadedID
	}
	return uploadedID, nil
}

func (s *Server) resolveLeonardoVideoID(session *leonardo.TokenSession, id, remoteURL string, cache map[string]string) (string, error) {
	videoID := strings.TrimSpace(id)
	if videoID != "" {
		return videoID, nil
	}

	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" {
		return "", fmt.Errorf("either id or url is required")
	}
	if cache != nil {
		if cachedID, ok := cache[remoteURL]; ok && cachedID != "" {
			return cachedID, nil
		}
	}

	uploadedID, _, err := s.uploadLeonardoVideoFromURL(session, remoteURL)
	if err != nil {
		return "", err
	}
	if cache != nil {
		cache[remoteURL] = uploadedID
	}
	return uploadedID, nil
}

func (s *Server) resolveLeonardoVideoRef(session *leonardo.TokenSession, id, remoteURL string, durationHint float64, cache map[string]string) (leonardo.VideoRef, error) {
	videoID := strings.TrimSpace(id)
	if videoID != "" {
		return leonardo.VideoRef{
			ID:       videoID,
			Type:     "UPLOADED",
			Duration: durationHint,
		}, nil
	}

	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" {
		return leonardo.VideoRef{}, fmt.Errorf("either id or url is required")
	}
	if cache != nil {
		if cachedID, ok := cache[remoteURL]; ok && cachedID != "" {
			return leonardo.VideoRef{
				ID:       cachedID,
				Type:     "UPLOADED",
				Duration: durationHint,
			}, nil
		}
	}

	uploadedID, detectedDuration, err := s.uploadLeonardoVideoFromURL(session, remoteURL)
	if err != nil {
		return leonardo.VideoRef{}, err
	}
	if cache != nil {
		cache[remoteURL] = uploadedID
	}
	if durationHint <= 0 {
		durationHint = detectedDuration
	}
	return leonardo.VideoRef{
		ID:       uploadedID,
		Type:     "UPLOADED",
		Duration: durationHint,
	}, nil
}

func (s *Server) uploadLeonardoImageFromURL(session *leonardo.TokenSession, remoteURL string) (string, error) {
	imageData, contentType, ext, err := s.downloadRemoteImage(remoteURL)
	if err != nil {
		return "", err
	}
	return s.uploadLeonardoImageBytes(session, imageData, ext, contentType)
}

func isLeonardoS3PolicyExpired(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "Policy expired")
}

func isRetryableRemoteFetchError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "unexpected eof") ||
		strings.Contains(msg, "connection reset")
}

func (s *Server) uploadLeonardoBytesToStaging(session *leonardo.TokenSession, mediaData []byte, ext, contentType, mediaKind string) (*leonardo.UploadInitResult, error) {
	const maxInitAttempts = 2

	for attempt := 1; attempt <= maxInitAttempts; attempt++ {
		initResult, err := s.LeonardoClient.UploadInitImage(session, ext)
		if err != nil {
			return nil, fmt.Errorf("upload init failed: %w", err)
		}

		err = s.LeonardoClient.UploadImageToS3(initResult.URL, initResult.Fields, mediaData, contentType)
		if err == nil {
			return initResult, nil
		}
		if attempt < maxInitAttempts && isLeonardoS3PolicyExpired(err) {
			log.Printf("[Leonardo] %s upload policy expired for uploadID=%s; refreshing upload ticket", mediaKind, initResult.UploadID)
			continue
		}
		return nil, fmt.Errorf("s3 upload failed: %w", err)
	}

	return nil, fmt.Errorf("s3 upload failed: exhausted upload ticket refresh attempts")
}

func (s *Server) uploadLeonardoVideoFromURL(session *leonardo.TokenSession, remoteURL string) (string, float64, error) {
	videoData, contentType, ext, duration, err := s.downloadRemoteVideo(remoteURL)
	if err != nil {
		return "", 0, err
	}
	log.Printf("[Leonardo] Remote video fetched: url=%s contentType=%s ext=%s bytes=%d duration=%.3fs", remoteURL, contentType, ext, len(videoData), duration)
	videoID, err := s.uploadLeonardoVideoBytes(session, videoData, ext, contentType)
	if err != nil {
		return "", 0, err
	}
	log.Printf("[Leonardo] Remote video uploaded: url=%s videoID=%s duration=%.3fs", remoteURL, videoID, duration)
	return videoID, duration, nil
}

func (s *Server) uploadLeonardoImageBytes(session *leonardo.TokenSession, imageData []byte, ext, contentType string) (string, error) {
	initResult, err := s.uploadLeonardoBytesToStaging(session, imageData, ext, contentType, "image")
	if err != nil {
		return "", err
	}
	imageID, err := s.LeonardoClient.WaitForInitImage(session, initResult.UploadID, initImageLookupTimeout)
	if err != nil {
		return "", fmt.Errorf("wait for init image failed: %w", err)
	}
	return imageID, nil
}

func (s *Server) uploadLeonardoVideoBytes(session *leonardo.TokenSession, videoData []byte, ext, contentType string) (string, error) {
	initResult, err := s.uploadLeonardoBytesToStaging(session, videoData, ext, contentType, "video")
	if err != nil {
		return "", err
	}
	log.Printf("[Leonardo] Video upload staged: uploadID=%s ext=%s contentType=%s bytes=%d", initResult.UploadID, ext, contentType, len(videoData))

	// Leonardo's web flow waits for uploaded_media to reach COMPLETE before
	// reusing the staged upload as a video guidance asset.
	uploadedMedia, err := s.LeonardoClient.WaitForUploadedMedia(session, initResult.UploadID, initImageLookupTimeout)
	if err != nil {
		return "", fmt.Errorf("wait for staged video asset failed: %w", err)
	}
	videoDuration := 0.0
	if uploadedMedia.Duration != nil {
		videoDuration = *uploadedMedia.Duration
	}
	log.Printf("[Leonardo] Video upload ready: uploadID=%s status=%s width=%v height=%v duration=%.3fs url=%s", initResult.UploadID, uploadedMedia.Status, uploadedMedia.Width, uploadedMedia.Height, videoDuration, uploadedMedia.URL)
	return initResult.UploadID, nil
}

func (s *Server) downloadRemoteImage(remoteURL string) ([]byte, string, string, error) {
	parsedURL, err := url.Parse(strings.TrimSpace(remoteURL))
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid image url: %w", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return nil, "", "", fmt.Errorf("image url must use http or https")
	}
	if parsedURL.RawPath == "" {
		parsedURL.RawPath = parsedURL.EscapedPath()
	}

	httpClient, err := s.newResourceHTTPClient(remoteImageFetchTimeout)
	if err != nil {
		return nil, "", "", err
	}

	req, err := http.NewRequest("GET", parsedURL.String(), nil)
	if err != nil {
		return nil, "", "", err
	}
	req.Header.Set("User-Agent", "leo-go-image-fetch/1.0")
	req.Header.Set("Accept", "image/*,*/*;q=0.8")

	for attempt := 1; attempt <= remoteFetchMaxAttempts; attempt++ {
		resp, err := httpClient.Do(req)
		if err != nil {
			if attempt < remoteFetchMaxAttempts && isRetryableRemoteFetchError(err) {
				log.Printf("[Leonardo] Remote image fetch attempt %d/%d failed for %s: %v; retrying", attempt, remoteFetchMaxAttempts, remoteURL, err)
				continue
			}
			return nil, "", "", fmt.Errorf("fetch image url failed: %w", err)
		}

		imageData, readErr := io.ReadAll(io.LimitReader(resp.Body, maxRemoteImageBytes+1))
		resp.Body.Close()
		if readErr != nil {
			if attempt < remoteFetchMaxAttempts && isRetryableRemoteFetchError(readErr) {
				log.Printf("[Leonardo] Remote image read attempt %d/%d failed for %s: %v; retrying", attempt, remoteFetchMaxAttempts, remoteURL, readErr)
				continue
			}
			return nil, "", "", fmt.Errorf("read image url failed: %w", readErr)
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, "", "", fmt.Errorf("image url returned %d", resp.StatusCode)
		}
		if len(imageData) == 0 {
			return nil, "", "", fmt.Errorf("image url returned empty body")
		}
		if len(imageData) > maxRemoteImageBytes {
			return nil, "", "", fmt.Errorf("image url exceeds %d MB limit", maxRemoteImageBytes>>20)
		}

		contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
		if mediaType, _, err := mime.ParseMediaType(contentType); err == nil && mediaType != "" {
			contentType = mediaType
		}
		if contentType == "" || !strings.HasPrefix(contentType, "image/") {
			contentType = http.DetectContentType(imageData)
		}
		if !strings.HasPrefix(contentType, "image/") {
			return nil, "", "", fmt.Errorf("image url did not return an image content type")
		}

		ext := imageExtFromContentType(contentType)
		if ext == "" {
			ext = imageExtFromURL(parsedURL.Path)
		}
		if ext == "" {
			ext = "jpg"
		}

		return imageData, contentType, ext, nil
	}

	return nil, "", "", fmt.Errorf("fetch image url failed after %d attempt(s)", remoteFetchMaxAttempts)
}

func (s *Server) downloadRemoteVideo(remoteURL string) ([]byte, string, string, float64, error) {
	parsedURL, err := url.Parse(strings.TrimSpace(remoteURL))
	if err != nil {
		return nil, "", "", 0, fmt.Errorf("invalid video url: %w", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return nil, "", "", 0, fmt.Errorf("video url must use http or https")
	}
	if parsedURL.RawPath == "" {
		parsedURL.RawPath = parsedURL.EscapedPath()
	}

	httpClient, err := s.newResourceHTTPClient(remoteImageFetchTimeout)
	if err != nil {
		return nil, "", "", 0, err
	}

	req, err := http.NewRequest("GET", parsedURL.String(), nil)
	if err != nil {
		return nil, "", "", 0, err
	}
	req.Header.Set("User-Agent", "leo-go-video-fetch/1.0")
	req.Header.Set("Accept", "video/*,*/*;q=0.8")

	for attempt := 1; attempt <= remoteFetchMaxAttempts; attempt++ {
		resp, err := httpClient.Do(req)
		if err != nil {
			if attempt < remoteFetchMaxAttempts && isRetryableRemoteFetchError(err) {
				log.Printf("[Leonardo] Remote video fetch attempt %d/%d failed for %s: %v; retrying", attempt, remoteFetchMaxAttempts, remoteURL, err)
				continue
			}
			return nil, "", "", 0, fmt.Errorf("fetch video url failed: %w", err)
		}

		videoData, readErr := io.ReadAll(io.LimitReader(resp.Body, maxRemoteVideoBytes+1))
		resp.Body.Close()
		if readErr != nil {
			if attempt < remoteFetchMaxAttempts && isRetryableRemoteFetchError(readErr) {
				log.Printf("[Leonardo] Remote video read attempt %d/%d failed for %s: %v; retrying", attempt, remoteFetchMaxAttempts, remoteURL, readErr)
				continue
			}
			return nil, "", "", 0, fmt.Errorf("read video url failed: %w", readErr)
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, "", "", 0, fmt.Errorf("video url returned %d", resp.StatusCode)
		}
		if len(videoData) == 0 {
			return nil, "", "", 0, fmt.Errorf("video url returned empty body")
		}
		if len(videoData) > maxRemoteVideoBytes {
			return nil, "", "", 0, fmt.Errorf("video url exceeds %d MB limit", maxRemoteVideoBytes>>20)
		}

		contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
		if mediaType, _, err := mime.ParseMediaType(contentType); err == nil && mediaType != "" {
			contentType = mediaType
		}
		if contentType == "" || !strings.HasPrefix(contentType, "video/") {
			contentType = http.DetectContentType(videoData)
		}
		if !strings.HasPrefix(contentType, "video/") {
			return nil, "", "", 0, fmt.Errorf("video url did not return a video content type")
		}

		ext := videoExtFromContentType(contentType)
		if ext == "" {
			ext = videoExtFromURL(parsedURL.Path)
		}
		if ext == "" {
			ext = "mp4"
		}

		duration := detectRemoteVideoDuration(videoData, contentType, ext)
		return videoData, contentType, ext, duration, nil
	}

	return nil, "", "", 0, fmt.Errorf("fetch video url failed after %d attempt(s)", remoteFetchMaxAttempts)
}

func detectRemoteVideoDuration(videoData []byte, contentType, ext string) float64 {
	normalizedExt := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(ext), "."))
	if normalizedExt != "mp4" && normalizedExt != "m4v" && normalizedExt != "mov" && !strings.Contains(strings.ToLower(contentType), "mp4") && !strings.Contains(strings.ToLower(contentType), "quicktime") {
		return 0
	}

	duration, err := parseISOBMFFDuration(videoData)
	if err != nil || duration <= 0 {
		return 0
	}
	return duration
}

func parseISOBMFFDuration(data []byte) (float64, error) {
	moov, err := findISOBMFFBox(data, "moov")
	if err != nil {
		return 0, err
	}
	mvhd, err := findISOBMFFBox(moov, "mvhd")
	if err != nil {
		return 0, err
	}
	if len(mvhd) < 20 {
		return 0, fmt.Errorf("mvhd box too short")
	}

	version := mvhd[0]
	switch version {
	case 0:
		if len(mvhd) < 20 {
			return 0, fmt.Errorf("mvhd version 0 too short")
		}
		timescale := binary.BigEndian.Uint32(mvhd[12:16])
		duration := binary.BigEndian.Uint32(mvhd[16:20])
		if timescale == 0 {
			return 0, fmt.Errorf("mvhd timescale is zero")
		}
		return float64(duration) / float64(timescale), nil
	case 1:
		if len(mvhd) < 32 {
			return 0, fmt.Errorf("mvhd version 1 too short")
		}
		timescale := binary.BigEndian.Uint32(mvhd[20:24])
		duration := binary.BigEndian.Uint64(mvhd[24:32])
		if timescale == 0 {
			return 0, fmt.Errorf("mvhd timescale is zero")
		}
		return float64(duration) / float64(timescale), nil
	default:
		return 0, fmt.Errorf("unsupported mvhd version %d", version)
	}
}

func findISOBMFFBox(data []byte, target string) ([]byte, error) {
	for offset := 0; offset+8 <= len(data); {
		size := uint64(binary.BigEndian.Uint32(data[offset : offset+4]))
		boxType := string(data[offset+4 : offset+8])
		headerSize := 8
		if size == 1 {
			if offset+16 > len(data) {
				return nil, fmt.Errorf("truncated large box header")
			}
			size = binary.BigEndian.Uint64(data[offset+8 : offset+16])
			headerSize = 16
		} else if size == 0 {
			size = uint64(len(data) - offset)
		}
		if size < uint64(headerSize) {
			return nil, fmt.Errorf("invalid box size for %s", boxType)
		}

		end := offset + int(size)
		if end > len(data) {
			return nil, fmt.Errorf("box %s extends past data", boxType)
		}

		payload := data[offset+headerSize : end]
		if boxType == target {
			return payload, nil
		}
		if boxType == "moov" || boxType == "trak" || boxType == "mdia" || boxType == "minf" || boxType == "stbl" || boxType == "edts" || boxType == "udta" || boxType == "meta" {
			if nested, err := findISOBMFFBox(payload, target); err == nil {
				return nested, nil
			}
		}

		offset = end
	}
	return nil, fmt.Errorf("box %s not found", target)
}

func (s *Server) newResourceHTTPClient(timeout time.Duration) (*http.Client, error) {
	transport := &http.Transport{}

	proxyStr := ""
	if s.Config.GetBool("resource_use_proxy", false) {
		proxyStr = strings.TrimSpace(s.Config.GetString("resource_proxy", ""))
	} else if s.Config.GetBool("use_proxy", false) {
		proxyStr = strings.TrimSpace(s.Config.GetString("proxy", ""))
	}

	if proxyStr != "" {
		proxyURL, err := url.Parse(proxyStr)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy url: %w", err)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}

	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}, nil
}

func imageExtFromContentType(contentType string) string {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/png":
		return "png"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	case "image/bmp":
		return "bmp"
	case "image/tiff":
		return "tiff"
	default:
		return ""
	}
}

func imageExtFromURL(rawPath string) string {
	ext := strings.TrimPrefix(strings.ToLower(pathpkg.Ext(rawPath)), ".")
	switch ext {
	case "jpg", "jpeg", "png", "webp", "gif", "bmp", "tif", "tiff":
		if ext == "jpeg" {
			return "jpg"
		}
		if ext == "tif" {
			return "tiff"
		}
		return ext
	default:
		return ""
	}
}

func videoExtFromContentType(contentType string) string {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "video/mp4":
		return "mp4"
	case "video/quicktime":
		return "mov"
	case "video/webm":
		return "webm"
	case "video/x-msvideo":
		return "avi"
	default:
		return ""
	}
}

func videoExtFromURL(rawPath string) string {
	ext := strings.TrimPrefix(strings.ToLower(pathpkg.Ext(rawPath)), ".")
	switch ext {
	case "mp4", "mov", "webm", "avi", "m4v":
		return ext
	default:
		return ""
	}
}

// pollGenerationStatus runs in a goroutine to auto-update log status.
func (s *Server) pollGenerationStatus(session *leonardo.TokenSession, genID string, tokenID string, modelID string, startedAt time.Time) {
	const (
		pollInterval = 10 * time.Second
		maxDuration  = 10 * time.Minute
	)

	deadline := time.Now().Add(maxDuration)
	startTime := startedAt
	if startTime.IsZero() {
		startTime = time.Now()
	}

	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)

		status, err := s.LeonardoClient.PollGenerationStatus(session, genID)
		if err != nil {
			log.Printf("[poll] error polling %s: %v", genID, err)
			continue
		}

		elapsed := time.Since(startTime).Seconds()

		switch status.Status {
		case "COMPLETE":
			log.Printf("[poll] generation %s completed (%.1fs)", genID, elapsed)
			// Fetch detail for preview URL
			detail, err := s.LeonardoClient.GetGenerationDetail(session, genID)
			if err == nil && len(detail.Images) > 0 {
				previewURL := ""
				previewKind := ""
				for _, img := range detail.Images {
					if img.MotionMP4 != "" {
						previewURL = img.MotionMP4
						previewKind = "video"
						break
					}
					if img.URL != "" && previewURL == "" {
						previewURL = img.URL
						previewKind = "image"
					}
				}
				if previewURL != "" {
					if finalURL, materializeErr := s.materializeGeneratedMedia(previewURL, genID, previewKind); materializeErr == nil {
						previewURL = finalURL
					} else {
						log.Printf("[poll] failed to materialize generated media for %s: %v", genID, materializeErr)
					}
				}
				if s.ReqLog != nil {
					s.ReqLog.UpdateByGenerationID(genID, "COMPLETE", 200, previewURL, previewKind, "")
					s.ReqLog.UpdateDuration(genID, elapsed)
				}
			} else {
				if s.ReqLog != nil {
					s.ReqLog.UpdateByGenerationID(genID, "COMPLETE", 200, "", "", "")
					s.ReqLog.UpdateDuration(genID, elapsed)
				}
			}
			// Refresh token credits
			s.reportSeedanceGenerationSuccess(tokenID, modelID)
			s.refreshTokenCredits(tokenID, session)
			return

		case "FAILED":
			log.Printf("[poll] generation %s failed (%.1fs)", genID, elapsed)
			if s.ReqLog != nil {
				s.ReqLog.UpdateByGenerationID(genID, "FAILED", 502, "", "", "Leonardo reported generation status FAILED")
				s.ReqLog.UpdateDuration(genID, elapsed)
			}
			s.refreshTokenCredits(tokenID, session)
			return
		}
		// Still PENDING, continue polling
	}

	// Timeout
	log.Printf("[poll] generation %s timed out after %v", genID, maxDuration)
	if s.ReqLog != nil {
		s.ReqLog.UpdateByGenerationID(genID, "FAILED", 504, "", "", "Generation polling timed out")
		s.ReqLog.UpdateDuration(genID, time.Since(startTime).Seconds())
	}
	s.refreshTokenCredits(tokenID, session)
}

// refreshTokenCredits queries latest credits from Leonardo and updates the token pool.
func (s *Server) refreshTokenCredits(tokenID string, session *leonardo.TokenSession) {
	if tokenID == "" || session == nil || s.LeonardoClient == nil || s.TokenMgr == nil {
		return
	}
	credits, err := s.LeonardoClient.QueryCredits(session)
	if err != nil {
		log.Printf("[poll] failed to refresh credits for token %s: %v", tokenID, err)
		return
	}
	if credits != nil {
		s.TokenMgr.UpdateCredits(tokenID, float64(credits.TotalTokens), float64(credits.SubscriptionTokens+credits.PaidTokens+credits.RolloverTokens))
		log.Printf("[poll] refreshed credits for token %s: %d remaining", tokenID, credits.TotalTokens)
	}
}

// applyTokenCreditCost updates the local token balance immediately after
// Leonardo accepts a generation request. A later credits query can still
// correct the exact balance if upstream adjusts it.
func (s *Server) applyTokenCreditCost(tokenID string, creditCost int) {
	if tokenID == "" || creditCost <= 0 || s.TokenMgr == nil {
		return
	}
	info := s.TokenMgr.GetByID(tokenID)
	if info == nil {
		return
	}
	current := toFloat64(info["credits_available"])
	total := toFloat64(info["credits_total"])
	if current <= 0 && total <= 0 {
		return
	}
	next := current - float64(creditCost)
	if next < 0 {
		next = 0
	}
	if total <= 0 {
		total = current
	}
	if err := s.TokenMgr.UpdateCredits(tokenID, next, total); err != nil {
		log.Printf("[poll] failed to apply credit cost for token %s: %v", tokenID, err)
		return
	}
	log.Printf("[poll] applied credit cost for token %s: -%d, %.0f remaining", tokenID, creditCost, next)
}

func (s *Server) reportSeedanceGenerationSuccess(tokenID string, modelID string) {
	if tokenID == "" || s.TokenMgr == nil {
		return
	}
	autoDisableEnabled := false
	if s.Config != nil {
		autoDisableEnabled = s.Config.GetBool("token_success_auto_disable_enabled", false)
	}
	info := s.TokenMgr.ReportModelSuccessWithAutoDisable(tokenID, modelID, autoDisableEnabled)
	if info == nil || !autoDisableEnabled {
		return
	}
	if strings.EqualFold(strings.TrimSpace(toString(info["status"])), "exhausted") {
		log.Printf("[token] auto-disabled exhausted Seedance token %s after model usage fast=%v standard=%v", tokenID, info["seedance_fast_success_count"], info["seedance_standard_success_count"])
	}
}

func (s *Server) getLeonardoSessionExcluding(tokenID string, excluded map[string]bool) (*leonardo.TokenSession, string) {
	return s.getLeonardoSessionForModelExcluding(tokenID, excluded, "")
}

func (s *Server) getLeonardoSessionForModelExcluding(tokenID string, excluded map[string]bool, modelID string) (*leonardo.TokenSession, string) {
	if tokenID != "" {
		if excluded != nil && excluded[tokenID] {
			return nil, ""
		}
		return s.getLeonardoSessionForModel(tokenID, modelID)
	}

	strategy := "round_robin"
	if s.Config != nil {
		strategy = strings.TrimSpace(s.Config.GetString("token_rotation_strategy", "round_robin"))
	}

	maxAttempts := s.TokenMgr.Count()
	if maxAttempts < 1 {
		return nil, ""
	}
	if strings.EqualFold(strategy, "random") {
		maxAttempts *= 2
	}

	tried := make(map[string]bool)
	for i := 0; i < maxAttempts; i++ {
		info := s.TokenMgr.GetAvailableTokenForPlatform("leonardo", strategy)
		if info == nil {
			return nil, ""
		}

		foundID := strings.TrimSpace(toString(info["id"]))
		if foundID == "" || tried[foundID] || (excluded != nil && excluded[foundID]) {
			continue
		}
		tried[foundID] = true
		if !s.seedanceTokenCanRunModel(info, modelID) {
			continue
		}

		rawToken := strings.TrimSpace(toString(info["value"]))
		if rawToken == "" {
			continue
		}
		session := s.getOrCreateLeonardoSession(foundID, rawToken)
		if session == nil {
			continue
		}
		if err := s.LeonardoClient.EnsureValidJWT(session); err != nil {
			log.Printf("[token] failed to prepare Leonardo session for %s: %v", foundID, err)
			continue
		}
		return session, foundID
	}
	return nil, ""
}

// getLeonardoSession finds a Leonardo session from the token pool.
// Returns the session and the token ID used.
func (s *Server) getLeonardoSession(tokenID string) (*leonardo.TokenSession, string) {
	return s.getLeonardoSessionForModel(tokenID, "")
}

func (s *Server) getLeonardoSessionForModel(tokenID string, modelID string) (*leonardo.TokenSession, string) {
	// If specific tokenID provided, use that
	if tokenID != "" {
		info := s.TokenMgr.GetByID(tokenID)
		if info == nil {
			return nil, ""
		}
		if !s.seedanceTokenCanRunModel(info, modelID) {
			return nil, ""
		}
		rawToken, _ := info["value"].(string)
		if rawToken == "" {
			return nil, ""
		}
		session := s.getOrCreateLeonardoSession(tokenID, rawToken)
		if session == nil {
			return nil, ""
		}
		if err := s.LeonardoClient.EnsureValidJWT(session); err != nil {
			return nil, ""
		}
		return session, tokenID
	}

	// Otherwise select an available Leonardo token using the configured rotation strategy.
	strategy := "round_robin"
	if s.Config != nil {
		strategy = strings.TrimSpace(s.Config.GetString("token_rotation_strategy", "round_robin"))
	}

	maxAttempts := s.TokenMgr.Count()
	if maxAttempts < 1 {
		return nil, ""
	}
	if strings.EqualFold(strategy, "random") {
		maxAttempts *= 2
	}

	tried := make(map[string]bool)
	for i := 0; i < maxAttempts; i++ {
		info := s.TokenMgr.GetAvailableTokenForPlatform("leonardo", strategy)
		if info == nil {
			return nil, ""
		}

		foundID := strings.TrimSpace(toString(info["id"]))
		if foundID == "" || tried[foundID] {
			continue
		}
		tried[foundID] = true
		if !s.seedanceTokenCanRunModel(info, modelID) {
			continue
		}

		rawToken := strings.TrimSpace(toString(info["value"]))
		if rawToken == "" {
			continue
		}
		session := s.getOrCreateLeonardoSession(foundID, rawToken)
		if session == nil {
			continue
		}
		if err := s.LeonardoClient.EnsureValidJWT(session); err != nil {
			log.Printf("[token] failed to prepare Leonardo session for %s: %v", foundID, err)
			continue
		}
		return session, foundID
	}
	return nil, ""
}

func (s *Server) seedanceTokenCanRunModel(info map[string]interface{}, modelID string) bool {
	if info == nil || s == nil || s.Config == nil || !s.Config.GetBool("token_success_auto_disable_enabled", false) {
		return true
	}
	fastCount := int(toFloat64(info["seedance_fast_success_count"]))
	standardCount := int(toFloat64(info["seedance_standard_success_count"]))
	if fastCount >= 2 || (standardCount >= 1 && fastCount >= 1) {
		return false
	}
	switch strings.TrimSpace(modelID) {
	case "seedance-2.0":
		return standardCount < 1
	case "seedance-2.0-fast":
		return fastCount < 2
	default:
		return true
	}
}

func statusForLeonardoRefreshError(err error) int {
	if err == nil {
		return http.StatusBadRequest
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "rate limited") || strings.Contains(msg, "(429)") || strings.Contains(msg, "returned 429") {
		return http.StatusTooManyRequests
	}
	return http.StatusBadRequest
}

func isInvalidLeonardoTokenError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "invalid") ||
		strings.Contains(msg, "expired") ||
		strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "401") ||
		strings.Contains(msg, "no jwt found")
}

func isAbnormalLeonardoTokenError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "403") ||
		strings.Contains(msg, "forbidden") ||
		strings.Contains(msg, "eof") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "connection") ||
		strings.Contains(msg, "proxy") ||
		strings.Contains(msg, "tls")
}

func runHTTPProxyConnectivityTest(enabled bool, proxyStr, targetURL string) map[string]interface{} {
	result := map[string]interface{}{
		"enabled":    enabled,
		"proxy":      strings.TrimSpace(proxyStr),
		"target_url": targetURL,
	}
	if !enabled {
		result["message"] = "proxy disabled"
		return result
	}
	if strings.TrimSpace(proxyStr) == "" {
		result["message"] = "proxy is empty"
		return result
	}

	start := time.Now()
	statusCode, message, err := doHTTPProxyProbe(proxyStr, targetURL)
	result["elapsed_ms"] = time.Since(start).Milliseconds()
	if statusCode > 0 {
		result["status_code"] = statusCode
	}
	if err != nil {
		result["ok"] = false
		result["message"] = err.Error()
		return result
	}
	result["ok"] = true
	result["message"] = message
	return result
}

func doHTTPProxyProbe(proxyStr, targetURL string) (int, string, error) {
	proxyURL, err := url.Parse(strings.TrimSpace(proxyStr))
	if err != nil {
		return 0, "", fmt.Errorf("invalid proxy url: %w", err)
	}

	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   15 * time.Second,
	}
	req, err := http.NewRequest("GET", targetURL, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("User-Agent", "leo-go-proxy-test/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 500 {
		return resp.StatusCode, "upstream responded through proxy", nil
	}
	return resp.StatusCode, fmt.Sprintf("upstream returned %d", resp.StatusCode), nil
}

func (s *Server) runLeonardoProxyBusinessTest(enabled bool, proxyStr string) map[string]interface{} {
	result := map[string]interface{}{
		"enabled":    enabled,
		"target_url": leonardo.SessionURL,
	}
	if !enabled {
		result["message"] = "proxy disabled"
		return result
	}
	if strings.TrimSpace(proxyStr) == "" {
		result["message"] = "proxy is empty"
		return result
	}

	var tokenID, tokenValue, tokenSource string
	for _, t := range s.TokenMgr.ListFull() {
		platform, _ := t["platform"].(string)
		status, _ := t["status"].(string)
		if platform != "leonardo" || status != "active" {
			continue
		}
		tokenID, _ = t["id"].(string)
		tokenValue, _ = t["value"].(string)
		tokenSource, _ = t["source"].(string)
		if tokenValue != "" {
			break
		}
	}

	if tokenValue == "" {
		result["message"] = "no active Leonardo token available for business test"
		return result
	}

	result["token_id"] = tokenID
	result["token_source"] = tokenSource
	result["token_preview"] = maskProxyTokenValue(tokenValue)

	client := leonardo.NewClient(strings.TrimSpace(proxyStr))
	start := time.Now()
	session, credits, err := client.ValidateToken(tokenValue)
	result["elapsed_ms"] = time.Since(start).Milliseconds()
	if err != nil {
		result["ok"] = false
		result["status_code"] = statusForLeonardoRefreshError(err)
		result["message"] = err.Error()
		return result
	}

	result["ok"] = true
	result["status_code"] = 200
	if session != nil {
		result["account_id"] = session.HasuraUserID
		result["email"] = session.Email
	}
	if credits != nil {
		result["message"] = fmt.Sprintf("plan=%s, credits=%d", credits.Plan, credits.TotalTokens)
	} else {
		result["message"] = "session refresh succeeded"
	}
	return result
}

func maskProxyTokenValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 10 {
		return "***"
	}
	return value[:5] + "..." + value[len(value)-5:]
}

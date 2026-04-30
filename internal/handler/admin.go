package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"mime"
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
				leoInfo["hint"] = "Token已保存，请稍后点击「刷新积分」获取账号信息"
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
	credits, err := s.LeonardoClient.QueryCredits(session)
	if err != nil {
		return session, nil, fmt.Errorf("token validation failed: %w", err)
	}
	return session, credits, nil
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
	deleted := 0
	for _, id := range body.IDs {
		if s.TokenMgr.Remove(id) == nil {
			deleted++
		}
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true, "deleted": deleted})
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
	account := r.URL.Query().Get("account")

	entries, curPage, totalPages := s.ReqLog.List(page, pageSize, failedOnly, account)

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

// HandleFailedAccounts handles GET /api/v1/logs/failed-accounts.
func (s *Server) HandleFailedAccounts(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}

	var accounts []interface{}
	if s.ReqLog != nil {
		for _, a := range s.ReqLog.FailedAccounts() {
			accounts = append(accounts, a)
		}
	}
	if accounts == nil {
		accounts = []interface{}{}
	}

	writeJSON(w, 200, map[string]interface{}{
		"accounts": accounts,
		"total":    len(accounts),
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

// HandleTokenCreditsRefresh handles POST /api/v1/tokens/{id}/credits/refresh.
func (s *Server) HandleTokenCreditsRefresh(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAdmin(r); err != nil {
		writeJSON(w, 401, map[string]string{"detail": "unauthorized"})
		return
	}
	// Extract token ID from path: /api/v1/tokens/{id}/credits/refresh
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
				"ok": false, "detail": "Leonardo积分刷新失败: " + err.Error(),
			})
			return
		}
		// Update token credits and expiry in the pool
		s.TokenMgr.UpdateCredits(tokenID, float64(credits.TotalTokens), float64(credits.SubscriptionTokens+credits.PaidTokens+credits.RolloverTokens))
		s.TokenMgr.UpdateExpiry(tokenID, float64(session.JWTExpiry.Unix()))
		s.TokenMgr.UpdateAccountInfo(tokenID, session.HasuraUserID, session.Email)
		writeJSON(w, 200, map[string]interface{}{
			"ok":                true,
			"credits_available": credits.TotalTokens,
			"credits_total":     credits.SubscriptionTokens + credits.PaidTokens + credits.RolloverTokens,
			"plan":              credits.Plan,
			"email":             session.Email,
			"jwt_remaining":     session.GetJWTRemainingSeconds(),
		})
		return
	}

	// For non-Leonardo tokens, return stub
	writeJSON(w, 200, map[string]interface{}{"ok": true, "message": "credits refresh not supported for this platform"})
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

var startTime = time.Now()

const maxRemoteImageBytes = 20 << 20

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
	session, usedTokenID := s.getLeonardoSession(body.TokenID)
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
	var videoRefs []leonardo.VideoRef
	for _, vr := range body.VideoReference {
		videoRefs = append(videoRefs, leonardo.VideoRef{
			ID:       vr.ID,
			Type:     vr.Type,
			Duration: vr.Duration,
		})
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

	if err != nil {
		// Log failed request
		if s.ReqLog != nil {
			s.ReqLog.Add(reqlog.Entry{
				ID:          fmt.Sprintf("leo-%d", time.Now().UnixNano()),
				StatusCode:  500,
				TaskStatus:  "FAILED",
				Type:        "video",
				DurationSec: int(elapsedSec),
				Model:       fmt.Sprintf("%s (%dx%d %ds)", body.Model, body.Width, body.Height, body.Duration),
				Prompt:      body.Prompt,
				ErrorCode:   err.Error(),
				Operation:   "leonardo.generate",
			})
		}
		writeJSON(w, 500, map[string]interface{}{
			"detail": fmt.Sprintf("generation failed: %v", err),
		})
		return
	}

	// Log pending request
	if s.ReqLog != nil {
		s.ReqLog.Add(reqlog.Entry{
			ID:           fmt.Sprintf("leo-%d", time.Now().UnixNano()),
			StatusCode:   200,
			TaskStatus:   "IN_PROGRESS",
			Type:         "video",
			DurationSec:  int(elapsedSec),
			Model:        fmt.Sprintf("%s (%dx%d %ds)", body.Model, body.Width, body.Height, body.Duration),
			Prompt:       body.Prompt,
			GenerationID: result.GenerationID,
			CreditCost:   result.APICreditCost,
			Operation:    "leonardo.generate",
		})
	}

	// Background polling goroutine to auto-update log status
	go s.pollGenerationStatus(session, result.GenerationID, usedTokenID)

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
					v["mp4_url"] = img.MotionMP4
					if firstMP4 == "" {
						firstMP4 = img.MotionMP4
					}
				}
				if img.MotionGIF != "" {
					v["gif_url"] = img.MotionGIF
				}
				if img.URL != "" {
					v["thumbnail_url"] = img.URL
					if firstThumb == "" {
						firstThumb = img.URL
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
				s.ReqLog.UpdateByGenerationID(genID, "COMPLETE", previewURL, previewKind)
			}
		}
	} else if status.Status == "FAILED" {
		if s.ReqLog != nil {
			s.ReqLog.UpdateByGenerationID(genID, "FAILED", "", "")
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

func (s *Server) uploadLeonardoImageFromURL(session *leonardo.TokenSession, remoteURL string) (string, error) {
	imageData, contentType, ext, err := s.downloadRemoteImage(remoteURL)
	if err != nil {
		return "", err
	}
	return s.uploadLeonardoImageBytes(session, imageData, ext, contentType)
}

func (s *Server) uploadLeonardoImageBytes(session *leonardo.TokenSession, imageData []byte, ext, contentType string) (string, error) {
	initResult, err := s.LeonardoClient.UploadInitImage(session, ext)
	if err != nil {
		return "", fmt.Errorf("upload init failed: %w", err)
	}
	if err := s.LeonardoClient.UploadImageToS3(initResult.URL, initResult.Fields, initResult.Key, imageData, contentType); err != nil {
		return "", fmt.Errorf("s3 upload failed: %w", err)
	}
	return initResult.ID, nil
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

	httpClient, err := s.newResourceHTTPClient(30 * time.Second)
	if err != nil {
		return nil, "", "", err
	}

	req, err := http.NewRequest("GET", parsedURL.String(), nil)
	if err != nil {
		return nil, "", "", err
	}
	req.Header.Set("User-Agent", "leo-go-image-fetch/1.0")
	req.Header.Set("Accept", "image/*,*/*;q=0.8")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, "", "", fmt.Errorf("fetch image url failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", "", fmt.Errorf("image url returned %d", resp.StatusCode)
	}

	imageData, err := io.ReadAll(io.LimitReader(resp.Body, maxRemoteImageBytes+1))
	if err != nil {
		return nil, "", "", fmt.Errorf("read image url failed: %w", err)
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

// pollGenerationStatus runs in a goroutine to auto-update log status.
func (s *Server) pollGenerationStatus(session *leonardo.TokenSession, genID string, tokenID string) {
	const (
		pollInterval = 10 * time.Second
		maxDuration  = 10 * time.Minute
	)

	deadline := time.Now().Add(maxDuration)
	startTime := time.Now()

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
				if s.ReqLog != nil {
					s.ReqLog.UpdateByGenerationID(genID, "COMPLETE", previewURL, previewKind)
					s.ReqLog.UpdateDuration(genID, elapsed)
				}
			} else {
				if s.ReqLog != nil {
					s.ReqLog.UpdateByGenerationID(genID, "COMPLETE", "", "")
					s.ReqLog.UpdateDuration(genID, elapsed)
				}
			}
			// Refresh token credits
			s.refreshTokenCredits(tokenID, session)
			return

		case "FAILED":
			log.Printf("[poll] generation %s failed (%.1fs)", genID, elapsed)
			if s.ReqLog != nil {
				s.ReqLog.UpdateByGenerationID(genID, "FAILED", "", "")
				s.ReqLog.UpdateDuration(genID, elapsed)
			}
			return
		}
		// Still PENDING, continue polling
	}

	// Timeout
	log.Printf("[poll] generation %s timed out after %v", genID, maxDuration)
	if s.ReqLog != nil {
		s.ReqLog.UpdateByGenerationID(genID, "FAILED", "", "")
	}
}

// refreshTokenCredits queries latest credits from Leonardo and updates the token pool.
func (s *Server) refreshTokenCredits(tokenID string, session *leonardo.TokenSession) {
	if tokenID == "" || session == nil {
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

// getLeonardoSession finds a Leonardo session from the token pool.
// Returns the session and the token ID used.
func (s *Server) getLeonardoSession(tokenID string) (*leonardo.TokenSession, string) {
	// If specific tokenID provided, use that
	if tokenID != "" {
		info := s.TokenMgr.GetByID(tokenID)
		if info == nil {
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

	// Otherwise find first available Leonardo token
	tokens := s.TokenMgr.ListFull()
	for _, t := range tokens {
		platform, _ := t["platform"].(string)
		status, _ := t["status"].(string)
		if platform == "leonardo" && status == "active" {
			rawToken, _ := t["value"].(string)
			if rawToken == "" {
				continue
			}
			foundID, _ := t["id"].(string)
			session := s.getOrCreateLeonardoSession(foundID, rawToken)
			if session == nil {
				continue
			}
			if err := s.LeonardoClient.EnsureValidJWT(session); err != nil {
				continue
			}
			return session, foundID
		}
	}
	return nil, ""
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

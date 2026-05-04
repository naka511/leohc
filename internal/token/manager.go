package token

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"sync"
	"time"

	"leo-go/internal/store"
)

// Token represents a single account token in the pool.
type Token struct {
	ID                 string  `json:"id"`
	Value              string  `json:"value"`
	Platform           string  `json:"platform"`   // "leonardo", etc.
	TokenType          string  `json:"token_type"` // "session_token", "cookie", "api_key"
	Status             string  `json:"status"`     // "active", "invalid", "exhausted", "disabled"
	Fails              int     `json:"fails"`
	SuccessCount       int     `json:"success_count"`
	TotalSuccessCount  int     `json:"total_success_count"`
	AddedAt            float64 `json:"added_at"`
	LastUsedAt         float64 `json:"last_used_at,omitempty"`
	ErrorUntil         float64 `json:"error_until,omitempty"`
	AccountName        string  `json:"account_name,omitempty"`
	AccountEmail       string  `json:"account_email,omitempty"`
	AccountUserID      string  `json:"account_user_id,omitempty"`
	Source             string  `json:"source,omitempty"`
	AutoRefresh        bool    `json:"auto_refresh"`
	RefreshProfileID   string  `json:"refresh_profile_id,omitempty"`
	RefreshProfileName string  `json:"refresh_profile_name,omitempty"`
	ExpiresAt          float64 `json:"expires_at,omitempty"`
	Credits            float64 `json:"credits,omitempty"`
	MaxCredits         float64 `json:"max_credits,omitempty"`
}

// Manager manages the token pool with thread-safe operations.
type Manager struct {
	mu      sync.Mutex
	tokens  []*Token
	store   *store.SQLiteStore
	rrIndex int // round-robin index
}

// NewManager creates a new token manager.
func NewManager(sqliteStore *store.SQLiteStore) *Manager {
	m := &Manager{
		store: sqliteStore,
	}
	m.load()
	return m
}

func (m *Manager) load() {
	if m.store == nil {
		return
	}
	rows, err := m.store.LoadTokens()
	if err != nil {
		log.Printf("[token_mgr] failed to load tokens: %v", err)
		return
	}
	for _, row := range rows {
		t := mapToToken(row)
		if t.ID != "" && t.Value != "" {
			m.tokens = append(m.tokens, t)
		}
	}
	log.Printf("[token_mgr] loaded %d tokens", len(m.tokens))
}

func (m *Manager) save() {
	if m.store == nil {
		return
	}
	var rows []map[string]interface{}
	for _, t := range m.tokens {
		rows = append(rows, tokenToMap(t))
	}
	if err := m.store.ReplaceTokens(rows); err != nil {
		log.Printf("[token_mgr] failed to save tokens: %v", err)
	}
}

// TokenValueHash returns a short hash of a token value for dedup.
func TokenValueHash(value string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return base64.RawURLEncoding.EncodeToString(h[:8])
}

// GenerateTokenID generates a short unique ID for a token.
func GenerateTokenID(value string) string {
	hash := TokenValueHash(value)
	if len(hash) > 8 {
		hash = hash[:8]
	}
	return hash
}

// Add adds a new token to the pool. Returns the token info and whether it was a duplicate.
func (m *Manager) Add(value, platform, tokenType, accountName, accountEmail, source string) (map[string]interface{}, bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, false, fmt.Errorf("token value is required")
	}
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform == "" {
		platform = "leonardo"
	}
	tokenType = strings.ToLower(strings.TrimSpace(tokenType))
	if tokenType == "" {
		tokenType = "session_token"
	}
	defaultAutoRefresh := platform == "leonardo" && strings.EqualFold(strings.TrimSpace(source), "cookie_import")

	tokenID := GenerateTokenID(value)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check for duplicate by value hash
	for _, t := range m.tokens {
		if t.ID == tokenID || strings.TrimSpace(t.Value) == value {
			// Update existing
			t.Value = value
			t.Status = "active"
			t.Fails = 0
			t.ErrorUntil = 0
			if accountName != "" {
				t.AccountName = accountName
			}
			if accountEmail != "" {
				t.AccountEmail = accountEmail
			}
			if source != "" {
				t.Source = source
			}
			if defaultAutoRefresh {
				t.AutoRefresh = true
			}
			m.save()
			return tokenToMap(t), true, nil
		}
	}

	now := float64(time.Now().Unix())
	t := &Token{
		ID:           tokenID,
		Value:        value,
		Platform:     platform,
		TokenType:    tokenType,
		Status:       "active",
		AddedAt:      now,
		AccountName:  accountName,
		AccountEmail: accountEmail,
		Source:       source,
		AutoRefresh:  defaultAutoRefresh,
	}
	m.tokens = append(m.tokens, t)
	m.save()
	return tokenToMap(t), false, nil
}

// Remove removes a token by ID.
func (m *Manager) Remove(tokenID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	idx := -1
	for i, t := range m.tokens {
		if t.ID == tokenID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("token not found")
	}
	m.tokens = append(m.tokens[:idx], m.tokens[idx+1:]...)
	m.save()
	return nil
}

// GetByID returns a token by its ID.
func (m *Manager) GetByID(tokenID string) map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tokens {
		if t.ID == tokenID {
			return tokenToMap(t)
		}
	}
	return nil
}

// GetAvailable returns the next available token using the given strategy.
func (m *Manager) GetAvailable(strategy string) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := float64(time.Now().Unix())
	var active []*Token
	for _, t := range m.tokens {
		if t.Status == "active" && (t.ErrorUntil == 0 || now >= t.ErrorUntil) {
			active = append(active, t)
		}
	}
	if len(active) == 0 {
		return ""
	}

	var chosen *Token
	switch strings.ToLower(strategy) {
	case "random":
		chosen = active[rand.Intn(len(active))]
	default: // round_robin
		if m.rrIndex >= len(active) {
			m.rrIndex = 0
		}
		chosen = active[m.rrIndex]
		m.rrIndex++
		if m.rrIndex >= len(active) {
			m.rrIndex = 0
		}
	}
	chosen.LastUsedAt = now
	return chosen.Value
}

// GetAvailableForPlatform returns the next available token for a specific platform.
func (m *Manager) GetAvailableForPlatform(platform, strategy string) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := float64(time.Now().Unix())
	var active []*Token
	for _, t := range m.tokens {
		if t.Platform == platform && t.Status == "active" && (t.ErrorUntil == 0 || now >= t.ErrorUntil) {
			active = append(active, t)
		}
	}
	if len(active) == 0 {
		return ""
	}

	var chosen *Token
	switch strings.ToLower(strategy) {
	case "random":
		chosen = active[rand.Intn(len(active))]
	default:
		if m.rrIndex >= len(active) {
			m.rrIndex = 0
		}
		chosen = active[m.rrIndex]
		m.rrIndex++
	}
	chosen.LastUsedAt = now
	return chosen.Value
}

// GetAvailableTokenForPlatform returns the next available token info for a specific platform.
func (m *Manager) GetAvailableTokenForPlatform(platform, strategy string) map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := float64(time.Now().Unix())
	var active []*Token
	for _, t := range m.tokens {
		if t.Platform == platform && t.Status == "active" && (t.ErrorUntil == 0 || now >= t.ErrorUntil) {
			active = append(active, t)
		}
	}
	if len(active) == 0 {
		return nil
	}

	var chosen *Token
	switch strings.ToLower(strategy) {
	case "random":
		chosen = active[rand.Intn(len(active))]
	default:
		if m.rrIndex >= len(active) {
			m.rrIndex = 0
		}
		chosen = active[m.rrIndex]
		m.rrIndex++
		if m.rrIndex >= len(active) {
			m.rrIndex = 0
		}
	}
	chosen.LastUsedAt = now
	return tokenToMap(chosen)
}

// ReportSuccess marks a token as successfully used.
func (m *Manager) ReportSuccess(tokenValue string) map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()

	t := m.findByValue(tokenValue)
	if t == nil {
		return nil
	}
	t.Fails = 0
	t.SuccessCount++
	t.TotalSuccessCount++
	t.ErrorUntil = 0
	m.save()
	return tokenToMap(t)
}

// ReportSuccessWithAutoDisable marks success and optionally disables if threshold reached.
func (m *Manager) ReportSuccessWithAutoDisable(tokenValue string, autoDisableEnabled bool, threshold int) map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()

	t := m.findByValue(tokenValue)
	if t == nil {
		return nil
	}
	t.Fails = 0
	t.SuccessCount++
	t.TotalSuccessCount++
	t.ErrorUntil = 0

	if autoDisableEnabled && threshold > 0 && t.SuccessCount >= threshold {
		t.Status = "exhausted"
	}
	m.save()
	return tokenToMap(t)
}

// ReportInvalid marks a token as invalid.
func (m *Manager) ReportInvalid(tokenValue string) map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()

	t := m.findByValue(tokenValue)
	if t == nil {
		return nil
	}
	t.Status = "invalid"
	m.save()
	return tokenToMap(t)
}

// ReportExhausted marks a token as exhausted.
func (m *Manager) ReportExhausted(tokenValue string) map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()

	t := m.findByValue(tokenValue)
	if t == nil {
		return nil
	}
	t.Status = "exhausted"
	m.save()
	return tokenToMap(t)
}

// ReportFail increments fails and backs off if threshold reached.
func (m *Manager) ReportFail(tokenValue string) map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()

	t := m.findByValue(tokenValue)
	if t == nil {
		return nil
	}
	t.Fails++

	// Exponential backoff for repeated failures
	delays := []int{60, 180, 600, 1800}
	idx := t.Fails - 1
	if idx >= len(delays) {
		idx = len(delays) - 1
	}
	if idx < 0 {
		idx = 0
	}
	t.ErrorUntil = float64(time.Now().Unix()) + float64(delays[idx])
	m.save()
	return tokenToMap(t)
}

// SetStatus sets a token's status.
func (m *Manager) SetStatus(tokenID, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, t := range m.tokens {
		if t.ID == tokenID {
			t.Status = status
			if status == "active" {
				t.Fails = 0
				t.ErrorUntil = 0
			}
			m.save()
			return nil
		}
	}
	return fmt.Errorf("token not found")
}

// List returns a summary of all tokens.
func (m *Manager) List() []map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []map[string]interface{}
	for _, t := range m.tokens {
		out = append(out, tokenToSummary(t))
	}
	return out
}

// ListFull returns full token info.
func (m *Manager) ListFull() []map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []map[string]interface{}
	for _, t := range m.tokens {
		out = append(out, tokenToMap(t))
	}
	return out
}

// Stats returns token pool statistics.
func (m *Manager) Stats() map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()

	total := len(m.tokens)
	active := 0
	invalid := 0
	exhausted := 0
	disabled := 0
	autoRefresh := 0
	now := float64(time.Now().Unix())

	for _, t := range m.tokens {
		switch t.Status {
		case "active":
			if t.ErrorUntil == 0 || now >= t.ErrorUntil {
				active++
			}
		case "invalid":
			invalid++
		case "exhausted":
			exhausted++
		case "disabled":
			disabled++
		}
		if t.AutoRefresh {
			autoRefresh++
		}
	}

	return map[string]interface{}{
		"total":        total,
		"active":       active,
		"invalid":      invalid,
		"exhausted":    exhausted,
		"disabled":     disabled,
		"auto_refresh": autoRefresh,
	}
}

// RemoveAutoRefreshByProfile removes auto-refresh binding for a profile.
func (m *Manager) RemoveAutoRefreshByProfile(profileID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	changed := false
	for _, t := range m.tokens {
		if t.RefreshProfileID == profileID {
			t.AutoRefresh = false
			t.RefreshProfileID = ""
			t.RefreshProfileName = ""
			changed = true
		}
	}
	if changed {
		m.save()
	}
}

// UpsertAutoRefreshed upserts a token that was automatically refreshed.
func (m *Manager) UpsertAutoRefreshed(value, accountName, accountEmail, userID, source, profileID, profileName string) (map[string]interface{}, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, false
	}

	tokenID := GenerateTokenID(value)
	now := float64(time.Now().Unix())

	m.mu.Lock()
	defer m.mu.Unlock()

	// First, find by profile ID
	for _, t := range m.tokens {
		if t.RefreshProfileID == profileID && profileID != "" {
			t.Value = value
			t.ID = tokenID
			t.Status = "active"
			t.Fails = 0
			t.ErrorUntil = 0
			t.SuccessCount = 0
			if accountName != "" {
				t.AccountName = accountName
			}
			if accountEmail != "" {
				t.AccountEmail = accountEmail
			}
			if userID != "" {
				t.AccountUserID = userID
			}
			if source != "" {
				t.Source = source
			}
			t.AutoRefresh = true
			t.RefreshProfileID = profileID
			t.RefreshProfileName = profileName
			m.save()
			return tokenToMap(t), true
		}
	}

	// Check for duplicate by value
	for _, t := range m.tokens {
		if t.ID == tokenID || strings.TrimSpace(t.Value) == value {
			t.Value = value
			t.Status = "active"
			t.Fails = 0
			t.ErrorUntil = 0
			t.SuccessCount = 0
			if accountName != "" {
				t.AccountName = accountName
			}
			if accountEmail != "" {
				t.AccountEmail = accountEmail
			}
			t.AutoRefresh = true
			t.RefreshProfileID = profileID
			t.RefreshProfileName = profileName
			m.save()
			return tokenToMap(t), true
		}
	}

	// New token
	t := &Token{
		ID:                 tokenID,
		Value:              value,
		Platform:           "leonardo",
		TokenType:          "session_token",
		Status:             "active",
		AddedAt:            now,
		AccountName:        accountName,
		AccountEmail:       accountEmail,
		AccountUserID:      userID,
		Source:             source,
		AutoRefresh:        true,
		RefreshProfileID:   profileID,
		RefreshProfileName: profileName,
	}
	m.tokens = append(m.tokens, t)
	m.save()
	return tokenToMap(t), false
}

// SetAutoRefresh sets the auto_refresh flag for a token by ID.
func (m *Manager) SetAutoRefresh(tokenID string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tokens {
		if t.ID == tokenID {
			t.AutoRefresh = enabled
			m.save()
			log.Printf("[token_mgr] auto_refresh set to %v for token %s", enabled, tokenID)
			return nil
		}
	}
	return fmt.Errorf("token not found")
}

// UpdateCredits updates the credits info for a token by ID.
func (m *Manager) UpdateCredits(tokenID string, credits, maxCredits float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tokens {
		if t.ID == tokenID {
			t.Credits = credits
			t.MaxCredits = maxCredits
			m.save()
			return nil
		}
	}
	return fmt.Errorf("token not found")
}

// UpdateExpiry updates the expiry time for a token by ID.
func (m *Manager) UpdateExpiry(tokenID string, expiresAt float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tokens {
		if t.ID == tokenID {
			t.ExpiresAt = expiresAt
			m.save()
			return nil
		}
	}
	return fmt.Errorf("token not found")
}

// UpdateAccountInfo updates account name and email for a token by ID.
func (m *Manager) UpdateAccountInfo(tokenID, name, email string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tokens {
		if t.ID == tokenID {
			if name != "" {
				t.AccountName = name
			}
			if email != "" {
				t.AccountEmail = email
			}
			m.save()
			return nil
		}
	}
	return fmt.Errorf("token not found")
}

// Count returns the total number of tokens.
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.tokens)
}

// findByValue finds a token by its raw value (must hold lock).
func (m *Manager) findByValue(value string) *Token {
	value = strings.TrimSpace(value)
	for _, t := range m.tokens {
		if strings.TrimSpace(t.Value) == value {
			return t
		}
	}
	return nil
}

// ---- serialization helpers ----

func tokenToMap(t *Token) map[string]interface{} {
	raw, _ := json.Marshal(t)
	var m map[string]interface{}
	json.Unmarshal(raw, &m)
	return m
}

func tokenToSummary(t *Token) map[string]interface{} {
	m := map[string]interface{}{
		"id":                   t.ID,
		"platform":             t.Platform,
		"token_type":           t.TokenType,
		"status":               t.Status,
		"fails":                t.Fails,
		"success_count":        t.SuccessCount,
		"total_success_count":  t.TotalSuccessCount,
		"added_at":             t.AddedAt,
		"last_used_at":         t.LastUsedAt,
		"error_until":          t.ErrorUntil,
		"account_name":         t.AccountName,
		"account_email":        t.AccountEmail,
		"source":               t.Source,
		"auto_refresh":         t.AutoRefresh,
		"refresh_profile_id":   t.RefreshProfileID,
		"refresh_profile_name": t.RefreshProfileName,
		"value_preview":        maskTokenValue(t.Value),
	}
	// Credits info for frontend
	if t.Credits > 0 || t.MaxCredits > 0 {
		m["credits_available"] = t.Credits
		m["credits_total"] = t.MaxCredits
	}
	// Expiry info for frontend
	if t.ExpiresAt > 0 {
		now := float64(time.Now().Unix())
		remaining := t.ExpiresAt - now
		m["expires_at"] = t.ExpiresAt
		m["remaining_seconds"] = int(remaining)
		m["is_expired"] = remaining <= 0
		expTime := time.Unix(int64(t.ExpiresAt), 0)
		m["expires_at_text"] = expTime.Format("2006-01-02 15:04")
	}
	return m
}

func maskTokenValue(value string) string {
	if len(value) <= 10 {
		return "***"
	}
	return value[:5] + "..." + value[len(value)-5:]
}

func mapToToken(m map[string]interface{}) *Token {
	raw, _ := json.Marshal(m)
	var t Token
	json.Unmarshal(raw, &t)
	if t.Platform == "" {
		t.Platform = "leonardo"
	}
	if t.TokenType == "" {
		t.TokenType = "session_token"
	}
	return &t
}

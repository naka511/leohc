package leonardo

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ──────────────────────────────────────────────────────────
// Leonardo Token 体系：
//   用户提交完整 cookie 字符串（从浏览器复制）
//   通过 get-session 接口换取 Cognito JWT（约1小时有效）
//   JWT 用于调用 GraphQL API
// ──────────────────────────────────────────────────────────

const (
	SessionURL   = "https://app.leonardo.ai/api/auth/get-session"
	GraphQLURL   = "https://api.leonardo.ai/v1/graphql"
	JWTMarginSec = 300 // JWT 过期前 5 分钟就刷新
)

const (
	defaultClientTimeout = 120 * time.Second
	defaultInitWait      = 180 * time.Second
	s3UploadMaxAttempts  = 3
	s3UploadRetryDelay   = 2 * time.Second
)

const defaultJWTRefreshMargin = 5 * time.Minute

func isRetryableGraphQLError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unexpected eof") ||
		strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "connection reset")
}

// TokenSession holds a Leonardo session with cached JWT.
type TokenSession struct {
	mu            sync.RWMutex
	FullCookie    string    // 完整的 cookie 字符串（用户从浏览器复制的）
	JWT           string    // Cognito id_token (short-lived, ~1h)
	JWTExpiry     time.Time // JWT expiration time
	CognitoSub    string    // e.g. "5f2e877a-0c1a-4ea1-b893-bfb4a6567a22"
	HasuraUserID  string    // e.g. "d5b484fd-1dcc-4cf5-a7a1-9ea83abd41ce"
	Email         string
	Plan          string
	LastRefreshed time.Time
}

// Credits holds Leonardo credit/token balances.
type Credits struct {
	PaidTokens         int    `json:"paidTokens"`
	SubscriptionTokens int    `json:"subscriptionTokens"`
	RolloverTokens     int    `json:"rolloverTokens"`
	Plan               string `json:"plan"`
	TokenRenewalDate   string `json:"tokenRenewalDate"`
	TotalTokens        int    `json:"totalTokens"`
}

// Client manages Leonardo API interactions.
type Client struct {
	httpClient       *http.Client
	proxy            string
	jwtRefreshMargin time.Duration
}

// NewClient creates a new Leonardo client.
func NewClient(proxy string) *Client {
	transport := &http.Transport{}
	if proxy != "" {
		if proxyURL, err := url.Parse(proxy); err == nil {
			transport.Proxy = http.ProxyURL(proxyURL)
		}
	}
	return &Client{
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   defaultClientTimeout,
		},
		proxy:            proxy,
		jwtRefreshMargin: defaultJWTRefreshMargin,
	}
}

func (c *Client) SetJWTRefreshMarginMinutes(minutes int) {
	if c == nil {
		return
	}
	if minutes < 0 {
		minutes = 0
	}
	if minutes > 1440 {
		minutes = 1440
	}
	c.jwtRefreshMargin = time.Duration(minutes) * time.Minute
}

func (c *Client) jwtRefreshMarginDuration() time.Duration {
	if c == nil {
		return defaultJWTRefreshMargin
	}
	if c.jwtRefreshMargin < 0 {
		return 0
	}
	return c.jwtRefreshMargin
}

// ──────────────────────────────────────────────────────────
// JWT Parsing (no verification, just decode payload)
// ──────────────────────────────────────────────────────────

type jwtClaims struct {
	Sub           string   `json:"sub"`
	Email         string   `json:"email"`
	Exp           int64    `json:"exp"`
	Iat           int64    `json:"iat"`
	HasuraClaims  string   `json:"https://hasura.io/jwt/claims"`
	CognitoGroups []string `json:"cognito:groups"`
}

type hasuraClaims struct {
	UserID       string   `json:"x-hasura-user-id"`
	DefaultRole  string   `json:"x-hasura-default-role"`
	AllowedRoles []string `json:"x-hasura-allowed-roles"`
}

// parseJWT decodes the JWT payload without verification.
func parseJWT(token string) (*jwtClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT format: expected 3 parts, got %d", len(parts))
	}

	// Base64url decode the payload
	payload := parts[1]
	// Add padding if needed
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}
	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to decode JWT payload: %w", err)
	}

	var claims jwtClaims
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return nil, fmt.Errorf("failed to parse JWT claims: %w", err)
	}
	return &claims, nil
}

// parseHasuraClaims extracts hasura user ID from JWT claims.
func parseHasuraClaims(raw string) (*hasuraClaims, error) {
	var hc hasuraClaims
	if err := json.Unmarshal([]byte(raw), &hc); err != nil {
		return nil, err
	}
	return &hc, nil
}

// ──────────────────────────────────────────────────────────
// Cookie 处理工具
// ──────────────────────────────────────────────────────────

// NormalizeCookie cleans up the cookie string.
// Accepts both:
//   - Raw cookie header: "k1=v1; k2=v2; ..."
//   - Just session_token value (legacy): "AlYJi..."
func NormalizeCookie(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// If it looks like a full cookie string (contains "=")
	if strings.Contains(raw, "=") && strings.Contains(raw, ";") {
		return raw
	}
	// If it looks like a single cookie value (no "="), assume it's session_token
	if !strings.Contains(raw, "=") {
		return "__Secure-better-auth.session_token=" + raw
	}
	// Could be a single k=v pair without semicolons
	return raw
}

// ExtractSessionToken extracts __Secure-better-auth.session_token from cookie string.
func ExtractSessionToken(cookieStr string) string {
	for _, part := range strings.Split(cookieStr, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "__Secure-better-auth.session_token=") {
			return strings.TrimPrefix(part, "__Secure-better-auth.session_token=")
		}
	}
	return ""
}

// ──────────────────────────────────────────────────────────
// Session Refresh: cookie → JWT
// ──────────────────────────────────────────────────────────

// sessionResponse is the response from get-session.
type sessionResponse struct {
	Session struct {
		Token string `json:"token"`
	} `json:"session"`
}

// RefreshSession calls get-session to obtain a fresh JWT.
func (c *Client) RefreshSession(session *TokenSession) error {
	req, err := http.NewRequest("GET", SessionURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	// 发送完整 cookie 字符串，包含所有必要的 cookie
	cookieStr := NormalizeCookie(session.FullCookie)
	req.Header.Set("Cookie", cookieStr)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36")
	req.Header.Set("Referer", "https://app.leonardo.ai/")
	req.Header.Set("Origin", "https://app.leonardo.ai")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Ch-Ua", `"Chromium";v="140", "Not=A?Brand";v="24", "Google Chrome";v="140"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"Windows"`)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("get-session request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != 200 {
		return formatSessionHTTPError(resp.StatusCode, body)
	}

	// Parse response - Leonardo's get-session returns JSON with session info
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("parse session response: %w (body: %s)", err, string(body[:min(len(body), 200)]))
	}

	// Try to extract token from various response structures
	jwt := extractJWT(result)
	if jwt == "" {
		return fmt.Errorf("no JWT found in session response, body keys: %v", getKeys(result))
	}

	// Parse the JWT to get expiry and user info
	claims, err := parseJWT(jwt)
	if err != nil {
		return fmt.Errorf("parse JWT: %w", err)
	}

	session.mu.Lock()
	defer session.mu.Unlock()

	session.JWT = jwt
	session.JWTExpiry = time.Unix(claims.Exp, 0)
	session.CognitoSub = claims.Sub
	session.Email = claims.Email
	session.LastRefreshed = time.Now()

	// Extract Hasura user ID
	if claims.HasuraClaims != "" {
		if hc, err := parseHasuraClaims(claims.HasuraClaims); err == nil {
			session.HasuraUserID = hc.UserID
		}
	}

	log.Printf("[Leonardo] JWT refreshed for %s, expires %s, user=%s",
		session.Email, session.JWTExpiry.Format(time.RFC3339), session.HasuraUserID)

	return nil
}

func formatSessionHTTPError(statusCode int, body []byte) error {
	if statusCode == http.StatusTooManyRequests {
		return fmt.Errorf("Leonardo rate limited get-session (429). Wait a minute before retrying refresh")
	}

	snippet := strings.TrimSpace(string(body))
	if snippet == "" {
		return fmt.Errorf("get-session returned %d", statusCode)
	}

	snippetLower := strings.ToLower(snippet)
	if strings.HasPrefix(snippetLower, "<!doctype html") || strings.HasPrefix(snippetLower, "<html") {
		return fmt.Errorf("get-session returned %d with an HTML error page", statusCode)
	}

	if len(snippet) > 200 {
		snippet = snippet[:200]
	}
	return fmt.Errorf("get-session returned %d: %s", statusCode, snippet)
}

// extractJWT tries to find the JWT in the session response.
func extractJWT(data map[string]interface{}) string {
	// Try data.session.token
	if sess, ok := data["session"].(map[string]interface{}); ok {
		if token, ok := sess["token"].(string); ok && strings.Contains(token, ".") {
			return token
		}
		// Try session.idToken
		if token, ok := sess["idToken"].(string); ok && strings.Contains(token, ".") {
			return token
		}
		// Try session.accessToken
		if token, ok := sess["accessToken"].(string); ok && strings.Contains(token, ".") {
			return token
		}
	}
	// Try data.token
	if token, ok := data["token"].(string); ok && strings.Contains(token, ".") {
		return token
	}
	// Try data.idToken
	if token, ok := data["idToken"].(string); ok && strings.Contains(token, ".") {
		return token
	}
	// Try data.user.token
	if user, ok := data["user"].(map[string]interface{}); ok {
		if token, ok := user["token"].(string); ok && strings.Contains(token, ".") {
			return token
		}
	}
	return ""
}

// getKeys returns all keys of a map for debugging.
func getKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// ──────────────────────────────────────────────────────────
// EnsureValidJWT: auto-refresh if expired
// ──────────────────────────────────────────────────────────

// EnsureValidJWT checks if the JWT is still valid, refreshes if needed.
func (c *Client) EnsureValidJWT(session *TokenSession) error {
	session.mu.RLock()
	needsRefresh := session.JWT == "" || time.Now().Add(c.jwtRefreshMarginDuration()).After(session.JWTExpiry)
	session.mu.RUnlock()

	if needsRefresh {
		log.Printf("[Leonardo] JWT expired or missing, refreshing...")
		return c.RefreshSession(session)
	}
	return nil
}

// IsJWTValid returns true if the cached JWT is still valid.
func (s *TokenSession) IsJWTValid() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.JWT != "" && time.Now().Before(s.JWTExpiry)
}

// GetJWTRemainingSeconds returns seconds until JWT expiry.
func (s *TokenSession) GetJWTRemainingSeconds() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.JWT == "" {
		return 0
	}
	remaining := time.Until(s.JWTExpiry).Seconds()
	if remaining < 0 {
		return 0
	}
	return int(remaining)
}

// ──────────────────────────────────────────────────────────
// Credits Query via GraphQL
// ──────────────────────────────────────────────────────────

const getTokensQuery = `query GetUserTokensFromSub($sub: String) {
  user_details(where: {cognitoId: {_eq: $sub}}) {
    id
    plan
    subscriptionGptTokens
    subscriptionModelTokens
    tokenRenewalDate
    streamTokens
    paidTokens
    subscriptionTokens
    rolloverTokens
  }
}`

// graphqlRequest is the request body for GraphQL calls.
type graphqlRequest struct {
	OperationName string                 `json:"operationName"`
	Variables     map[string]interface{} `json:"variables"`
	Query         string                 `json:"query"`
}

// QueryCredits fetches the user's token balance.
func (c *Client) QueryCredits(session *TokenSession) (*Credits, error) {
	// Ensure we have a valid JWT
	if err := c.EnsureValidJWT(session); err != nil {
		return nil, fmt.Errorf("ensure JWT: %w", err)
	}

	session.mu.RLock()
	jwt := session.JWT
	sub := session.CognitoSub
	session.mu.RUnlock()

	// Build GraphQL request
	gqlReq := graphqlRequest{
		OperationName: "GetUserTokensFromSub",
		Variables:     map[string]interface{}{"sub": sub},
		Query:         getTokensQuery,
	}

	reqBody, err := json.Marshal(gqlReq)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", GraphQLURL, strings.NewReader(string(reqBody)))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Origin", "https://app.leonardo.ai")
	req.Header.Set("Referer", "https://app.leonardo.ai/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("graphql request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("graphql returned %d: %s", resp.StatusCode, string(body[:min(len(body), 300)]))
	}

	// Parse response
	var gqlResp struct {
		Data struct {
			UserDetails []struct {
				ID                      string `json:"id"`
				Plan                    string `json:"plan"`
				PaidTokens              int    `json:"paidTokens"`
				SubscriptionTokens      int    `json:"subscriptionTokens"`
				RolloverTokens          int    `json:"rolloverTokens"`
				SubscriptionGptTokens   int    `json:"subscriptionGptTokens"`
				SubscriptionModelTokens int    `json:"subscriptionModelTokens"`
				TokenRenewalDate        string `json:"tokenRenewalDate"`
				StreamTokens            int    `json:"streamTokens"`
			} `json:"user_details"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}

	if err := json.Unmarshal(body, &gqlResp); err != nil {
		return nil, fmt.Errorf("parse graphql response: %w", err)
	}

	if len(gqlResp.Errors) > 0 {
		return nil, fmt.Errorf("graphql error: %s", gqlResp.Errors[0].Message)
	}

	if len(gqlResp.Data.UserDetails) == 0 {
		return nil, fmt.Errorf("no user details found for sub %s", sub)
	}

	ud := gqlResp.Data.UserDetails[0]
	credits := &Credits{
		PaidTokens:         ud.PaidTokens,
		SubscriptionTokens: ud.SubscriptionTokens,
		RolloverTokens:     ud.RolloverTokens,
		Plan:               ud.Plan,
		TokenRenewalDate:   ud.TokenRenewalDate,
		TotalTokens:        ud.PaidTokens + ud.SubscriptionTokens + ud.RolloverTokens,
	}

	// Update session plan info
	session.mu.Lock()
	session.Plan = ud.Plan
	session.mu.Unlock()

	return credits, nil
}

// ──────────────────────────────────────────────────────────
// Validate: check if cookie is valid
// ──────────────────────────────────────────────────────────

// ValidateToken checks if the cookie can produce a valid JWT and has credits.
func (c *Client) ValidateToken(fullCookie string) (*TokenSession, *Credits, error) {
	session := &TokenSession{
		FullCookie: fullCookie,
	}

	// Step 1: Refresh to get JWT
	if err := c.RefreshSession(session); err != nil {
		return nil, nil, fmt.Errorf("token validation failed: %w", err)
	}

	// Step 2: Query credits
	credits, err := c.QueryCredits(session)
	if err != nil {
		return session, nil, fmt.Errorf("credits query failed: %w", err)
	}

	return session, credits, nil
}

// ──────────────────────────────────────────────────────────
// Video Generation via GraphQL
// ──────────────────────────────────────────────────────────

const generateMutation = `mutation Generate($request: CreateGenerationRequest!) {
  generate(request: $request) {
    apiCreditCost
    generationId
    __typename
  }
}`

const statusQuery = `query GetAIGenerationFeedStatuses($where: generations_bool_exp = {}) {
  generations(where: $where) {
    id
    status
    __typename
  }
}`

const generationDetailQuery = `query GetGenerationDetail($where: generations_bool_exp = {}) {
  generations(where: $where) {
    id
    status
    prompt
    modelId
    motionModel
    imageWidth
    imageHeight
    createdAt
    generated_images(order_by: [{url: desc}]) {
      id
      url
      motionMP4URL
      motionGIFURL
      __typename
    }
    __typename
  }
}`

// ImageRef is a single image reference for guided generation (multi-image reference).
type ImageRef struct {
	ID       string `json:"id"`
	Type     string `json:"type"`     // "UPLOADED" or "GENERATED"
	Strength string `json:"strength"` // "LOW", "MID", "HIGH"
}

// FrameRef is a single start/end frame reference.
type FrameRef struct {
	ID   string `json:"id"`
	Type string `json:"type"` // "UPLOADED" or "GENERATED"
}

// VideoRef is a video reference for video-to-video guidance.
type VideoRef struct {
	ID       string  `json:"id"`
	Type     string  `json:"type"`     // "UPLOADED"
	Duration float64 `json:"duration"` // video duration in seconds
}

// GenerateRequest is the input for video generation.
type GenerateRequest struct {
	Model  string         `json:"model"`
	Public bool           `json:"public"`
	Params GenerateParams `json:"parameters"`
}

// GenerateParams are the generation parameters.
type GenerateParams struct {
	Prompt         string     `json:"prompt"`
	Mode           string     `json:"mode"`           // e.g. "RESOLUTION_720"
	PromptEnhance  string     `json:"prompt_enhance"` // "OFF" or "ON"
	Quantity       int        `json:"quantity"`
	Duration       int        `json:"duration"` // 4-15 seconds
	MotionHasAudio bool       `json:"motion_has_audio"`
	Width          int        `json:"width"`
	Height         int        `json:"height"`
	Seed           int        `json:"seed"`                  // -1 for random
	ImageRefs      []ImageRef `json:"image_refs,omitempty"`  // multi-image reference guidance
	StartFrame     []FrameRef `json:"start_frame,omitempty"` // start frame (first frame)
	EndFrame       []FrameRef `json:"end_frame,omitempty"`   // end frame (last frame)
	VideoRefs      []VideoRef `json:"video_refs,omitempty"`  // video reference guidance
}

// GenerateResponse is the response from the Generate mutation.
type GenerateResponse struct {
	GenerationID  string `json:"generationId"`
	APICreditCost int    `json:"apiCreditCost"`
}

// GenerationStatus holds the status of a generation.
type GenerationStatus struct {
	ID     string `json:"id"`
	Status string `json:"status"` // PENDING, COMPLETE, FAILED
}

// GenerationDetail holds detailed generation info including video URLs.
type GenerationDetail struct {
	ID        string           `json:"id"`
	Status    string           `json:"status"`
	Prompt    string           `json:"prompt"`
	ModelID   string           `json:"modelId"`
	Width     int              `json:"imageWidth"`
	Height    int              `json:"imageHeight"`
	CreatedAt string           `json:"createdAt"`
	Images    []GeneratedImage `json:"generated_images"`
}

// GeneratedImage holds info about a generated image/video.
type GeneratedImage struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	MotionMP4 string `json:"motionMP4URL"`
	MotionGIF string `json:"motionGIFURL"`
}

// doGraphQL sends a GraphQL request and returns the raw response body.
func (c *Client) doGraphQL(jwt string, gqlReq graphqlRequest) ([]byte, error) {
	reqBody, err := json.Marshal(gqlReq)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", GraphQLURL, strings.NewReader(string(reqBody)))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Origin", "https://app.leonardo.ai")
	req.Header.Set("Referer", "https://app.leonardo.ai/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36")
	req.Header.Set("X-Leo-Schema-Version", "latest")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("graphql request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("graphql returned %d: %s", resp.StatusCode, string(body[:min(len(body), 500)]))
	}

	return body, nil
}

// Generate submits a video generation request.
func (c *Client) Generate(session *TokenSession, genReq *GenerateRequest) (*GenerateResponse, error) {
	if err := c.EnsureValidJWT(session); err != nil {
		return nil, fmt.Errorf("ensure JWT: %w", err)
	}

	session.mu.RLock()
	jwt := session.JWT
	session.mu.RUnlock()

	// Set defaults
	if genReq.Params.Mode == "" {
		genReq.Params.Mode = "RESOLUTION_720"
	}
	if genReq.Params.Quantity == 0 {
		genReq.Params.Quantity = 1
	}
	if genReq.Params.Duration == 0 {
		genReq.Params.Duration = 4
	}
	if genReq.Params.Width == 0 {
		genReq.Params.Width = 1280
	}
	if genReq.Params.Height == 0 {
		genReq.Params.Height = 720
	}
	if genReq.Params.Seed == 0 {
		genReq.Params.Seed = -1
	}
	if genReq.Params.PromptEnhance == "" {
		genReq.Params.PromptEnhance = "OFF"
	}
	if genReq.Model == "" {
		genReq.Model = "seedance-2.0-fast"
	}

	params := map[string]interface{}{
		"prompt":           genReq.Params.Prompt,
		"mode":             genReq.Params.Mode,
		"prompt_enhance":   genReq.Params.PromptEnhance,
		"quantity":         genReq.Params.Quantity,
		"duration":         genReq.Params.Duration,
		"motion_has_audio": genReq.Params.MotionHasAudio,
		"width":            genReq.Params.Width,
		"height":           genReq.Params.Height,
		"seed":             genReq.Params.Seed,
	}

	// Build guidances map (supports image_reference, start_frame, end_frame)
	hasGuidances := len(genReq.Params.ImageRefs) > 0 || len(genReq.Params.StartFrame) > 0 || len(genReq.Params.EndFrame) > 0 || len(genReq.Params.VideoRefs) > 0
	if hasGuidances {
		guidances := map[string]interface{}{}

		// Multi-image reference guidance
		if len(genReq.Params.ImageRefs) > 0 {
			var refs []map[string]interface{}
			for _, ref := range genReq.Params.ImageRefs {
				imgType := ref.Type
				if imgType == "" {
					imgType = "UPLOADED"
				}
				strength := ref.Strength
				if strength == "" {
					strength = "MID"
				}
				refs = append(refs, map[string]interface{}{
					"image": map[string]interface{}{
						"id":   ref.ID,
						"type": imgType,
					},
					"strength": strength,
				})
			}
			guidances["image_reference"] = refs
			log.Printf("[Leonardo] Including %d image references in generation", len(refs))
		}

		// Start frame guidance
		if len(genReq.Params.StartFrame) > 0 {
			var frames []map[string]interface{}
			for _, f := range genReq.Params.StartFrame {
				fType := f.Type
				if fType == "" {
					fType = "UPLOADED"
				}
				frames = append(frames, map[string]interface{}{
					"image": map[string]interface{}{
						"id":   f.ID,
						"type": fType,
					},
				})
			}
			guidances["start_frame"] = frames
			log.Printf("[Leonardo] Including start_frame in generation")
		}

		// End frame guidance
		if len(genReq.Params.EndFrame) > 0 {
			var frames []map[string]interface{}
			for _, f := range genReq.Params.EndFrame {
				fType := f.Type
				if fType == "" {
					fType = "UPLOADED"
				}
				frames = append(frames, map[string]interface{}{
					"image": map[string]interface{}{
						"id":   f.ID,
						"type": fType,
					},
				})
			}
			guidances["end_frame"] = frames
			log.Printf("[Leonardo] Including end_frame in generation")
		}

		// Video reference guidance
		if len(genReq.Params.VideoRefs) > 0 {
			var refs []map[string]interface{}
			for _, v := range genReq.Params.VideoRefs {
				vType := v.Type
				if vType == "" {
					vType = "UPLOADED"
				}
				ref := map[string]interface{}{
					"video": map[string]interface{}{
						"id":   v.ID,
						"type": vType,
					},
				}
				if v.Duration > 0 {
					ref["video"].(map[string]interface{})["duration"] = v.Duration
				}
				refs = append(refs, ref)
			}
			guidances["video_reference_base"] = refs
			log.Printf("[Leonardo] Including video_reference_base in generation")
		}

		params["guidances"] = guidances
	}

	gqlReq := graphqlRequest{
		OperationName: "Generate",
		Variables: map[string]interface{}{
			"request": map[string]interface{}{
				"model":      genReq.Model,
				"public":     genReq.Public,
				"parameters": params,
			},
		},
		Query: generateMutation,
	}

	body, err := c.doGraphQL(jwt, gqlReq)
	if err != nil {
		return nil, err
	}

	var gqlResp struct {
		Data struct {
			Generate struct {
				APICreditCost int    `json:"apiCreditCost"`
				GenerationID  string `json:"generationId"`
			} `json:"generate"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}

	if err := json.Unmarshal(body, &gqlResp); err != nil {
		return nil, fmt.Errorf("parse generate response: %w", err)
	}

	if len(gqlResp.Errors) > 0 {
		return nil, fmt.Errorf("generate error: %s", gqlResp.Errors[0].Message)
	}

	log.Printf("[Leonardo] Generation submitted: id=%s, cost=%d tokens",
		gqlResp.Data.Generate.GenerationID, gqlResp.Data.Generate.APICreditCost)

	return &GenerateResponse{
		GenerationID:  gqlResp.Data.Generate.GenerationID,
		APICreditCost: gqlResp.Data.Generate.APICreditCost,
	}, nil
}

// PollGenerationStatus checks the status of a generation.
func (c *Client) PollGenerationStatus(session *TokenSession, generationID string) (*GenerationStatus, error) {
	if err := c.EnsureValidJWT(session); err != nil {
		return nil, fmt.Errorf("ensure JWT: %w", err)
	}

	session.mu.RLock()
	jwt := session.JWT
	session.mu.RUnlock()

	gqlReq := graphqlRequest{
		OperationName: "GetAIGenerationFeedStatuses",
		Variables: map[string]interface{}{
			"where": map[string]interface{}{
				"id": map[string]interface{}{
					"_in": []string{generationID},
				},
				"status": map[string]interface{}{
					"_in": []string{"PENDING", "COMPLETE", "FAILED"},
				},
			},
		},
		Query: statusQuery,
	}

	body, err := c.doGraphQL(jwt, gqlReq)
	if err != nil {
		return nil, err
	}

	var gqlResp struct {
		Data struct {
			Generations []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"generations"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}

	if err := json.Unmarshal(body, &gqlResp); err != nil {
		return nil, fmt.Errorf("parse status response: %w", err)
	}

	if len(gqlResp.Errors) > 0 {
		return nil, fmt.Errorf("status query error: %s", gqlResp.Errors[0].Message)
	}

	if len(gqlResp.Data.Generations) == 0 {
		return &GenerationStatus{ID: generationID, Status: "UNKNOWN"}, nil
	}

	gen := gqlResp.Data.Generations[0]
	return &GenerationStatus{
		ID:     gen.ID,
		Status: gen.Status,
	}, nil
}

// GetGenerationDetail fetches full details of a completed generation.
func (c *Client) GetGenerationDetail(session *TokenSession, generationID string) (*GenerationDetail, error) {
	if err := c.EnsureValidJWT(session); err != nil {
		return nil, fmt.Errorf("ensure JWT: %w", err)
	}

	session.mu.RLock()
	jwt := session.JWT
	session.mu.RUnlock()

	gqlReq := graphqlRequest{
		OperationName: "GetGenerationDetail",
		Variables: map[string]interface{}{
			"where": map[string]interface{}{
				"id": map[string]interface{}{
					"_in": []string{generationID},
				},
			},
		},
		Query: generationDetailQuery,
	}

	body, err := c.doGraphQL(jwt, gqlReq)
	if err != nil {
		return nil, err
	}

	var gqlResp struct {
		Data struct {
			Generations []GenerationDetail `json:"generations"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}

	if err := json.Unmarshal(body, &gqlResp); err != nil {
		return nil, fmt.Errorf("parse detail response: %w", err)
	}

	if len(gqlResp.Errors) > 0 {
		return nil, fmt.Errorf("detail query error: %s", gqlResp.Errors[0].Message)
	}

	if len(gqlResp.Data.Generations) == 0 {
		return nil, fmt.Errorf("generation %s not found", generationID)
	}

	return &gqlResp.Data.Generations[0], nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ──────────────────────────────────────────────────────────
// Image Upload for Guidance (multi-image reference)
// ──────────────────────────────────────────────────────────

const uploadImageMutation = `mutation UploadImage($uploadImageInput: UploadImageInput!) {
  uploadImage(arg1: $uploadImageInput) {
    uploadId
    url
    fields
    __typename
  }
}`

const initImageModerationQuery = `query GetInitImageModeration($akUUID: uuid!) {
  init_image_moderation(where: {akUUID: {_eq: $akUUID}}) {
    akUUID
    initImageId
    checkStatus
    __typename
  }
}`

const uploadedMediaByIDQuery = `query GetUploadedMediaById($uploadId: uuid!) {
  uploaded_media(where: {id: {_eq: $uploadId}}, limit: 1) {
    duration
    fileSize
    height
    id
    status
    statusReason
    thumbnailUrl
    url
    video_fps
    videoCodec
    width
    __typename
  }
}`

// UploadInitResult holds the response from the upload init mutation.
type UploadInitResult struct {
	UploadID string `json:"uploadId"`
	Fields   string `json:"fields"`
	URL      string `json:"url"`
}

type InitImageModeration struct {
	AKUUID      string `json:"akUUID"`
	InitImageID string `json:"initImageId"`
	CheckStatus string `json:"checkStatus"`
}

type UploadedMedia struct {
	ID           string   `json:"id"`
	Status       string   `json:"status"`
	StatusReason *string  `json:"statusReason"`
	URL          string   `json:"url"`
	ThumbnailURL *string  `json:"thumbnailUrl"`
	Duration     *float64 `json:"duration"`
	FileSize     *int64   `json:"fileSize"`
	Height       *int     `json:"height"`
	Width        *int     `json:"width"`
	VideoFPS     *float64 `json:"video_fps"`
	VideoCodec   *string  `json:"videoCodec"`
}

// UploadInitImage initializes an image upload slot on Leonardo.
// Returns the upload details including S3 presigned URL and fields.
func (c *Client) UploadInitImage(session *TokenSession, ext string) (*UploadInitResult, error) {
	if err := c.EnsureValidJWT(session); err != nil {
		return nil, fmt.Errorf("ensure JWT: %w", err)
	}

	session.mu.RLock()
	jwt := session.JWT
	session.mu.RUnlock()

	if ext == "" {
		ext = "jpg"
	}

	gqlReq := graphqlRequest{
		OperationName: "UploadImage",
		Variables: map[string]interface{}{
			"uploadImageInput": map[string]interface{}{
				"uploadType": "INIT",
				"extension":  ext,
			},
		},
		Query: uploadImageMutation,
	}

	body, err := c.doGraphQL(jwt, gqlReq)
	if err != nil {
		return nil, err
	}

	var gqlResp struct {
		Data struct {
			UploadImage UploadInitResult `json:"uploadImage"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}

	if err := json.Unmarshal(body, &gqlResp); err != nil {
		return nil, fmt.Errorf("parse upload init response: %w", err)
	}

	if len(gqlResp.Errors) > 0 {
		return nil, fmt.Errorf("upload init error: %s", gqlResp.Errors[0].Message)
	}

	result := &gqlResp.Data.UploadImage
	log.Printf("[Leonardo] Upload init: uploadId=%s, url=%s", result.UploadID, result.URL)
	return result, nil
}

// WaitForInitImage polls Leonardo moderation status until the uploaded image
// becomes available as a usable init image ID.
func (c *Client) WaitForInitImage(session *TokenSession, uploadID string, timeout time.Duration) (string, error) {
	if strings.TrimSpace(uploadID) == "" {
		return "", fmt.Errorf("upload id is required")
	}
	if timeout <= 0 {
		timeout = defaultInitWait
	}
	if err := c.EnsureValidJWT(session); err != nil {
		return "", fmt.Errorf("ensure JWT: %w", err)
	}

	deadline := time.Now().Add(timeout)
	pollInterval := 1500 * time.Millisecond
	lastStatus := ""

	for time.Now().Before(deadline) {
		session.mu.RLock()
		jwt := session.JWT
		session.mu.RUnlock()

		gqlReq := graphqlRequest{
			OperationName: "GetInitImageModeration",
			Variables: map[string]interface{}{
				"akUUID": uploadID,
			},
			Query: initImageModerationQuery,
		}

		body, err := c.doGraphQL(jwt, gqlReq)
		if err != nil {
			if isRetryableGraphQLError(err) {
				log.Printf("[Leonardo] Transient init image polling error for uploadID=%s: %v; retrying", uploadID, err)
				time.Sleep(pollInterval)
				continue
			}
			return "", err
		}

		var gqlResp struct {
			Data struct {
				InitImageModeration []InitImageModeration `json:"init_image_moderation"`
			} `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}

		if err := json.Unmarshal(body, &gqlResp); err != nil {
			return "", fmt.Errorf("parse init image moderation response: %w", err)
		}

		if len(gqlResp.Errors) > 0 {
			return "", fmt.Errorf("init image moderation error: %s", gqlResp.Errors[0].Message)
		}

		if len(gqlResp.Data.InitImageModeration) > 0 {
			item := gqlResp.Data.InitImageModeration[0]
			lastStatus = strings.ToUpper(strings.TrimSpace(item.CheckStatus))
			if strings.TrimSpace(item.InitImageID) != "" {
				return item.InitImageID, nil
			}
			switch lastStatus {
			case "FAILED", "REJECTED", "BLOCKED", "ERROR":
				return "", fmt.Errorf("init image moderation %s", strings.ToLower(lastStatus))
			}
		}

		time.Sleep(pollInterval)
	}

	if lastStatus != "" {
		return "", fmt.Errorf("timed out waiting for init image id (last status: %s)", lastStatus)
	}
	return "", fmt.Errorf("timed out waiting for init image moderation")
}

// WaitForUploadedMedia polls the uploaded_media table until the staged upload
// becomes a usable video asset with COMPLETE status.
func (c *Client) WaitForUploadedMedia(session *TokenSession, uploadID string, timeout time.Duration) (*UploadedMedia, error) {
	if strings.TrimSpace(uploadID) == "" {
		return nil, fmt.Errorf("upload id is required")
	}
	if timeout <= 0 {
		timeout = defaultInitWait
	}
	if err := c.EnsureValidJWT(session); err != nil {
		return nil, fmt.Errorf("ensure JWT: %w", err)
	}

	deadline := time.Now().Add(timeout)
	pollInterval := 1500 * time.Millisecond
	lastStatus := ""
	lastReason := ""

	for time.Now().Before(deadline) {
		session.mu.RLock()
		jwt := session.JWT
		session.mu.RUnlock()

		gqlReq := graphqlRequest{
			OperationName: "GetUploadedMediaById",
			Variables: map[string]interface{}{
				"uploadId": uploadID,
			},
			Query: uploadedMediaByIDQuery,
		}

		body, err := c.doGraphQL(jwt, gqlReq)
		if err != nil {
			if isRetryableGraphQLError(err) {
				log.Printf("[Leonardo] Transient uploaded_media polling error for uploadID=%s: %v; retrying", uploadID, err)
				time.Sleep(pollInterval)
				continue
			}
			return nil, err
		}

		var gqlResp struct {
			Data struct {
				UploadedMedia []UploadedMedia `json:"uploaded_media"`
			} `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}

		if err := json.Unmarshal(body, &gqlResp); err != nil {
			return nil, fmt.Errorf("parse uploaded media response: %w", err)
		}
		if len(gqlResp.Errors) > 0 {
			return nil, fmt.Errorf("uploaded media query error: %s", gqlResp.Errors[0].Message)
		}

		if len(gqlResp.Data.UploadedMedia) > 0 {
			item := gqlResp.Data.UploadedMedia[0]
			lastStatus = strings.ToUpper(strings.TrimSpace(item.Status))
			if item.StatusReason != nil {
				lastReason = strings.TrimSpace(*item.StatusReason)
			}
			switch lastStatus {
			case "COMPLETE", "COMPLETED", "READY":
				return &item, nil
			case "FAILED", "REJECTED", "BLOCKED", "ERROR":
				if lastReason != "" {
					return nil, fmt.Errorf("uploaded media %s: %s", strings.ToLower(lastStatus), lastReason)
				}
				return nil, fmt.Errorf("uploaded media %s", strings.ToLower(lastStatus))
			}
		}

		time.Sleep(pollInterval)
	}

	if lastStatus != "" {
		if lastReason != "" {
			return nil, fmt.Errorf("timed out waiting for uploaded media completion (last status: %s, reason: %s)", lastStatus, lastReason)
		}
		return nil, fmt.Errorf("timed out waiting for uploaded media completion (last status: %s)", lastStatus)
	}
	return nil, fmt.Errorf("timed out waiting for uploaded media")
}

func parseUploadFields(fieldsJSON string) (map[string]string, error) {
	var fields map[string]string
	if err := json.Unmarshal([]byte(fieldsJSON), &fields); err != nil {
		return nil, fmt.Errorf("parse upload fields: %w", err)
	}

	if len(fields) == 0 {
		return nil, fmt.Errorf("upload fields were empty")
	}

	return fields, nil
}

func inferUploadFilename(contentType string) string {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "image/png":
		return "upload.png"
	case "image/webp":
		return "upload.webp"
	case "image/gif":
		return "upload.gif"
	case "image/bmp":
		return "upload.bmp"
	case "image/tiff":
		return "upload.tiff"
	case "video/mp4":
		return "upload.mp4"
	case "video/quicktime":
		return "upload.mov"
	case "video/webm":
		return "upload.webm"
	case "video/x-msvideo":
		return "upload.avi"
	case "video/x-matroska":
		return "upload.mkv"
	default:
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "video/") {
			return "upload.mp4"
		}
		return "upload.jpg"
	}
}

func buildS3UploadBody(fields map[string]string, imageData []byte, contentType string) ([]byte, string) {
	boundary := "----LeoUpload" + fmt.Sprintf("%d", time.Now().UnixNano())
	var body bytes.Buffer

	for k, v := range fields {
		body.WriteString("--" + boundary + "\r\n")
		body.WriteString(fmt.Sprintf("Content-Disposition: form-data; name=\"%s\"\r\n\r\n%s\r\n", k, v))
	}

	body.WriteString("--" + boundary + "\r\n")
	if contentType == "" {
		contentType = "image/jpeg"
	}
	body.WriteString(fmt.Sprintf("Content-Disposition: form-data; name=\"file\"; filename=\"%s\"\r\n", inferUploadFilename(contentType)))
	body.WriteString(fmt.Sprintf("Content-Type: %s\r\n\r\n", contentType))
	body.Write(imageData)
	body.WriteString("\r\n--" + boundary + "--\r\n")

	return body.Bytes(), boundary
}

func isRetryableUploadError(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "connection reset by peer"),
		strings.Contains(msg, "broken pipe"),
		strings.Contains(msg, "timeout"),
		strings.Contains(msg, "temporary failure"),
		strings.Contains(msg, "temporarily unavailable"),
		strings.Contains(msg, "unexpected eof"),
		strings.Contains(msg, "eof"):
		return true
	default:
		return false
	}
}

func isRetryableUploadStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	default:
		return statusCode >= 500
	}
}

// UploadImageToS3 uploads the actual image data to the S3 presigned URL.
// fieldsJSON is the JSON string returned by uploadImage, containing S3 form fields.
func (c *Client) UploadImageToS3(uploadURL string, fieldsJSON string, imageData []byte, contentType string) error {
	fields, err := parseUploadFields(fieldsJSON)
	if err != nil {
		return err
	}

	body, boundary := buildS3UploadBody(fields, imageData, contentType)

	for attempt := 1; attempt <= s3UploadMaxAttempts; attempt++ {
		req, err := http.NewRequest("POST", uploadURL, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			if attempt < s3UploadMaxAttempts && isRetryableUploadError(err) {
				log.Printf("[Leonardo] S3 upload attempt %d/%d failed: %v; retrying", attempt, s3UploadMaxAttempts, err)
				time.Sleep(time.Duration(attempt) * s3UploadRetryDelay)
				continue
			}
			return fmt.Errorf("s3 upload failed after %d attempt(s): %w", attempt, err)
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode < 300 {
			if attempt > 1 {
				log.Printf("[Leonardo] Image uploaded to S3 successfully on attempt %d/%d", attempt, s3UploadMaxAttempts)
			} else {
				log.Printf("[Leonardo] Image uploaded to S3 successfully")
			}
			return nil
		}

		snippet := string(respBody[:min(len(respBody), 300)])
		statusErr := fmt.Errorf("s3 upload returned %d: %s", resp.StatusCode, snippet)
		if attempt < s3UploadMaxAttempts && isRetryableUploadStatus(resp.StatusCode) {
			log.Printf("[Leonardo] S3 upload attempt %d/%d returned retryable status %d; retrying", attempt, s3UploadMaxAttempts, resp.StatusCode)
			time.Sleep(time.Duration(attempt) * s3UploadRetryDelay)
			continue
		}
		return statusErr
	}

	return fmt.Errorf("s3 upload failed after %d attempt(s)", s3UploadMaxAttempts)
}

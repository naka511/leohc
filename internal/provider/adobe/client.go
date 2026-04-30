package adobe

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"leo-go/internal/config"
	"leo-go/internal/provider"
)

// Error types matching Python's exception hierarchy.
type RequestError struct{ Msg string }
type QuotaError struct{ Msg string }
type AuthError struct{ Msg string }
type TempError struct {
	Msg        string
	StatusCode int
	ErrorType  string
}

func (e *RequestError) Error() string { return e.Msg }
func (e *QuotaError) Error() string   { return e.Msg }
func (e *AuthError) Error() string    { return e.Msg }
func (e *TempError) Error() string    { return e.Msg }

const (
	submitURL      = "https://firefly-3p.ff.adobe.io/v2/3p-images/generate-async"
	videoSubmitURL = "https://firefly-3p.ff.adobe.io/v2/3p-videos/generate-async"
	uploadURL      = "https://firefly-3p.ff.adobe.io/v2/storage/image"
	defaultAPIKey  = "clio-playground-web"
)

// Client implements the Adobe Firefly provider.
type Client struct {
	APIKey     string
	UserAgent  string
	SecChUA    string
	BasicProxy string
	ResProxy   string
	Timeout    int
	httpClient *http.Client
}

// NewClient creates a new Adobe client.
func NewClient() *Client {
	cfg := config.Global()
	c := &Client{
		APIKey:    defaultAPIKey,
		UserAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36",
		SecChUA:   `"Google Chrome";v="147", "Not.A/Brand";v="8", "Chromium";v="147"`,
		Timeout:   cfg.GetInt("generate_timeout", 300),
	}
	if cfg.GetBool("use_proxy", false) {
		c.BasicProxy = cfg.GetString("proxy")
	}
	if cfg.GetBool("resource_use_proxy", false) {
		c.ResProxy = cfg.GetString("resource_proxy")
	}
	c.httpClient = c.buildHTTPClient(c.BasicProxy)
	return c
}

func (c *Client) buildHTTPClient(proxyURL string) *http.Client {
	transport := &http.Transport{}
	if proxyURL != "" {
		if u, err := url.Parse(proxyURL); err == nil {
			transport.Proxy = http.ProxyURL(u)
		}
	}
	return &http.Client{Transport: transport, Timeout: 60 * time.Second}
}

func (c *Client) Name() string { return "adobe" }

func extractUserID(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	padded := parts[1]
	if m := len(padded) % 4; m != 0 {
		padded += strings.Repeat("=", 4-m)
	}
	decoded, err := base64.URLEncoding.DecodeString(padded)
	if err != nil {
		return ""
	}
	var claims map[string]interface{}
	if json.Unmarshal(decoded, &claims) != nil {
		return ""
	}
	if uid, ok := claims["user_id"].(string); ok {
		return uid
	}
	return ""
}

func computeNonce(userID, prompt string) string {
	p := prompt
	if len(p) > 256 {
		p = p[:256]
	}
	h := sha256.Sum256([]byte(userID + "-" + p))
	return fmt.Sprintf("%x", h)
}

func (c *Client) browserHeaders() map[string]string {
	return map[string]string{
		"user-agent":         c.UserAgent,
		"origin":             "https://firefly.adobe.com",
		"referer":            "https://firefly.adobe.com/",
		"accept-language":    "en-US,en;q=0.9",
		"sec-ch-ua":          c.SecChUA,
		"sec-ch-ua-mobile":   "?0",
		"sec-ch-ua-platform": `"Windows"`,
		"sec-fetch-site":     "cross-site",
		"sec-fetch-mode":     "cors",
		"sec-fetch-dest":     "empty",
	}
}

func (c *Client) submitHeaders(token, prompt string) map[string]string {
	h := c.browserHeaders()
	uid := extractUserID(token)
	h["Authorization"] = "Bearer " + token
	h["x-api-key"] = c.APIKey
	h["x-nonce"] = computeNonce(uid, prompt)
	h["content-type"] = "application/json"
	h["accept"] = "*/*"
	return h
}

func (c *Client) pollHeaders(token string) map[string]string {
	return map[string]string{
		"Authorization":      "Bearer " + token,
		"x-api-key":          c.APIKey,
		"accept":             "*/*",
		"referer":            "https://firefly.adobe.com/",
		"origin":             "https://firefly.adobe.com",
		"user-agent":         c.UserAgent,
		"sec-ch-ua":          c.SecChUA,
		"sec-ch-ua-mobile":   "?0",
		"sec-ch-ua-platform": `"Windows"`,
		"sec-fetch-site":     "cross-site",
		"sec-fetch-mode":     "cors",
		"sec-fetch-dest":     "empty",
	}
}

func (c *Client) doJSON(method, reqURL string, headers map[string]string, body interface{}) (int, []byte, http.Header, error) {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, nil, nil, err
		}
		reqBody = strings.NewReader(string(data))
	}
	req, err := http.NewRequest(method, reqURL, reqBody)
	if err != nil {
		return 0, nil, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, nil, &TempError{Msg: fmt.Sprintf("request failed: %v", err), ErrorType: "connection"}
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, respBody, resp.Header, nil
}

func checkAuthOrQuota(statusCode int, respHeaders http.Header, respBody []byte) error {
	if statusCode != 401 && statusCode != 403 {
		return nil
	}
	accessErr := strings.ToLower(strings.TrimSpace(respHeaders.Get("x-access-error")))
	bodyLower := strings.ToLower(string(respBody))
	if accessErr == "taste_exhausted" || strings.Contains(bodyLower, "token quota exhausted") {
		return &QuotaError{Msg: "Adobe quota exhausted for this account"}
	}
	return &AuthError{Msg: "Token invalid or expired"}
}

func extractJobID(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}

func normalizeTaskStatus(val string) string {
	s := strings.ToUpper(strings.TrimSpace(val))
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "_")
	aliases := map[string]string{
		"SUCCESS": "COMPLETED", "SUCCEEDED": "COMPLETED", "DONE": "COMPLETED", "COMPLETE": "COMPLETED",
		"FAILURE": "FAILED", "FAIL": "FAILED", "CANCELED": "CANCELLED",
	}
	if mapped, ok := aliases[s]; ok {
		return mapped
	}
	return s
}

func isInProgress(status string) bool {
	s := strings.ToUpper(status)
	return s == "IN_PROGRESS" || s == "RUNNING" || s == "PROCESSING" || s == "PENDING" || s == "QUEUED" || s == "STARTED"
}

func isFailed(status string) bool {
	s := normalizeTaskStatus(status)
	return s == "FAILED" || s == "CANCELLED" || s == "ERROR"
}

// UploadImage uploads an image to Adobe storage.
func (c *Client) UploadImage(ctx context.Context, token string, data []byte, mime string) (string, error) {
	if len(data) == 0 {
		return "", &RequestError{Msg: "image is empty"}
	}
	headers := map[string]string{
		"authorization": "Bearer " + token,
		"x-api-key":     c.APIKey,
		"content-type":  mime,
		"accept":        "application/json",
	}
	req, err := http.NewRequestWithContext(ctx, "POST", uploadURL, strings.NewReader(string(data)))
	if err != nil {
		return "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", &TempError{Msg: fmt.Sprintf("upload failed: %v", err), ErrorType: "connection"}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if err := checkAuthOrQuota(resp.StatusCode, resp.Header, body); err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", &RequestError{Msg: fmt.Sprintf("upload failed: %d %s", resp.StatusCode, truncate(string(body), 300))}
	}
	var result map[string]interface{}
	if json.Unmarshal(body, &result) != nil {
		return "", &RequestError{Msg: "upload: invalid response"}
	}
	images, _ := result["images"].([]interface{})
	if len(images) == 0 {
		return "", &RequestError{Msg: "upload: no image id returned"}
	}
	first, _ := images[0].(map[string]interface{})
	imageID, _ := first["id"].(string)
	if imageID == "" {
		return "", &RequestError{Msg: "upload: no image id returned"}
	}
	return imageID, nil
}

// Generate performs image generation (submit → poll → download).
func (c *Client) Generate(ctx context.Context, req provider.ImageRequest) (*provider.JobResult, error) {
	payloads := BuildImagePayloadCandidates(req)
	if len(payloads) == 0 {
		return nil, &RequestError{Msg: "no payload candidates"}
	}

	var submitStatus int
	var submitBody []byte
	var submitHeaders http.Header
	var lastErr string

	for _, payload := range payloads {
		prompt, _ := payload["prompt"].(string)
		if prompt == "" {
			prompt = req.Prompt
		}
		status, body, headers, err := c.doJSON("POST", submitURL, c.submitHeaders(req.Token, prompt), payload)
		if err != nil {
			return nil, err
		}
		submitStatus = status
		submitBody = body
		submitHeaders = headers
		if status == 200 {
			break
		}
		if status == 401 || status == 403 {
			break
		}
		lastErr = truncate(string(body), 300)
	}

	if err := checkAuthOrQuota(submitStatus, submitHeaders, submitBody); err != nil {
		return nil, err
	}
	if submitStatus != 200 {
		if submitStatus == 429 || submitStatus == 451 || submitStatus >= 500 {
			return nil, &TempError{Msg: fmt.Sprintf("submit failed: %d %s", submitStatus, lastErr), StatusCode: submitStatus}
		}
		return nil, &RequestError{Msg: fmt.Sprintf("submit failed: %d %s", submitStatus, lastErr)}
	}

	var submitData map[string]interface{}
	json.Unmarshal(submitBody, &submitData)

	pollURL := submitHeaders.Get("x-override-status-link")
	if pollURL == "" {
		links, _ := submitData["links"].(map[string]interface{})
		result, _ := links["result"].(map[string]interface{})
		pollURL, _ = result["href"].(string)
	}
	if pollURL == "" {
		return nil, &RequestError{Msg: "submit succeeded but no poll url returned"}
	}

	upstreamJobID := extractJobID(pollURL)
	return c.pollImageJob(ctx, req.Token, pollURL, upstreamJobID, req.Timeout, req.ReturnURL)
}

func (c *Client) pollImageJob(ctx context.Context, token, pollURL, jobID string, timeout int, returnURL bool) (*provider.JobResult, error) {
	if timeout <= 0 {
		timeout = 300
	}
	start := time.Now()
	for {
		status, body, headers, err := c.doJSON("GET", pollURL, c.pollHeaders(token), nil)
		if err != nil {
			if time.Since(start) > time.Duration(timeout)*time.Second {
				return nil, &RequestError{Msg: "generation timed out"}
			}
			log.Printf("[adobe] poll error, retrying: %v", err)
			time.Sleep(3 * time.Second)
			continue
		}
		if authErr := checkAuthOrQuota(status, headers, body); authErr != nil {
			return nil, authErr
		}
		if status != 200 {
			if status == 429 || status == 451 || status >= 500 {
				if time.Since(start) > time.Duration(timeout)*time.Second {
					return nil, &TempError{Msg: fmt.Sprintf("poll failed: %d", status), StatusCode: status}
				}
				time.Sleep(3 * time.Second)
				continue
			}
			return nil, &RequestError{Msg: fmt.Sprintf("poll failed: %d %s", status, truncate(string(body), 300))}
		}

		var latest map[string]interface{}
		json.Unmarshal(body, &latest)
		taskStatus := extractTaskStatus(latest, headers)
		progress := extractProgress(latest, headers)

		outputs, _ := latest["outputs"].([]interface{})
		if len(outputs) > 0 {
			first, _ := outputs[0].(map[string]interface{})
			imgObj, _ := first["image"].(map[string]interface{})
			imageURL, _ := imgObj["presignedUrl"].(string)
			if imageURL == "" {
				return nil, &RequestError{Msg: "job finished without image url"}
			}
			result := &provider.JobResult{
				ImageURL: imageURL,
				Meta:     latest,
				Progress: 100.0,
				JobID:    jobID,
			}
			if !returnURL {
				imgBytes, dlErr := c.downloadBytes(imageURL)
				if dlErr != nil {
					return nil, dlErr
				}
				result.ImageBytes = imgBytes
			}
			return result, nil
		}

		if isFailed(taskStatus) {
			return nil, &RequestError{Msg: fmt.Sprintf("image job failed: %v", latest)}
		}
		_ = progress
		if time.Since(start) > time.Duration(timeout)*time.Second {
			return nil, &RequestError{Msg: "generation timed out"}
		}
		time.Sleep(3 * time.Second)
	}
}

// GenerateVideo performs video generation.
func (c *Client) GenerateVideo(ctx context.Context, req provider.VideoRequest) (*provider.JobResult, error) {
	payload := BuildVideoPayload(req)
	status, body, headers, err := c.doJSON("POST", videoSubmitURL, c.submitHeaders(req.Token, req.Prompt), payload)
	if err != nil {
		return nil, err
	}
	if authErr := checkAuthOrQuota(status, headers, body); authErr != nil {
		return nil, authErr
	}
	if status != 200 {
		if status == 429 || status == 451 || status >= 500 {
			return nil, &TempError{Msg: fmt.Sprintf("video submit failed: %d", status), StatusCode: status}
		}
		return nil, &RequestError{Msg: fmt.Sprintf("video submit failed: %d %s", status, truncate(string(body), 300))}
	}

	var submitData map[string]interface{}
	json.Unmarshal(body, &submitData)
	pollURL := headers.Get("x-override-status-link")
	if pollURL == "" {
		links, _ := submitData["links"].(map[string]interface{})
		result, _ := links["result"].(map[string]interface{})
		pollURL, _ = result["href"].(string)
	}
	if pollURL == "" {
		return nil, &RequestError{Msg: "video submit succeeded but no poll url"}
	}
	pollURL = normalizeVideoPollURL(pollURL)
	jobID := extractJobID(pollURL)
	return c.pollVideoJob(ctx, req.Token, pollURL, jobID, req.Timeout, req.ReturnURL)
}

func (c *Client) pollVideoJob(ctx context.Context, token, pollURL, jobID string, timeout int, returnURL bool) (*provider.JobResult, error) {
	if timeout <= 0 {
		timeout = 600
	}
	start := time.Now()
	for {
		status, body, headers, err := c.doJSON("GET", pollURL, c.pollHeaders(token), nil)
		if err != nil {
			if time.Since(start) > time.Duration(timeout)*time.Second {
				return nil, &RequestError{Msg: "video generation timed out"}
			}
			time.Sleep(3 * time.Second)
			continue
		}
		if authErr := checkAuthOrQuota(status, headers, body); authErr != nil {
			return nil, authErr
		}
		if status != 200 {
			if time.Since(start) > time.Duration(timeout)*time.Second {
				return nil, &TempError{Msg: fmt.Sprintf("video poll failed: %d", status), StatusCode: status}
			}
			time.Sleep(3 * time.Second)
			continue
		}

		var latest map[string]interface{}
		json.Unmarshal(body, &latest)
		taskStatus := extractTaskStatus(latest, headers)

		outputs, _ := latest["outputs"].([]interface{})
		if len(outputs) > 0 {
			first, _ := outputs[0].(map[string]interface{})
			vidObj, _ := first["video"].(map[string]interface{})
			videoURL, _ := vidObj["presignedUrl"].(string)
			if videoURL == "" {
				return nil, &RequestError{Msg: "video job finished without video url"}
			}
			return &provider.JobResult{VideoURL: videoURL, Meta: latest, Progress: 100.0, JobID: jobID}, nil
		}
		if isFailed(taskStatus) {
			return nil, &RequestError{Msg: fmt.Sprintf("video job failed: %v", latest)}
		}
		if time.Since(start) > time.Duration(timeout)*time.Second {
			return nil, &RequestError{Msg: "video generation timed out"}
		}
		time.Sleep(3 * time.Second)
	}
}

func (c *Client) downloadBytes(imgURL string) ([]byte, error) {
	resp, err := c.httpClient.Get(imgURL)
	if err != nil {
		return nil, &TempError{Msg: fmt.Sprintf("download failed: %v", err), ErrorType: "connection"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, &RequestError{Msg: fmt.Sprintf("download failed: %d", resp.StatusCode)}
	}
	return io.ReadAll(resp.Body)
}

func normalizeVideoPollURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	if !strings.HasPrefix(u.Host, "firefly-epo") {
		return raw
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 {
		return raw
	}
	jobID := parts[len(parts)-1]
	return fmt.Sprintf("https://bks-epo8522.adobe.io/v2/jobs/result/%s?host=%s/", jobID, u.Host)
}

func extractTaskStatus(data map[string]interface{}, headers http.Header) string {
	candidates := []string{}
	for _, key := range []string{"status", "state", "task_status", "taskStatus"} {
		if v, ok := data[key].(string); ok && v != "" {
			candidates = append(candidates, v)
		}
	}
	for _, sub := range []string{"task", "result", "meta", "metadata"} {
		if obj, ok := data[sub].(map[string]interface{}); ok {
			for _, key := range []string{"status", "state"} {
				if v, ok := obj[key].(string); ok && v != "" {
					candidates = append(candidates, v)
				}
			}
		}
	}
	for _, hdr := range []string{"x-task-status", "x-status"} {
		if v := headers.Get(hdr); v != "" {
			candidates = append(candidates, v)
		}
	}
	for _, c := range candidates {
		s := normalizeTaskStatus(c)
		if s != "" {
			return s
		}
	}
	return ""
}

func extractProgress(data map[string]interface{}, headers http.Header) float64 {
	for _, key := range []string{"progress", "percentage", "task_progress"} {
		if v, ok := data[key].(float64); ok {
			if v <= 1.0 {
				return v * 100.0
			}
			return v
		}
	}
	return 0
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// ValidateToken is a no-op for now (tokens are validated on use).
func (c *Client) ValidateToken(ctx context.Context, token string) error { return nil }

// RefreshToken delegates to the refresh manager (handled externally).
func (c *Client) RefreshToken(ctx context.Context, credential interface{}) (string, error) {
	return "", fmt.Errorf("use RefreshManager for token refresh")
}

// GetCredits is not yet implemented.
func (c *Client) GetCredits(ctx context.Context, token string) (*provider.Credits, error) {
	return nil, fmt.Errorf("not implemented")
}

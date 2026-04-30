package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"leo-go/internal/config"
	"leo-go/internal/provider"
	"leo-go/internal/provider/adobe"
	"leo-go/internal/provider/leonardo"
	"leo-go/internal/reqlog"
	"leo-go/internal/token"
)

// Server holds all dependencies for HTTP handlers.
type Server struct {
	TokenMgr       *token.Manager
	Provider       provider.Provider
	Registry       *provider.Registry
	Config         *config.Manager
	GeneratedDir   string
	LeonardoClient *leonardo.Client
	ReqLog         *reqlog.Store
	leoSessionMu   sync.Mutex
	leoSessions    map[string]*leonardo.TokenSession
}

// requireAPIKey validates the X-API-Key or Authorization header.
func (s *Server) requireAPIKey(r *http.Request) error {
	expected := s.Config.GetString("api_key")
	if expected == "" {
		return nil
	}
	key := r.Header.Get("X-API-Key")
	if key == "" {
		auth := r.Header.Get("Authorization")
		if strings.HasPrefix(auth, "Bearer ") {
			key = strings.TrimPrefix(auth, "Bearer ")
		}
	}
	if strings.TrimSpace(key) != expected {
		return fmt.Errorf("invalid api key")
	}
	return nil
}

// HandleListModels handles GET /v1/models.
func (s *Server) HandleListModels(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAPIKey(r); err != nil {
		writeJSON(w, 401, map[string]interface{}{"error": map[string]string{"message": err.Error(), "type": "authentication_error"}})
		return
	}
	var data []map[string]interface{}
	for _, m := range s.Registry.AllModels() {
		if m.Hidden {
			continue
		}
		item := map[string]interface{}{
			"id": m.ID, "object": "model", "owned_by": m.OwnedBy, "description": m.Description,
		}
		if len(m.Parameters) > 0 {
			item["parameters"] = m.Parameters
		}
		data = append(data, item)
	}
	for _, m := range s.Registry.AllVideoModels() {
		if m.Hidden {
			continue
		}
		item := map[string]interface{}{
			"id": m.ID, "object": "model", "owned_by": m.OwnedBy, "description": m.Description,
		}
		if len(m.Parameters) > 0 {
			item["parameters"] = m.Parameters
		}
		data = append(data, item)
	}
	writeJSON(w, 200, map[string]interface{}{"object": "list", "data": data})
}

// HandleImageGeneration handles POST /v1/images/generations.
func (s *Server) HandleImageGeneration(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAPIKey(r); err != nil {
		writeJSON(w, 401, errorResp("invalid api key", "authentication_error"))
		return
	}
	var data map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		writeJSON(w, 400, errorResp("invalid request body", "invalid_request_error"))
		return
	}

	prompt := strings.TrimSpace(fmt.Sprintf("%v", data["prompt"]))
	if prompt == "" || prompt == "<nil>" {
		prompt = extractPromptFromMessages(data)
	}
	if len(prompt) < 3 {
		writeJSON(w, 400, errorResp("prompt must contain at least 3 characters", "invalid_request_error"))
		return
	}

	modelID, _ := data["model"].(string)
	ratio, resolution, resolvedModel := adobe.ResolveRatioAndResolution(data, modelID)
	modelConf := adobe.ResolveModel(resolvedModel)

	strategy := s.Config.GetString("token_rotation_strategy", "round_robin")
	maxAttempts := s.Config.GetInt("retry_max_attempts", 3)
	if !s.Config.GetBool("retry_enabled", true) {
		maxAttempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		tok := s.TokenMgr.GetAvailable(strategy)
		if tok == "" {
			writeJSON(w, 503, errorResp("No active tokens available", "server_error"))
			return
		}

		req := provider.ImageRequest{
			Token:            tok,
			Prompt:           prompt,
			AspectRatio:      ratio,
			OutputResolution: resolution,
			ModelID:          getString(modelConf, "upstream_model_id", "gemini-flash"),
			ModelVersion:     getString(modelConf, "upstream_model_version", "nano-banana-2"),
			PayloadStyle:     getString(modelConf, "payload_style", "banana"),
			Timeout:          s.Config.GetInt("generate_timeout", 300),
			ReturnURL:        true,
		}
		if gm, ok := modelConf["generation_metadata"].(map[string]interface{}); ok {
			req.GenMetadata = gm
		}
		if gs, ok := modelConf["generation_settings"].(map[string]interface{}); ok {
			req.GenSettings = gs
		}
		if mp, ok := modelConf["model_specific_payload"].(map[string]interface{}); ok {
			req.ModelPayload = mp
		}

		result, err := s.Provider.Generate(r.Context(), req)
		if err != nil {
			lastErr = err
			switch err.(type) {
			case *adobe.AuthError:
				s.TokenMgr.ReportInvalid(tok)
				continue
			case *adobe.QuotaError:
				s.TokenMgr.ReportExhausted(tok)
				continue
			case *adobe.TempError:
				s.TokenMgr.ReportFail(tok)
				continue
			default:
				writeJSON(w, 500, errorResp(err.Error(), "server_error"))
				return
			}
		}

		s.TokenMgr.ReportSuccess(tok)
		imageURL := result.ImageURL
		writeJSON(w, 200, map[string]interface{}{
			"created": time.Now().Unix(),
			"model":   resolvedModel,
			"data":    []map[string]interface{}{{"url": imageURL}},
		})
		return
	}

	// All retries exhausted
	if lastErr != nil {
		switch lastErr.(type) {
		case *adobe.QuotaError:
			writeJSON(w, 429, errorResp("Token quota exhausted", "rate_limit_error"))
		case *adobe.AuthError:
			writeJSON(w, 401, errorResp("Token invalid or expired", "authentication_error"))
		default:
			writeJSON(w, 503, errorResp(lastErr.Error(), "server_error"))
		}
	} else {
		writeJSON(w, 503, errorResp("No active tokens available", "server_error"))
	}
}

// HandleChatCompletions handles POST /v1/chat/completions — wraps image generation in OpenAI format.
func (s *Server) HandleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAPIKey(r); err != nil {
		writeJSON(w, 401, errorResp("invalid api key", "authentication_error"))
		return
	}
	var data map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		writeJSON(w, 400, errorResp("invalid request body", "invalid_request_error"))
		return
	}

	prompt := extractPromptFromMessages(data)
	if prompt == "" {
		prompt = strings.TrimSpace(fmt.Sprintf("%v", data["prompt"]))
	}
	if prompt == "<nil>" {
		prompt = ""
	}
	if len(prompt) < 3 {
		writeJSON(w, 400, errorResp("prompt must contain at least 3 characters", "invalid_request_error"))
		return
	}

	// Inject prompt back and delegate
	data["prompt"] = prompt
	body, _ := json.Marshal(data)
	// Reconstruct request
	r.Body = newReadCloser(body)
	s.HandleImageGeneration(w, r)
}

// HandleVideoGeneration handles POST /v1/video/generations.
func (s *Server) HandleVideoGeneration(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAPIKey(r); err != nil {
		writeJSON(w, 401, errorResp("invalid api key", "authentication_error"))
		return
	}
	var data map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		writeJSON(w, 400, errorResp("invalid request body", "invalid_request_error"))
		return
	}

	prompt := strings.TrimSpace(fmt.Sprintf("%v", data["prompt"]))
	if prompt == "<nil>" {
		prompt = ""
	}
	if len(prompt) < 3 {
		writeJSON(w, 400, errorResp("prompt must contain at least 3 characters", "invalid_request_error"))
		return
	}

	modelID, _ := data["model"].(string)
	if modelID == "" {
		modelID = "sora2"
	}
	ratio, _ := data["aspect_ratio"].(string)
	if ratio == "" {
		ratio = "16:9"
	}
	duration := 10
	if d, ok := data["duration"].(float64); ok {
		duration = int(d)
	}

	// Parse size (e.g. "1280x720")
	width, height := 1280, 720
	if size, ok := data["size"].(string); ok && size != "" {
		parts := strings.Split(size, "x")
		if len(parts) == 2 {
			if w, err := strconv.Atoi(parts[0]); err == nil {
				width = w
			}
			if h, err := strconv.Atoi(parts[1]); err == nil {
				height = h
			}
		}
	}

	if strings.HasPrefix(modelID, "seedance") {
		s.handleLeonardoVideoGeneration(w, r, prompt, modelID, duration, width, height)
		return
	}

	strategy := s.Config.GetString("token_rotation_strategy", "round_robin")
	tok := s.TokenMgr.GetAvailable(strategy)
	if tok == "" {
		writeJSON(w, 503, errorResp("No active tokens available", "server_error"))
		return
	}

	req := provider.VideoRequest{
		Token:       tok,
		Prompt:      prompt,
		AspectRatio: ratio,
		Duration:    duration,
		ModelID:     modelID,
		Timeout:     s.Config.GetInt("generate_timeout", 600),
		ReturnURL:   true,
	}

	result, err := s.Provider.GenerateVideo(r.Context(), req)
	if err != nil {
		switch err.(type) {
		case *adobe.QuotaError:
			s.TokenMgr.ReportExhausted(tok)
			writeJSON(w, 429, errorResp("Token quota exhausted", "rate_limit_error"))
		case *adobe.AuthError:
			s.TokenMgr.ReportInvalid(tok)
			writeJSON(w, 401, errorResp("Token invalid or expired", "authentication_error"))
		default:
			writeJSON(w, 500, errorResp(err.Error(), "server_error"))
		}
		return
	}

	s.TokenMgr.ReportSuccess(tok)
	writeJSON(w, 200, map[string]interface{}{
		"created": time.Now().Unix(),
		"model":   modelID,
		"data":    []map[string]interface{}{{"url": result.VideoURL}},
	})
}

// ---- Helpers ----

func extractPromptFromMessages(data map[string]interface{}) string {
	messages, _ := data["messages"].([]interface{})
	for _, msg := range messages {
		m, _ := msg.(map[string]interface{})
		content := m["content"]
		switch c := content.(type) {
		case string:
			if strings.TrimSpace(c) != "" {
				return strings.TrimSpace(c)
			}
		case []interface{}:
			for _, part := range c {
				p, _ := part.(map[string]interface{})
				if p["type"] == "text" {
					if text, ok := p["text"].(string); ok && strings.TrimSpace(text) != "" {
						return strings.TrimSpace(text)
					}
				}
			}
		}
	}
	return ""
}

func getString(m map[string]interface{}, key, def string) string {
	v, ok := m[key].(string)
	if !ok || v == "" {
		return def
	}
	return v
}

func errorResp(message, errType string) map[string]interface{} {
	return map[string]interface{}{
		"error": map[string]interface{}{"message": message, "type": errType},
	}
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

type readCloser struct {
	data []byte
	pos  int
}

func newReadCloser(data []byte) *readCloser { return &readCloser{data: data} }
func (rc *readCloser) Read(p []byte) (int, error) {
	if rc.pos >= len(rc.data) {
		return 0, fmt.Errorf("EOF")
	}
	n := copy(p, rc.data[rc.pos:])
	rc.pos += n
	return n, nil
}
func (rc *readCloser) Close() error { return nil }

func (s *Server) reloadRuntimeClients() {
	basicProxy := ""
	if s.Config.GetBool("use_proxy", false) {
		basicProxy = s.Config.GetString("proxy", "")
	}

	s.LeonardoClient = leonardo.NewClient(basicProxy)
	s.leoSessionMu.Lock()
	s.leoSessions = make(map[string]*leonardo.TokenSession)
	s.leoSessionMu.Unlock()

	adobeClient := adobe.NewClient()
	if s.Registry != nil {
		s.Registry.Register(adobeClient)
	}
	s.Provider = adobeClient
}

func (s *Server) handleLeonardoVideoGeneration(w http.ResponseWriter, r *http.Request, prompt string, modelID string, duration int, width int, height int) {
	if s.LeonardoClient == nil {
		writeJSON(w, 500, errorResp("Leonardo client not initialized", "server_error"))
		return
	}

	session, usedTokenID := s.getLeonardoSession("")
	if session == nil {
		writeJSON(w, 503, errorResp("No Leonardo tokens available", "server_error"))
		return
	}

	genReq := &leonardo.GenerateRequest{
		Model: modelID,
		Params: leonardo.GenerateParams{
			Prompt:         prompt,
			Mode:           "RESOLUTION_720", // default Leonardo mode for these models
			Duration:       duration,
			Width:          width,
			Height:         height,
			MotionHasAudio: true,
		},
	}

	startTime := time.Now()
	result, err := s.LeonardoClient.Generate(session, genReq)
	if err != nil {
		s.TokenMgr.ReportInvalid(usedTokenID)
		writeJSON(w, 500, errorResp(fmt.Sprintf("generation failed: %v", err), "server_error"))
		return
	}

	if s.ReqLog != nil {
		s.ReqLog.Add(reqlog.Entry{
			Timestamp:    float64(startTime.Unix()),
			StatusCode:   200,
			TaskStatus:   "IN_PROGRESS",
			Type:         "video",
			Model:        fmt.Sprintf("%s (%dx%d %ds)", modelID, width, height, duration),
			Prompt:       prompt,
			GenerationID: result.GenerationID,
			Operation:    "leonardo.generate",
		})
	}

	timeout := s.Config.GetInt("generate_timeout", 600)
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)

	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Second)
		status, err := s.LeonardoClient.PollGenerationStatus(session, result.GenerationID)
		if err != nil {
			continue
		}
		if status.Status == "FAILED" {
			if s.ReqLog != nil {
				s.ReqLog.UpdateByGenerationID(result.GenerationID, "FAILED", "", "")
			}
			writeJSON(w, 500, errorResp("Generation failed in Leonardo", "server_error"))
			return
		}
		if status.Status == "COMPLETE" {
			detail, err := s.LeonardoClient.GetGenerationDetail(session, result.GenerationID)
			if err == nil && len(detail.Images) > 0 {
				var url string
				for _, img := range detail.Images {
					if img.MotionMP4 != "" {
						url = img.MotionMP4
						break
					}
				}
				if url != "" {
					if s.ReqLog != nil {
						s.ReqLog.UpdateByGenerationID(result.GenerationID, "COMPLETED", url, "")
					}
					s.TokenMgr.ReportSuccess(usedTokenID)
					writeJSON(w, 200, map[string]interface{}{
						"created": time.Now().Unix(),
						"model":   modelID,
						"data":    []map[string]interface{}{{"url": url}},
					})
					return
				}
			}
		}
	}
	if s.ReqLog != nil {
		s.ReqLog.UpdateByGenerationID(result.GenerationID, "FAILED", "", "Timeout")
	}
	writeJSON(w, 504, errorResp("Generation timed out", "timeout"))
}

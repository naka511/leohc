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
	"leo-go/internal/provider/leonardo"
	"leo-go/internal/reqlog"
	"leo-go/internal/token"
)

var openAIModelCatalog = []map[string]interface{}{
	{
		"id":          "seedance-2.0",
		"object":      "model",
		"owned_by":    "seedance",
		"description": "Seedance 2.0 standard video generation",
		"parameters": map[string]interface{}{
			"duration": []int{4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
			"size":     []string{"1280x720", "720x1280", "720x720"},
		},
	},
	{
		"id":          "seedance-2.0-fast",
		"object":      "model",
		"owned_by":    "seedance",
		"description": "Seedance 2.0 fast video generation",
		"parameters": map[string]interface{}{
			"duration": []int{4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
			"size":     []string{"1280x720", "720x1280", "720x720"},
		},
	},
}

// Server holds all dependencies for HTTP handlers.
type Server struct {
	TokenMgr                *token.Manager
	Config                  *config.Manager
	GeneratedDir            string
	LeonardoClient          *leonardo.Client
	ReqLog                  *reqlog.Store
	cookieImportMu          sync.Mutex
	cookieImportJobs        map[string]*cookieImportJob
	leoSessionMu            sync.Mutex
	leoSessions             map[string]*leonardo.TokenSession
	autoRefreshMu           sync.Mutex
	autoRefreshRun          map[string]time.Time
	autoRefreshBusy         map[string]bool
	autoRefreshLoopStarted  bool
	autoRefreshSweepRunning bool
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
	writeJSON(w, 200, map[string]interface{}{"object": "list", "data": openAIModelCatalog})
}

// HandleImageGeneration handles POST /v1/images/generations.
func (s *Server) HandleImageGeneration(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAPIKey(r); err != nil {
		writeJSON(w, 401, errorResp("invalid api key", "authentication_error"))
		return
	}
	writeJSON(w, 400, errorResp("image generation is not supported by this deployment; use /v1/video/generations with model seedance-2.0 or seedance-2.0-fast", "invalid_request_error"))
}

// HandleChatCompletions handles POST /v1/chat/completions.
func (s *Server) HandleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAPIKey(r); err != nil {
		writeJSON(w, 401, errorResp("invalid api key", "authentication_error"))
		return
	}
	writeJSON(w, 400, errorResp("chat completions are not supported by this deployment; use /v1/video/generations with model seedance-2.0 or seedance-2.0-fast", "invalid_request_error"))
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
	if prompt == "" || prompt == "<nil>" {
		prompt = extractPromptFromMessages(data)
	}
	if prompt == "<nil>" {
		prompt = ""
	}
	if len(prompt) < 3 {
		writeJSON(w, 400, errorResp("prompt must contain at least 3 characters", "invalid_request_error"))
		return
	}

	modelID, _ := data["model"].(string)
	if modelID == "" {
		modelID = "seedance-2.0-fast"
	}
	if !isSupportedSeedanceModel(modelID) {
		writeJSON(w, 400, errorResp("unsupported model; available models are seedance-2.0 and seedance-2.0-fast", "invalid_request_error"))
		return
	}
	duration := 10
	if d, ok := data["duration"].(float64); ok {
		duration = int(d)
	}
	if duration < 4 || duration > 15 {
		writeJSON(w, 400, errorResp("duration must be between 4 and 15 seconds", "invalid_request_error"))
		return
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

	session, usedTokenID := s.getLeonardoSession("")
	if session == nil {
		s.logVideoRequestFailure("openai.video.generate", prompt, modelID, duration, width, height, usedTokenID, session, 503, "No Leonardo tokens available")
		writeJSON(w, 503, errorResp("No Leonardo tokens available", "server_error"))
		return
	}

	imageRefs, startFrames, endFrames, videoRefs, err := s.resolveOpenAIVideoGuidanceInputs(data, session)
	if err != nil {
		s.logVideoRequestFailure("openai.video.generate", prompt, modelID, duration, width, height, usedTokenID, session, 400, err.Error())
		writeJSON(w, 400, errorResp(err.Error(), "invalid_request_error"))
		return
	}

	s.handleLeonardoVideoGeneration(w, r, session, usedTokenID, prompt, modelID, duration, width, height, imageRefs, startFrames, endFrames, videoRefs)
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

func errorResp(message, errType string) map[string]interface{} {
	return map[string]interface{}{
		"error": map[string]interface{}{"message": message, "type": errType},
	}
}

func (s *Server) resolveReqLogAccount(tokenID string, session *leonardo.TokenSession) (string, string) {
	accountName := ""
	accountEmail := ""

	if tokenID != "" && s.TokenMgr != nil {
		if info := s.TokenMgr.GetByID(tokenID); info != nil {
			accountName = strings.TrimSpace(toString(info["account_name"]))
			accountEmail = strings.TrimSpace(toString(info["account_email"]))
			if accountEmail == "" {
				accountEmail = strings.TrimSpace(toString(info["refresh_profile_email"]))
			}
			if accountName == "" {
				accountName = strings.TrimSpace(toString(info["refresh_profile_name"]))
			}
		}
	}

	if accountEmail == "" && session != nil {
		accountEmail = strings.TrimSpace(session.Email)
	}

	return accountName, accountEmail
}

func (s *Server) logVideoRequestFailure(operation, prompt, modelID string, duration, width, height int, tokenID string, session *leonardo.TokenSession, statusCode int, errorMessage string) {
	if s.ReqLog == nil {
		return
	}
	accountName, accountEmail := s.resolveReqLogAccount(tokenID, session)
	s.ReqLog.Add(reqlog.Entry{
		ID:           fmt.Sprintf("log-%d", time.Now().UnixNano()),
		StatusCode:   statusCode,
		TaskStatus:   "FAILED",
		Type:         "video",
		TokenID:      tokenID,
		AccountName:  accountName,
		AccountEmail: accountEmail,
		Model:        fmt.Sprintf("%s (%dx%d %ds)", modelID, width, height, duration),
		Prompt:       prompt,
		ErrorCode:    strconv.Itoa(statusCode),
		ErrorMessage: errorMessage,
		Operation:    operation,
	})
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
	if s.Config != nil {
		s.LeonardoClient.SetJWTRefreshMarginMinutes(s.Config.GetInt("jwt_refresh_margin_minutes", 5))
	}
	s.leoSessionMu.Lock()
	s.leoSessions = make(map[string]*leonardo.TokenSession)
	s.leoSessionMu.Unlock()
}

func isSupportedSeedanceModel(modelID string) bool {
	switch strings.TrimSpace(modelID) {
	case "seedance-2.0", "seedance-2.0-fast":
		return true
	default:
		return false
	}
}

func (s *Server) handleLeonardoVideoGeneration(w http.ResponseWriter, r *http.Request, session *leonardo.TokenSession, usedTokenID string, prompt string, modelID string, duration int, width int, height int, imageRefs []leonardo.ImageRef, startFrames []leonardo.FrameRef, endFrames []leonardo.FrameRef, videoRefs []leonardo.VideoRef) {
	if s.LeonardoClient == nil {
		writeJSON(w, 500, errorResp("Leonardo client not initialized", "server_error"))
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
			ImageRefs:      imageRefs,
			StartFrame:     startFrames,
			EndFrame:       endFrames,
			VideoRefs:      videoRefs,
		},
	}

	startTime := time.Now()
	result, err := s.LeonardoClient.Generate(session, genReq)
	if err != nil {
		s.TokenMgr.ReportInvalid(usedTokenID)
		s.logVideoRequestFailure("openai.video.generate", prompt, modelID, duration, width, height, usedTokenID, session, 502, fmt.Sprintf("generation failed: %v", err))
		writeJSON(w, 500, errorResp(fmt.Sprintf("generation failed: %v", err), "server_error"))
		return
	}

	if s.ReqLog != nil {
		accountName, accountEmail := s.resolveReqLogAccount(usedTokenID, session)
		s.ReqLog.Add(reqlog.Entry{
			Timestamp:    float64(startTime.Unix()),
			StatusCode:   200,
			TaskStatus:   "IN_PROGRESS",
			Type:         "video",
			TokenID:      usedTokenID,
			AccountName:  accountName,
			AccountEmail: accountEmail,
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
		elapsed := time.Since(startTime).Seconds()
		if status.Status == "FAILED" {
			if s.ReqLog != nil {
				s.ReqLog.UpdateByGenerationID(result.GenerationID, "FAILED", 502, "", "", "Leonardo reported generation status FAILED")
				s.ReqLog.UpdateDuration(result.GenerationID, elapsed)
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
					finalURL, materializeErr := s.materializeGeneratedMedia(url, result.GenerationID, "video")
					if materializeErr != nil {
						s.logVideoRequestFailure("openai.video.generate", prompt, modelID, duration, width, height, usedTokenID, session, 502, fmt.Sprintf("save generated media failed: %v", materializeErr))
						writeJSON(w, 500, errorResp(fmt.Sprintf("save generated media failed: %v", materializeErr), "server_error"))
						return
					}
					if s.ReqLog != nil {
						s.ReqLog.UpdateByGenerationID(result.GenerationID, "COMPLETE", 200, finalURL, "video", "")
						s.ReqLog.UpdateDuration(result.GenerationID, elapsed)
					}
					s.TokenMgr.ReportSuccess(usedTokenID)
					writeJSON(w, 200, map[string]interface{}{
						"created": time.Now().Unix(),
						"model":   modelID,
						"data":    []map[string]interface{}{{"url": finalURL}},
					})
					return
				}
			}
		}
	}
	if s.ReqLog != nil {
		s.ReqLog.UpdateByGenerationID(result.GenerationID, "FAILED", 504, "", "", "Generation timed out")
		s.ReqLog.UpdateDuration(result.GenerationID, time.Since(startTime).Seconds())
	}
	writeJSON(w, 504, errorResp("Generation timed out", "timeout"))
}

func (s *Server) resolveOpenAIVideoGuidanceInputs(data map[string]interface{}, session *leonardo.TokenSession) ([]leonardo.ImageRef, []leonardo.FrameRef, []leonardo.FrameRef, []leonardo.VideoRef, error) {
	uploadCache := make(map[string]string)

	var imageRefs []leonardo.ImageRef
	var startFrames []leonardo.FrameRef
	var endFrames []leonardo.FrameRef
	var videoRefs []leonardo.VideoRef
	hasVideoInput := strings.TrimSpace(toString(data["video_url"])) != ""
	if !hasVideoInput {
		if rawVideos, ok := data["video_reference"].([]interface{}); ok {
			for _, item := range rawVideos {
				entry, _ := item.(map[string]interface{})
				if strings.TrimSpace(toString(entry["id"])) != "" || strings.TrimSpace(toString(entry["url"])) != "" {
					hasVideoInput = true
					break
				}
			}
		}
	}

	if imageURL := strings.TrimSpace(toString(data["image_url"])); imageURL != "" {
		imageID, err := s.resolveLeonardoImageID(session, "", imageURL, uploadCache)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("invalid image_url: %w", err)
		}
		if hasVideoInput {
			imageRefs = append(imageRefs, leonardo.ImageRef{
				ID:       imageID,
				Type:     "UPLOADED",
				Strength: "MID",
			})
		} else {
			startFrames = append(startFrames, leonardo.FrameRef{ID: imageID, Type: "UPLOADED"})
		}
	}

	if imageURL := strings.TrimSpace(toString(data["start_image_url"])); imageURL != "" {
		imageID, err := s.resolveLeonardoImageID(session, "", imageURL, uploadCache)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("invalid start_image_url: %w", err)
		}
		startFrames = append(startFrames, leonardo.FrameRef{ID: imageID, Type: "UPLOADED"})
	}

	if imageURL := strings.TrimSpace(toString(data["end_image_url"])); imageURL != "" {
		imageID, err := s.resolveLeonardoImageID(session, "", imageURL, uploadCache)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("invalid end_image_url: %w", err)
		}
		endFrames = append(endFrames, leonardo.FrameRef{ID: imageID, Type: "UPLOADED"})
	}

	if rawURLs, ok := data["image_urls"].([]interface{}); ok {
		for idx, rawURL := range rawURLs {
			imageURL := strings.TrimSpace(toString(rawURL))
			if imageURL == "" {
				continue
			}
			imageID, err := s.resolveLeonardoImageID(session, "", imageURL, uploadCache)
			if err != nil {
				return nil, nil, nil, nil, fmt.Errorf("invalid image_urls[%d]: %w", idx, err)
			}
			imageRefs = append(imageRefs, leonardo.ImageRef{
				ID:       imageID,
				Type:     "UPLOADED",
				Strength: "MID",
			})
		}
	}

	if rawGuidance, ok := data["image_guidance"].([]interface{}); ok {
		for idx, item := range rawGuidance {
			entry, _ := item.(map[string]interface{})
			rawID := toString(entry["id"])
			rawURL := toString(entry["url"])
			imageID, err := s.resolveLeonardoImageID(session, rawID, rawURL, uploadCache)
			if err != nil {
				return nil, nil, nil, nil, fmt.Errorf("invalid image_guidance[%d]: %w", idx, err)
			}
			refType := strings.TrimSpace(toString(entry["type"]))
			if refType == "" || (strings.TrimSpace(rawID) == "" && strings.TrimSpace(rawURL) != "") {
				refType = "UPLOADED"
			}
			strength := strings.ToUpper(strings.TrimSpace(toString(entry["strength"])))
			if strength == "" {
				strength = "MID"
			}
			imageRefs = append(imageRefs, leonardo.ImageRef{
				ID:       imageID,
				Type:     refType,
				Strength: strength,
			})
		}
	}

	if rawFrames, ok := data["start_frame"].([]interface{}); ok {
		for idx, item := range rawFrames {
			entry, _ := item.(map[string]interface{})
			rawID := toString(entry["id"])
			rawURL := toString(entry["url"])
			imageID, err := s.resolveLeonardoImageID(session, rawID, rawURL, uploadCache)
			if err != nil {
				return nil, nil, nil, nil, fmt.Errorf("invalid start_frame[%d]: %w", idx, err)
			}
			refType := strings.TrimSpace(toString(entry["type"]))
			if refType == "" || (strings.TrimSpace(rawID) == "" && strings.TrimSpace(rawURL) != "") {
				refType = "UPLOADED"
			}
			startFrames = append(startFrames, leonardo.FrameRef{ID: imageID, Type: refType})
		}
	}

	if rawFrames, ok := data["end_frame"].([]interface{}); ok {
		for idx, item := range rawFrames {
			entry, _ := item.(map[string]interface{})
			rawID := toString(entry["id"])
			rawURL := toString(entry["url"])
			imageID, err := s.resolveLeonardoImageID(session, rawID, rawURL, uploadCache)
			if err != nil {
				return nil, nil, nil, nil, fmt.Errorf("invalid end_frame[%d]: %w", idx, err)
			}
			refType := strings.TrimSpace(toString(entry["type"]))
			if refType == "" || (strings.TrimSpace(rawID) == "" && strings.TrimSpace(rawURL) != "") {
				refType = "UPLOADED"
			}
			endFrames = append(endFrames, leonardo.FrameRef{ID: imageID, Type: refType})
		}
	}

	if videoURL := strings.TrimSpace(toString(data["video_url"])); videoURL != "" {
		videoRef, err := s.resolveLeonardoVideoRef(session, "", videoURL, 0, uploadCache)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("invalid video_url: %w", err)
		}
		videoRefs = append(videoRefs, videoRef)
	}

	if rawVideos, ok := data["video_reference"].([]interface{}); ok {
		for idx, item := range rawVideos {
			entry, _ := item.(map[string]interface{})
			rawID := toString(entry["id"])
			rawURL := toString(entry["url"])
			var durationHint float64
			switch durationValue := entry["duration"].(type) {
			case float64:
				durationHint = durationValue
			case int:
				durationHint = float64(durationValue)
			}
			videoRef, err := s.resolveLeonardoVideoRef(session, rawID, rawURL, durationHint, uploadCache)
			if err != nil {
				return nil, nil, nil, nil, fmt.Errorf("invalid video_reference[%d]: %w", idx, err)
			}
			refType := strings.TrimSpace(toString(entry["type"]))
			if refType == "" || (strings.TrimSpace(rawID) == "" && strings.TrimSpace(rawURL) != "") {
				refType = "UPLOADED"
			}
			videoRef.Type = refType
			videoRefs = append(videoRefs, videoRef)
		}
	}

	return imageRefs, startFrames, endFrames, videoRefs, nil
}

func toString(v interface{}) string {
	if v == nil {
		return ""
	}
	switch value := v.(type) {
	case string:
		return value
	default:
		return fmt.Sprintf("%v", value)
	}
}

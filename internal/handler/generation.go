package handler

import (
	"encoding/json"
	"fmt"
	"log"
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
		"id":          "video-2.0",
		"object":      "model",
		"owned_by":    "leonardo",
		"description": "Video 2.0 standard video generation",
		"aliases":     []string{"seedance-2.0"},
		"parameters": map[string]interface{}{
			"duration": []int{4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
			"size":     []string{"1280x720", "720x1280", "720x720"},
		},
	},
	{
		"id":          "video-2.0-fast",
		"object":      "model",
		"owned_by":    "leonardo",
		"description": "Video 2.0 fast video generation",
		"aliases":     []string{"seedance-2.0-fast"},
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
	generatedStorageMu      sync.Mutex
	cookieImportMu          sync.Mutex
	cookieImportJobs        map[string]*cookieImportJob
	tokenRefreshJobMu       sync.Mutex
	tokenRefreshJobs        map[string]*tokenRefreshBatchJob
	leoSessionMu            sync.Mutex
	leoSessions             map[string]*leonardo.TokenSession
	autoRefreshMu           sync.Mutex
	autoRefreshRun          map[string]time.Time
	autoRefreshBusy         map[string]bool
	autoRefreshLoopStarted  bool
	autoRefreshSweepRunning bool
}

type generationRetryPolicy struct {
	Enabled       bool
	MaxAttempts   int
	BackoffBase   time.Duration
	StatusCodes   map[int]struct{}
	ErrorMatchers []string
}

type videoGenerationAttemptFailure struct {
	StatusCode      int
	Message         string
	ErrorType       string
	RetryCodeSource string
	MarkInvalid     bool
}

type videoGenerationSubmission struct {
	GenerationID string
	CreatedAt    time.Time
}

func (s *Server) expireStaleRunningLogs() int {
	if s == nil || s.ReqLog == nil {
		return 0
	}

	timeoutSec := 600
	if s.Config != nil {
		timeoutSec = s.Config.GetInt("generate_timeout", 600)
	}
	if timeoutSec < 1 {
		timeoutSec = 600
	}

	return s.ReqLog.ExpireStaleRunning(time.Duration(timeoutSec)*time.Second, time.Now())
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
	writeJSON(w, 400, errorResp("image generation is not supported by this deployment; use /v1/video/generations with model video-2.0 or video-2.0-fast", "invalid_request_error"))
}

// HandleChatCompletions handles POST /v1/chat/completions.
func (s *Server) HandleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAPIKey(r); err != nil {
		writeJSON(w, 401, errorResp("invalid api key", "authentication_error"))
		return
	}
	writeJSON(w, 400, errorResp("chat completions are not supported by this deployment; use /v1/video/generations with model video-2.0 or video-2.0-fast", "invalid_request_error"))
}

// HandleVideoGeneration handles POST /v1/video/generations.
func (s *Server) HandleVideoGeneration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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

	requestedModelID, _ := data["model"].(string)
	if strings.TrimSpace(requestedModelID) == "" {
		requestedModelID = "video-2.0-fast"
	}
	modelID, ok := normalizeSeedanceModelID(requestedModelID)
	if !ok {
		writeJSON(w, 400, errorResp("unsupported model; available models are video-2.0 and video-2.0-fast; seedance-2.0 and seedance-2.0-fast are also supported as aliases", "invalid_request_error"))
		return
	}
	responseModelID := publicVideoModelID(modelID)
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

	retryPolicy := s.loadGenerationRetryPolicy()
	triedTokenIDs := make(map[string]bool)
	var lastFailure *videoGenerationAttemptFailure
	var lastTokenID string
	var lastSession *leonardo.TokenSession
	var lastAttempt int

	for attempt := 1; attempt <= retryPolicy.MaxAttempts; attempt++ {
		session, usedTokenID := s.getLeonardoSessionForModelExcluding("", triedTokenIDs, modelID)
		if session == nil {
			if lastFailure != nil {
				break
			}
			s.logVideoRequestFailure("openai.video.generate", prompt, modelID, duration, width, height, usedTokenID, session, attempt, 503, "No Leonardo tokens available")
			writeJSON(w, 503, errorResp("No Leonardo tokens available", "server_error"))
			return
		}

		imageRefs, startFrames, endFrames, videoRefs, err := s.resolveOpenAIVideoGuidanceInputs(data, session)
		if err != nil {
			s.logVideoRequestFailure("openai.video.generate", prompt, modelID, duration, width, height, usedTokenID, session, attempt, 400, err.Error())
			writeJSON(w, 400, errorResp(err.Error(), "invalid_request_error"))
			return
		}

		submission, failure := s.submitLeonardoVideoGeneration(session, usedTokenID, attempt, prompt, modelID, duration, width, height, imageRefs, startFrames, endFrames, videoRefs)
		if failure == nil {
			go s.trackLeonardoVideoGeneration(session, usedTokenID, modelID, submission.GenerationID, submission.CreatedAt)
			writeJSON(w, http.StatusAccepted, map[string]interface{}{
				"id":         submission.GenerationID,
				"object":     "video.generation",
				"created":    submission.CreatedAt.Unix(),
				"model":      responseModelID,
				"status":     "in_progress",
				"poll_url":   fmt.Sprintf("/v1/video/generations/%s", submission.GenerationID),
				"request_id": submission.GenerationID,
			})
			return
		}

		lastFailure = failure
		lastTokenID = usedTokenID
		lastSession = session
		lastAttempt = attempt

		if retryPolicy.shouldRetry(failure) && attempt < retryPolicy.MaxAttempts {
			triedTokenIDs[usedTokenID] = true
			if s.TokenMgr != nil && usedTokenID != "" {
				s.TokenMgr.ReportFail(usedTokenID)
			}
			delay := retryPolicy.backoffDelay(attempt)
			if delay > 0 {
				time.Sleep(delay)
			}
			continue
		}

		if s.TokenMgr != nil && usedTokenID != "" {
			if failure.MarkInvalid {
				s.TokenMgr.ReportInvalid(usedTokenID)
			} else if retryPolicy.shouldRetry(failure) {
				s.TokenMgr.ReportFail(usedTokenID)
			}
		}
		s.logVideoRequestFailure("openai.video.generate", prompt, modelID, duration, width, height, usedTokenID, session, attempt, failure.StatusCode, failure.Message)
		writeJSON(w, failure.StatusCode, errorResp(failure.Message, failure.ErrorType))
		return
	}

	if lastFailure != nil {
		if s.TokenMgr != nil && lastTokenID != "" {
			if lastFailure.MarkInvalid {
				s.TokenMgr.ReportInvalid(lastTokenID)
			} else if retryPolicy.shouldRetry(lastFailure) {
				s.TokenMgr.ReportFail(lastTokenID)
			}
		}
		s.logVideoRequestFailure("openai.video.generate", prompt, modelID, duration, width, height, lastTokenID, lastSession, lastAttempt, lastFailure.StatusCode, lastFailure.Message)
		writeJSON(w, lastFailure.StatusCode, errorResp(lastFailure.Message, lastFailure.ErrorType))
		return
	}

	writeJSON(w, 503, errorResp("No Leonardo tokens available", "server_error"))
}

// HandleVideoGenerationStatus handles GET /v1/video/generations/{id}.
func (s *Server) HandleVideoGenerationStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.requireAPIKey(r); err != nil {
		writeJSON(w, 401, errorResp("invalid api key", "authentication_error"))
		return
	}
	generationID := extractPathParam(r.URL.Path, "/v1/video/generations/")
	if generationID == "" {
		writeJSON(w, 400, errorResp("generation id is required", "invalid_request_error"))
		return
	}
	if s.ReqLog == nil {
		writeJSON(w, 404, errorResp("generation not found", "not_found_error"))
		return
	}

	entry, ok := s.ReqLog.FindByGenerationID(generationID)
	if !ok {
		writeJSON(w, 404, errorResp("generation not found", "not_found_error"))
		return
	}

	modelID := publicVideoModelID(strings.TrimSpace(entry.Model))
	status := "in_progress"
	response := map[string]interface{}{
		"id":         generationID,
		"object":     "video.generation",
		"created":    int64(entry.Timestamp),
		"model":      modelID,
		"status":     status,
		"request_id": generationID,
	}

	switch strings.ToUpper(strings.TrimSpace(entry.TaskStatus)) {
	case "COMPLETE":
		status = "succeeded"
		response["status"] = status
		if entry.PreviewURL != "" {
			response["data"] = []map[string]interface{}{{"url": entry.PreviewURL}}
		} else {
			response["data"] = []map[string]interface{}{}
		}
	case "FAILED":
		status = "failed"
		response["status"] = status
		response["error"] = map[string]interface{}{
			"message": strings.TrimSpace(entry.ErrorMessage),
			"type":    "server_error",
		}
	default:
		response["status"] = status
	}

	returnedCode := http.StatusOK
	if status == "in_progress" {
		returnedCode = http.StatusAccepted
	}
	writeJSON(w, returnedCode, response)
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

func (s *Server) logVideoRequestFailure(operation, prompt, modelID string, duration, width, height int, tokenID string, session *leonardo.TokenSession, tokenAttempt int, statusCode int, errorMessage string) {
	if s.ReqLog == nil {
		return
	}
	if tokenAttempt <= 0 {
		tokenAttempt = 1
	}
	accountName, accountEmail := s.resolveReqLogAccount(tokenID, session)
	s.ReqLog.Add(reqlog.Entry{
		ID:           fmt.Sprintf("log-%d", time.Now().UnixNano()),
		StatusCode:   statusCode,
		TaskStatus:   "FAILED",
		Type:         "video",
		TokenID:      tokenID,
		TokenAttempt: tokenAttempt,
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

func (s *Server) loadGenerationRetryPolicy() generationRetryPolicy {
	policy := generationRetryPolicy{
		Enabled:     false,
		MaxAttempts: 1,
		BackoffBase: time.Second,
		StatusCodes: map[int]struct{}{},
	}
	if s == nil || s.Config == nil {
		return policy
	}

	policy.Enabled = s.Config.GetBool("retry_enabled", true)
	policy.MaxAttempts = s.Config.GetInt("retry_max_attempts", 3)
	if policy.MaxAttempts < 1 {
		policy.MaxAttempts = 1
	}
	if !policy.Enabled {
		policy.MaxAttempts = 1
	}

	backoffSeconds := s.Config.GetFloat("retry_backoff_seconds", 1)
	if backoffSeconds < 0 {
		backoffSeconds = 0
	}
	policy.BackoffBase = time.Duration(backoffSeconds * float64(time.Second))

	for _, code := range s.Config.GetIntSlice("retry_on_status_codes", []int{429, 451, 500, 502, 503, 504}) {
		if code > 0 {
			policy.StatusCodes[code] = struct{}{}
		}
	}
	for _, item := range s.Config.GetStringSlice("retry_on_error_types", []string{"timeout", "connection", "proxy"}) {
		item = strings.ToLower(strings.TrimSpace(item))
		if item != "" {
			policy.ErrorMatchers = append(policy.ErrorMatchers, item)
		}
	}
	return policy
}

func (p generationRetryPolicy) shouldRetry(failure *videoGenerationAttemptFailure) bool {
	if !p.Enabled || failure == nil {
		return false
	}
	if _, ok := p.StatusCodes[failure.StatusCode]; ok {
		return true
	}

	haystacks := []string{
		strings.ToLower(strings.TrimSpace(failure.Message)),
		strings.ToLower(strings.TrimSpace(failure.RetryCodeSource)),
		normalizeRetryMatcher(failure.Message),
		normalizeRetryMatcher(failure.RetryCodeSource),
	}
	for _, matcher := range p.ErrorMatchers {
		normalizedMatcher := normalizeRetryMatcher(matcher)
		for _, haystack := range haystacks {
			if haystack == "" {
				continue
			}
			if strings.Contains(haystack, matcher) || (normalizedMatcher != "" && strings.Contains(haystack, normalizedMatcher)) {
				return true
			}
		}
	}
	return false
}

func (p generationRetryPolicy) backoffDelay(attempt int) time.Duration {
	if attempt <= 0 || p.BackoffBase <= 0 {
		return 0
	}
	delay := p.BackoffBase
	for i := 1; i < attempt; i++ {
		delay *= 2
	}
	return delay
}

func normalizeRetryMatcher(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}
	replacer := strings.NewReplacer(" ", "_", "-", "_", ":", "_", ";", "_", ",", "_", ".", "_", "/", "_", "\\", "_", "(", "_", ")", "_")
	return replacer.Replace(raw)
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
	// 保留现有 Leonardo 会话缓存，避免仅仅因为保存系统配置就丢失 JWT，
	// 导致下一次手动刷新总是重新续成 1 小时。
	s.leoSessionMu.Lock()
	if s.leoSessions == nil {
		s.leoSessions = make(map[string]*leonardo.TokenSession)
	}
	s.leoSessionMu.Unlock()
}

func normalizeSeedanceModelID(modelID string) (string, bool) {
	switch strings.TrimSpace(modelID) {
	case "video-2.0", "seedance-2.0":
		return "seedance-2.0", true
	case "video-2.0-fast", "seedance-2.0-fast":
		return "seedance-2.0-fast", true
	default:
		return "", false
	}
}

func publicVideoModelID(modelID string) string {
	switch strings.TrimSpace(modelID) {
	case "seedance-2.0", "video-2.0":
		return "video-2.0"
	case "seedance-2.0-fast", "video-2.0-fast":
		return "video-2.0-fast"
	default:
		return strings.TrimSpace(modelID)
	}
}

func (s *Server) submitLeonardoVideoGeneration(session *leonardo.TokenSession, usedTokenID string, tokenAttempt int, prompt string, modelID string, duration int, width int, height int, imageRefs []leonardo.ImageRef, startFrames []leonardo.FrameRef, endFrames []leonardo.FrameRef, videoRefs []leonardo.VideoRef) (*videoGenerationSubmission, *videoGenerationAttemptFailure) {
	if s.LeonardoClient == nil {
		return nil, &videoGenerationAttemptFailure{
			StatusCode:      http.StatusInternalServerError,
			Message:         "Leonardo client not initialized",
			ErrorType:       "server_error",
			RetryCodeSource: "server_error leonardo_client_not_initialized",
		}
	}

	genReq := &leonardo.GenerateRequest{
		Model:  modelID,
		Public: true,
		Params: leonardo.GenerateParams{
			Prompt:         prompt,
			Mode:           "RESOLUTION_720",
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
		statusCode := statusCodeFromGenerationError(err)
		return nil, &videoGenerationAttemptFailure{
			StatusCode:      statusCode,
			Message:         fmt.Sprintf("generation failed: %v", err),
			ErrorType:       "server_error",
			RetryCodeSource: extractRetryCodeSource(err.Error()),
			MarkInvalid:     !isRetryableGenerationError(err),
		}
	}
	s.applyTokenCreditCost(usedTokenID, result.APICreditCost)

	if s.ReqLog != nil {
		accountName, accountEmail := s.resolveReqLogAccount(usedTokenID, session)
		if tokenAttempt <= 0 {
			tokenAttempt = 1
		}
		s.ReqLog.Add(reqlog.Entry{
			Timestamp:    float64(startTime.Unix()),
			StatusCode:   200,
			TaskStatus:   "IN_PROGRESS",
			Type:         "video",
			TokenID:      usedTokenID,
			TokenAttempt: tokenAttempt,
			AccountName:  accountName,
			AccountEmail: accountEmail,
			Model:        modelID,
			ModelParams:  fmt.Sprintf("%dx%d %ds", width, height, duration),
			Prompt:       prompt,
			GenerationID: result.GenerationID,
			CreditCost:   result.APICreditCost,
			Operation:    "leonardo.generate",
		})
	}

	return &videoGenerationSubmission{
		GenerationID: result.GenerationID,
		CreatedAt:    startTime,
	}, nil
}

func (s *Server) trackLeonardoVideoGeneration(session *leonardo.TokenSession, usedTokenID string, modelID string, generationID string, startTime time.Time) {
	timeout := s.Config.GetInt("generate_timeout", 600)
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)

	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Second)
		status, pollErr := s.LeonardoClient.PollGenerationStatus(session, generationID)
		if pollErr != nil {
			continue
		}
		elapsed := time.Since(startTime).Seconds()
		if status.Status == "FAILED" {
			failureMessage := "Leonardo reported generation status FAILED"
			if reason, reasonErr := s.LeonardoClient.GetGenerationFailureReason(session, generationID); reasonErr != nil {
				log.Printf("[Leonardo] failed to fetch generation failure reason for %s: %v", generationID, reasonErr)
			} else if strings.TrimSpace(reason) != "" {
				failureMessage = strings.TrimSpace(reason)
			}
			if s.ReqLog != nil {
				s.ReqLog.UpdateByGenerationID(generationID, "FAILED", 502, "", "", failureMessage)
				s.ReqLog.UpdateDuration(generationID, elapsed)
			}
			s.refreshTokenCredits(usedTokenID, session)
			return
		}
		if status.Status == "COMPLETE" {
			detail, detailErr := s.LeonardoClient.GetGenerationDetail(session, generationID)
			if detailErr == nil && len(detail.Images) > 0 {
				var url string
				for _, img := range detail.Images {
					if img.MotionMP4 != "" {
						url = img.MotionMP4
						break
					}
				}
				if url != "" {
					finalURL, materializeErr := s.materializeGeneratedMedia(url, generationID, "video")
					if materializeErr != nil {
						if s.ReqLog != nil {
							s.ReqLog.UpdateByGenerationID(generationID, "FAILED", 502, "", "", fmt.Sprintf("save generated media failed: %v", materializeErr))
							s.ReqLog.UpdateDuration(generationID, elapsed)
						}
						s.refreshTokenCredits(usedTokenID, session)
						return
					}
					if s.ReqLog != nil {
						s.ReqLog.UpdateByGenerationID(generationID, "COMPLETE", 200, finalURL, "video", "")
						s.ReqLog.UpdateDuration(generationID, elapsed)
					}
					s.reportSeedanceGenerationSuccess(usedTokenID, modelID)
					s.refreshTokenCredits(usedTokenID, session)
					return
				}
			}
		}
	}
	if s.ReqLog != nil {
		s.ReqLog.UpdateByGenerationID(generationID, "FAILED", 504, "", "", "Generation timed out")
		s.ReqLog.UpdateDuration(generationID, time.Since(startTime).Seconds())
	}
	s.refreshTokenCredits(usedTokenID, session)
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

func toFloat64(v interface{}) float64 {
	switch value := v.(type) {
	case float64:
		return value
	case float32:
		return float64(value)
	case int:
		return float64(value)
	case int64:
		return float64(value)
	case int32:
		return float64(value)
	case json.Number:
		f, _ := value.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(value), 64)
		return f
	default:
		f, _ := strconv.ParseFloat(strings.TrimSpace(fmt.Sprintf("%v", value)), 64)
		return f
	}
}

func isExpiredTokenInfo(info map[string]interface{}) bool {
	if info == nil {
		return false
	}
	expiresAt := toFloat64(info["expires_at"])
	if expiresAt <= 0 {
		return false
	}
	return float64(time.Now().Unix()) >= expiresAt
}

func statusCodeFromGenerationError(err error) int {
	if err == nil {
		return http.StatusBadGateway
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, " 429"), strings.Contains(msg, "(429)"), strings.Contains(msg, "returned 429"), strings.Contains(msg, "rate limit"):
		return http.StatusTooManyRequests
	case strings.Contains(msg, " 451"), strings.Contains(msg, "(451)"), strings.Contains(msg, "returned 451"):
		return 451
	case strings.Contains(msg, " 500"), strings.Contains(msg, "(500)"), strings.Contains(msg, "returned 500"):
		return http.StatusInternalServerError
	case strings.Contains(msg, " 502"), strings.Contains(msg, "(502)"), strings.Contains(msg, "returned 502"):
		return http.StatusBadGateway
	case strings.Contains(msg, " 503"), strings.Contains(msg, "(503)"), strings.Contains(msg, "returned 503"):
		return http.StatusServiceUnavailable
	case strings.Contains(msg, " 504"), strings.Contains(msg, "(504)"), strings.Contains(msg, "returned 504"), strings.Contains(msg, "timeout"):
		return http.StatusGatewayTimeout
	default:
		return http.StatusBadGateway
	}
}

func isRetryableGenerationError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "unexpected eof") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "returned 429") ||
		strings.Contains(msg, "(429)") ||
		strings.Contains(msg, "proxy")
}

func extractRetryCodeSource(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}

	codes := []string{}
	for _, code := range []string{"429", "451", "500", "502", "503", "504"} {
		if strings.Contains(raw, code) {
			codes = append(codes, code)
		}
	}

	keywords := []string{}
	for _, item := range []string{"timeout", "connection", "proxy", "insufficient_tokens", "insufficient tokens", "rate limit", "unexpected eof", "server_error"} {
		if strings.Contains(raw, item) {
			keywords = append(keywords, item)
		}
	}

	parts := []string{raw, normalizeRetryMatcher(raw)}
	if len(codes) > 0 {
		parts = append(parts, strings.Join(codes, " "))
	}
	if len(keywords) > 0 {
		parts = append(parts, strings.Join(keywords, " "))
	}
	return strings.Join(parts, " ")
}

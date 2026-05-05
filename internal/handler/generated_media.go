package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"time"
)

const (
	generatedMediaFetchTimeout = 5 * time.Minute
	maxGeneratedMediaBytes     = 500 << 20
)

type generatedMediaPayload struct {
	Data        []byte
	ContentType string
	FileName    string
}

func (s *Server) materializeGeneratedMedia(sourceURL, generationID, mediaKind string) (string, error) {
	sourceURL = strings.TrimSpace(sourceURL)
	if sourceURL == "" {
		return "", fmt.Errorf("source url is required")
	}

	useUpstream := s != nil && s.Config != nil && s.Config.GetBool("use_upstream_result_url", false)
	imgBedEnabled := s != nil && s.Config != nil && s.Config.GetBool("imgbed_enabled", false)
	imgBedAPIURL := ""
	imgBedAPIKey := ""
	if s != nil && s.Config != nil {
		imgBedAPIURL = strings.TrimSpace(s.Config.GetString("imgbed_api_url", ""))
		imgBedAPIKey = strings.TrimSpace(s.Config.GetString("imgbed_api_key", ""))
	}
	imgBedReady := imgBedEnabled && imgBedAPIURL != "" && imgBedAPIKey != ""

	if !imgBedReady && useUpstream {
		return sourceURL, nil
	}

	payload, err := s.fetchGeneratedMediaPayload(sourceURL, generationID, mediaKind)
	if err != nil {
		if useUpstream {
			return sourceURL, nil
		}
		return "", err
	}

	if imgBedEnabled && !imgBedReady {
		log.Printf("[generated] image bed enabled but config incomplete; api_url=%t api_key=%t", imgBedAPIURL != "", imgBedAPIKey != "")
	}

	if imgBedReady {
		uploadedURL, uploadErr := s.uploadGeneratedMediaToImgBed(payload, imgBedAPIURL, imgBedAPIKey)
		if uploadErr == nil {
			return uploadedURL, nil
		}
		log.Printf("[generated] image bed upload failed for %s: %v", payload.FileName, uploadErr)
		if useUpstream {
			return sourceURL, nil
		}
	}

	localURL, err := s.saveGeneratedMediaToLocal(payload)
	if err != nil {
		if useUpstream {
			return sourceURL, nil
		}
		return "", err
	}
	return localURL, nil
}

func (s *Server) fetchGeneratedMediaPayload(sourceURL, generationID, mediaKind string) (*generatedMediaPayload, error) {
	parsedURL, err := url.Parse(strings.TrimSpace(sourceURL))
	if err != nil {
		return nil, fmt.Errorf("invalid generated media url: %w", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return nil, fmt.Errorf("generated media url must use http or https")
	}
	if parsedURL.RawPath == "" {
		parsedURL.RawPath = parsedURL.EscapedPath()
	}

	httpClient, err := s.newResourceHTTPClient(generatedMediaFetchTimeout)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("GET", parsedURL.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "leo-go-generated-fetch/1.0")
	req.Header.Set("Accept", "*/*")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch generated media failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("generated media url returned %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxGeneratedMediaBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read generated media failed: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("generated media url returned empty body")
	}
	if len(data) > maxGeneratedMediaBytes {
		return nil, fmt.Errorf("generated media exceeds %d MB limit", maxGeneratedMediaBytes>>20)
	}

	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	if mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0])); mediaType != "" {
		contentType = mediaType
	}

	ext := ""
	switch mediaKind {
	case "image":
		ext = imageExtFromContentType(contentType)
		if ext == "" {
			ext = imageExtFromURL(parsedURL.Path)
		}
		if ext == "" {
			ext = "jpg"
		}
	default:
		ext = videoExtFromContentType(contentType)
		if ext == "" {
			ext = videoExtFromURL(parsedURL.Path)
		}
		if ext == "" {
			ext = "mp4"
		}
	}

	baseName := strings.TrimSpace(generationID)
	if baseName == "" {
		baseName = strings.TrimSuffix(pathpkg.Base(parsedURL.Path), pathpkg.Ext(parsedURL.Path))
	}
	baseName = sanitizeGeneratedFilename(baseName)
	if baseName == "" {
		baseName = fmt.Sprintf("generated-%d", time.Now().Unix())
	}

	return &generatedMediaPayload{
		Data:        data,
		ContentType: contentType,
		FileName:    fmt.Sprintf("%s.%s", baseName, ext),
	}, nil
}

func (s *Server) saveGeneratedMediaToLocal(payload *generatedMediaPayload) (string, error) {
	if payload == nil {
		return "", fmt.Errorf("generated media payload is required")
	}
	if s == nil || strings.TrimSpace(s.GeneratedDir) == "" {
		return "", fmt.Errorf("generated dir is not configured")
	}

	fileName := strings.TrimSpace(payload.FileName)
	if fileName == "" {
		return "", fmt.Errorf("generated media file name is required")
	}
	filePath := filepath.Join(s.GeneratedDir, fileName)

	s.generatedStorageMu.Lock()
	defer s.generatedStorageMu.Unlock()

	if err := os.MkdirAll(s.GeneratedDir, 0o755); err != nil {
		return "", fmt.Errorf("ensure generated dir: %w", err)
	}
	if err := os.WriteFile(filePath, payload.Data, 0o644); err != nil {
		return "", fmt.Errorf("save generated media: %w", err)
	}
	if _, pruneErr := s.enforceGeneratedStorageLimitLocked(); pruneErr != nil {
		log.Printf("[generated] failed to prune generated storage after saving %s: %v", fileName, pruneErr)
	}

	return s.buildGeneratedPublicURL(fileName), nil
}

func (s *Server) uploadGeneratedMediaToImgBed(payload *generatedMediaPayload, apiURL, apiKey string) (string, error) {
	if payload == nil {
		return "", fmt.Errorf("generated media payload is required")
	}
	if strings.TrimSpace(apiURL) == "" {
		return "", fmt.Errorf("image bed api url is required")
	}
	if strings.TrimSpace(apiKey) == "" {
		return "", fmt.Errorf("image bed api key is required")
	}

	uploadURL, err := buildImgBedUploadURL(apiURL, apiKey)
	if err != nil {
		return "", err
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, escapeMultipartFilename(payload.FileName)))
	if strings.TrimSpace(payload.ContentType) != "" {
		partHeader.Set("Content-Type", payload.ContentType)
	} else {
		partHeader.Set("Content-Type", "application/octet-stream")
	}
	part, err := writer.CreatePart(partHeader)
	if err != nil {
		return "", fmt.Errorf("create image bed upload part: %w", err)
	}
	if _, err := part.Write(payload.Data); err != nil {
		return "", fmt.Errorf("write image bed upload body: %w", err)
	}
	_ = writer.WriteField("authCode", apiKey)
	_ = writer.WriteField("returnFormat", "full")
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("finalize image bed upload body: %w", err)
	}

	httpClient, err := s.newResourceHTTPClient(generatedMediaFetchTimeout)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", uploadURL, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "leo-go-imgbed-upload/1.0")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload to image bed failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", fmt.Errorf("read image bed response failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("image bed returned %d: %s", resp.StatusCode, summarizeResponseBody(respBody))
	}

	uploadedURL, err := extractImgBedResponseURL(respBody)
	if err != nil {
		return "", fmt.Errorf("parse image bed response failed: %w", err)
	}
	return uploadedURL, nil
}

func (s *Server) buildGeneratedPublicURL(fileName string) string {
	fileName = strings.TrimLeft(strings.ReplaceAll(fileName, "\\", "/"), "/")
	if s.Config != nil {
		baseURL := strings.TrimSpace(s.Config.GetString("public_base_url", ""))
		if baseURL != "" {
			return strings.TrimRight(baseURL, "/") + "/generated/" + fileName
		}
	}
	return "/generated/" + fileName
}

func buildImgBedUploadURL(rawURL, apiKey string) (string, error) {
	parsedURL, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", fmt.Errorf("invalid image bed api url: %w", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return "", fmt.Errorf("image bed api url must use http or https")
	}
	query := parsedURL.Query()
	if strings.TrimSpace(query.Get("authCode")) == "" {
		query.Set("authCode", strings.TrimSpace(apiKey))
	}
	if strings.TrimSpace(query.Get("returnFormat")) == "" {
		query.Set("returnFormat", "full")
	}
	parsedURL.RawQuery = query.Encode()
	return parsedURL.String(), nil
}

func extractImgBedResponseURL(body []byte) (string, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return "", fmt.Errorf("empty response body")
	}
	if looksLikeHTTPURL(string(trimmed)) {
		return string(trimmed), nil
	}

	var decoded interface{}
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		return "", fmt.Errorf("invalid json response: %w", err)
	}
	if extracted := findResponseURL(decoded); extracted != "" {
		return extracted, nil
	}
	if msg := findResponseMessage(decoded); msg != "" {
		return "", fmt.Errorf(msg)
	}
	return "", fmt.Errorf("no usable url found in response")
}

func findResponseURL(value interface{}) string {
	switch v := value.(type) {
	case string:
		if looksLikeHTTPURL(v) {
			return strings.TrimSpace(v)
		}
	case []interface{}:
		for _, item := range v {
			if found := findResponseURL(item); found != "" {
				return found
			}
		}
	case map[string]interface{}:
		for _, key := range []string{"url", "src", "link", "href"} {
			if found := findResponseURL(v[key]); found != "" {
				return found
			}
		}
		for _, key := range []string{"data", "result", "image", "file", "payload"} {
			if found := findResponseURL(v[key]); found != "" {
				return found
			}
		}
		for _, item := range v {
			if found := findResponseURL(item); found != "" {
				return found
			}
		}
	}
	return ""
}

func findResponseMessage(value interface{}) string {
	switch v := value.(type) {
	case string:
		text := strings.TrimSpace(v)
		if text != "" && !looksLikeHTTPURL(text) {
			return text
		}
	case []interface{}:
		for _, item := range v {
			if found := findResponseMessage(item); found != "" {
				return found
			}
		}
	case map[string]interface{}:
		for _, key := range []string{"message", "msg", "error", "detail"} {
			if found := findResponseMessage(v[key]); found != "" {
				return found
			}
		}
	}
	return ""
}

func looksLikeHTTPURL(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(strings.ToLower(value), "http://") || strings.HasPrefix(strings.ToLower(value), "https://")
}

func summarizeResponseBody(body []byte) string {
	text := strings.TrimSpace(string(bytes.TrimSpace(body)))
	if text == "" {
		return "empty response"
	}
	if len(text) > 240 {
		return text[:240] + "..."
	}
	return text
}

func escapeMultipartFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "\\\\")
	name = strings.ReplaceAll(name, `"`, `\"`)
	return name
}

func sanitizeGeneratedFilename(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return ""
	}

	var b strings.Builder
	lastDash := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			lastDash = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_':
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}

	out := strings.Trim(b.String(), "-")
	if out == "" {
		return ""
	}
	return out
}

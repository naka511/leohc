package handler

import (
	"fmt"
	"io"
	"log"
	"net/http"
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

func (s *Server) materializeGeneratedMedia(sourceURL, generationID, mediaKind string) (string, error) {
	sourceURL = strings.TrimSpace(sourceURL)
	if sourceURL == "" {
		return "", fmt.Errorf("source url is required")
	}

	if s.GeneratedDir == "" {
		return "", fmt.Errorf("generated dir is not configured")
	}

	localURL, err := s.downloadGeneratedMediaToLocal(sourceURL, generationID, mediaKind)
	if err != nil {
		if s.Config != nil && s.Config.GetBool("use_upstream_result_url", false) {
			return sourceURL, nil
		}
		return "", err
	}

	if s.Config != nil && s.Config.GetBool("use_upstream_result_url", false) {
		return sourceURL, nil
	}
	return localURL, nil
}

func (s *Server) downloadGeneratedMediaToLocal(sourceURL, generationID, mediaKind string) (string, error) {
	parsedURL, err := url.Parse(strings.TrimSpace(sourceURL))
	if err != nil {
		return "", fmt.Errorf("invalid generated media url: %w", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return "", fmt.Errorf("generated media url must use http or https")
	}
	if parsedURL.RawPath == "" {
		parsedURL.RawPath = parsedURL.EscapedPath()
	}

	httpClient, err := s.newResourceHTTPClient(generatedMediaFetchTimeout)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("GET", parsedURL.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "leo-go-generated-fetch/1.0")
	req.Header.Set("Accept", "*/*")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch generated media failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("generated media url returned %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxGeneratedMediaBytes+1))
	if err != nil {
		return "", fmt.Errorf("read generated media failed: %w", err)
	}
	if len(data) == 0 {
		return "", fmt.Errorf("generated media url returned empty body")
	}
	if len(data) > maxGeneratedMediaBytes {
		return "", fmt.Errorf("generated media exceeds %d MB limit", maxGeneratedMediaBytes>>20)
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
	fileName := fmt.Sprintf("%s.%s", baseName, ext)
	filePath := filepath.Join(s.GeneratedDir, fileName)

	s.generatedStorageMu.Lock()
	defer s.generatedStorageMu.Unlock()

	if err := os.MkdirAll(s.GeneratedDir, 0o755); err != nil {
		return "", fmt.Errorf("ensure generated dir: %w", err)
	}
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		return "", fmt.Errorf("save generated media: %w", err)
	}
	if _, pruneErr := s.enforceGeneratedStorageLimitLocked(); pruneErr != nil {
		log.Printf("[generated] failed to prune generated storage after saving %s: %v", fileName, pruneErr)
	}

	return s.buildGeneratedPublicURL(fileName), nil
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

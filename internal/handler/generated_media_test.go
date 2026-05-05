package handler

import (
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"leo-go/internal/config"
)

func TestMaterializeGeneratedMediaUsesRemoteURLUploadWhenUpstreamModeEnabled(t *testing.T) {
	cfg := config.Global()
	original := cfg.GetAll()
	cfg.SetAll(map[string]interface{}{})
	t.Cleanup(func() {
		cfg.SetAll(original)
	})

	sourceHits := 0
	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceHits++
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("unexpected-fetch"))
	}))
	defer sourceServer.Close()

	uploadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart form: %v", err)
		}
		if got := r.FormValue("url"); got != sourceServer.URL+"/remote.mp4" {
			t.Fatalf("expected remote url form field, got %q", got)
		}
		if got := r.FormValue("authCode"); got != "secret-123" {
			t.Fatalf("expected authCode form field, got %q", got)
		}
		if _, _, err := r.FormFile("file"); err == nil {
			t.Fatal("did not expect file payload when upstream direct mode is enabled")
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"src":"https://imgbed.example/remote-mode.mp4"}}`))
	}))
	defer uploadServer.Close()

	cfg.SetAll(map[string]interface{}{
		"use_upstream_result_url": true,
		"imgbed_enabled":          true,
		"imgbed_api_url":          uploadServer.URL + "/upload?serverCompress=false",
		"imgbed_api_key":          "secret-123",
	})

	srv := &Server{
		Config:       cfg,
		GeneratedDir: t.TempDir(),
	}
	resultURL, err := srv.materializeGeneratedMedia(sourceServer.URL+"/remote.mp4", "remote-mode", "video")
	if err != nil {
		t.Fatalf("materializeGeneratedMedia returned error: %v", err)
	}
	if resultURL != "https://imgbed.example/remote-mode.mp4" {
		t.Fatalf("expected remote imgbed url, got %q", resultURL)
	}
	if sourceHits != 0 {
		t.Fatalf("expected source media not to be fetched locally, got %d hit(s)", sourceHits)
	}
}

func TestMaterializeGeneratedMediaUploadsToImgBed(t *testing.T) {
	cfg := config.Global()
	original := cfg.GetAll()
	cfg.SetAll(map[string]interface{}{})
	t.Cleanup(func() {
		cfg.SetAll(original)
	})

	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("fake-png-data"))
	}))
	defer sourceServer.Close()

	uploaded := false
	uploadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("authCode"); got != "secret-123" {
			t.Fatalf("expected authCode query, got %q", got)
		}
		if got := r.URL.Query().Get("returnFormat"); got != "full" {
			t.Fatalf("expected returnFormat=full, got %q", got)
		}
		if got := r.URL.Query().Get("serverCompress"); got != "false" {
			t.Fatalf("expected existing query param to be preserved, got %q", got)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart form: %v", err)
		}
		if got := r.FormValue("authCode"); got != "secret-123" {
			t.Fatalf("expected authCode form field, got %q", got)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("read file field: %v", err)
		}
		defer file.Close()

		data, err := io.ReadAll(file)
		if err != nil {
			t.Fatalf("read uploaded file: %v", err)
		}
		if string(data) != "fake-png-data" {
			t.Fatalf("unexpected uploaded payload: %q", string(data))
		}
		if header.Filename != "demo-image.png" {
			t.Fatalf("unexpected uploaded filename: %q", header.Filename)
		}
		uploaded = true

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"src":"https://imgbed.example/demo-image.png"}}`))
	}))
	defer uploadServer.Close()

	cfg.SetAll(map[string]interface{}{
		"imgbed_enabled": true,
		"imgbed_api_url": uploadServer.URL + "/upload?serverCompress=false",
		"imgbed_api_key": "secret-123",
	})

	srv := &Server{
		Config:       cfg,
		GeneratedDir: t.TempDir(),
	}
	resultURL, err := srv.materializeGeneratedMedia(sourceServer.URL+"/result.png", "demo-image", "image")
	if err != nil {
		t.Fatalf("materializeGeneratedMedia returned error: %v", err)
	}
	if !uploaded {
		t.Fatal("expected media to be uploaded to image bed")
	}
	if resultURL != "https://imgbed.example/demo-image.png" {
		t.Fatalf("expected imgbed url, got %q", resultURL)
	}

	entries, err := os.ReadDir(srv.GeneratedDir)
	if err != nil {
		t.Fatalf("read generated dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no local files when image bed upload succeeds, got %d", len(entries))
	}
}

func TestMaterializeGeneratedMediaFallsBackToLocalWhenImgBedUploadFails(t *testing.T) {
	cfg := config.Global()
	original := cfg.GetAll()
	cfg.SetAll(map[string]interface{}{})
	t.Cleanup(func() {
		cfg.SetAll(original)
	})

	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("fake-video-data"))
	}))
	defer sourceServer.Close()

	uploadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"upstream failed"}`))
	}))
	defer uploadServer.Close()

	cfg.SetAll(map[string]interface{}{
		"imgbed_enabled": true,
		"imgbed_api_url": uploadServer.URL + "/upload",
		"imgbed_api_key": "secret-123",
	})

	dir := t.TempDir()
	srv := &Server{
		Config:       cfg,
		GeneratedDir: dir,
	}
	resultURL, err := srv.materializeGeneratedMedia(sourceServer.URL+"/result.mp4", "demo-video", "video")
	if err != nil {
		t.Fatalf("materializeGeneratedMedia returned error: %v", err)
	}
	if resultURL != "/generated/demo-video.mp4" {
		t.Fatalf("expected local generated url fallback, got %q", resultURL)
	}

	filePath := filepath.Join(dir, "demo-video.mp4")
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("expected local fallback file to exist: %v", err)
	}
	if string(data) != "fake-video-data" {
		t.Fatalf("unexpected local fallback payload: %q", string(data))
	}
}

func TestExtractImgBedResponseURLParsesNestedResponse(t *testing.T) {
	url, err := extractImgBedResponseURL([]byte(`{"data":{"items":[{"link":"https://imgbed.example/final.mp4"}]}}`))
	if err != nil {
		t.Fatalf("extractImgBedResponseURL returned error: %v", err)
	}
	if url != "https://imgbed.example/final.mp4" {
		t.Fatalf("expected nested response url, got %q", url)
	}
}

func TestExtractImgBedResponseURLReturnsMessageWhenMissingURL(t *testing.T) {
	_, err := extractImgBedResponseURL([]byte(`{"success":false,"message":"auth failed"}`))
	if err == nil {
		t.Fatal("expected error when response does not include a url")
	}
	if !strings.Contains(err.Error(), "auth failed") {
		t.Fatalf("expected message to surface original error, got %v", err)
	}
}

func TestEscapeMultipartFilename(t *testing.T) {
	name := escapeMultipartFilename(`demo"file.png`)
	if strings.Contains(name, `"`) && !strings.Contains(name, `\"`) {
		t.Fatalf("expected quotes to be escaped, got %q", name)
	}
}

func TestMultipartFileFieldNameConsistency(t *testing.T) {
	var body strings.Builder
	writer := multipart.NewWriter(&body)
	if _, err := writer.CreateFormFile("file", "demo.png"); err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	if !strings.Contains(body.String(), `name="file"`) {
		t.Fatalf("expected multipart payload to keep file field name, got %q", body.String())
	}
}

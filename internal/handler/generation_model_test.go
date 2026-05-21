package handler

import (
	"errors"
	"testing"
)

func TestNormalizeVideoModelIDSupportsSora2(t *testing.T) {
	modelID, ok := normalizeVideoModelID("sora-2")
	if !ok {
		t.Fatal("normalizeVideoModelID did not accept sora-2")
	}
	if modelID != "sora-2" {
		t.Fatalf("modelID = %q, want sora-2", modelID)
	}
	if publicVideoModelID(modelID) != "sora-2" {
		t.Fatalf("publicVideoModelID(%q) = %q, want sora-2", modelID, publicVideoModelID(modelID))
	}
}

func TestNormalizeVideoModelIDSupportsKlingO3(t *testing.T) {
	modelID, ok := normalizeVideoModelID("kling-o3")
	if !ok {
		t.Fatal("normalizeVideoModelID did not accept kling-o3")
	}
	if modelID != "kling-video-o-3" {
		t.Fatalf("modelID = %q, want kling-video-o-3", modelID)
	}
	if publicVideoModelID(modelID) != "kling-o3" {
		t.Fatalf("publicVideoModelID(%q) = %q, want kling-o3", modelID, publicVideoModelID(modelID))
	}
}

func TestSora2DefaultsMatchTextToVideoCapture(t *testing.T) {
	if got := defaultVideoDuration("sora-2"); got != 8 {
		t.Fatalf("defaultVideoDuration(sora-2) = %d, want 8", got)
	}
	width, height := defaultVideoSize("sora-2")
	if width != 720 || height != 1280 {
		t.Fatalf("defaultVideoSize(sora-2) = %dx%d, want 720x1280", width, height)
	}
}

func TestKlingO3DefaultsMatchTextToVideoCapture(t *testing.T) {
	if got := defaultVideoDuration("kling-video-o-3"); got != 3 {
		t.Fatalf("defaultVideoDuration(kling-video-o-3) = %d, want 3", got)
	}
	width, height := defaultVideoSize("kling-video-o-3")
	if width != 1080 || height != 1920 {
		t.Fatalf("defaultVideoSize(kling-video-o-3) = %dx%d, want 1080x1920", width, height)
	}
	if got := leonardoVideoResolutionMode("kling-video-o-3", width, height); got != "RESOLUTION_1080" {
		t.Fatalf("leonardoVideoResolutionMode(kling-video-o-3) = %q, want RESOLUTION_1080", got)
	}
}

func TestSora2AllowedDurationsAndSizes(t *testing.T) {
	for _, duration := range []int{4, 8, 12} {
		if !isAllowedSora2Duration(duration) {
			t.Fatalf("duration %d should be allowed for sora-2", duration)
		}
	}
	for _, duration := range []int{5, 10, 15} {
		if isAllowedSora2Duration(duration) {
			t.Fatalf("duration %d should not be allowed for sora-2", duration)
		}
	}
	if !isAllowedSora2Size(720, 1280) {
		t.Fatal("720x1280 should be allowed for sora-2")
	}
	if !isAllowedSora2Size(1280, 720) {
		t.Fatal("1280x720 should be allowed for sora-2")
	}
	if isAllowedSora2Size(960, 960) {
		t.Fatal("960x960 should not be allowed for sora-2")
	}
}

func TestKlingO3AllowedDurationsSizesAndGuidance(t *testing.T) {
	if !isAllowedKlingO3Duration(3) {
		t.Fatal("duration 3 should be allowed for kling-o3")
	}
	if isAllowedKlingO3Duration(4) {
		t.Fatal("duration 4 should not be allowed for kling-o3")
	}
	if !isAllowedKlingO3Size(1080, 1920) {
		t.Fatal("1080x1920 should be allowed for kling-o3")
	}
	if !isAllowedKlingO3Size(1920, 1080) {
		t.Fatal("1920x1080 should be allowed for kling-o3")
	}
	if isAllowedKlingO3Size(1280, 720) {
		t.Fatal("1280x720 should not be allowed for kling-o3")
	}
	if hasUnsupportedKlingO3GuidanceInput(map[string]interface{}{"prompt": "text only"}) {
		t.Fatal("text-only request should not have unsupported kling-o3 guidance input")
	}
	if hasUnsupportedKlingO3GuidanceInput(map[string]interface{}{"image_url": "https://example.com/a.png"}) {
		t.Fatal("image_url should be allowed as Kling O3 image-reference guidance")
	}
	if hasUnsupportedKlingO3GuidanceInput(map[string]interface{}{"image_guidance": []interface{}{map[string]interface{}{"id": "img"}}}) {
		t.Fatal("image_guidance should be allowed as Kling O3 image-reference guidance")
	}
	if hasUnsupportedKlingO3GuidanceInput(map[string]interface{}{"start_frame": []interface{}{map[string]interface{}{"id": "img"}}}) {
		t.Fatal("start_frame should be allowed as Kling O3 start-frame guidance")
	}
	if hasUnsupportedKlingO3GuidanceInput(map[string]interface{}{"end_frame": []interface{}{map[string]interface{}{"id": "img"}}}) {
		t.Fatal("end_frame should be allowed as Kling O3 end-frame guidance")
	}
	if hasUnsupportedKlingO3GuidanceInput(map[string]interface{}{
		"image_guidance": []interface{}{
			map[string]interface{}{"id": "img-1"},
			map[string]interface{}{"id": "img-2"},
			map[string]interface{}{"id": "img-3"},
			map[string]interface{}{"id": "img-4"},
		},
	}) {
		t.Fatal("multiple image_guidance entries should be allowed as Kling O3 image-reference guidance")
	}
	if !hasUnsupportedKlingO3GuidanceInput(map[string]interface{}{"video_reference": []interface{}{map[string]interface{}{"id": "vid"}}}) {
		t.Fatal("video_reference should be detected as unsupported Kling O3 guidance input")
	}
}

func TestSora2UnsupportedGuidanceDetection(t *testing.T) {
	if hasUnsupportedSora2GuidanceInput(map[string]interface{}{"prompt": "text only"}) {
		t.Fatal("text-only request should not have unsupported guidance input")
	}
	if hasUnsupportedSora2GuidanceInput(map[string]interface{}{"image_url": "https://example.com/a.png"}) {
		t.Fatal("image_url should be allowed as Sora 2 start-frame guidance")
	}
	if hasUnsupportedSora2GuidanceInput(map[string]interface{}{"start_image_url": "https://example.com/a.png"}) {
		t.Fatal("start_image_url should be allowed as Sora 2 start-frame guidance")
	}
	if hasUnsupportedSora2GuidanceInput(map[string]interface{}{"start_frame": []interface{}{map[string]interface{}{"id": "img"}}}) {
		t.Fatal("start_frame should be allowed as Sora 2 start-frame guidance")
	}
	if !hasUnsupportedSora2GuidanceInput(map[string]interface{}{"video_reference": []interface{}{map[string]interface{}{"id": "vid"}}}) {
		t.Fatal("video_reference should be detected as unsupported Sora 2 guidance input")
	}
	if !hasUnsupportedSora2GuidanceInput(map[string]interface{}{"end_image_url": "https://example.com/end.png"}) {
		t.Fatal("end_image_url should be detected as unsupported Sora 2 guidance input")
	}
}

func TestCountSora2StartFrameInputs(t *testing.T) {
	if got := countSora2StartFrameInputs(map[string]interface{}{"prompt": "text only"}); got != 0 {
		t.Fatalf("count = %d, want 0", got)
	}
	if got := countSora2StartFrameInputs(map[string]interface{}{"image_url": "https://example.com/a.png"}); got != 1 {
		t.Fatalf("count = %d, want 1", got)
	}
	got := countSora2StartFrameInputs(map[string]interface{}{
		"image_url":       "https://example.com/a.png",
		"start_image_url": "https://example.com/b.png",
		"start_frame":     []interface{}{map[string]interface{}{"id": "img"}},
	})
	if got != 3 {
		t.Fatalf("count = %d, want 3", got)
	}
}

func TestRetryableGuidancePreparationError(t *testing.T) {
	retryable := []string{
		`invalid image_urls[0]: upload init failed: graphql request failed: Post "https://api.leonardo.ai/v1/graphql": EOF`,
		`invalid image_urls[2]: s3 upload failed: s3 upload returned 403: Policy expired`,
		`invalid start_frame[0]: s3 upload failed: s3 upload failed after 3 attempt(s): read: connection reset by peer`,
		`invalid video_reference[0]: wait for staged video asset failed: context deadline exceeded`,
		`invalid image_urls[0]: wait for init image failed: moderation polling returned unknown error`,
	}
	for _, msg := range retryable {
		if !isRetryableGuidancePreparationError(errors.New(msg)) {
			t.Fatalf("expected retryable guidance preparation error for %q", msg)
		}
	}

	nonRetryable := []string{
		`invalid image_url: image url returned 400`,
		`invalid image_urls[0]: image url returned 404`,
		`invalid image_urls[0]: image url did not return an image content type`,
		`invalid image_urls[0]: either id or url is required`,
	}
	for _, msg := range nonRetryable {
		if isRetryableGuidancePreparationError(errors.New(msg)) {
			t.Fatalf("expected non-retryable guidance preparation error for %q", msg)
		}
	}
}

func TestRequiredCreditsForVideoModel(t *testing.T) {
	tests := []struct {
		modelID string
		want    float64
		ok      bool
	}{
		{modelID: "video-2.0", want: video2RequiredCredits, ok: true},
		{modelID: "seedance-2.0", want: video2RequiredCredits, ok: true},
		{modelID: "video-2.0-fast", want: video2FastRequiredCredits, ok: true},
		{modelID: "seedance-2.0-fast", want: video2FastRequiredCredits, ok: true},
		{modelID: "sora-2", want: 0, ok: false},
		{modelID: "kling-o3", want: 0, ok: false},
	}
	for _, tt := range tests {
		got, ok := requiredCreditsForVideoModel(tt.modelID)
		if ok != tt.ok || got != tt.want {
			t.Fatalf("requiredCreditsForVideoModel(%q) = %.0f, %v; want %.0f, %v", tt.modelID, got, ok, tt.want, tt.ok)
		}
	}
}

func TestTokenCreditsAvailable(t *testing.T) {
	if got, ok := tokenCreditsAvailable(map[string]interface{}{"credits_available": 3950}); !ok || got != 3950 {
		t.Fatalf("credits_available = %.0f, %v; want 3950, true", got, ok)
	}
	if got, ok := tokenCreditsAvailable(map[string]interface{}{"credits": 8500}); !ok || got != 8500 {
		t.Fatalf("credits = %.0f, %v; want 8500, true", got, ok)
	}
	if got, ok := tokenCreditsAvailable(map[string]interface{}{"max_credits": 8500}); !ok || got != 0 {
		t.Fatalf("max_credits-only = %.0f, %v; want 0, true", got, ok)
	}
	if _, ok := tokenCreditsAvailable(map[string]interface{}{}); ok {
		t.Fatal("empty token info should not have known credits")
	}
}

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

func TestSora2DefaultsMatchTextToVideoCapture(t *testing.T) {
	if got := defaultVideoDuration("sora-2"); got != 8 {
		t.Fatalf("defaultVideoDuration(sora-2) = %d, want 8", got)
	}
	width, height := defaultVideoSize("sora-2")
	if width != 720 || height != 1280 {
		t.Fatalf("defaultVideoSize(sora-2) = %dx%d, want 720x1280", width, height)
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

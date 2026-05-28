package leonardo

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestGenerateBuildsSora2TextToVideoPayload(t *testing.T) {
	var requestBody string
	client := NewClient("")
	client.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			requestBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"data":{"generate":{"apiCreditCost":12,"generationId":"gen-sora-2"}}}`)),
			}, nil
		}),
	}
	session := &TokenSession{
		JWT:       "jwt",
		JWTExpiry: time.Now().Add(time.Hour),
	}

	result, err := client.Generate(session, &GenerateRequest{
		Model:  "sora2",
		Public: true,
		Params: GenerateParams{
			Prompt:         "龟兔赛跑",
			Mode:           "RESOLUTION_720",
			Duration:       8,
			Quantity:       1,
			Width:          720,
			Height:         1280,
			MotionHasAudio: true,
			Seed:           -1,
			ImageRefs:      []ImageRef{{ID: "ignored-for-sora"}},
		},
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if result.GenerationID != "gen-sora-2" || result.APICreditCost != 12 {
		t.Fatalf("unexpected result: %+v", result)
	}

	payload := mustJSONMap(t, requestBody)
	if payload["operationName"] != "Generate" {
		t.Fatalf("operationName = %v, want Generate", payload["operationName"])
	}
	request := payload["variables"].(map[string]interface{})["request"].(map[string]interface{})
	if request["model"] != "sora-2" {
		t.Fatalf("model = %v, want sora-2", request["model"])
	}
	if request["public"] != true {
		t.Fatalf("public = %v, want true", request["public"])
	}
	params := request["parameters"].(map[string]interface{})
	want := map[string]interface{}{
		"height":   float64(1280),
		"width":    float64(720),
		"duration": float64(8),
		"quantity": float64(1),
		"prompt":   "龟兔赛跑",
		"mode":     "RESOLUTION_720",
	}
	if len(params) != len(want) {
		t.Fatalf("params keys = %v, want only %v", keysOf(params), keysOf(want))
	}
	for key, wantValue := range want {
		if params[key] != wantValue {
			t.Fatalf("params[%s] = %v, want %v", key, params[key], wantValue)
		}
	}
}

func TestGenerateBuildsSora2ImageToVideoPayload(t *testing.T) {
	var requestBody string
	client := NewClient("")
	client.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			requestBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"data":{"generate":{"apiCreditCost":12,"generationId":"gen-sora-2-image"}}}`)),
			}, nil
		}),
	}
	session := &TokenSession{
		JWT:       "jwt",
		JWTExpiry: time.Now().Add(time.Hour),
	}

	_, err := client.Generate(session, &GenerateRequest{
		Model:  "sora2",
		Public: true,
		Params: GenerateParams{
			Prompt:   "武侠视频",
			Mode:     "RESOLUTION_720",
			Duration: 8,
			Quantity: 1,
			Width:    720,
			Height:   1280,
			StartFrame: []FrameRef{{
				ID:   "53f075af-2c0a-43b0-a90a-9e24c6050cb4",
				Type: "UPLOADED",
			}},
		},
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	payload := mustJSONMap(t, requestBody)
	request := payload["variables"].(map[string]interface{})["request"].(map[string]interface{})
	params := request["parameters"].(map[string]interface{})
	want := map[string]interface{}{
		"height":   float64(1280),
		"width":    float64(720),
		"duration": float64(8),
		"quantity": float64(1),
		"prompt":   "武侠视频",
		"mode":     "RESOLUTION_720",
	}
	if len(params) != len(want)+1 {
		t.Fatalf("params keys = %v, want scalar keys %v plus guidances", keysOf(params), keysOf(want))
	}
	for key, wantValue := range want {
		if params[key] != wantValue {
			t.Fatalf("params[%s] = %v, want %v", key, params[key], wantValue)
		}
	}
	guidances := params["guidances"].(map[string]interface{})
	startFrames := guidances["start_frame"].([]interface{})
	if len(startFrames) != 1 {
		t.Fatalf("start_frame length = %d, want 1", len(startFrames))
	}
	image := startFrames[0].(map[string]interface{})["image"].(map[string]interface{})
	if image["id"] != "53f075af-2c0a-43b0-a90a-9e24c6050cb4" {
		t.Fatalf("start_frame image id = %v", image["id"])
	}
	if image["type"] != "UPLOADED" {
		t.Fatalf("start_frame image type = %v, want UPLOADED", image["type"])
	}
}

func TestGenerateBuildsKlingO3TextToVideoPayload(t *testing.T) {
	var requestBody string
	client := NewClient("")
	client.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			requestBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"data":{"generate":{"apiCreditCost":10,"generationId":"gen-kling-o3"}}}`)),
			}, nil
		}),
	}
	session := &TokenSession{
		JWT:       "jwt",
		JWTExpiry: time.Now().Add(time.Hour),
	}

	result, err := client.Generate(session, &GenerateRequest{
		Model:  "ko3",
		Public: true,
		Params: GenerateParams{
			Prompt:         "龟兔赛跑",
			Duration:       3,
			Quantity:       1,
			Width:          1080,
			Height:         1920,
			MotionHasAudio: true,
		},
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if result.GenerationID != "gen-kling-o3" || result.APICreditCost != 10 {
		t.Fatalf("unexpected result: %+v", result)
	}

	payload := mustJSONMap(t, requestBody)
	request := payload["variables"].(map[string]interface{})["request"].(map[string]interface{})
	if request["model"] != "kling-video-o-3" {
		t.Fatalf("model = %v, want kling-video-o-3", request["model"])
	}
	if request["public"] != true {
		t.Fatalf("public = %v, want true", request["public"])
	}
	params := request["parameters"].(map[string]interface{})
	want := map[string]interface{}{
		"height":           float64(1920),
		"width":            float64(1080),
		"duration":         float64(3),
		"mode":             "RESOLUTION_1080",
		"motion_has_audio": true,
		"quantity":         float64(1),
		"prompt":           "龟兔赛跑",
	}
	if len(params) != len(want) {
		t.Fatalf("params keys = %v, want only %v", keysOf(params), keysOf(want))
	}
	for key, wantValue := range want {
		if params[key] != wantValue {
			t.Fatalf("params[%s] = %v, want %v", key, params[key], wantValue)
		}
	}
}

func TestGenerateBuildsKlingO3TextToVideoPayloadWithSquareSizeAndLongDuration(t *testing.T) {
	var requestBody string
	client := NewClient("")
	client.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			requestBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"data":{"generate":{"apiCreditCost":10,"generationId":"gen-kling-o3-square"}}}`)),
			}, nil
		}),
	}
	session := &TokenSession{
		JWT:       "jwt",
		JWTExpiry: time.Now().Add(time.Hour),
	}

	_, err := client.Generate(session, &GenerateRequest{
		Model:  "kling-video-o-3",
		Public: true,
		Params: GenerateParams{
			Prompt:         "square video",
			Duration:       15,
			Quantity:       1,
			Width:          1440,
			Height:         1440,
			MotionHasAudio: true,
		},
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	payload := mustJSONMap(t, requestBody)
	request := payload["variables"].(map[string]interface{})["request"].(map[string]interface{})
	params := request["parameters"].(map[string]interface{})
	if params["width"] != float64(1440) || params["height"] != float64(1440) {
		t.Fatalf("size = %vx%v, want 1440x1440", params["width"], params["height"])
	}
	if params["duration"] != float64(15) {
		t.Fatalf("duration = %v, want 15", params["duration"])
	}
}

func TestGenerateBuildsKlingO3MultiImageToVideoPayload(t *testing.T) {
	var requestBody string
	client := NewClient("")
	client.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			requestBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"data":{"generate":{"apiCreditCost":10,"generationId":"gen-kling-o3-image"}}}`)),
			}, nil
		}),
	}
	session := &TokenSession{
		JWT:       "jwt",
		JWTExpiry: time.Now().Add(time.Hour),
	}

	_, err := client.Generate(session, &GenerateRequest{
		Model:  "kling-video-o-3",
		Public: true,
		Params: GenerateParams{
			Prompt:         "猫咪跳舞",
			Duration:       3,
			Quantity:       1,
			Width:          1080,
			Height:         1920,
			MotionHasAudio: true,
			ImageRefs: []ImageRef{{
				ID:       "f02f2740-708a-4333-9253-f2bf788fe201",
				Type:     "UPLOADED",
				Strength: "MID",
			}, {
				ID:       "b3941f10-34ab-4535-8725-ff44a3f2ca21",
				Type:     "UPLOADED",
				Strength: "MID",
			}, {
				ID:       "09eff9d4-284a-4454-aa42-2a5c64906af6",
				Type:     "UPLOADED",
				Strength: "MID",
			}, {
				ID:       "b9b7f87c-3312-44c6-a92d-a81745ec0635",
				Type:     "UPLOADED",
				Strength: "MID",
			}},
		},
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	payload := mustJSONMap(t, requestBody)
	request := payload["variables"].(map[string]interface{})["request"].(map[string]interface{})
	params := request["parameters"].(map[string]interface{})
	guidances := params["guidances"].(map[string]interface{})
	imageRefs := guidances["image_reference"].([]interface{})
	if len(imageRefs) != 4 {
		t.Fatalf("image_reference length = %d, want 4", len(imageRefs))
	}
	wantIDs := []string{
		"f02f2740-708a-4333-9253-f2bf788fe201",
		"b3941f10-34ab-4535-8725-ff44a3f2ca21",
		"09eff9d4-284a-4454-aa42-2a5c64906af6",
		"b9b7f87c-3312-44c6-a92d-a81745ec0635",
	}
	for idx, rawRef := range imageRefs {
		ref := rawRef.(map[string]interface{})
		image := ref["image"].(map[string]interface{})
		if image["id"] != wantIDs[idx] {
			t.Fatalf("image_reference[%d] id = %v, want %s", idx, image["id"], wantIDs[idx])
		}
		if image["type"] != "UPLOADED" {
			t.Fatalf("image_reference[%d] type = %v, want UPLOADED", idx, image["type"])
		}
		if ref["strength"] != "MID" {
			t.Fatalf("image_reference[%d] strength = %v, want MID", idx, ref["strength"])
		}
	}
}

func TestGenerateBuildsKlingO3ImageToVideoPayload(t *testing.T) {
	var requestBody string
	client := NewClient("")
	client.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			requestBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"data":{"generate":{"apiCreditCost":10,"generationId":"gen-kling-o3-image"}}}`)),
			}, nil
		}),
	}
	session := &TokenSession{
		JWT:       "jwt",
		JWTExpiry: time.Now().Add(time.Hour),
	}

	_, err := client.Generate(session, &GenerateRequest{
		Model:  "kling-video-o-3",
		Public: true,
		Params: GenerateParams{
			Prompt:         "猫咪跳舞",
			Duration:       3,
			Quantity:       1,
			Width:          1080,
			Height:         1920,
			MotionHasAudio: true,
			ImageRefs: []ImageRef{{
				ID:       "f02f2740-708a-4333-9253-f2bf788fe201",
				Type:     "UPLOADED",
				Strength: "MID",
			}},
		},
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	payload := mustJSONMap(t, requestBody)
	request := payload["variables"].(map[string]interface{})["request"].(map[string]interface{})
	params := request["parameters"].(map[string]interface{})
	guidances := params["guidances"].(map[string]interface{})
	imageRefs := guidances["image_reference"].([]interface{})
	if len(imageRefs) != 1 {
		t.Fatalf("image_reference length = %d, want 1", len(imageRefs))
	}
	ref := imageRefs[0].(map[string]interface{})
	image := ref["image"].(map[string]interface{})
	if image["id"] != "f02f2740-708a-4333-9253-f2bf788fe201" {
		t.Fatalf("image id = %v", image["id"])
	}
	if image["type"] != "UPLOADED" {
		t.Fatalf("image type = %v, want UPLOADED", image["type"])
	}
	if ref["strength"] != "MID" {
		t.Fatalf("strength = %v, want MID", ref["strength"])
	}
}

func TestGenerateBuildsKlingO3StartEndFramePayload(t *testing.T) {
	var requestBody string
	client := NewClient("")
	client.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			requestBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"data":{"generate":{"apiCreditCost":10,"generationId":"gen-kling-o3-frame"}}}`)),
			}, nil
		}),
	}
	session := &TokenSession{
		JWT:       "jwt",
		JWTExpiry: time.Now().Add(time.Hour),
	}

	_, err := client.Generate(session, &GenerateRequest{
		Model:  "kling-video-o-3",
		Public: true,
		Params: GenerateParams{
			Prompt:         "从图一过渡到图二",
			Duration:       3,
			Quantity:       1,
			Width:          1080,
			Height:         1920,
			MotionHasAudio: true,
			StartFrame: []FrameRef{{
				ID:   "f02f2740-708a-4333-9253-f2bf788fe201",
				Type: "UPLOADED",
			}},
			EndFrame: []FrameRef{{
				ID:   "09eff9d4-284a-4454-aa42-2a5c64906af6",
				Type: "UPLOADED",
			}},
		},
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	payload := mustJSONMap(t, requestBody)
	request := payload["variables"].(map[string]interface{})["request"].(map[string]interface{})
	params := request["parameters"].(map[string]interface{})
	guidances := params["guidances"].(map[string]interface{})

	startFrames := guidances["start_frame"].([]interface{})
	if len(startFrames) != 1 {
		t.Fatalf("start_frame length = %d, want 1", len(startFrames))
	}
	startImage := startFrames[0].(map[string]interface{})["image"].(map[string]interface{})
	if startImage["id"] != "f02f2740-708a-4333-9253-f2bf788fe201" {
		t.Fatalf("start_frame image id = %v", startImage["id"])
	}
	if startImage["type"] != "UPLOADED" {
		t.Fatalf("start_frame image type = %v, want UPLOADED", startImage["type"])
	}

	endFrames := guidances["end_frame"].([]interface{})
	if len(endFrames) != 1 {
		t.Fatalf("end_frame length = %d, want 1", len(endFrames))
	}
	endImage := endFrames[0].(map[string]interface{})["image"].(map[string]interface{})
	if endImage["id"] != "09eff9d4-284a-4454-aa42-2a5c64906af6" {
		t.Fatalf("end_frame image id = %v", endImage["id"])
	}
	if endImage["type"] != "UPLOADED" {
		t.Fatalf("end_frame image type = %v, want UPLOADED", endImage["type"])
	}
}

func TestGenerateBuildsKlingO3VideoReferencePayload(t *testing.T) {
	var requestBody string
	client := NewClient("")
	client.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			requestBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"data":{"generate":{"apiCreditCost":10,"generationId":"gen-kling-o3-video"}}}`)),
			}, nil
		}),
	}
	session := &TokenSession{
		JWT:       "jwt",
		JWTExpiry: time.Now().Add(time.Hour),
	}

	_, err := client.Generate(session, &GenerateRequest{
		Model:  "kling-video-o-3",
		Public: true,
		Params: GenerateParams{
			Prompt:         "把视频中的香水替换成牙膏",
			Mode:           "RESOLUTION_1080",
			Duration:       5,
			Quantity:       1,
			MotionHasAudio: true,
			VideoRefs: []VideoRef{{
				ID:       "fbeda0e3-a8b3-45d6-a22e-4e53da4148f9",
				Type:     "UPLOADED",
				Duration: 7.918005,
			}},
		},
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	payload := mustJSONMap(t, requestBody)
	request := payload["variables"].(map[string]interface{})["request"].(map[string]interface{})
	params := request["parameters"].(map[string]interface{})
	want := map[string]interface{}{
		"height":           float64(0),
		"width":            float64(0),
		"duration":         float64(5),
		"mode":             "RESOLUTION_1080",
		"motion_has_audio": true,
		"quantity":         float64(1),
		"prompt":           "把视频中的香水替换成牙膏",
	}
	for key, wantValue := range want {
		if params[key] != wantValue {
			t.Fatalf("params[%s] = %v, want %v", key, params[key], wantValue)
		}
	}
	guidances := params["guidances"].(map[string]interface{})
	videoRefs := guidances["video_reference_base"].([]interface{})
	if len(videoRefs) != 1 {
		t.Fatalf("video_reference_base length = %d, want 1", len(videoRefs))
	}
	video := videoRefs[0].(map[string]interface{})["video"].(map[string]interface{})
	if video["id"] != "fbeda0e3-a8b3-45d6-a22e-4e53da4148f9" {
		t.Fatalf("video id = %v", video["id"])
	}
	if video["type"] != "UPLOADED" {
		t.Fatalf("video type = %v, want UPLOADED", video["type"])
	}
	if video["duration"] != 7.918005 {
		t.Fatalf("video duration = %v, want 7.918005", video["duration"])
	}
}

func TestGenerateBuildsKlingO3VideoReferencePayloadWithDefaults(t *testing.T) {
	var requestBody string
	client := NewClient("")
	client.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			requestBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"data":{"generate":{"apiCreditCost":10,"generationId":"gen-kling-o3-video-defaults"}}}`)),
			}, nil
		}),
	}
	session := &TokenSession{
		JWT:       "jwt",
		JWTExpiry: time.Now().Add(time.Hour),
	}

	_, err := client.Generate(session, &GenerateRequest{
		Model:  "kling-video-o-3",
		Public: true,
		Params: GenerateParams{
			Prompt:         "参考视频生成视频",
			Quantity:       1,
			MotionHasAudio: true,
			VideoRefs: []VideoRef{{
				ID:       "fbeda0e3-a8b3-45d6-a22e-4e53da4148f9",
				Type:     "UPLOADED",
				Duration: 7.918005,
			}},
		},
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	payload := mustJSONMap(t, requestBody)
	request := payload["variables"].(map[string]interface{})["request"].(map[string]interface{})
	params := request["parameters"].(map[string]interface{})
	want := map[string]interface{}{
		"height":           float64(0),
		"width":            float64(0),
		"duration":         float64(5),
		"mode":             "RESOLUTION_1080",
		"motion_has_audio": true,
		"quantity":         float64(1),
		"prompt":           "参考视频生成视频",
	}
	for key, wantValue := range want {
		if params[key] != wantValue {
			t.Fatalf("params[%s] = %v, want %v", key, params[key], wantValue)
		}
	}
	guidances := params["guidances"].(map[string]interface{})
	videoRefs := guidances["video_reference_base"].([]interface{})
	if len(videoRefs) != 1 {
		t.Fatalf("video_reference_base length = %d, want 1", len(videoRefs))
	}
	video := videoRefs[0].(map[string]interface{})["video"].(map[string]interface{})
	if video["id"] != "fbeda0e3-a8b3-45d6-a22e-4e53da4148f9" {
		t.Fatalf("video id = %v", video["id"])
	}
	if video["duration"] != 7.918005 {
		t.Fatalf("video duration = %v, want 7.918005", video["duration"])
	}
}

func TestGenerateBuildsKlingO3ImageAndVideoReferencePayload(t *testing.T) {
	var requestBody string
	client := NewClient("")
	client.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			requestBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"data":{"generate":{"apiCreditCost":10,"generationId":"gen-kling-o3-image-video"}}}`)),
			}, nil
		}),
	}
	session := &TokenSession{
		JWT:       "jwt",
		JWTExpiry: time.Now().Add(time.Hour),
	}

	_, err := client.Generate(session, &GenerateRequest{
		Model:  "kling-video-o-3",
		Public: true,
		Params: GenerateParams{
			Prompt:         "把视频中的香水替换图片的小熊",
			Mode:           "RESOLUTION_1080",
			Duration:       5,
			Quantity:       1,
			MotionHasAudio: true,
			ImageRefs: []ImageRef{{
				ID:       "b9b7f87c-3312-44c6-a92d-a81745ec0635",
				Type:     "UPLOADED",
				Strength: "MID",
			}},
			VideoRefs: []VideoRef{{
				ID:       "f232eea2-b9e8-4a17-8270-fa5a36dbe8dc",
				Type:     "UPLOADED",
				Duration: 4.017007,
			}},
		},
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	payload := mustJSONMap(t, requestBody)
	request := payload["variables"].(map[string]interface{})["request"].(map[string]interface{})
	params := request["parameters"].(map[string]interface{})
	want := map[string]interface{}{
		"height":           float64(0),
		"width":            float64(0),
		"duration":         float64(5),
		"mode":             "RESOLUTION_1080",
		"motion_has_audio": true,
		"quantity":         float64(1),
		"prompt":           "把视频中的香水替换图片的小熊",
	}
	for key, wantValue := range want {
		if params[key] != wantValue {
			t.Fatalf("params[%s] = %v, want %v", key, params[key], wantValue)
		}
	}
	guidances := params["guidances"].(map[string]interface{})
	imageRefs := guidances["image_reference"].([]interface{})
	if len(imageRefs) != 1 {
		t.Fatalf("image_reference length = %d, want 1", len(imageRefs))
	}
	imageRef := imageRefs[0].(map[string]interface{})
	image := imageRef["image"].(map[string]interface{})
	if image["id"] != "b9b7f87c-3312-44c6-a92d-a81745ec0635" {
		t.Fatalf("image id = %v", image["id"])
	}
	if image["type"] != "UPLOADED" {
		t.Fatalf("image type = %v, want UPLOADED", image["type"])
	}
	if imageRef["strength"] != "MID" {
		t.Fatalf("image strength = %v, want MID", imageRef["strength"])
	}

	videoRefs := guidances["video_reference_base"].([]interface{})
	if len(videoRefs) != 1 {
		t.Fatalf("video_reference_base length = %d, want 1", len(videoRefs))
	}
	video := videoRefs[0].(map[string]interface{})["video"].(map[string]interface{})
	if video["id"] != "f232eea2-b9e8-4a17-8270-fa5a36dbe8dc" {
		t.Fatalf("video id = %v", video["id"])
	}
	if video["type"] != "UPLOADED" {
		t.Fatalf("video type = %v, want UPLOADED", video["type"])
	}
	if video["duration"] != 4.017007 {
		t.Fatalf("video duration = %v, want 4.017007", video["duration"])
	}
}

func TestGenerateBuildsKlingO3MultiImageAndVideoReferencePayload(t *testing.T) {
	var requestBody string
	client := NewClient("")
	client.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			requestBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"data":{"generate":{"apiCreditCost":10,"generationId":"gen-kling-o3-multi-image-video"}}}`)),
			}, nil
		}),
	}
	session := &TokenSession{
		JWT:       "jwt",
		JWTExpiry: time.Now().Add(time.Hour),
	}

	_, err := client.Generate(session, &GenerateRequest{
		Model:  "kling-video-o-3",
		Public: true,
		Params: GenerateParams{
			Prompt:         "用多张图片替换视频主体",
			Duration:       5,
			Quantity:       1,
			MotionHasAudio: true,
			ImageRefs: []ImageRef{{
				ID:       "b9b7f87c-3312-44c6-a92d-a81745ec0635",
				Type:     "UPLOADED",
				Strength: "MID",
			}, {
				ID:       "09eff9d4-284a-4454-aa42-2a5c64906af6",
				Type:     "UPLOADED",
				Strength: "MID",
			}, {
				ID:       "f02f2740-708a-4333-9253-f2bf788fe201",
				Type:     "UPLOADED",
				Strength: "MID",
			}},
			VideoRefs: []VideoRef{{
				ID:       "f232eea2-b9e8-4a17-8270-fa5a36dbe8dc",
				Type:     "UPLOADED",
				Duration: 4.017007,
			}},
		},
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	payload := mustJSONMap(t, requestBody)
	request := payload["variables"].(map[string]interface{})["request"].(map[string]interface{})
	params := request["parameters"].(map[string]interface{})
	want := map[string]interface{}{
		"height":           float64(0),
		"width":            float64(0),
		"duration":         float64(5),
		"mode":             "RESOLUTION_1080",
		"motion_has_audio": true,
		"quantity":         float64(1),
		"prompt":           "用多张图片替换视频主体",
	}
	for key, wantValue := range want {
		if params[key] != wantValue {
			t.Fatalf("params[%s] = %v, want %v", key, params[key], wantValue)
		}
	}
	guidances := params["guidances"].(map[string]interface{})
	imageRefs := guidances["image_reference"].([]interface{})
	if len(imageRefs) != 3 {
		t.Fatalf("image_reference length = %d, want 3", len(imageRefs))
	}
	wantImageIDs := []string{
		"b9b7f87c-3312-44c6-a92d-a81745ec0635",
		"09eff9d4-284a-4454-aa42-2a5c64906af6",
		"f02f2740-708a-4333-9253-f2bf788fe201",
	}
	for idx, rawRef := range imageRefs {
		ref := rawRef.(map[string]interface{})
		image := ref["image"].(map[string]interface{})
		if image["id"] != wantImageIDs[idx] {
			t.Fatalf("image_reference[%d] id = %v, want %s", idx, image["id"], wantImageIDs[idx])
		}
		if image["type"] != "UPLOADED" {
			t.Fatalf("image_reference[%d] type = %v, want UPLOADED", idx, image["type"])
		}
		if ref["strength"] != "MID" {
			t.Fatalf("image_reference[%d] strength = %v, want MID", idx, ref["strength"])
		}
	}
	videoRefs := guidances["video_reference_base"].([]interface{})
	if len(videoRefs) != 1 {
		t.Fatalf("video_reference_base length = %d, want 1", len(videoRefs))
	}
	video := videoRefs[0].(map[string]interface{})["video"].(map[string]interface{})
	if video["id"] != "f232eea2-b9e8-4a17-8270-fa5a36dbe8dc" {
		t.Fatalf("video id = %v", video["id"])
	}
	if video["type"] != "UPLOADED" {
		t.Fatalf("video type = %v, want UPLOADED", video["type"])
	}
	if video["duration"] != 4.017007 {
		t.Fatalf("video duration = %v, want 4.017007", video["duration"])
	}
}

func TestGenerateBuildsKlingO3ImageAndVideoReferencePayloadWithCustomSizeAndDuration(t *testing.T) {
	var requestBody string
	client := NewClient("")
	client.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			requestBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"data":{"generate":{"apiCreditCost":10,"generationId":"gen-kling-o3-image-video-custom"}}}`)),
			}, nil
		}),
	}
	session := &TokenSession{
		JWT:       "jwt",
		JWTExpiry: time.Now().Add(time.Hour),
	}

	_, err := client.Generate(session, &GenerateRequest{
		Model:  "kling-video-o-3",
		Public: true,
		Params: GenerateParams{
			Prompt:         "把视频中的香水替换图片的小熊",
			Duration:       3,
			Quantity:       1,
			Width:          1080,
			Height:         1920,
			MotionHasAudio: true,
			ImageRefs: []ImageRef{{
				ID:       "b9b7f87c-3312-44c6-a92d-a81745ec0635",
				Type:     "UPLOADED",
				Strength: "MID",
			}},
			VideoRefs: []VideoRef{{
				ID:       "f232eea2-b9e8-4a17-8270-fa5a36dbe8dc",
				Type:     "UPLOADED",
				Duration: 4.017007,
			}},
		},
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	payload := mustJSONMap(t, requestBody)
	request := payload["variables"].(map[string]interface{})["request"].(map[string]interface{})
	params := request["parameters"].(map[string]interface{})
	want := map[string]interface{}{
		"height":           float64(1920),
		"width":            float64(1080),
		"duration":         float64(3),
		"mode":             "RESOLUTION_1080",
		"motion_has_audio": true,
		"quantity":         float64(1),
		"prompt":           "把视频中的香水替换图片的小熊",
	}
	for key, wantValue := range want {
		if params[key] != wantValue {
			t.Fatalf("params[%s] = %v, want %v", key, params[key], wantValue)
		}
	}
	guidances := params["guidances"].(map[string]interface{})
	if _, ok := guidances["image_reference"]; !ok {
		t.Fatal("guidances missing image_reference")
	}
	if _, ok := guidances["video_reference_base"]; !ok {
		t.Fatal("guidances missing video_reference_base")
	}
}

func TestGenerateRejectsUnsupportedSora2Options(t *testing.T) {
	client := NewClient("")
	session := &TokenSession{
		JWT:       "jwt",
		JWTExpiry: time.Now().Add(time.Hour),
	}

	cases := []struct {
		name   string
		params GenerateParams
		want   string
	}{
		{
			name: "duration",
			params: GenerateParams{
				Prompt:   "test",
				Duration: 10,
				Width:    720,
				Height:   1280,
			},
			want: "duration must be 4, 8, or 12",
		},
		{
			name: "size",
			params: GenerateParams{
				Prompt:   "test",
				Duration: 8,
				Width:    960,
				Height:   960,
			},
			want: "size must be 720x1280 or 1280x720",
		},
		{
			name: "start frames",
			params: GenerateParams{
				Prompt:     "test",
				Duration:   8,
				Width:      720,
				Height:     1280,
				StartFrame: []FrameRef{{ID: "one"}, {ID: "two"}},
			},
			want: "at most one uploaded image",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.Generate(session, &GenerateRequest{
				Model:  "sora2",
				Public: true,
				Params: tc.params,
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestGenerateRejectsUnsupportedKlingO3Options(t *testing.T) {
	client := NewClient("")
	session := &TokenSession{
		JWT:       "jwt",
		JWTExpiry: time.Now().Add(time.Hour),
	}

	cases := []struct {
		name   string
		params GenerateParams
		want   string
	}{
		{
			name: "duration",
			params: GenerateParams{
				Prompt:   "test",
				Duration: 2,
				Width:    1080,
				Height:   1920,
			},
			want: "duration must be between 3 and 15 seconds",
		},
		{
			name: "size",
			params: GenerateParams{
				Prompt:   "test",
				Duration: 3,
				Width:    1280,
				Height:   720,
			},
			want: "size must be 1440x1440, 1080x1920, or 1920x1080",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.Generate(session, &GenerateRequest{
				Model:  "kling-video-o-3",
				Public: true,
				Params: tc.params,
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestGetGenerationFailureReasonExtractsModerationDetails(t *testing.T) {
	var requestBody string
	client := NewClient("")
	client.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			requestBody = string(body)
			payload := mustJSONMap(t, requestBody)
			switch payload["operationName"] {
			case "GetGenerationPromptModerations":
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"data":{"generations":[{"id":"gen-failed","status":"FAILED","prompt_moderations":[{"moderationClassification":["NSFW","EXTREME_VIOLENCE"]}]}]}}`)),
				}, nil
			case "GetGenerationFailureNotes":
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"data":{"generations":[{"id":"gen-failed","status":"FAILED","notes":[{"noteType":"PROVIDER_FAILURE","failureReason":{"errorCode":"PROVIDER_MODERATION_ERROR"}}]}]}}`)),
				}, nil
			default:
				t.Fatalf("unexpected operationName: %v", payload["operationName"])
				return nil, nil
			}
		}),
	}
	session := &TokenSession{
		JWT:       "jwt",
		JWTExpiry: time.Now().Add(time.Hour),
	}

	reason, err := client.GetGenerationFailureReason(session, "gen-failed")
	if err != nil {
		t.Fatalf("GetGenerationFailureReason returned error: %v", err)
	}
	if reason != "PROVIDER_MODERATION_ERROR: NSFW, EXTREME_VIOLENCE" {
		t.Fatalf("reason = %q", reason)
	}

	payload := mustJSONMap(t, requestBody)
	if payload["operationName"] != "GetGenerationFailureNotes" {
		t.Fatalf("last operationName = %v, want GetGenerationFailureNotes", payload["operationName"])
	}
}

func TestGetGenerationFailureReasonIgnoresDifferentGenerationID(t *testing.T) {
	client := NewClient("")
	client.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			payload := mustJSONMap(t, string(body))
			switch payload["operationName"] {
			case "GetGenerationPromptModerations":
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"data":{"generations":[{"id":"other-gen","status":"FAILED","prompt_moderations":[{"moderationClassification":["NSFW"]}]}]}}`)),
				}, nil
			case "GetGenerationFailureNotes":
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"data":{"generations":[{"id":"other-gen","status":"FAILED","notes":[{"noteType":"PROVIDER_FAILURE","failureReason":{"errorCode":"PROVIDER_MODERATION_ERROR"}}]}]}}`)),
				}, nil
			case "IntrospectGenerationType":
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"data":{"__type":{"fields":[]}}}`)),
				}, nil
			case "GetGenerationFailureReason":
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"data":{"generations":[]}}`)),
				}, nil
			default:
				t.Fatalf("unexpected operationName: %v", payload["operationName"])
				return nil, nil
			}
		}),
	}
	session := &TokenSession{
		JWT:       "jwt",
		JWTExpiry: time.Now().Add(time.Hour),
	}

	reason, err := client.GetGenerationFailureReason(session, "gen-failed")
	if err != nil {
		t.Fatalf("GetGenerationFailureReason returned error: %v", err)
	}
	if reason != "" {
		t.Fatalf("reason = %q, want empty", reason)
	}
}

func mustJSONMap(t *testing.T, raw string) map[string]interface{} {
	t.Helper()
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("unmarshal request JSON: %v\n%s", err, raw)
	}
	return out
}

func keysOf(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

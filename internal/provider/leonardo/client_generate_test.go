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
		Model:  "sora-2",
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
		Model:  "sora-2",
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
				Model:  "sora-2",
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
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{
					"data": {
						"generations": [{
							"id": "gen-failed",
							"status": "FAILED",
							"prompt_moderations": [{
								"moderationClassification": ["NSFW", "EXTREME_VIOLENCE"]
							}],
							"notes": [{
								"noteType": "PROVIDER_FAILURE",
								"failureReason": {"errorCode": "PROVIDER_MODERATION_ERROR"}
							}]
						}]
					}
				}`)),
			}, nil
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
	if payload["operationName"] != "GetGenerationModerationFailureReason" {
		t.Fatalf("operationName = %v, want GetGenerationModerationFailureReason", payload["operationName"])
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
			case "GetGenerationModerationFailureReason":
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"data":{"generations":[{"id":"other-gen","status":"FAILED","prompt_moderations":[{"moderationClassification":["NSFW"]}],"notes":[{"noteType":"PROVIDER_FAILURE","failureReason":{"errorCode":"PROVIDER_MODERATION_ERROR"}}]}]}}`)),
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

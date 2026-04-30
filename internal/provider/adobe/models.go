package adobe

import (
	"fmt"
	"math"
	"strings"
	"time"

	"leo-go/internal/provider"
)

// Model catalog — ported from Python core/models/catalog.py

var ImageModelCatalog = map[string]map[string]interface{}{
	"firefly-image-4": {
		"description":            "Adobe Firefly Image 4 — highest quality",
		"upstream_model_id":      "gemini-flash",
		"upstream_model_version": "nano-banana-2",
		"payload_style":          "banana",
		"generation_metadata":    map[string]interface{}{"module": "text2image"},
	},
	"firefly-image-3": {
		"description":            "Adobe Firefly Image 3",
		"upstream_model_id":      "firefly",
		"upstream_model_version": "nano-strawberry",
		"payload_style":          "strawberry",
		"generation_metadata":    map[string]interface{}{"module": "text2image"},
	},
	"dall-e-3": {
		"description":            "DALL-E 3 compatible (via Firefly Image 4)",
		"upstream_model_id":      "gemini-flash",
		"upstream_model_version": "nano-banana-2",
		"payload_style":          "banana",
		"generation_metadata":    map[string]interface{}{"module": "text2image"},
	},
}

var VideoModelCatalog = map[string]map[string]interface{}{
	"sora2": {
		"description":    "Sora 2 video generation",
		"engine":         "sora2",
		"upstream_model": "openai:firefly:colligo:sora2",
		"duration_options":    []int{5, 10, 15, 20},
		"aspect_ratio_options": []string{"16:9", "9:16"},
		"allow_request_overrides": true,
		"duration":        10,
		"aspect_ratio":    "16:9",
	},
	"kling-v3": {
		"description":    "Kling v3 video generation",
		"engine":         "kling",
		"upstream_model_id": "kling",
		"duration_options":    []int{5, 10},
		"aspect_ratio_options": []string{"16:9", "9:16"},
		"allow_request_overrides": true,
		"duration":        5,
		"aspect_ratio":    "16:9",
	},
	"veo31": {
		"description": "Google Veo 3.1 video generation",
		"engine":      "veo31-standard",
		"duration_options":    []int{8},
		"aspect_ratio_options": []string{"16:9", "9:16"},
		"allow_request_overrides": true,
		"duration":        8,
		"aspect_ratio":    "16:9",
	},
}

// SupportedRatios is the set of valid aspect ratios for image generation.
var SupportedRatios = map[string]bool{
	"1:1": true, "4:3": true, "3:4": true, "16:9": true, "9:16": true,
	"3:2": true, "2:3": true, "21:9": true, "9:21": true,
}

// SupportedModels returns image model info list.
func (c *Client) SupportedModels() []provider.ModelInfo {
	var models []provider.ModelInfo
	for id, conf := range ImageModelCatalog {
		desc, _ := conf["description"].(string)
		models = append(models, provider.ModelInfo{
			ID: id, Description: desc, OwnedBy: "adobe2api",
		})
	}
	return models
}

// SupportedVideoModels returns video model info list.
func (c *Client) SupportedVideoModels() []provider.ModelInfo {
	var models []provider.ModelInfo
	for id, conf := range VideoModelCatalog {
		desc, _ := conf["description"].(string)
		params := make(map[string]interface{})
		if opts, ok := conf["duration_options"]; ok {
			params["duration"] = opts
		}
		if opts, ok := conf["aspect_ratio_options"]; ok {
			params["aspect_ratio"] = opts
		}
		m := provider.ModelInfo{ID: id, Description: desc, OwnedBy: "adobe2api"}
		if len(params) > 0 {
			m.Parameters = params
		}
		models = append(models, m)
	}
	return models
}

// ResolveModel looks up model config by ID, falling back to default.
func ResolveModel(modelID string) map[string]interface{} {
	id := strings.TrimSpace(modelID)
	if id == "" {
		id = "firefly-image-4"
	}
	if conf, ok := ImageModelCatalog[id]; ok {
		return conf
	}
	return ImageModelCatalog["firefly-image-4"]
}

// ResolveRatioAndResolution resolves aspect ratio and output resolution from request data.
func ResolveRatioAndResolution(data map[string]interface{}, modelID string) (ratio, resolution, resolvedModel string) {
	resolvedModel = strings.TrimSpace(fmt.Sprintf("%v", modelID))
	if resolvedModel == "" || resolvedModel == "<nil>" {
		resolvedModel = "firefly-image-4"
	}

	ratioRaw := ""
	for _, key := range []string{"aspect_ratio", "aspectRatio", "size"} {
		if v, ok := data[key].(string); ok && v != "" {
			ratioRaw = strings.TrimSpace(v)
			break
		}
	}
	if ratioRaw == "" {
		ratioRaw = "1:1"
	}
	// Convert WxH to ratio
	if strings.Contains(ratioRaw, "x") || strings.Contains(ratioRaw, "X") || strings.Contains(ratioRaw, "×") {
		ratioRaw = sizeToRatio(ratioRaw)
	}
	ratio = ratioRaw

	resRaw := ""
	for _, key := range []string{"output_resolution", "outputResolution", "quality"} {
		if v, ok := data[key].(string); ok && v != "" {
			resRaw = strings.TrimSpace(strings.ToUpper(v))
			break
		}
	}
	if resRaw == "" {
		resRaw = "2K"
	}
	switch resRaw {
	case "1K", "2K", "4K":
		resolution = resRaw
	case "LOW", "SD":
		resolution = "1K"
	case "HIGH", "HD":
		resolution = "2K"
	case "ULTRA", "UHD":
		resolution = "4K"
	default:
		resolution = "2K"
	}
	return ratio, resolution, resolvedModel
}

func sizeToRatio(size string) string {
	size = strings.ReplaceAll(size, "×", "x")
	size = strings.ReplaceAll(size, "X", "x")
	parts := strings.SplitN(size, "x", 2)
	if len(parts) != 2 {
		return "1:1"
	}
	w := parseInt(parts[0], 1)
	h := parseInt(parts[1], 1)
	if w <= 0 || h <= 0 {
		return "1:1"
	}
	return simplifyRatio(w, h)
}

func simplifyRatio(w, h int) string {
	g := gcd(w, h)
	rw, rh := w/g, h/g
	known := map[string]bool{"1:1": true, "4:3": true, "3:4": true, "16:9": true, "9:16": true, "3:2": true, "2:3": true}
	key := fmt.Sprintf("%d:%d", rw, rh)
	if known[key] {
		return key
	}
	ratio := float64(w) / float64(h)
	best := "1:1"
	bestDiff := math.MaxFloat64
	for k := range known {
		p := strings.SplitN(k, ":", 2)
		kw := parseInt(p[0], 1)
		kh := parseInt(p[1], 1)
		diff := math.Abs(ratio - float64(kw)/float64(kh))
		if diff < bestDiff {
			bestDiff = diff
			best = k
		}
	}
	return best
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func parseInt(s string, def int) int {
	s = strings.TrimSpace(s)
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	if n == 0 {
		return def
	}
	return n
}

// --- Payload builders ---

// BuildImagePayloadCandidates builds candidate payloads for image generation.
func BuildImagePayloadCandidates(req provider.ImageRequest) []map[string]interface{} {
	seedVal := int(time.Now().UnixNano() % 999999)
	upstreamModelID := req.ModelID
	if upstreamModelID == "" {
		upstreamModelID = "gemini-flash"
	}
	upstreamModelVersion := req.ModelVersion
	if upstreamModelVersion == "" {
		upstreamModelVersion = "nano-banana-2"
	}

	size := imageSize(req.AspectRatio, req.OutputResolution)

	base := map[string]interface{}{
		"n":            1,
		"seeds":        []int{seedVal},
		"modelId":      upstreamModelID,
		"modelVersion": upstreamModelVersion,
		"prompt":       req.Prompt,
		"size":         size,
		"output":       map[string]interface{}{"storeInputs": true},
	}
	if req.GenMetadata != nil {
		base["generationMetadata"] = req.GenMetadata
	} else {
		base["generationMetadata"] = map[string]interface{}{"module": "text2image"}
	}
	if req.GenSettings != nil {
		base["generationSettings"] = req.GenSettings
	}
	if req.ModelPayload != nil {
		base["modelSpecificPayload"] = req.ModelPayload
	}

	// Add source images
	if len(req.SourceImageIDs) > 0 {
		var refs []map[string]interface{}
		for i, id := range req.SourceImageIDs {
			refs = append(refs, map[string]interface{}{
				"id": id, "usage": "general", "promptReference": i + 1,
			})
		}
		base["referenceBlobs"] = refs
	}

	return []map[string]interface{}{base}
}

// BuildVideoPayload builds a video generation payload.
func BuildVideoPayload(req provider.VideoRequest) map[string]interface{} {
	seedVal := int(time.Now().UnixNano() % 999999)
	engine := req.ModelID
	if engine == "" {
		engine = "sora2"
	}

	size := videoSize(req.AspectRatio, req.Resolution)
	duration := req.Duration
	if duration <= 0 {
		duration = 10
	}

	payload := map[string]interface{}{
		"n":                  1,
		"seeds":              []int{seedVal},
		"prompt":             req.Prompt,
		"size":               size,
		"duration":           duration,
		"generateAudio":      true,
		"generationMetadata": map[string]interface{}{"module": "text2video"},
		"output":             map[string]interface{}{"storeInputs": true},
		"referenceBlobs":     []interface{}{},
	}

	switch {
	case strings.HasPrefix(engine, "kling"):
		payload["modelId"] = "kling"
		payload["modelVersion"] = "kling_o3_pro_t2v"
	case strings.HasPrefix(engine, "veo31"):
		payload["modelId"] = "veo"
		if strings.Contains(engine, "fast") {
			payload["modelVersion"] = "3.1-fast-generate"
		} else {
			payload["modelVersion"] = "3.1-generate"
		}
	default: // sora2
		payload["modelId"] = "sora"
		payload["modelVersion"] = "sora-2"
		payload["model"] = "openai:firefly:colligo:sora2"
		payload["fps"] = 24
	}

	return payload
}

func imageSize(aspectRatio, resolution string) map[string]interface{} {
	baseWidth := 2048
	switch strings.ToUpper(resolution) {
	case "1K":
		baseWidth = 1024
	case "4K":
		baseWidth = 4096
	}
	parts := strings.SplitN(aspectRatio, ":", 2)
	w, h := 1, 1
	if len(parts) == 2 {
		w = parseInt(parts[0], 1)
		h = parseInt(parts[1], 1)
	}
	ratio := float64(w) / float64(h)
	width := baseWidth
	height := int(float64(baseWidth) / ratio)
	// Round to nearest multiple of 8
	height = (height + 3) / 8 * 8
	width = (width + 3) / 8 * 8
	return map[string]interface{}{"width": width, "height": height}
}

func videoSize(aspectRatio, resolution string) map[string]interface{} {
	res := strings.ToLower(resolution)
	if res == "1080p" {
		if aspectRatio == "16:9" {
			return map[string]interface{}{"width": 1920, "height": 1080}
		}
		return map[string]interface{}{"width": 1080, "height": 1920}
	}
	if aspectRatio == "16:9" {
		return map[string]interface{}{"width": 1280, "height": 720}
	}
	return map[string]interface{}{"width": 720, "height": 1280}
}

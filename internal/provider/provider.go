package provider

import "context"

// ImageRequest represents a request for image generation.
type ImageRequest struct {
	Token            string
	Prompt           string
	AspectRatio      string
	OutputResolution string
	ModelID          string
	ModelVersion     string
	PayloadStyle     string
	GenMetadata      map[string]interface{}
	GenSettings      map[string]interface{}
	ModelPayload     map[string]interface{}
	SourceImageIDs   []string
	Timeout          int
	ReturnURL        bool
}

// VideoRequest represents a request for video generation.
type VideoRequest struct {
	Token            string
	Prompt           string
	AspectRatio      string
	Duration         int
	Resolution       string
	ReferenceMode    string
	ModelID          string
	ModelVersion     string
	PayloadStyle     string
	GenMetadata      map[string]interface{}
	GenSettings      map[string]interface{}
	ModelPayload     map[string]interface{}
	SourceImageIDs   []string
	Timeout          int
	ReturnURL        bool
}

// JobResult is returned after submitting a generation job.
type JobResult struct {
	ImageBytes []byte
	ImageURL   string
	VideoURL   string
	Meta       map[string]interface{}
	Progress   float64
	JobID      string
}

// PollResult is returned when checking job status.
type PollResult struct {
	Status   string
	Progress float64
	Meta     map[string]interface{}
}

// ModelInfo describes a supported model.
type ModelInfo struct {
	ID          string                 `json:"id"`
	Description string                `json:"description"`
	OwnedBy    string                 `json:"owned_by"`
	Parameters map[string]interface{} `json:"parameters,omitempty"`
	Hidden     bool                   `json:"hidden,omitempty"`
}

// Credits represents account quota information.
type Credits struct {
	Used      float64 `json:"used"`
	Remaining float64 `json:"remaining"`
	Total     float64 `json:"total"`
}

// Provider defines the interface each platform must implement.
type Provider interface {
	// Name returns the provider identifier (e.g., "adobe", "leonardo").
	Name() string

	// Generate performs image generation synchronously.
	Generate(ctx context.Context, req ImageRequest) (*JobResult, error)

	// GenerateVideo performs video generation.
	GenerateVideo(ctx context.Context, req VideoRequest) (*JobResult, error)

	// UploadImage uploads an image and returns a reference ID.
	UploadImage(ctx context.Context, token string, data []byte, mime string) (string, error)

	// ValidateToken checks whether a token is valid.
	ValidateToken(ctx context.Context, token string) error

	// RefreshToken refreshes a token using stored credentials.
	RefreshToken(ctx context.Context, credential interface{}) (string, error)

	// SupportedModels returns the list of models this provider supports.
	SupportedModels() []ModelInfo

	// SupportedVideoModels returns the list of video models.
	SupportedVideoModels() []ModelInfo

	// GetCredits retrieves account credit info.
	GetCredits(ctx context.Context, token string) (*Credits, error)
}

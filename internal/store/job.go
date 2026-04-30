package store

import (
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// JobRecord represents an asynchronous generation job.
type JobRecord struct {
	ID          string  `json:"id"`
	Prompt      string  `json:"prompt"`
	AspectRatio string  `json:"aspect_ratio"`
	Model       string  `json:"model,omitempty"`
	Kind        string  `json:"kind,omitempty"`
	Status      string  `json:"status"`
	Progress    float64 `json:"progress"`
	ImageURL    string  `json:"image_url,omitempty"`
	Error       string  `json:"error,omitempty"`
	CreatedAt   float64 `json:"created_at"`
	UpdatedAt   float64 `json:"updated_at"`
}

// JobStore manages in-memory async jobs with a size cap.
type JobStore struct {
	mu       sync.Mutex
	items    map[string]*JobRecord
	maxItems int
}

// NewJobStore creates a new JobStore.
func NewJobStore(maxItems int) *JobStore {
	if maxItems <= 0 {
		maxItems = 200
	}
	return &JobStore{
		items:    make(map[string]*JobRecord),
		maxItems: maxItems,
	}
}

func (s *JobStore) cleanup() {
	if len(s.items) <= s.maxItems {
		return
	}
	// Remove oldest 50 entries
	type kv struct {
		id string
		ts float64
	}
	var entries []kv
	for id, r := range s.items {
		entries = append(entries, kv{id, r.CreatedAt})
	}
	// Sort by CreatedAt ascending
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[j].ts < entries[i].ts {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}
	removeCount := 50
	if removeCount > len(entries) {
		removeCount = len(entries)
	}
	for i := 0; i < removeCount; i++ {
		delete(s.items, entries[i].id)
	}
}

// Create creates a new job.
func (s *JobStore) Create(prompt, aspectRatio, model, kind string) *JobRecord {
	now := float64(time.Now().Unix())
	rec := &JobRecord{
		ID:          strings.ReplaceAll(uuid.New().String(), "-", ""),
		Prompt:      prompt,
		AspectRatio: aspectRatio,
		Model:       model,
		Kind:        kind,
		Status:      "queued",
		Progress:    0,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup()
	s.items[rec.ID] = rec
	return rec
}

// Get returns a job by ID.
func (s *JobStore) Get(jobID string) *JobRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.items[jobID]
}

// Update updates fields of a job by ID.
func (s *JobStore) Update(jobID string, updates map[string]interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.items[jobID]
	if rec == nil {
		return
	}
	for k, v := range updates {
		switch k {
		case "status":
			if s, ok := v.(string); ok {
				rec.Status = s
			}
		case "progress":
			if f, ok := v.(float64); ok {
				rec.Progress = f
			}
		case "image_url":
			if s, ok := v.(string); ok {
				rec.ImageURL = s
			}
		case "error":
			if s, ok := v.(string); ok {
				rec.Error = s
			}
		}
	}
	rec.UpdatedAt = float64(time.Now().Unix())
}

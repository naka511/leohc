package reqlog

import (
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Entry represents a single generation request log.
type Entry struct {
	ID           string  `json:"id"`
	Timestamp    float64 `json:"ts"`
	StatusCode   int     `json:"status_code"`
	TaskStatus   string  `json:"task_status"` // "IN_PROGRESS", "COMPLETE", "FAILED"
	Type         string  `json:"type"`        // "image", "video"
	DurationSec  float64 `json:"duration_sec"`
	TokenID      string  `json:"token_id,omitempty"`
	TokenAttempt int     `json:"token_attempt,omitempty"`
	AccountName  string  `json:"token_account_name"`
	AccountEmail string  `json:"token_account_email"`
	Model        string  `json:"model"`
	Prompt       string  `json:"prompt_preview"`
	ErrorCode    string  `json:"error_code,omitempty"`
	ErrorMessage string  `json:"error_message,omitempty"`
	GenerationID string  `json:"generation_id,omitempty"`
	PreviewURL   string  `json:"preview_url,omitempty"`
	PreviewKind  string  `json:"preview_kind,omitempty"`
	CreditCost   int     `json:"credit_cost"`
	Operation    string  `json:"operation,omitempty"`
}

// Store is a thread-safe log store with JSON file persistence.
type Store struct {
	mu       sync.Mutex
	entries  []Entry
	filePath string
}

// NewStore creates a new log store. If filePath is non-empty, loads existing
// logs from disk and persists all changes automatically.
func NewStore(filePath string) *Store {
	s := &Store{
		entries:  make([]Entry, 0),
		filePath: filePath,
	}
	if filePath != "" {
		s.loadFromDisk()
	}
	return s
}

// loadFromDisk reads saved log entries from the JSON file.
func (s *Store) loadFromDisk() {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[reqlog] failed to read %s: %v", s.filePath, err)
		}
		return
	}
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		log.Printf("[reqlog] failed to parse %s: %v", s.filePath, err)
		return
	}
	for i := range entries {
		entries[i].TaskStatus = normalizeTaskStatus(entries[i].TaskStatus)
		entries[i].DurationSec = normalizeDuration(entries[i].DurationSec)
		if entries[i].ErrorMessage == "" && entries[i].ErrorCode != "" && !isNumericErrorCode(entries[i].ErrorCode) {
			entries[i].ErrorMessage = entries[i].ErrorCode
		}
	}
	s.entries = entries
	log.Printf("[reqlog] loaded %d log entries from %s", len(entries), s.filePath)
}

// saveToDisk writes all entries to the JSON file.
// Must be called with s.mu held.
func (s *Store) saveToDisk() {
	if s.filePath == "" {
		return
	}
	data, err := json.Marshal(s.entries)
	if err != nil {
		log.Printf("[reqlog] failed to marshal logs: %v", err)
		return
	}
	if err := os.WriteFile(s.filePath, data, 0644); err != nil {
		log.Printf("[reqlog] failed to write %s: %v", s.filePath, err)
	}
}

// Add inserts a new log entry at the beginning (newest first).
func (s *Store) Add(entry Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entry.Timestamp == 0 {
		entry.Timestamp = float64(time.Now().Unix())
	}
	entry.TaskStatus = normalizeTaskStatus(entry.TaskStatus)
	entry.DurationSec = normalizeDuration(entry.DurationSec)
	if entry.ErrorMessage == "" && entry.ErrorCode != "" && !isNumericErrorCode(entry.ErrorCode) {
		entry.ErrorMessage = entry.ErrorCode
	}

	// Prepend
	s.entries = append([]Entry{entry}, s.entries...)

	s.saveToDisk()
}

// UpdateByGenerationID updates an entry matching the generation ID.
func (s *Store) UpdateByGenerationID(genID string, taskStatus string, statusCode int, previewURL string, previewKind string, errorMessage string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.entries {
		if s.entries[i].GenerationID == genID {
			s.entries[i].TaskStatus = normalizeTaskStatus(taskStatus)
			if statusCode > 0 {
				s.entries[i].StatusCode = statusCode
				if statusCode >= 400 {
					s.entries[i].ErrorCode = strconv.Itoa(statusCode)
				}
			}
			if previewURL != "" {
				s.entries[i].PreviewURL = previewURL
			}
			if previewKind != "" {
				s.entries[i].PreviewKind = previewKind
			}
			if errorMessage != "" {
				s.entries[i].ErrorMessage = errorMessage
			}
			if s.entries[i].DurationSec <= 0 && s.entries[i].Timestamp > 0 && s.entries[i].TaskStatus != "IN_PROGRESS" {
				elapsed := time.Since(time.Unix(int64(s.entries[i].Timestamp), 0)).Seconds()
				s.entries[i].DurationSec = normalizeDuration(elapsed)
			}
			s.saveToDisk()
			return true
		}
	}
	return false
}

// UpdateDuration updates the duration of an entry by generation ID.
func (s *Store) UpdateDuration(genID string, durationSec float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.entries {
		if s.entries[i].GenerationID == genID {
			s.entries[i].DurationSec = normalizeDuration(durationSec)
			s.saveToDisk()
			return
		}
	}
}

// List returns paginated log entries with optional filtering.
func (s *Store) List(page, pageSize int, failedOnly bool) ([]Entry, int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Filter — exclude IN_PROGRESS items (they are shown via Running())
	var filtered []Entry
	for _, e := range s.entries {
		if normalizeTaskStatus(e.TaskStatus) == "IN_PROGRESS" {
			continue
		}
		if failedOnly && normalizeTaskStatus(e.TaskStatus) != "FAILED" {
			continue
		}
		filtered = append(filtered, e)
	}

	total := len(filtered)
	if pageSize <= 0 {
		pageSize = 50
	}
	totalPages := (total + pageSize - 1) / pageSize
	if totalPages < 1 {
		totalPages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}

	start := (page - 1) * pageSize
	end := start + pageSize
	if start >= total {
		return []Entry{}, page, totalPages
	}
	if end > total {
		end = total
	}

	return filtered[start:end], page, totalPages
}

// Running returns entries that are still in progress.
func (s *Store) Running() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()

	var result []Entry
	for _, e := range s.entries {
		if normalizeTaskStatus(e.TaskStatus) == "IN_PROGRESS" {
			result = append(result, e)
		}
	}
	return result
}

// Stats computes statistics for log entries within a time range.
func (s *Store) Stats(rangeStr string) map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	var startTime time.Time

	switch rangeStr {
	case "today":
		startTime = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	case "7d":
		startTime = now.AddDate(0, 0, -7)
	case "30d":
		startTime = now.AddDate(0, 0, -30)
	default:
		startTime = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	}

	startTs := float64(startTime.Unix())
	var images, videos, total, failed int

	for _, e := range s.entries {
		if e.Timestamp < startTs {
			continue
		}
		total++
		taskStatus := normalizeTaskStatus(e.TaskStatus)
		if taskStatus == "FAILED" {
			failed++
		}
		switch e.Type {
		case "image":
			if taskStatus == "COMPLETE" {
				images++
			}
		case "video":
			if taskStatus == "COMPLETE" {
				videos++
			}
		}
	}

	return map[string]interface{}{
		"generated_images": images,
		"generated_videos": videos,
		"total_requests":   total,
		"failed_requests":  failed,
		"start_ts":         startTs,
		"end_ts":           float64(now.Unix()),
	}
}

// Clear removes all entries. Returns the count cleared.
func (s *Store) Clear() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	n := len(s.entries)
	s.entries = s.entries[:0]
	s.saveToDisk()
	return n
}

func normalizeTaskStatus(status string) string {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "COMPLETED":
		return "COMPLETE"
	case "":
		return "IN_PROGRESS"
	default:
		return strings.ToUpper(strings.TrimSpace(status))
	}
}

func normalizeDuration(durationSec float64) float64 {
	if durationSec <= 0 {
		return 0
	}
	if durationSec < 0.1 {
		return 0.1
	}
	return float64(int(durationSec*10+0.5)) / 10
}

func isNumericErrorCode(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	_, err := strconv.Atoi(value)
	return err == nil
}

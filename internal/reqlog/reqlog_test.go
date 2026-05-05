package reqlog

import (
	"testing"
	"time"
)

func TestExpireStaleRunningMarksTimedOutEntriesFailed(t *testing.T) {
	store := &Store{
		entries: []Entry{
			{
				ID:         "stale",
				Timestamp:  float64(time.Now().Add(-40 * time.Minute).Unix()),
				TaskStatus: "IN_PROGRESS",
				Type:       "video",
			},
			{
				ID:         "fresh",
				Timestamp:  float64(time.Now().Add(-5 * time.Minute).Unix()),
				TaskStatus: "IN_PROGRESS",
				Type:       "video",
			},
		},
	}

	expired := store.ExpireStaleRunning(30*time.Minute, time.Now())
	if expired != 1 {
		t.Fatalf("expected 1 expired entry, got %d", expired)
	}

	if got := store.entries[0].TaskStatus; got != "FAILED" {
		t.Fatalf("expected stale entry to become FAILED, got %q", got)
	}
	if got := store.entries[0].StatusCode; got != 504 {
		t.Fatalf("expected stale entry status code 504, got %d", got)
	}
	if got := store.entries[0].ErrorMessage; got != "Generation polling timed out" {
		t.Fatalf("unexpected stale entry error message: %q", got)
	}
	if got := store.entries[1].TaskStatus; got != "IN_PROGRESS" {
		t.Fatalf("expected fresh entry to remain IN_PROGRESS, got %q", got)
	}
}

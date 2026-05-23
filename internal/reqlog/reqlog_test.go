package reqlog

import (
	"fmt"
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

func TestListCollectsOnlyRequestedPage(t *testing.T) {
	store := &Store{
		entries: []Entry{
			{ID: "newest", TaskStatus: "COMPLETE"},
			{ID: "running", TaskStatus: "IN_PROGRESS"},
			{ID: "failed", TaskStatus: "FAILED"},
			{ID: "oldest", TaskStatus: "COMPLETE"},
		},
	}

	entries, page, totalPages := store.List(1, 2, false)
	if page != 1 {
		t.Fatalf("expected page 1, got %d", page)
	}
	if totalPages != 2 {
		t.Fatalf("expected 2 total pages, got %d", totalPages)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].ID != "newest" || entries[1].ID != "failed" {
		t.Fatalf("unexpected page entries: %+v", entries)
	}

	failed, _, failedPages := store.List(1, 20, true)
	if failedPages != 1 {
		t.Fatalf("expected failed logs to fit on 1 page, got %d", failedPages)
	}
	if len(failed) != 1 || failed[0].ID != "failed" {
		t.Fatalf("expected only failed entry, got %+v", failed)
	}
}

func TestRunningLimitCapsReturnedEntries(t *testing.T) {
	store := &Store{
		entries: []Entry{
			{ID: "a", TaskStatus: "IN_PROGRESS"},
			{ID: "b", TaskStatus: "IN_PROGRESS"},
			{ID: "c", TaskStatus: "IN_PROGRESS"},
		},
	}

	entries := store.RunningLimit(2)
	if len(entries) != 2 {
		t.Fatalf("expected 2 running entries, got %d", len(entries))
	}
	if entries[0].ID != "a" || entries[1].ID != "b" {
		t.Fatalf("unexpected running entries: %+v", entries)
	}
}

func TestSetMaxEntriesPrunesOldLogs(t *testing.T) {
	store := &Store{maxEntries: 5000}
	for i := 0; i < 101; i++ {
		store.entries = append(store.entries, Entry{ID: fmt.Sprintf("log-%03d", i), TaskStatus: "COMPLETE"})
	}

	store.SetMaxEntries(100)
	if len(store.entries) != 100 {
		t.Fatalf("expected 100 retained entries, got %d", len(store.entries))
	}
	if store.entries[0].ID != "log-000" || store.entries[99].ID != "log-099" {
		t.Fatalf("expected newest entries to remain, got %+v", store.entries)
	}
}

func TestAddHonorsMaxEntries(t *testing.T) {
	store := &Store{maxEntries: 100}

	for i := 0; i < 101; i++ {
		store.Add(Entry{ID: fmt.Sprintf("log-%03d", i), TaskStatus: "COMPLETE"})
	}

	if len(store.entries) != 100 {
		t.Fatalf("expected 100 retained entries, got %d", len(store.entries))
	}
	if store.entries[0].ID != "log-100" || store.entries[99].ID != "log-001" {
		t.Fatalf("expected newest entries to remain, got %+v", store.entries)
	}
}

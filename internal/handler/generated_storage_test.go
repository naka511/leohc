package handler

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"leo-go/internal/config"
)

func TestGetGeneratedStorageStats(t *testing.T) {
	cfg := config.Global()
	original := cfg.GetAll()
	cfg.SetAll(map[string]interface{}{})
	t.Cleanup(func() {
		cfg.SetAll(original)
	})

	dir := t.TempDir()
	writeSizedTestFile(t, filepath.Join(dir, "a.bin"), 1<<20, time.Now().Add(-2*time.Hour))
	writeSizedTestFile(t, filepath.Join(dir, "b.bin"), 512<<10, time.Now().Add(-1*time.Hour))

	srv := &Server{
		Config:       cfg,
		GeneratedDir: dir,
	}
	stats, err := srv.getGeneratedStorageStats()
	if err != nil {
		t.Fatalf("getGeneratedStorageStats returned error: %v", err)
	}
	if stats.FileCount != 2 {
		t.Fatalf("expected 2 files, got %d", stats.FileCount)
	}
	if want := int64((1 << 20) + (512 << 10)); stats.Bytes != want {
		t.Fatalf("expected %d bytes, got %d", want, stats.Bytes)
	}
}

func TestEnforceGeneratedStorageLimitPrunesOldestFiles(t *testing.T) {
	cfg := config.Global()
	original := cfg.GetAll()
	cfg.SetAll(map[string]interface{}{
		"generated_max_size_mb":   5,
		"generated_prune_size_mb": 2,
	})
	t.Cleanup(func() {
		cfg.SetAll(original)
	})

	dir := t.TempDir()
	oldFile := filepath.Join(dir, "old.bin")
	midFile := filepath.Join(dir, "mid.bin")
	newFile := filepath.Join(dir, "new.bin")
	now := time.Now()
	writeSizedTestFile(t, oldFile, 4<<20, now.Add(-3*time.Hour))
	writeSizedTestFile(t, midFile, 3<<20, now.Add(-2*time.Hour))
	writeSizedTestFile(t, newFile, 2<<20, now.Add(-1*time.Hour))

	srv := &Server{
		Config:       cfg,
		GeneratedDir: dir,
	}
	stats, err := srv.enforceGeneratedStorageLimit()
	if err != nil {
		t.Fatalf("enforceGeneratedStorageLimit returned error: %v", err)
	}
	if stats.FileCount != 1 {
		t.Fatalf("expected 1 file after prune, got %d", stats.FileCount)
	}
	if want := int64(2 << 20); stats.Bytes != want {
		t.Fatalf("expected %d bytes after prune, got %d", want, stats.Bytes)
	}
	if _, err := os.Stat(newFile); err != nil {
		t.Fatalf("expected newest file to remain, got error: %v", err)
	}
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Fatalf("expected oldest file to be removed, got err=%v", err)
	}
	if _, err := os.Stat(midFile); !os.IsNotExist(err) {
		t.Fatalf("expected middle file to be removed, got err=%v", err)
	}
}

func writeSizedTestFile(t *testing.T, path string, size int, modTime time.Time) {
	t.Helper()

	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("write test file %s: %v", path, err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("set modtime for %s: %v", path, err)
	}
}

package handler

import (
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type generatedStorageStats struct {
	Bytes     int64
	FileCount int
}

type generatedStorageFile struct {
	Path    string
	Size    int64
	ModTime time.Time
}

func (s *Server) getGeneratedStorageStats() (generatedStorageStats, error) {
	s.generatedStorageMu.Lock()
	defer s.generatedStorageMu.Unlock()

	stats, _, err := s.scanGeneratedStorageLocked()
	return stats, err
}

func (s *Server) enforceGeneratedStorageLimit() (generatedStorageStats, error) {
	s.generatedStorageMu.Lock()
	defer s.generatedStorageMu.Unlock()

	return s.enforceGeneratedStorageLimitLocked()
}

func (s *Server) scanGeneratedStorageLocked() (generatedStorageStats, []generatedStorageFile, error) {
	var stats generatedStorageStats
	if s == nil || strings.TrimSpace(s.GeneratedDir) == "" {
		return stats, nil, nil
	}

	dir := strings.TrimSpace(s.GeneratedDir)
	if info, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return stats, nil, nil
		}
		return stats, nil, fmt.Errorf("stat generated dir: %w", err)
	} else if !info.IsDir() {
		return stats, nil, fmt.Errorf("generated path is not a directory")
	}

	files := make([]generatedStorageFile, 0)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info == nil || info.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		stats.Bytes += info.Size()
		stats.FileCount++
		files = append(files, generatedStorageFile{
			Path:    path,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
		return nil
	})
	if err != nil {
		return stats, nil, fmt.Errorf("scan generated dir: %w", err)
	}

	return stats, files, nil
}

func (s *Server) enforceGeneratedStorageLimitLocked() (generatedStorageStats, error) {
	stats, files, err := s.scanGeneratedStorageLocked()
	if err != nil {
		return stats, err
	}

	maxBytes, pruneBytes := s.generatedStorageLimitBytes()
	if maxBytes <= 0 || stats.Bytes <= maxBytes || len(files) == 0 {
		return stats, nil
	}

	targetBytes := maxBytes - pruneBytes
	if targetBytes < 0 {
		targetBytes = 0
	}

	newestPath := findNewestGeneratedFile(files)
	sort.Slice(files, func(i, j int) bool {
		if files[i].ModTime.Equal(files[j].ModTime) {
			return files[i].Path < files[j].Path
		}
		return files[i].ModTime.Before(files[j].ModTime)
	})

	var deletedBytes int64
	deletedCount := 0
	for _, file := range files {
		if stats.Bytes-deletedBytes <= targetBytes {
			break
		}
		if sameGeneratedFilePath(file.Path, newestPath) {
			continue
		}
		if removeErr := os.Remove(file.Path); removeErr != nil {
			if os.IsNotExist(removeErr) {
				deletedBytes += file.Size
				deletedCount++
				continue
			}
			return stats, fmt.Errorf("remove generated file %s: %w", filepath.Base(file.Path), removeErr)
		}
		deletedBytes += file.Size
		deletedCount++
	}

	if deletedBytes > 0 {
		stats.Bytes -= deletedBytes
		if stats.Bytes < 0 {
			stats.Bytes = 0
		}
		stats.FileCount -= deletedCount
		if stats.FileCount < 0 {
			stats.FileCount = 0
		}
		log.Printf("[generated] pruned %d files (%.1f MB), usage now %.1f MB", deletedCount, generatedStorageUsageMB(deletedBytes), generatedStorageUsageMB(stats.Bytes))
	}

	return stats, nil
}

func (s *Server) generatedStorageLimitBytes() (int64, int64) {
	maxMB := 1024
	pruneMB := 200
	if s != nil && s.Config != nil {
		maxMB = s.Config.GetInt("generated_max_size_mb", 1024)
		pruneMB = s.Config.GetInt("generated_prune_size_mb", 200)
	}
	if maxMB < 0 {
		maxMB = 0
	}
	if pruneMB < 0 {
		pruneMB = 0
	}
	return int64(maxMB) << 20, int64(pruneMB) << 20
}

func generatedStorageUsageMB(bytes int64) float64 {
	if bytes <= 0 {
		return 0
	}
	return math.Round((float64(bytes)/(1<<20))*10) / 10
}

func findNewestGeneratedFile(files []generatedStorageFile) string {
	if len(files) == 0 {
		return ""
	}
	newest := files[0]
	for _, file := range files[1:] {
		if file.ModTime.After(newest.ModTime) || (file.ModTime.Equal(newest.ModTime) && file.Path > newest.Path) {
			newest = file
		}
	}
	return newest.Path
}

func sameGeneratedFilePath(a, b string) bool {
	cleanA := filepath.Clean(strings.TrimSpace(a))
	cleanB := filepath.Clean(strings.TrimSpace(b))
	if cleanA == "" || cleanB == "" {
		return cleanA == cleanB
	}
	return strings.EqualFold(cleanA, cleanB)
}

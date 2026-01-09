package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gwlsn/shrinkray/internal/browse"
	"github.com/gwlsn/shrinkray/internal/config"
	"github.com/gwlsn/shrinkray/internal/ffmpeg"
	"github.com/gwlsn/shrinkray/internal/jobs"
)

func setupTestHandler(t *testing.T) (*Handler, string) {
	tmpDir := t.TempDir()

	// Create test directory structure
	tvDir := filepath.Join(tmpDir, "TV Shows", "Test Show", "Season 1")
	if err := os.MkdirAll(tvDir, 0755); err != nil {
		t.Fatalf("failed to create test dirs: %v", err)
	}

	// Create fake video files
	for _, name := range []string{"episode1.mkv", "episode2.mkv"} {
		path := filepath.Join(tvDir, name)
		if err := os.WriteFile(path, []byte("fake video"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
	}

	cfg := &config.Config{
		MediaPath:        tmpDir,
		OriginalHandling: "replace",
		Workers:          1,
		FFmpegPath:       "ffmpeg",
		FFprobePath:      "ffprobe",
	}

	prober := ffmpeg.NewProber(cfg.FFprobePath)
	browser := browse.NewBrowser(prober, cfg.MediaPath)
	queue := jobs.NewQueue()
	pool := jobs.NewWorkerPool(queue, cfg, nil)

	handler := NewHandler(browser, queue, pool, cfg, "")

	return handler, tmpDir
}

func TestBrowseEndpoint(t *testing.T) {
	handler, tmpDir := setupTestHandler(t)

	// Test browsing root
	req := httptest.NewRequest("GET", "/api/browse", nil)
	w := httptest.NewRecorder()

	handler.Browse(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var result browse.BrowseResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if result.Path != tmpDir {
		t.Errorf("expected path %s, got %s", tmpDir, result.Path)
	}

	if len(result.Entries) == 0 {
		t.Error("expected at least one entry")
	}

	t.Logf("Browse response: %d entries", len(result.Entries))
}

func TestBrowseWithPath(t *testing.T) {
	handler, tmpDir := setupTestHandler(t)

	tvDir := filepath.Join(tmpDir, "TV Shows")

	req := httptest.NewRequest("GET", "/api/browse?path="+url.QueryEscape(tvDir), nil)
	w := httptest.NewRecorder()

	handler.Browse(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var result browse.BrowseResult
	json.Unmarshal(w.Body.Bytes(), &result)

	if result.Path != tvDir {
		t.Errorf("expected path %s, got %s", tvDir, result.Path)
	}

	if result.Parent != tmpDir {
		t.Errorf("expected parent %s, got %s", tmpDir, result.Parent)
	}
}

func TestPresetsEndpoint(t *testing.T) {
	handler, _ := setupTestHandler(t)

	req := httptest.NewRequest("GET", "/api/presets", nil)
	w := httptest.NewRecorder()

	handler.Presets(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var presets []*ffmpeg.Preset
	if err := json.Unmarshal(w.Body.Bytes(), &presets); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if len(presets) != 4 {
		t.Errorf("expected 4 presets, got %d", len(presets))
	}

	t.Logf("Presets: %v", presets)
}

func TestJobsEndpoint(t *testing.T) {
	handler, _ := setupTestHandler(t)

	// Initially should have no jobs
	req := httptest.NewRequest("GET", "/api/jobs", nil)
	w := httptest.NewRecorder()

	handler.ListJobs(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var result map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &result)

	jobs, _ := result["jobs"].([]interface{})
	if len(jobs) != 0 {
		t.Errorf("expected 0 jobs, got %d", len(jobs))
	}
}

func TestCreateJobsEndpoint(t *testing.T) {
	handler, tmpDir := setupTestHandler(t)

	// Create jobs for the test show
	showDir := filepath.Join(tmpDir, "TV Shows", "Test Show", "Season 1")

	reqBody := CreateJobsRequest{
		Paths:    []string{showDir},
		PresetID: "compress",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/api/jobs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.CreateJobs(w, req)

	// Will fail because fake video files can't be probed
	// But we can at least check it tries
	t.Logf("Create jobs response: %d - %s", w.Code, w.Body.String())
}

func TestConfigEndpoint(t *testing.T) {
	handler, _ := setupTestHandler(t)

	// Get config
	req := httptest.NewRequest("GET", "/api/config", nil)
	w := httptest.NewRecorder()

	handler.GetConfig(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var cfg map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &cfg)

	if cfg["original_handling"] != "replace" {
		t.Errorf("expected original_handling 'replace', got %v", cfg["original_handling"])
	}

	t.Logf("Config: %v", cfg)
}

func TestUpdateConfigEndpoint(t *testing.T) {
	handler, _ := setupTestHandler(t)

	// Update config
	keepVal := "keep"
	reqBody := UpdateConfigRequest{
		OriginalHandling: &keepVal,
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("PUT", "/api/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.UpdateConfig(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	// Verify it changed
	req = httptest.NewRequest("GET", "/api/config", nil)
	w = httptest.NewRecorder()

	handler.GetConfig(w, req)

	var cfg map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &cfg)

	if cfg["original_handling"] != "keep" {
		t.Errorf("expected original_handling 'keep', got %v", cfg["original_handling"])
	}
}

func TestStatsEndpoint(t *testing.T) {
	handler, _ := setupTestHandler(t)

	req := httptest.NewRequest("GET", "/api/stats", nil)
	w := httptest.NewRecorder()

	handler.Stats(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var stats jobs.Stats
	if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil {
		t.Fatalf("failed to parse stats: %v", err)
	}

	if stats.Total != 0 {
		t.Errorf("expected 0 total jobs, got %d", stats.Total)
	}

	t.Logf("Stats: %+v", stats)
}

func TestJobStreamEndpoint(t *testing.T) {
	handler, _ := setupTestHandler(t)

	// Create a request with a context that will timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	req := httptest.NewRequest("GET", "/api/jobs/stream", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	// Run in goroutine since it blocks
	done := make(chan bool)
	go func() {
		handler.JobStream(w, req)
		done <- true
	}()

	// Wait for timeout or completion
	select {
	case <-done:
		// Good - context cancelled
	case <-time.After(time.Second):
		t.Error("SSE handler didn't respect context cancellation")
	}

	// Should have SSE headers
	if w.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("expected text/event-stream content type, got %s", w.Header().Get("Content-Type"))
	}

	// Should have initial data
	if !bytes.Contains(w.Body.Bytes(), []byte("data:")) {
		t.Error("expected SSE data in response")
	}

	t.Logf("SSE response: %s", w.Body.String()[:min(200, len(w.Body.String()))])
}


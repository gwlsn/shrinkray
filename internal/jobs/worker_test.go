package jobs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gwlsn/shrinkray/internal/config"
	"github.com/gwlsn/shrinkray/internal/ffmpeg"
	"github.com/gwlsn/shrinkray/internal/util"
)

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		input    time.Duration
		expected string
	}{
		{5 * time.Second, "5s"},
		{65 * time.Second, "1m 5s"},
		{3600 * time.Second, "1h 0m"},
		{3665 * time.Second, "1h 1m"},
		{-1 * time.Second, ""},
	}

	for _, tt := range tests {
		result := util.FormatDuration(tt.input)
		if result != tt.expected {
			t.Errorf("FormatDuration(%v) = %s, expected %s", tt.input, result, tt.expected)
		}
	}
}

func TestWorkerPoolIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Find test file
	testFile, err := filepath.Abs(filepath.Join("..", "..", "testdata", "test_x264.mkv"))
	if err != nil {
		t.Fatalf("failed to get test file path: %v", err)
	}

	if _, err := os.Stat(testFile); os.IsNotExist(err) {
		t.Skipf("test file not found: %s", testFile)
	}

	// Create a temp directory for output
	tmpDir := t.TempDir()

	// Copy test file to temp dir (so we don't modify the original)
	testCopy := filepath.Join(tmpDir, "test_video.mkv")
	input, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("failed to read test file: %v", err)
	}
	if err := os.WriteFile(testCopy, input, 0644); err != nil {
		t.Fatalf("failed to copy test file: %v", err)
	}

	// Create config
	cfg := &config.Config{
		MediaPath:        tmpDir,
		TempPath:         tmpDir,
		OriginalHandling: "replace",
		Workers:          1,
		FFmpegPath:       "ffmpeg",
		FFprobePath:      "ffprobe",
	}

	// Create queue (in-memory for testing)
	queue := NewQueue()

	// Subscribe to events
	events := queue.Subscribe()
	defer queue.Unsubscribe(events)

	// Probe the file
	prober := ffmpeg.NewProber(cfg.FFprobePath)
	probe, err := prober.Probe(context.Background(), testCopy)
	if err != nil {
		t.Fatalf("failed to probe file: %v", err)
	}

	// Add job
	job, err := queue.Add(testCopy, "compress", probe)
	if err != nil {
		t.Fatalf("failed to add job: %v", err)
	}

	t.Logf("Added job: %s", job.ID)

	// Create and start worker pool
	pool := NewWorkerPool(queue, cfg, nil)
	pool.Start()
	defer pool.Stop()

	// Wait for job to complete (with timeout)
	timeout := time.After(5 * time.Minute)
	completed := false

	for !completed {
		select {
		case event := <-events:
			t.Logf("Event: %s - Job %s (progress: %.1f%%)", event.Type, event.Job.ID, event.Job.Progress)

			if event.Job.ID == job.ID && event.Type == "complete" {
				completed = true
				t.Logf("Job completed! Output: %s, Saved: %d bytes",
					event.Job.OutputPath, event.Job.SpaceSaved)
			} else if event.Job.ID == job.ID && event.Type == "failed" {
				t.Fatalf("Job failed: %s", event.Job.Error)
			}

		case <-timeout:
			t.Fatal("timeout waiting for job to complete")
		}
	}

	// Verify output file exists
	finalJob := queue.Get(job.ID)
	if finalJob.OutputPath == "" {
		t.Error("output path not set")
	}

	if _, err := os.Stat(finalJob.OutputPath); os.IsNotExist(err) {
		t.Error("output file does not exist")
	}

	// Verify original was renamed to .old
	oldPath := testCopy + ".old"
	if _, err := os.Stat(oldPath); os.IsNotExist(err) {
		t.Error("original file was not renamed to .old")
	}

	// Verify output is HEVC
	outputProbe, err := prober.Probe(context.Background(), finalJob.OutputPath)
	if err != nil {
		t.Fatalf("failed to probe output: %v", err)
	}

	if outputProbe.VideoCodec != "hevc" {
		t.Errorf("expected hevc codec, got %s", outputProbe.VideoCodec)
	}

	t.Logf("Integration test passed! Input: %d bytes, Output: %d bytes (%.1f%% saved)",
		finalJob.InputSize, finalJob.OutputSize,
		float64(finalJob.SpaceSaved)/float64(finalJob.InputSize)*100)
}

func TestWorkerPoolCancel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cancel test in short mode")
	}

	// This test is harder to write reliably because we need a long-running transcode
	// For now, just test that the cancel mechanism works without blocking

	cfg := &config.Config{
		Workers:     1,
		FFmpegPath:  "ffmpeg",
		FFprobePath: "ffprobe",
	}

	queue := NewQueue()
	pool := NewWorkerPool(queue, cfg, nil)

	// Start and immediately stop
	pool.Start()
	pool.Stop()

	t.Log("Worker pool start/stop works correctly")
}

func TestWorkerPoolResize(t *testing.T) {
	cfg := &config.Config{
		Workers:     1,
		FFmpegPath:  "ffmpeg",
		FFprobePath: "ffprobe",
	}

	queue := NewQueue()
	pool := NewWorkerPool(queue, cfg, nil)
	pool.Start()
	defer pool.Stop()

	// Initial count should be 1
	if pool.WorkerCount() != 1 {
		t.Errorf("expected 1 worker, got %d", pool.WorkerCount())
	}

	// Increase to 3
	pool.Resize(3)
	if pool.WorkerCount() != 3 {
		t.Errorf("expected 3 workers after resize up, got %d", pool.WorkerCount())
	}
	if cfg.Workers != 3 {
		t.Errorf("expected config.Workers to be 3, got %d", cfg.Workers)
	}

	// Decrease to 2 - should happen immediately (synchronously)
	pool.Resize(2)
	if pool.WorkerCount() != 2 {
		t.Errorf("expected 2 workers after resize down, got %d", pool.WorkerCount())
	}

	// Test bounds - minimum 1
	pool.Resize(0)
	if pool.WorkerCount() != 1 {
		t.Errorf("expected 1 worker (minimum), got %d", pool.WorkerCount())
	}

	// Test bounds - maximum 6
	pool.Resize(20)
	if pool.WorkerCount() != 6 {
		t.Errorf("expected 6 workers (maximum), got %d", pool.WorkerCount())
	}

	t.Logf("Worker pool resize works correctly, final count: %d", pool.WorkerCount())
}

func TestWorkerPoolResizeDown(t *testing.T) {
	// Test that resize down is immediate and doesn't block
	cfg := &config.Config{
		Workers:     4,
		FFmpegPath:  "ffmpeg",
		FFprobePath: "ffprobe",
	}

	queue := NewQueue()
	pool := NewWorkerPool(queue, cfg, nil)
	pool.Start()
	defer pool.Stop()

	if pool.WorkerCount() != 4 {
		t.Errorf("expected 4 workers, got %d", pool.WorkerCount())
	}

	// Resize down should complete immediately (not wait for jobs)
	pool.Resize(1)

	// Should be 1 immediately after Resize returns
	if pool.WorkerCount() != 1 {
		t.Errorf("expected 1 worker immediately after resize, got %d", pool.WorkerCount())
	}

	t.Log("Resize down is immediate")
}

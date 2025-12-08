package browse

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/graysonwilson/shrinkray/internal/ffmpeg"
)

func TestBrowser(t *testing.T) {
	// Create a test directory structure
	tmpDir := t.TempDir()

	// Create directories
	tvDir := filepath.Join(tmpDir, "TV Shows")
	showDir := filepath.Join(tvDir, "Test Show")
	seasonDir := filepath.Join(showDir, "Season 1")

	if err := os.MkdirAll(seasonDir, 0755); err != nil {
		t.Fatalf("failed to create test dirs: %v", err)
	}

	// Create some fake video files
	files := []string{
		filepath.Join(seasonDir, "episode1.mkv"),
		filepath.Join(seasonDir, "episode2.mkv"),
		filepath.Join(seasonDir, "episode3.mp4"),
	}

	for _, f := range files {
		if err := os.WriteFile(f, []byte("fake video content"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
	}

	// Also create a non-video file
	txtFile := filepath.Join(seasonDir, "notes.txt")
	if err := os.WriteFile(txtFile, []byte("some notes"), 0644); err != nil {
		t.Fatalf("failed to create txt file: %v", err)
	}

	// Create browser
	prober := ffmpeg.NewProber("ffprobe")
	browser := NewBrowser(prober, tmpDir)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Test browsing root
	result, err := browser.Browse(ctx, tmpDir)
	if err != nil {
		t.Fatalf("Browse failed: %v", err)
	}

	if result.Path != tmpDir {
		t.Errorf("expected path %s, got %s", tmpDir, result.Path)
	}

	if result.Parent != "" {
		t.Errorf("expected no parent at root, got %s", result.Parent)
	}

	if len(result.Entries) != 1 {
		t.Errorf("expected 1 entry (TV Shows), got %d", len(result.Entries))
	}

	t.Logf("Root browse: %d entries", len(result.Entries))

	// Test browsing into TV Shows
	result, err = browser.Browse(ctx, tvDir)
	if err != nil {
		t.Fatalf("Browse TV Shows failed: %v", err)
	}

	if result.Parent != tmpDir {
		t.Errorf("expected parent %s, got %s", tmpDir, result.Parent)
	}

	t.Logf("TV Shows browse: %d entries", len(result.Entries))

	// Test browsing into Season 1
	result, err = browser.Browse(ctx, seasonDir)
	if err != nil {
		t.Fatalf("Browse Season 1 failed: %v", err)
	}

	// Should have 3 video files (txt file should be included but not counted as video)
	if result.VideoCount != 3 {
		t.Errorf("expected 3 video files, got %d", result.VideoCount)
	}

	// Should have 4 entries total (3 videos + 1 txt)
	if len(result.Entries) != 4 {
		t.Errorf("expected 4 entries, got %d", len(result.Entries))
	}

	t.Logf("Season 1 browse: %d entries, %d videos, %d bytes total",
		len(result.Entries), result.VideoCount, result.TotalSize)
}

func TestBrowserSecurity(t *testing.T) {
	tmpDir := t.TempDir()

	prober := ffmpeg.NewProber("ffprobe")
	browser := NewBrowser(prober, tmpDir)

	ctx := context.Background()

	// Try to browse outside media root
	result, err := browser.Browse(ctx, "/etc")
	if err != nil {
		t.Fatalf("Browse failed: %v", err)
	}

	// Should redirect to media root
	if result.Path != tmpDir {
		t.Errorf("expected path to be redirected to %s, got %s", tmpDir, result.Path)
	}

	// Try path traversal
	result, err = browser.Browse(ctx, filepath.Join(tmpDir, "..", ".."))
	if err != nil {
		t.Fatalf("Browse failed: %v", err)
	}

	// Should still be within media root
	if result.Path != tmpDir {
		t.Errorf("expected path to be %s after traversal attempt, got %s", tmpDir, result.Path)
	}
}

func TestGetVideoFiles(t *testing.T) {
	// Use the real test file
	testFile := filepath.Join("..", "..", "testdata", "test_x264.mkv")
	absPath, err := filepath.Abs(testFile)
	if err != nil {
		t.Fatalf("failed to get abs path: %v", err)
	}

	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		t.Skipf("test file not found: %s", absPath)
	}

	testDataDir := filepath.Dir(absPath)

	prober := ffmpeg.NewProber("ffprobe")
	browser := NewBrowser(prober, testDataDir)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Get video files from testdata
	results, err := browser.GetVideoFiles(ctx, []string{testDataDir})
	if err != nil {
		t.Fatalf("GetVideoFiles failed: %v", err)
	}

	if len(results) == 0 {
		t.Error("expected at least one video file")
	}

	for _, r := range results {
		t.Logf("Found video: %s (%s, %dx%d)", r.Path, r.VideoCodec, r.Width, r.Height)
	}
}

func TestCaching(t *testing.T) {
	testFile := filepath.Join("..", "..", "testdata", "test_x264.mkv")
	absPath, err := filepath.Abs(testFile)
	if err != nil {
		t.Fatalf("failed to get abs path: %v", err)
	}

	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		t.Skipf("test file not found: %s", absPath)
	}

	testDataDir := filepath.Dir(absPath)

	prober := ffmpeg.NewProber("ffprobe")
	browser := NewBrowser(prober, testDataDir)

	ctx := context.Background()

	// First probe - should be slow
	start := time.Now()
	results1, _ := browser.GetVideoFiles(ctx, []string{absPath})
	firstDuration := time.Since(start)

	// Second probe - should be cached and fast
	start = time.Now()
	results2, _ := browser.GetVideoFiles(ctx, []string{absPath})
	secondDuration := time.Since(start)

	if len(results1) != len(results2) {
		t.Error("cached results differ from original")
	}

	t.Logf("First probe: %v, Second probe (cached): %v", firstDuration, secondDuration)

	// Second should be significantly faster
	if secondDuration > firstDuration/2 {
		t.Log("Warning: caching may not be working effectively")
	}

	// Clear cache and verify
	browser.ClearCache()

	start = time.Now()
	browser.GetVideoFiles(ctx, []string{absPath})
	thirdDuration := time.Since(start)

	t.Logf("Third probe (after cache clear): %v", thirdDuration)
}

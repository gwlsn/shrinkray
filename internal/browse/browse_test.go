package browse

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gwlsn/shrinkray/internal/ffmpeg"
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

	// First browse triggers background computation; counts may be 0
	t.Logf("Root browse: %d entries", len(result.Entries))

	// Wait for background goroutines to finish populating count cache
	time.Sleep(200 * time.Millisecond)

	// Second browse should return cached recursive counts
	result, err = browser.Browse(ctx, tmpDir)
	if err != nil {
		t.Fatalf("Browse (cached) failed: %v", err)
	}

	if len(result.Entries) == 1 {
		tvEntry := result.Entries[0]
		if tvEntry.FileCount != 3 {
			t.Errorf("expected TV Shows folder to recursively report 3 videos, got %d", tvEntry.FileCount)
		}
		// Each fake video file is 18 bytes ("fake video content"), so 3 × 18 = 54
		if tvEntry.TotalSize != 54 {
			t.Errorf("expected TV Shows folder to recursively report 54 bytes total size, got %d", tvEntry.TotalSize)
		}
	}

	// Test browsing into TV Shows
	result, err = browser.Browse(ctx, tvDir)
	if err != nil {
		t.Fatalf("Browse TV Shows failed: %v", err)
	}

	if result.Parent != tmpDir {
		t.Errorf("expected parent %s, got %s", tmpDir, result.Parent)
	}

	// First browse triggers background count for "Test Show"
	t.Logf("TV Shows browse: %d entries", len(result.Entries))
	time.Sleep(200 * time.Millisecond)

	// Second browse returns cached counts
	result, err = browser.Browse(ctx, tvDir)
	if err != nil {
		t.Fatalf("Browse TV Shows (cached) failed: %v", err)
	}

	if len(result.Entries) == 1 {
		showEntry := result.Entries[0]
		if showEntry.FileCount != 3 {
			t.Errorf("expected Test Show folder to recursively report 3 videos, got %d", showEntry.FileCount)
		}
	}

	t.Logf("TV Shows browse (cached): %d entries", len(result.Entries))

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

func TestGetVideoFilesWithProgress(t *testing.T) {
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

	// Get video files from testdata with progress callback
	var progressCalls int
	results, err := browser.GetVideoFilesWithProgress(ctx, []string{testDataDir}, func(probed, total int) {
		progressCalls++
		t.Logf("Progress: %d/%d", probed, total)
	})
	if err != nil {
		t.Fatalf("GetVideoFilesWithProgress failed: %v", err)
	}

	if len(results) == 0 {
		t.Error("expected at least one video file")
	}

	if progressCalls == 0 {
		t.Error("expected progress callback to be called")
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
	results1, _ := browser.GetVideoFilesWithProgress(ctx, []string{absPath}, nil)
	firstDuration := time.Since(start)

	// Second probe - should be cached and fast
	start = time.Now()
	results2, _ := browser.GetVideoFilesWithProgress(ctx, []string{absPath}, nil)
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
	browser.GetVideoFilesWithProgress(ctx, []string{absPath}, nil)
	thirdDuration := time.Since(start)

	t.Logf("Third probe (after cache clear): %v", thirdDuration)
}

func TestGetVideoFilesWithProgress_PreservesPathOrder(t *testing.T) {
	// Regression test for issue #86: jobs should be ordered by submission order, not alphabetically.
	// We submit two directories in reverse-alphabetical order and verify the results
	// come back with all files from the first directory before files from the second.
	dir1 := filepath.Join("..", "..", "testdata", "vmaf_samples_backup") // 'v' > 'v' but _backup comes after vmaf_samples alphabetically
	dir2 := filepath.Join("..", "..", "testdata", "vmaf_samples")

	absDir1, err := filepath.Abs(dir1)
	if err != nil {
		t.Fatalf("failed to get abs path: %v", err)
	}
	absDir2, err := filepath.Abs(dir2)
	if err != nil {
		t.Fatalf("failed to get abs path: %v", err)
	}

	if _, err := os.Stat(absDir1); os.IsNotExist(err) {
		t.Skipf("test directory not found: %s", absDir1)
	}
	if _, err := os.Stat(absDir2); os.IsNotExist(err) {
		t.Skipf("test directory not found: %s", absDir2)
	}

	// Use a parent that covers both directories
	parentDir := filepath.Dir(absDir1)
	prober := ffmpeg.NewProber("ffprobe")
	browser := NewBrowser(prober, parentDir)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Submit vmaf_samples_backup FIRST, then vmaf_samples
	// If order is preserved, all _backup files should appear before vmaf_samples files
	results, err := browser.GetVideoFilesWithProgress(ctx, []string{absDir1, absDir2}, nil)
	if err != nil {
		t.Fatalf("GetVideoFilesWithProgress failed: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("expected video files but got none")
	}

	// Verify files from both directories are present and in the correct order
	seenFirstDir := false
	seenSecondDir := false
	for i, r := range results {
		inDir1 := strings.HasPrefix(r.Path, absDir1+string(filepath.Separator))
		inDir2 := strings.HasPrefix(r.Path, absDir2+string(filepath.Separator))

		if inDir1 {
			seenFirstDir = true
		}
		if inDir2 {
			seenSecondDir = true
		}

		// Once we've seen a file from dir2, we should NOT see any more from dir1
		if seenSecondDir && inDir1 {
			t.Errorf("order violation at index %d: found dir1 file %q after dir2 files started", i, r.Path)
		}

		t.Logf("  [%d] %s (dir1=%v dir2=%v)", i, r.Path, inDir1, inDir2)
	}

	if !seenFirstDir {
		t.Error("expected files from first directory (vmaf_samples_backup)")
	}
	if !seenSecondDir {
		t.Error("expected files from second directory (vmaf_samples)")
	}
}

package browse

import (
	"context"
	"os"
	"path/filepath"
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
	result, err := browser.Browse(ctx, tmpDir, false)
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
	result, err = browser.Browse(ctx, tvDir, false)
	if err != nil {
		t.Fatalf("Browse TV Shows failed: %v", err)
	}

	if result.Parent != tmpDir {
		t.Errorf("expected parent %s, got %s", tmpDir, result.Parent)
	}

	t.Logf("TV Shows browse: %d entries", len(result.Entries))

	// Test browsing into Season 1
	result, err = browser.Browse(ctx, seasonDir, false)
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
	result, err := browser.Browse(ctx, "/etc", false)
	if err != nil {
		t.Fatalf("Browse failed: %v", err)
	}

	// Should redirect to media root
	if result.Path != tmpDir {
		t.Errorf("expected path to be redirected to %s, got %s", tmpDir, result.Path)
	}

	// Try path traversal
	result, err = browser.Browse(ctx, filepath.Join(tmpDir, "..", ".."), false)
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

func TestCountVideosCaches(t *testing.T) {
	tmpDir := t.TempDir()

	// Create first video file
	video1 := filepath.Join(tmpDir, "one.mkv")
	if err := os.WriteFile(video1, []byte("1234"), 0644); err != nil {
		t.Fatalf("failed to write video1: %v", err)
	}

	browser := NewBrowser(ffmpeg.NewProber("ffprobe"), tmpDir)

	count1, size1 := browser.countVideos(tmpDir, false)
	if count1 != 1 {
		t.Fatalf("expected count1=1, got %d", count1)
	}
	if size1 != int64(len("1234")) {
		t.Fatalf("expected size1=%d, got %d", len("1234"), size1)
	}

	// Add another video file after cache is populated
	video2 := filepath.Join(tmpDir, "two.mp4")
	if err := os.WriteFile(video2, []byte("12345678"), 0644); err != nil {
		t.Fatalf("failed to write video2: %v", err)
	}

	// Should still return cached values (no change)
	count2, size2 := browser.countVideos(tmpDir, false)
	if count2 != 1 || size2 != size1 {
		t.Fatalf("expected cached values (count=1 size=%d), got count=%d size=%d", size1, count2, size2)
	}

	// Clear cache, then it should pick up the new file
	browser.ClearCache()
	count3, size3 := browser.countVideos(tmpDir, false)
	if count3 != 2 {
		t.Fatalf("expected count3=2, got %d", count3)
	}
	expectedSize := int64(len("1234") + len("12345678"))
	if size3 != expectedSize {
		t.Fatalf("expected size3=%d, got %d", expectedSize, size3)
	}
}

func TestCountVideosRecursive(t *testing.T) {
	tmpDir := t.TempDir()

	root := filepath.Join(tmpDir, "Root")
	sub := filepath.Join(root, "Sub")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(sub, "a.mkv"), []byte("a"), 0644); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b.mp4"), []byte("bb"), 0644); err != nil {
		t.Fatalf("write b: %v", err)
	}

	browser := NewBrowser(ffmpeg.NewProber("ffprobe"), tmpDir)

	countNo, sizeNo := browser.countVideos(root, false)
	if countNo != 0 || sizeNo != 0 {
		t.Fatalf("non-recursive should be 0/0, got %d/%d", countNo, sizeNo)
	}

	countRec, sizeRec := browser.countVideos(root, true)
	if countRec != 2 {
		t.Fatalf("recursive should be 2, got %d", countRec)
	}
	expectedSize := int64(len("a") + len("bb"))
	if sizeRec != expectedSize {
		t.Fatalf("expected size %d, got %d", expectedSize, sizeRec)
	}
}

func TestCountVideosTTLStale(t *testing.T) {
	tmpDir := t.TempDir()

	file1 := filepath.Join(tmpDir, "one.mkv")
	if err := os.WriteFile(file1, []byte("1234"), 0644); err != nil {
		t.Fatalf("write file1: %v", err)
	}

	browser := NewBrowser(ffmpeg.NewProber("ffprobe"), tmpDir)
	browser.SetDirInfoTTLMinutes(1)

	count1, size1 := browser.countVideos(tmpDir, false)
	if count1 != 1 {
		t.Fatalf("expected count1=1, got %d", count1)
	}
	if size1 != int64(len("1234")) {
		t.Fatalf("expected size1=%d, got %d", len("1234"), size1)
	}

	// Add a new file after cache is populated
	file2 := filepath.Join(tmpDir, "two.mp4")
	if err := os.WriteFile(file2, []byte("12345678"), 0644); err != nil {
		t.Fatalf("write file2: %v", err)
	}

	// Force cache entry to look stale
	key := dirInfoKey{tmpDir, false}
	browser.dirInfoCacheMu.Lock()
	info := browser.dirInfoCache[key]
	info.computedAt = time.Now().Add(-2 * time.Minute)
	browser.dirInfoCache[key] = info
	browser.dirInfoCacheMu.Unlock()

	count2, size2 := browser.countVideos(tmpDir, false)
	if count2 != 2 {
		t.Fatalf("expected count2=2, got %d", count2)
	}
	expectedSize := int64(len("1234") + len("12345678"))
	if size2 != expectedSize {
		t.Fatalf("expected size2=%d, got %d", expectedSize, size2)
	}
}

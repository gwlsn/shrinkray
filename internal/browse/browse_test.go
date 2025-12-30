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

// TestDiscoverMediaFiles tests the recursive file discovery with depth control
func TestDiscoverMediaFiles(t *testing.T) {
	// Create a test directory structure:
	// root/
	//   a.mp4
	//   b.txt
	//   sub1/
	//     c.mkv
	//     sub2/
	//       d.mp4
	//   .hidden/
	//     e.mp4

	tmpDir := t.TempDir()

	// Create directories
	sub1 := filepath.Join(tmpDir, "sub1")
	sub2 := filepath.Join(sub1, "sub2")
	hidden := filepath.Join(tmpDir, ".hidden")

	for _, dir := range []string{sub1, sub2, hidden} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("failed to create dir %s: %v", dir, err)
		}
	}

	// Create test files
	files := map[string]string{
		filepath.Join(tmpDir, "a.mp4"):   "video",
		filepath.Join(tmpDir, "b.txt"):   "text",
		filepath.Join(sub1, "c.mkv"):     "video",
		filepath.Join(sub2, "d.mp4"):     "video",
		filepath.Join(hidden, "e.mp4"):   "video in hidden dir",
	}

	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("failed to create file %s: %v", path, err)
		}
	}

	// Test cases
	tests := []struct {
		name      string
		recursive bool
		maxDepth  *int
		want      []string // expected file names (not full paths)
	}{
		{
			name:      "recursion off - only root files",
			recursive: false,
			maxDepth:  nil,
			want:      []string{"a.mp4"},
		},
		{
			name:      "recursion on - unlimited depth",
			recursive: true,
			maxDepth:  nil,
			want:      []string{"a.mp4", "c.mkv", "d.mp4"},
		},
		{
			name:      "recursion on - maxDepth 0 (same as recursion off)",
			recursive: true,
			maxDepth:  intPtr(0),
			want:      []string{"a.mp4"},
		},
		{
			name:      "recursion on - maxDepth 1 (root + one level)",
			recursive: true,
			maxDepth:  intPtr(1),
			want:      []string{"a.mp4", "c.mkv"},
		},
		{
			name:      "recursion on - maxDepth 2 (all levels)",
			recursive: true,
			maxDepth:  intPtr(2),
			want:      []string{"a.mp4", "c.mkv", "d.mp4"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			paths, err := DiscoverMediaFiles(tmpDir, tt.recursive, tt.maxDepth)
			if err != nil {
				t.Fatalf("DiscoverMediaFiles failed: %v", err)
			}

			// Extract just file names for comparison
			var gotNames []string
			for _, p := range paths {
				gotNames = append(gotNames, filepath.Base(p))
			}

			if len(gotNames) != len(tt.want) {
				t.Errorf("got %d files %v, want %d files %v", len(gotNames), gotNames, len(tt.want), tt.want)
				return
			}

			for i, name := range tt.want {
				if gotNames[i] != name {
					t.Errorf("at index %d: got %s, want %s", i, gotNames[i], name)
				}
			}
		})
	}
}

// TestDiscoverMediaFilesStableSort tests that results are sorted consistently
func TestDiscoverMediaFilesStableSort(t *testing.T) {
	tmpDir := t.TempDir()

	// Create files with names that would sort differently lexically
	fileNames := []string{"z.mp4", "a.mp4", "m.mp4", "b.mkv"}
	for _, name := range fileNames {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte("video"), 0644); err != nil {
			t.Fatalf("failed to create file: %v", err)
		}
	}

	// Run multiple times to verify consistent ordering
	for i := 0; i < 3; i++ {
		paths, err := DiscoverMediaFiles(tmpDir, false, nil)
		if err != nil {
			t.Fatalf("DiscoverMediaFiles failed: %v", err)
		}

		expected := []string{"a.mp4", "b.mkv", "m.mp4", "z.mp4"}
		for j, p := range paths {
			if filepath.Base(p) != expected[j] {
				t.Errorf("run %d: at index %d got %s, want %s", i, j, filepath.Base(p), expected[j])
			}
		}
	}
}

// TestDiscoverMediaFilesNonexistent tests error handling for nonexistent directories
func TestDiscoverMediaFilesNonexistent(t *testing.T) {
	_, err := DiscoverMediaFiles("/nonexistent/path/that/does/not/exist", true, nil)
	if err == nil {
		t.Error("expected error for nonexistent directory, got nil")
	}
}

// TestDiscoverMediaFilesHiddenFiles tests that hidden files are skipped
func TestDiscoverMediaFilesHiddenFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create normal and hidden files
	files := []string{
		filepath.Join(tmpDir, "normal.mp4"),
		filepath.Join(tmpDir, ".hidden.mp4"),
	}

	for _, f := range files {
		if err := os.WriteFile(f, []byte("video"), 0644); err != nil {
			t.Fatalf("failed to create file: %v", err)
		}
	}

	paths, err := DiscoverMediaFiles(tmpDir, false, nil)
	if err != nil {
		t.Fatalf("DiscoverMediaFiles failed: %v", err)
	}

	if len(paths) != 1 {
		t.Errorf("expected 1 file, got %d", len(paths))
	}

	if len(paths) > 0 && filepath.Base(paths[0]) != "normal.mp4" {
		t.Errorf("expected normal.mp4, got %s", filepath.Base(paths[0]))
	}
}

// TestGetVideoFilesWithOptions tests the full flow with options
func TestGetVideoFilesWithOptions(t *testing.T) {
	// Create a test directory structure
	tmpDir := t.TempDir()

	sub1 := filepath.Join(tmpDir, "sub1")
	if err := os.MkdirAll(sub1, 0755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}

	// Create video files
	rootVideo := filepath.Join(tmpDir, "root.mp4")
	subVideo := filepath.Join(sub1, "sub.mkv")

	for _, f := range []string{rootVideo, subVideo} {
		if err := os.WriteFile(f, []byte("fake video"), 0644); err != nil {
			t.Fatalf("failed to create file: %v", err)
		}
	}

	prober := ffmpeg.NewProber("ffprobe")
	browser := NewBrowser(prober, tmpDir)

	ctx := context.Background()

	// Test with recursion off - should only find root video
	opts := GetVideoFilesOptions{Recursive: false}
	results, err := browser.GetVideoFilesWithOptions(ctx, []string{tmpDir}, opts)
	if err != nil {
		t.Fatalf("GetVideoFilesWithOptions failed: %v", err)
	}

	// Note: We can't test exact count here since ffprobe may fail on fake files,
	// but the discovery logic is tested in TestDiscoverMediaFiles
	t.Logf("Found %d video files with recursion off", len(results))

	// Test with recursion on
	opts = GetVideoFilesOptions{Recursive: true}
	results, err = browser.GetVideoFilesWithOptions(ctx, []string{tmpDir}, opts)
	if err != nil {
		t.Fatalf("GetVideoFilesWithOptions failed: %v", err)
	}
	t.Logf("Found %d video files with recursion on", len(results))
}

// intPtr is a helper to create an int pointer for tests
func intPtr(i int) *int {
	return &i
}

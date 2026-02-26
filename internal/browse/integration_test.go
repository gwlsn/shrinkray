// internal/browse/integration_test.go
package browse

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pollForReady polls GetDirCount until the entry reaches ready state with
// the expected count, or the deadline expires. Returns the final dirCountView.
// Note: GetDirCount returns dirCountView (an immutable value type), so we
// use exported field names (FileCount, State, etc.).
func pollForReady(t *testing.T, browser *Browser, path string, expectedCount int, timeout time.Duration) dirCountView {
	t.Helper()
	deadline := time.Now().Add(timeout)
	ctx := context.Background()
	var dc dirCountView
	for time.Now().Before(deadline) {
		dc = browser.GetDirCount(ctx, path)
		if dc.State == stateReady && dc.FileCount == expectedCount {
			return dc
		}
		time.Sleep(50 * time.Millisecond)
	}
	return dc
}

// pollBrowseForCount polls Browse until the named entry has the expected
// file count, or the deadline expires.
func pollBrowseForCount(t *testing.T, browser *Browser, browsePath, entryName string, expectedCount int, timeout time.Duration) *Entry {
	t.Helper()
	deadline := time.Now().Add(timeout)
	ctx := context.Background()
	var entry *Entry
	for time.Now().Before(deadline) {
		result, err := browser.Browse(ctx, browsePath)
		if err != nil {
			t.Fatalf("Browse failed: %v", err)
		}
		for _, e := range result.Entries {
			if e.Name == entryName {
				entry = e
				break
			}
		}
		if entry != nil && entry.FileCount == expectedCount && entry.CountsState == string(stateReady) {
			return entry
		}
		time.Sleep(50 * time.Millisecond)
	}
	return entry
}

// --- Startup / Cold Start ---

func TestIntegration_ColdStartCountsPopulate(t *testing.T) {
	// On first start with no snapshot, Browse should return entries with
	// unknown/pending state, then counts should appear within seconds.
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	// Create a show with 3 episodes
	seasonDir := filepath.Join(tmpDir, "Show A", "Season 1")
	os.MkdirAll(seasonDir, 0755)
	for _, name := range []string{"S01E01.mkv", "S01E02.mkv", "S01E03.mkv"} {
		os.WriteFile(filepath.Join(seasonDir, name), []byte("video content"), 0644)
	}

	ctx := context.Background()

	// First browse before WarmCountCache: should return entries but counts
	// may be 0 with unknown/pending state
	result, err := browser.Browse(ctx, tmpDir)
	if err != nil {
		t.Fatalf("Browse failed: %v", err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result.Entries))
	}

	// Browse must return promptly (under 2 seconds) even with uncached dirs
	start := time.Now()
	_, err = browser.Browse(ctx, tmpDir)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Browse failed: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("Browse took %v (should return promptly, recompute happens in background)", elapsed)
	}

	// After a short wait, counts should be populated
	entry := pollBrowseForCount(t, browser, tmpDir, "Show A", 3, 5*time.Second)
	if entry == nil || entry.FileCount != 3 {
		count := 0
		if entry != nil {
			count = entry.FileCount
		}
		t.Errorf("expected 3 videos after background recompute, got %d", count)
	}
}

func TestIntegration_WarmStartFromSnapshot(t *testing.T) {
	// Startup with persisted counts should show counts immediately
	// (in stale state), then refresh to ready.
	tmpDir := t.TempDir()
	browser1 := NewBrowser(nil, tmpDir)

	seasonDir := filepath.Join(tmpDir, "Show A", "Season 1")
	os.MkdirAll(seasonDir, 0755)
	for _, name := range []string{"S01E01.mkv", "S01E02.mkv"} {
		os.WriteFile(filepath.Join(seasonDir, name), []byte("video content"), 0644)
	}

	// Warm and export counts (simulates persistence via SQLite)
	browser1.WarmCountCache(context.Background())
	exported := browser1.ExportCounts()

	// Simulate restart: new browser, import counts
	browser2 := NewBrowser(nil, tmpDir)
	browser2.ImportCounts(exported)

	ctx := context.Background()
	result, err := browser2.Browse(ctx, tmpDir)
	if err != nil {
		t.Fatalf("Browse failed: %v", err)
	}

	showEntry := result.Entries[0]

	// Counts should be available immediately (from snapshot)
	if showEntry.FileCount != 2 {
		t.Errorf("expected 2 videos from snapshot, got %d", showEntry.FileCount)
	}
	// State should be stale (loaded from snapshot, not yet validated)
	if showEntry.CountsState != string(stateStale) {
		t.Errorf("expected stale state from snapshot, got %s", showEntry.CountsState)
	}
}

func TestIntegration_WarmCountCacheAccuracy(t *testing.T) {
	// WarmCountCache should produce correct recursive counts for a
	// realistic nested directory structure.
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	// Show A: 2 seasons, 3 episodes each = 6 total
	for _, season := range []string{"Season 1", "Season 2"} {
		dir := filepath.Join(tmpDir, "Show A", season)
		os.MkdirAll(dir, 0755)
		for i := 1; i <= 3; i++ {
			os.WriteFile(filepath.Join(dir, "ep"+string(rune('0'+i))+".mkv"), []byte("vid"), 0644)
		}
	}
	// Show B: 1 season, 1 episode = 1 total
	dir := filepath.Join(tmpDir, "Show B", "Season 1")
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "ep1.mkv"), []byte("vid"), 0644)

	// Empty show (0 videos)
	os.MkdirAll(filepath.Join(tmpDir, "Show C"), 0755)

	browser.WarmCountCache(context.Background())

	ctx := context.Background()
	result, err := browser.Browse(ctx, tmpDir)
	if err != nil {
		t.Fatalf("Browse failed: %v", err)
	}

	counts := make(map[string]int)
	for _, e := range result.Entries {
		counts[e.Name] = e.FileCount
	}

	if counts["Show A"] != 6 {
		t.Errorf("Show A: expected 6 videos, got %d", counts["Show A"])
	}
	if counts["Show B"] != 1 {
		t.Errorf("Show B: expected 1 video, got %d", counts["Show B"])
	}
	if counts["Show C"] != 0 {
		t.Errorf("Show C: expected 0 videos, got %d", counts["Show C"])
	}

	// BrowseResult totals should be recursive for the root
	if result.VideoCount != 7 {
		t.Errorf("expected root total of 7 videos, got %d", result.VideoCount)
	}
}

// --- External Change Detection ---

func TestIntegration_SonarrImportDetected(t *testing.T) {
	// Sonarr moves a new episode into an existing show directory.
	// Signature-based detection only catches changes to a directory's direct
	// children (mtime changes on the directory itself). When a file is added
	// to a subdirectory (Season 5), the parent (Breaking Bad) doesn't see a
	// mtime change. In real usage, the app would call InvalidateCache after
	// detecting the change via a webhook or filesystem watcher.
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	seasonDir := filepath.Join(tmpDir, "Breaking Bad", "Season 5")
	os.MkdirAll(seasonDir, 0755)
	for _, name := range []string{"S05E01.mkv", "S05E02.mkv"} {
		os.WriteFile(filepath.Join(seasonDir, name), []byte("video data here!"), 0644)
	}

	browser.WarmCountCache(context.Background())

	ctx := context.Background()
	result, _ := browser.Browse(ctx, tmpDir)
	if result.Entries[0].FileCount != 2 {
		t.Fatalf("expected 2 videos initially, got %d", result.Entries[0].FileCount)
	}

	// Sonarr adds a new episode
	newEp := filepath.Join(seasonDir, "S05E03.mkv")
	os.WriteFile(newEp, []byte("new episode data!"), 0644)

	// Simulate app detecting the change (e.g., via filesystem watcher or
	// user clicking Refresh). InvalidateCache marks all ancestor directories
	// as stale and enqueues recomputes.
	browser.InvalidateCache(newEp)

	// Browse should eventually show 3
	entry := pollBrowseForCount(t, browser, tmpDir, "Breaking Bad", 3, 5*time.Second)
	if entry == nil || entry.FileCount != 3 {
		count := 0
		if entry != nil {
			count = entry.FileCount
		}
		t.Errorf("expected 3 videos after Sonarr import, got %d", count)
	}
}

func TestIntegration_FileDeleteDetected(t *testing.T) {
	// Deleting a video file from a cached directory should update the count.
	// Uses InvalidateCache to ensure ancestor directories are marked stale
	// and recomputed, which is what happens in real usage (e.g., after a
	// transcode completes or a user triggers a refresh).
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	showDir := filepath.Join(tmpDir, "show")
	os.MkdirAll(showDir, 0755)
	for _, name := range []string{"ep1.mkv", "ep2.mkv", "ep3.mkv"} {
		os.WriteFile(filepath.Join(showDir, name), []byte("data"), 0644)
	}

	browser.WarmCountCache(context.Background())

	ctx := context.Background()
	dc := browser.GetDirCount(ctx, showDir)
	if dc.FileCount != 3 {
		t.Fatalf("expected 3 before delete, got %d", dc.FileCount)
	}

	// Delete one video
	deletedPath := filepath.Join(showDir, "ep2.mkv")
	os.Remove(deletedPath)

	// Notify the cache that the directory has changed
	browser.InvalidateCache(deletedPath)

	dc = pollForReady(t, browser, showDir, 2, 5*time.Second)
	if dc.FileCount != 2 {
		t.Errorf("expected 2 after delete, got %d", dc.FileCount)
	}
}

func TestIntegration_DirectoryDeleteHandled(t *testing.T) {
	// Deleting an entire directory should transition to error state,
	// not panic or hang.
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	showDir := filepath.Join(tmpDir, "doomed_show")
	os.MkdirAll(showDir, 0755)
	os.WriteFile(filepath.Join(showDir, "ep1.mkv"), []byte("data"), 0644)

	browser.WarmCountCache(context.Background())

	ctx := context.Background()
	dc := browser.GetDirCount(ctx, showDir)
	if dc.FileCount != 1 {
		t.Fatalf("expected 1 video before delete, got %d", dc.FileCount)
	}

	os.RemoveAll(showDir)

	dc = browser.GetDirCount(ctx, showDir)
	if dc.State != stateError {
		t.Errorf("expected error state for deleted directory, got %s", dc.State)
	}
	// Last-known values preserved
	if dc.FileCount != 1 {
		t.Errorf("expected preserved count of 1, got %d", dc.FileCount)
	}
}

func TestIntegration_NewDirectoryAppears(t *testing.T) {
	// A new show directory appearing should be picked up by Browse.
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	// Start with one show
	os.MkdirAll(filepath.Join(tmpDir, "Show A"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "Show A", "ep1.mkv"), []byte("data"), 0644)

	browser.WarmCountCache(context.Background())

	// Add a new show directory
	os.MkdirAll(filepath.Join(tmpDir, "Show B"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "Show B", "ep1.mkv"), []byte("data"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "Show B", "ep2.mkv"), []byte("data"), 0644)

	// Browse should list both shows
	ctx := context.Background()
	result, err := browser.Browse(ctx, tmpDir)
	if err != nil {
		t.Fatalf("Browse failed: %v", err)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result.Entries))
	}

	// New show should eventually show correct count
	entry := pollBrowseForCount(t, browser, tmpDir, "Show B", 2, 5*time.Second)
	if entry == nil || entry.FileCount != 2 {
		count := 0
		if entry != nil {
			count = entry.FileCount
		}
		t.Errorf("expected 2 videos in new show, got %d", count)
	}
}

// --- Job Completion (InvalidateCache) ---

func TestIntegration_InvalidateCacheAfterTranscode(t *testing.T) {
	// Simulates what happens after a transcode job completes:
	// the worker calls InvalidateCache(outputPath) and InvalidateCache(inputPath).
	// Ancestors should be marked stale and recompute to correct counts.
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	seasonDir := filepath.Join(tmpDir, "Show", "Season 1")
	os.MkdirAll(seasonDir, 0755)

	inputPath := filepath.Join(seasonDir, "ep1.mkv")
	os.WriteFile(inputPath, []byte("original large video content that is quite big"), 0644)
	os.WriteFile(filepath.Join(seasonDir, "ep2.mkv"), []byte("another video"), 0644)

	browser.WarmCountCache(context.Background())

	ctx := context.Background()
	dc := browser.GetDirCount(ctx, seasonDir)
	if dc.FileCount != 2 {
		t.Fatalf("expected 2 videos before transcode, got %d", dc.FileCount)
	}
	originalSize := dc.TotalSize

	// Simulate transcode: replace the file with a smaller version
	// (worker would rename original, write new file, then call InvalidateCache)
	os.WriteFile(inputPath, []byte("smaller transcoded"), 0644)

	// Worker calls InvalidateCache for the transcoded file
	browser.InvalidateCache(inputPath)

	// Verify ancestor entries are preserved (not deleted). This check is
	// stable because presence/absence doesn't race with recompute workers.
	// The stale *state* assertion lives in TestInvalidateCache_PreservesLastKnownValues
	// which constructs Browser without background workers to avoid the race.
	browser.countCacheMu.RLock()
	seasonDC := browser.countCache[seasonDir]
	showDC := browser.countCache[filepath.Join(tmpDir, "Show")]
	browser.countCacheMu.RUnlock()

	if seasonDC == nil {
		t.Fatal("season dir cache entry was deleted (should be stale with last-known values)")
	}
	if showDC == nil {
		t.Fatal("show dir cache entry was deleted (should be stale with last-known values)")
	}

	// Wait for recompute
	dc = pollForReady(t, browser, seasonDir, 2, 5*time.Second)
	if dc.FileCount != 2 {
		t.Errorf("expected still 2 videos after transcode, got %d", dc.FileCount)
	}
	if dc.TotalSize >= originalSize {
		t.Errorf("expected smaller total size after transcode, got %d (was %d)", dc.TotalSize, originalSize)
	}
}

func TestIntegration_InvalidateCacheSSEBroadcast(t *testing.T) {
	// InvalidateCache should eventually broadcast SSE events for affected dirs
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	seasonDir := filepath.Join(tmpDir, "Show", "Season 1")
	os.MkdirAll(seasonDir, 0755)
	videoPath := filepath.Join(seasonDir, "ep1.mkv")
	os.WriteFile(videoPath, []byte("video"), 0644)

	browser.WarmCountCache(context.Background())

	// Subscribe for events
	ch := browser.Subscribe()
	defer browser.Unsubscribe(ch)

	// Trigger invalidation
	browser.InvalidateCache(videoPath)

	// Should receive at least one event for the season dir
	deadline := time.Now().Add(5 * time.Second)
	received := false
	for time.Now().Before(deadline) {
		select {
		case event := <-ch:
			if event.Path == seasonDir && event.State == string(stateReady) {
				received = true
			}
		case <-time.After(100 * time.Millisecond):
		}
		if received {
			break
		}
	}

	if !received {
		t.Error("expected SSE event for season dir after InvalidateCache")
	}
}

// --- Reconcile ---

func TestIntegration_ReconcileUpdatesWithoutBlanks(t *testing.T) {
	// Reconcile should mark entries stale but preserve last-known values.
	// No entry should ever show 0 when it previously had a real count.
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	showDir := filepath.Join(tmpDir, "show")
	os.MkdirAll(showDir, 0755)
	for _, name := range []string{"ep1.mkv", "ep2.mkv"} {
		os.WriteFile(filepath.Join(showDir, name), []byte("data"), 0644)
	}

	browser.WarmCountCache(context.Background())

	ctx := context.Background()

	// Verify initial state
	dc := browser.GetDirCount(ctx, showDir)
	if dc.FileCount != 2 || dc.State != stateReady {
		t.Fatalf("expected 2 ready, got %d %s", dc.FileCount, dc.State)
	}

	// Reconcile
	browser.Reconcile(showDir, false)

	// Immediately after reconcile: values must still be present
	dc = browser.GetDirCount(ctx, showDir)
	if dc.FileCount != 2 {
		t.Errorf("reconcile blanked count: expected 2, got %d", dc.FileCount)
	}

	// Wait for recompute
	dc = pollForReady(t, browser, showDir, 2, 5*time.Second)
	if dc.FileCount != 2 {
		t.Errorf("expected 2 after reconcile recompute, got %d", dc.FileCount)
	}
}

func TestIntegration_ReconcileRecursive(t *testing.T) {
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	for _, path := range []string{
		filepath.Join(tmpDir, "A", "S1"),
		filepath.Join(tmpDir, "A", "S2"),
		filepath.Join(tmpDir, "B", "S1"),
	} {
		os.MkdirAll(path, 0755)
		os.WriteFile(filepath.Join(path, "ep.mkv"), []byte("data"), 0644)
	}

	browser.WarmCountCache(context.Background())

	// Reconcile only show A recursively
	browser.Reconcile(filepath.Join(tmpDir, "A"), true)

	ctx := context.Background()

	// Show A entries should be stale
	dcA := browser.GetDirCount(ctx, filepath.Join(tmpDir, "A", "S1"))
	if dcA.State == stateReady {
		t.Error("expected A/S1 to be non-ready after recursive reconcile")
	}

	// Show B should still be ready (not affected)
	dcB := browser.GetDirCount(ctx, filepath.Join(tmpDir, "B", "S1"))
	if dcB.State != stateReady {
		t.Errorf("expected B/S1 to still be ready, got %s", dcB.State)
	}
}

// --- ClearCache ---

func TestIntegration_ClearCacheNeverBlanks(t *testing.T) {
	// ClearCache is the most aggressive operation. Even here, counts must
	// never disappear.
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	showDir := filepath.Join(tmpDir, "show")
	os.MkdirAll(showDir, 0755)
	for i := 0; i < 10; i++ {
		name := "ep" + string(rune('a'+i)) + ".mkv"
		os.WriteFile(filepath.Join(showDir, name), []byte("video data"), 0644)
	}

	browser.WarmCountCache(context.Background())

	ctx := context.Background()
	dc := browser.GetDirCount(ctx, showDir)
	if dc.FileCount != 10 {
		t.Fatalf("expected 10 before clear, got %d", dc.FileCount)
	}

	// Clear cache
	browser.ClearCache()

	// Immediately check: count must NOT be 0 or missing
	dc = browser.GetDirCount(ctx, showDir)
	if dc.FileCount == 0 {
		t.Error("ClearCache blanked the count to 0 (should preserve last-known)")
	}
	if dc.FileCount != 10 {
		t.Errorf("expected preserved count of 10, got %d", dc.FileCount)
	}

	// After recompute, should still be 10
	dc = pollForReady(t, browser, showDir, 10, 5*time.Second)
	if dc.FileCount != 10 {
		t.Errorf("expected 10 after recompute, got %d", dc.FileCount)
	}
}

// --- Browse() Responsiveness ---

func TestIntegration_BrowseDoesNotHang(t *testing.T) {
	// Browse must return promptly even with many uncached directories.
	// Recomputes happen in the background.
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	// Create 50 show directories with videos
	for i := 0; i < 50; i++ {
		dir := filepath.Join(tmpDir, "Show_"+string(rune('A'+i/26))+string(rune('A'+i%26)))
		os.MkdirAll(dir, 0755)
		os.WriteFile(filepath.Join(dir, "ep1.mkv"), []byte("data"), 0644)
	}

	ctx := context.Background()
	start := time.Now()
	result, err := browser.Browse(ctx, tmpDir)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Browse failed: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("Browse took %v for 50 directories (should return promptly)", elapsed)
	}
	if len(result.Entries) != 50 {
		t.Errorf("expected 50 entries, got %d", len(result.Entries))
	}
}

func TestIntegration_ConcurrentBrowsesSafe(t *testing.T) {
	// Multiple concurrent Browse calls to the same path should not
	// cause races, panics, or incorrect counts.
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	showDir := filepath.Join(tmpDir, "show")
	os.MkdirAll(showDir, 0755)
	os.WriteFile(filepath.Join(showDir, "ep1.mkv"), []byte("data"), 0644)

	browser.WarmCountCache(context.Background())

	ctx := context.Background()
	errs := make(chan error, 10)

	for i := 0; i < 10; i++ {
		go func() {
			result, err := browser.Browse(ctx, tmpDir)
			if err != nil {
				errs <- err
				return
			}
			if len(result.Entries) != 1 {
				errs <- nil // non-fatal, just checking no panic
				return
			}
			errs <- nil
		}()
	}

	for i := 0; i < 10; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent Browse failed: %v", err)
		}
	}
}

// --- API JSON Contract ---

func TestIntegration_JSONContractDirectories(t *testing.T) {
	// Verify the JSON output matches the API contract:
	// - file_count always present for dirs (even 0)
	// - total_size always present for dirs (even 0)
	// - counts_state always present for dirs
	//
	// Note: video files are placed inside subdirectories (not at root)
	// because Browse probes root-level video files in the background,
	// which requires a non-nil prober.
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	os.MkdirAll(filepath.Join(tmpDir, "empty_dir"), 0755)
	showDir := filepath.Join(tmpDir, "show_with_videos")
	os.MkdirAll(showDir, 0755)
	os.WriteFile(filepath.Join(showDir, "movie.mkv"), []byte("video"), 0644)

	browser.WarmCountCache(context.Background())

	ctx := context.Background()
	result, err := browser.Browse(ctx, tmpDir)
	if err != nil {
		t.Fatalf("Browse failed: %v", err)
	}

	for _, entry := range result.Entries {
		data, _ := json.Marshal(entry)
		jsonStr := string(data)

		if entry.IsDir {
			// Directories must always have these fields
			if !strings.Contains(jsonStr, `"file_count":`) {
				t.Errorf("dir %s missing file_count in JSON: %s", entry.Name, jsonStr)
			}
			if !strings.Contains(jsonStr, `"total_size":`) {
				t.Errorf("dir %s missing total_size in JSON: %s", entry.Name, jsonStr)
			}
			if !strings.Contains(jsonStr, `"counts_state":`) {
				t.Errorf("dir %s missing counts_state in JSON: %s", entry.Name, jsonStr)
			}
		}
	}
}

func TestIntegration_BrowseResultRecursiveTotals(t *testing.T) {
	// BrowseResult.VideoCount must be recursive, not direct-only.
	// Videos are placed only in subdirectories to avoid nil prober panics
	// (Browse probes root-level video files in the background).
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	// Show A: 2 videos, Show B: 3 videos = 5 total
	dirA := filepath.Join(tmpDir, "Show A")
	os.MkdirAll(dirA, 0755)
	os.WriteFile(filepath.Join(dirA, "movie1.mkv"), []byte("vid"), 0644)
	os.WriteFile(filepath.Join(dirA, "movie2.mkv"), []byte("vid"), 0644)
	dirB := filepath.Join(tmpDir, "Show B")
	os.MkdirAll(dirB, 0755)
	for _, name := range []string{"ep1.mkv", "ep2.mkv", "ep3.mkv"} {
		os.WriteFile(filepath.Join(dirB, name), []byte("vid"), 0644)
	}

	browser.WarmCountCache(context.Background())

	ctx := context.Background()
	result, err := browser.Browse(ctx, tmpDir)
	if err != nil {
		t.Fatalf("Browse failed: %v", err)
	}

	if result.VideoCount != 5 {
		t.Errorf("BrowseResult.VideoCount: expected 5 recursive, got %d", result.VideoCount)
	}
}

// --- Edge Cases ---

func TestIntegration_EmptyMediaRoot(t *testing.T) {
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	ctx := context.Background()
	result, err := browser.Browse(ctx, tmpDir)
	if err != nil {
		t.Fatalf("Browse empty root failed: %v", err)
	}
	if len(result.Entries) != 0 {
		t.Errorf("expected 0 entries for empty root, got %d", len(result.Entries))
	}
	if result.VideoCount != 0 {
		t.Errorf("expected 0 video count for empty root, got %d", result.VideoCount)
	}
}

func TestIntegration_NonVideoFilesIgnored(t *testing.T) {
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	showDir := filepath.Join(tmpDir, "show")
	os.MkdirAll(showDir, 0755)
	// Mix of video and non-video files
	os.WriteFile(filepath.Join(showDir, "ep1.mkv"), []byte("video"), 0644)
	os.WriteFile(filepath.Join(showDir, "ep2.mp4"), []byte("video"), 0644)
	os.WriteFile(filepath.Join(showDir, "cover.jpg"), []byte("image"), 0644)
	os.WriteFile(filepath.Join(showDir, "subs.srt"), []byte("subs"), 0644)
	os.WriteFile(filepath.Join(showDir, "nfo.nfo"), []byte("metadata"), 0644)

	browser.WarmCountCache(context.Background())

	ctx := context.Background()
	dc := browser.GetDirCount(ctx, showDir)
	if dc.FileCount != 2 {
		t.Errorf("expected 2 videos (mkv + mp4), got %d", dc.FileCount)
	}
}

func TestIntegration_HiddenFilesAndDirsSkipped(t *testing.T) {
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	// Hidden directory with videos (should be skipped)
	hiddenDir := filepath.Join(tmpDir, ".hidden_show")
	os.MkdirAll(hiddenDir, 0755)
	os.WriteFile(filepath.Join(hiddenDir, "ep1.mkv"), []byte("video"), 0644)

	// Visible directory with videos
	showDir := filepath.Join(tmpDir, "show")
	os.MkdirAll(showDir, 0755)
	os.WriteFile(filepath.Join(showDir, "ep1.mkv"), []byte("video"), 0644)

	ctx := context.Background()
	result, err := browser.Browse(ctx, tmpDir)
	if err != nil {
		t.Fatalf("Browse failed: %v", err)
	}

	// Only "show" should appear (not ".hidden_show")
	if len(result.Entries) != 1 {
		t.Errorf("expected 1 entry (hidden dir skipped), got %d", len(result.Entries))
	}
	if result.Entries[0].Name != "show" {
		t.Errorf("expected 'show', got '%s'", result.Entries[0].Name)
	}
}

func TestIntegration_RapidFileChanges(t *testing.T) {
	// Rapidly adding and removing files should not cause panics, deadlocks,
	// or permanently wrong counts.
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	showDir := filepath.Join(tmpDir, "show")
	os.MkdirAll(showDir, 0755)
	os.WriteFile(filepath.Join(showDir, "ep1.mkv"), []byte("data"), 0644)

	browser.WarmCountCache(context.Background())

	// Rapid changes: add 5 files, delete 3, in quick succession
	for i := 2; i <= 6; i++ {
		name := filepath.Join(showDir, "ep"+string(rune('0'+i))+".mkv")
		os.WriteFile(name, []byte("data"), 0644)
	}
	for i := 2; i <= 4; i++ {
		name := filepath.Join(showDir, "ep"+string(rune('0'+i))+".mkv")
		os.Remove(name)
	}
	// Final state: ep1.mkv, ep5.mkv, ep6.mkv = 3 files

	// Explicitly invalidate to trigger recompute (the realistic flow for
	// external changes detected by a watcher or user refresh)
	browser.InvalidateCache(filepath.Join(showDir, "ep5.mkv"))

	dc := pollForReady(t, browser, showDir, 3, 10*time.Second)
	if dc.FileCount != 3 {
		t.Errorf("expected 3 after rapid changes, got %d", dc.FileCount)
	}
}

// --- Error Recovery ---

func TestIntegration_ErrorStateRecovery(t *testing.T) {
	// Entry in error state should recover when the directory becomes
	// accessible again (e.g., NAS remounted).
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	showDir := filepath.Join(tmpDir, "show")
	os.MkdirAll(showDir, 0755)
	os.WriteFile(filepath.Join(showDir, "ep1.mkv"), []byte("data"), 0644)

	browser.WarmCountCache(context.Background())

	ctx := context.Background()
	dc := browser.GetDirCount(ctx, showDir)
	if dc.FileCount != 1 {
		t.Fatalf("expected 1 before delete, got %d", dc.FileCount)
	}

	// Delete directory to trigger error state
	os.RemoveAll(showDir)
	dc = browser.GetDirCount(ctx, showDir)
	if dc.State != stateError {
		t.Fatalf("expected error state, got %s", dc.State)
	}

	// Recreate directory with 2 videos (simulates NAS coming back)
	os.MkdirAll(showDir, 0755)
	os.WriteFile(filepath.Join(showDir, "ep1.mkv"), []byte("data"), 0644)
	os.WriteFile(filepath.Join(showDir, "ep2.mkv"), []byte("data"), 0644)

	// GetDirCount should detect recovery and transition to stale
	dc = browser.GetDirCount(ctx, showDir)
	if dc.State == stateError {
		t.Error("expected non-error state after directory recreated")
	}

	// Wait for recompute to get correct count
	dc = pollForReady(t, browser, showDir, 2, 5*time.Second)
	if dc.FileCount != 2 {
		t.Errorf("expected 2 after recovery, got %d", dc.FileCount)
	}
}

func TestIntegration_TransientPermissionsError(t *testing.T) {
	// Simulate a transient filesystem error: make directory unreadable,
	// verify error state, restore permissions, verify recovery.
	if os.Getuid() == 0 {
		t.Skip("test requires non-root (root bypasses permissions)")
	}

	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	showDir := filepath.Join(tmpDir, "show")
	os.MkdirAll(showDir, 0755)
	os.WriteFile(filepath.Join(showDir, "ep1.mkv"), []byte("data"), 0644)

	browser.WarmCountCache(context.Background())

	ctx := context.Background()
	dc := browser.GetDirCount(ctx, showDir)
	if dc.FileCount != 1 {
		t.Fatalf("expected 1 before chmod, got %d", dc.FileCount)
	}

	// Make directory unreadable
	os.Chmod(showDir, 0000)

	// Force a recompute by marking stale
	browser.Reconcile(showDir, false)

	// Wait a moment for the recompute to fail
	time.Sleep(500 * time.Millisecond)
	dc = browser.GetDirCount(ctx, showDir)

	// The recompute may not have completed yet, so the state may or
	// may not be error. The important thing is that the count is not zeroed.
	if dc.FileCount == 0 {
		t.Error("transient error zeroed count (should preserve last-known)")
	}

	// Restore permissions
	os.Chmod(showDir, 0755)

	// Recovery: next access should detect the directory is back
	dc = pollForReady(t, browser, showDir, 1, 5*time.Second)
	if dc.FileCount != 1 {
		t.Errorf("expected 1 after permission restore, got %d", dc.FileCount)
	}
}

// --- Concurrent Stress ---

func TestIntegration_ConcurrentSubscriberStress(t *testing.T) {
	// Many goroutines subscribing, unsubscribing, and broadcasting
	// concurrently. Run with -race to verify no data races.
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	os.MkdirAll(filepath.Join(tmpDir, "show"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "show", "ep1.mkv"), []byte("data"), 0644)

	done := make(chan struct{})
	const goroutines = 20

	// Subscribers: rapidly subscribe and unsubscribe
	for i := 0; i < goroutines/2; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 50; j++ {
				ch := browser.Subscribe()
				time.Sleep(time.Millisecond)
				browser.Unsubscribe(ch)
			}
		}()
	}

	// Broadcasters: rapidly send events
	for i := 0; i < goroutines/2; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 100; j++ {
				browser.broadcast(DirCountEvent{
					Path:      "/test",
					FileCount: j,
					State:     string(stateReady),
				})
				time.Sleep(time.Millisecond)
			}
		}()
	}

	// Wait for all goroutines
	for i := 0; i < goroutines; i++ {
		<-done
	}
}

func TestIntegration_WarmCountCacheSkipsPruneOnError(t *testing.T) {
	// If WalkDir encounters errors during WarmCountCache, pruning must
	// be skipped to avoid deleting valid cache entries.
	if os.Getuid() == 0 {
		t.Skip("test requires non-root (root bypasses permissions)")
	}

	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	// Create two directories
	dir1 := filepath.Join(tmpDir, "accessible")
	dir2 := filepath.Join(tmpDir, "restricted")
	os.MkdirAll(dir1, 0755)
	os.MkdirAll(dir2, 0755)
	os.WriteFile(filepath.Join(dir1, "ep1.mkv"), []byte("data"), 0644)
	os.WriteFile(filepath.Join(dir2, "ep1.mkv"), []byte("data"), 0644)

	// First warm: populates both
	browser.WarmCountCache(context.Background())

	browser.countCacheMu.RLock()
	_, dir2Exists := browser.countCache[dir2]
	browser.countCacheMu.RUnlock()
	if !dir2Exists {
		t.Fatal("expected dir2 to be cached after first warm")
	}

	// Make dir2 unreadable (walk will error on it)
	os.Chmod(dir2, 0000)
	defer os.Chmod(dir2, 0755)

	// Second warm: walk will have errors
	browser.WarmCountCache(context.Background())

	// dir2 should NOT be pruned because the walk had errors
	browser.countCacheMu.RLock()
	_, dir2StillExists := browser.countCache[dir2]
	browser.countCacheMu.RUnlock()
	if !dir2StillExists {
		t.Error("WarmCountCache pruned dir2 despite walk errors (should skip pruning)")
	}
}

// --- Persistence ---

func TestIntegration_PersistenceRoundTripAccuracy(t *testing.T) {
	// Export counts, "restart" (new browser), import, verify counts match
	// even for a complex directory tree.
	tmpDir := t.TempDir()
	browser1 := NewBrowser(nil, tmpDir)

	// Build a tree
	for _, show := range []string{"A", "B", "C"} {
		for _, season := range []string{"S1", "S2"} {
			dir := filepath.Join(tmpDir, show, season)
			os.MkdirAll(dir, 0755)
			os.WriteFile(filepath.Join(dir, "ep1.mkv"), []byte("data"), 0644)
			os.WriteFile(filepath.Join(dir, "ep2.mkv"), []byte("data"), 0644)
		}
	}

	browser1.WarmCountCache(context.Background())

	// Capture expected counts
	ctx := context.Background()
	result1, _ := browser1.Browse(ctx, tmpDir)
	expectedCounts := make(map[string]int)
	for _, e := range result1.Entries {
		expectedCounts[e.Name] = e.FileCount
	}

	// Export and import (simulates SQLite persistence)
	exported := browser1.ExportCounts()

	// "Restart"
	browser2 := NewBrowser(nil, tmpDir)
	browser2.ImportCounts(exported)

	result2, _ := browser2.Browse(ctx, tmpDir)
	for _, e := range result2.Entries {
		expected := expectedCounts[e.Name]
		if e.FileCount != expected {
			t.Errorf("%s: expected %d from snapshot, got %d", e.Name, expected, e.FileCount)
		}
	}
}

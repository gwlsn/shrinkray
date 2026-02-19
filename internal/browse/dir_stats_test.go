package browse

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gwlsn/shrinkray/internal/ffmpeg"
)

func TestGetDirSig(t *testing.T) {
	dir := t.TempDir()

	sig, err := getDirSig(dir)
	if err != nil {
		t.Fatalf("getDirSig failed: %v", err)
	}

	// mtime should be set (directory was just created)
	if sig.mtime.IsZero() {
		t.Error("expected non-zero mtime")
	}
}

func TestGetDirSig_NonExistent(t *testing.T) {
	_, err := getDirSig("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Error("expected error for non-existent path")
	}
}

func TestDirSigDiffers_MtimeChange(t *testing.T) {
	dir := t.TempDir()

	sig1, err := getDirSig(dir)
	if err != nil {
		t.Fatalf("getDirSig failed: %v", err)
	}

	// Sleep to ensure the filesystem timestamp granularity is exceeded.
	// Some filesystems (ext4, tmpfs) have 1-second mtime resolution,
	// so operations within the same second produce identical timestamps.
	time.Sleep(1100 * time.Millisecond)

	// Create a file to change the directory mtime
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello"), 0644); err != nil {
		t.Fatalf("write file failed: %v", err)
	}

	sig2, err := getDirSig(dir)
	if err != nil {
		t.Fatalf("getDirSig failed: %v", err)
	}

	if !sig1.differs(sig2) {
		t.Error("expected signatures to differ after adding a file")
	}
}

func TestDirSigDiffers_MtimeOnlyFallback(t *testing.T) {
	// Simulate NFS/SMB where inode and ctime are zero
	sig1 := dirSig{mtime: time.Now()}
	sig2 := dirSig{mtime: sig1.mtime.Add(1 * time.Second)}

	if !sig1.differs(sig2) {
		t.Error("expected mtime-only signatures to differ")
	}

	sig3 := dirSig{mtime: sig1.mtime}
	if sig1.differs(sig3) {
		t.Error("expected identical mtime-only signatures to match")
	}
}

func TestDirSigDiffers_SameDir(t *testing.T) {
	dir := t.TempDir()

	sig1, _ := getDirSig(dir)
	sig2, _ := getDirSig(dir)

	if sig1.differs(sig2) {
		t.Error("expected identical signatures for same unchanged directory")
	}
}

func TestDirCountStates(t *testing.T) {
	// Verify state constants are distinct and non-empty
	states := []dirCountState{stateUnknown, statePending, stateReady, stateStale, stateError}
	seen := make(map[dirCountState]bool)
	for _, s := range states {
		if s == "" {
			t.Error("state constant must not be empty")
		}
		if seen[s] {
			t.Errorf("duplicate state constant: %s", s)
		}
		seen[s] = true
	}
}

func TestGetDirCount_CacheMiss(t *testing.T) {
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	// Create a subdirectory with a video file
	subDir := filepath.Join(tmpDir, "shows")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "ep1.mkv"), []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	dc := browser.GetDirCount(ctx, subDir)

	// First access: should be unknown or pending (recompute enqueued)
	if dc.State != stateUnknown && dc.State != statePending {
		t.Errorf("expected unknown or pending on cache miss, got %s", dc.State)
	}

	// Wait for background recompute to finish
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		dc = browser.GetDirCount(ctx, subDir)
		if dc.State == stateReady {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if dc.State != stateReady {
		t.Fatalf("expected ready after recompute, got %s", dc.State)
	}
	if dc.FileCount != 1 {
		t.Errorf("expected 1 video, got %d", dc.FileCount)
	}
}

func TestGetDirCount_DetectsStaleness(t *testing.T) {
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	// Create subdir with one video
	subDir := filepath.Join(tmpDir, "shows")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "ep1.mkv"), []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// Warm the cache
	browser.countVideos(ctx, subDir)

	// Verify it's cached and ready
	dc := browser.GetDirCount(ctx, subDir)
	if dc.FileCount != 1 {
		t.Fatalf("expected 1 video after warm, got %d", dc.FileCount)
	}

	// Add a second video file (changes directory mtime)
	time.Sleep(1100 * time.Millisecond) // ensure mtime granularity
	if err := os.WriteFile(filepath.Join(subDir, "ep2.mkv"), []byte("fake2"), 0644); err != nil {
		t.Fatal(err)
	}

	// Next GetDirCount should detect staleness
	dc = browser.GetDirCount(ctx, subDir)
	if dc.State == stateReady {
		t.Error("expected stale or pending after directory change, got ready")
	}
	// Last-known values must be preserved (not zeroed)
	if dc.FileCount != 1 {
		t.Errorf("expected last-known count of 1 during recompute, got %d", dc.FileCount)
	}

	// Wait for recompute
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		dc = browser.GetDirCount(ctx, subDir)
		if dc.State == stateReady && dc.FileCount == 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if dc.FileCount != 2 {
		t.Errorf("expected 2 videos after recompute, got %d", dc.FileCount)
	}
}

func TestGetDirCount_DeletedDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	subDir := filepath.Join(tmpDir, "shows")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "ep1.mkv"), []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// Warm cache
	browser.countVideos(ctx, subDir)

	// Delete the directory
	if err := os.RemoveAll(subDir); err != nil {
		t.Fatal(err)
	}

	// GetDirCount should handle deletion gracefully
	dc := browser.GetDirCount(ctx, subDir)
	if dc.State != stateError {
		t.Errorf("expected error state for deleted directory, got %s", dc.State)
	}
	// Last-known values should still be accessible
	if dc.FileCount != 1 {
		t.Errorf("expected preserved count of 1 for deleted dir, got %d", dc.FileCount)
	}
}

func TestGetDirCount_NeverDeletedCacheMiss(t *testing.T) {
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	// Ask for a directory that was never cached and doesn't exist
	dc := browser.GetDirCount(context.Background(), filepath.Join(tmpDir, "nonexistent"))
	if dc.State != stateError {
		t.Errorf("expected error for non-existent uncached dir, got %s", dc.State)
	}
	if dc.FileCount != 0 {
		t.Errorf("expected 0 count for never-cached dir, got %d", dc.FileCount)
	}
}

func TestBrowseSSE_SubscribeReceivesBroadcast(t *testing.T) {
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	ch := browser.Subscribe()
	defer browser.Unsubscribe(ch)

	event := DirCountEvent{
		Path:      "/media/test",
		FileCount: 42,
		TotalSize: 1024,
		State:     string(stateReady),
		UpdatedAt: time.Now(),
	}
	browser.broadcast(event)

	select {
	case received := <-ch:
		if received.Path != event.Path {
			t.Errorf("expected path %s, got %s", event.Path, received.Path)
		}
		if received.FileCount != 42 {
			t.Errorf("expected count 42, got %d", received.FileCount)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for broadcast event")
	}
}

func TestBrowseSSE_UnsubscribeStopsEvents(t *testing.T) {
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	ch := browser.Subscribe()
	browser.Unsubscribe(ch)

	// Channel should be closed after unsubscribe
	_, ok := <-ch
	if ok {
		t.Error("expected channel to be closed after unsubscribe")
	}
}

func TestBrowseSSE_FullChannelDoesNotBlock(t *testing.T) {
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	ch := browser.Subscribe()
	defer browser.Unsubscribe(ch)

	// Fill the channel buffer
	for i := 0; i < 100; i++ {
		browser.broadcast(DirCountEvent{Path: "/test", FileCount: i})
	}

	// One more broadcast should not block (non-blocking send drops the event)
	done := make(chan struct{})
	go func() {
		browser.broadcast(DirCountEvent{Path: "/test", FileCount: 999})
		close(done)
	}()

	select {
	case <-done:
		// Good: broadcast returned without blocking
	case <-time.After(1 * time.Second):
		t.Fatal("broadcast blocked on full channel")
	}
}

func TestInvalidateCache_PreservesLastKnownValues(t *testing.T) {
	tmpDir := t.TempDir()
	// Construct directly without starting workers so enqueueRecompute
	// pushes to the buffered channel but nothing consumes it. This lets
	// us assert the stale state without a race against background recompute.
	absDir, _ := filepath.Abs(tmpDir)
	browser := &Browser{
		mediaRoot:         absDir,
		cache:             make(map[string]*ffmpeg.ProbeResult),
		countCache:        make(map[string]*dirCount),
		countSem:          make(chan struct{}, 8),
		browseSubscribers: make(map[chan DirCountEvent]struct{}),
		recomputeQueue:    make(chan string, 256),
	}

	subDir := filepath.Join(tmpDir, "shows")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	videoPath := filepath.Join(subDir, "ep1.mkv")
	if err := os.WriteFile(videoPath, []byte("fake video"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// Warm cache
	browser.countVideos(ctx, subDir)

	// Verify cached
	browser.countCacheMu.RLock()
	cached, exists := browser.countCache[subDir]
	browser.countCacheMu.RUnlock()
	if !exists || cached.fileCount != 1 {
		t.Fatalf("expected cached count of 1, got exists=%v count=%d", exists, cached.fileCount)
	}

	// Invalidate
	browser.InvalidateCache(videoPath)

	// Cache entry must still exist with last-known values
	browser.countCacheMu.RLock()
	after, stillExists := browser.countCache[subDir]
	browser.countCacheMu.RUnlock()

	if !stillExists {
		t.Fatal("InvalidateCache deleted the cache entry (should mark stale)")
	}
	if after.fileCount != 1 {
		t.Errorf("expected preserved count of 1, got %d", after.fileCount)
	}
	if after.state == stateReady {
		t.Error("expected non-ready state after invalidation")
	}
}

func TestClearCache_PreservesLastKnownValues(t *testing.T) {
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	subDir := filepath.Join(tmpDir, "shows")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "ep1.mkv"), []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	browser.countVideos(ctx, subDir)

	// Clear cache
	browser.ClearCache()

	// Count cache entries must not be deleted
	browser.countCacheMu.RLock()
	cached, exists := browser.countCache[subDir]
	browser.countCacheMu.RUnlock()

	if !exists {
		t.Fatal("ClearCache deleted count cache entries (should mark stale)")
	}
	if cached.fileCount != 1 {
		t.Errorf("expected preserved count of 1, got %d", cached.fileCount)
	}
}

func TestWarmCountCache_CapturesSignatures(t *testing.T) {
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	subDir := filepath.Join(tmpDir, "shows")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "ep1.mkv"), []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}

	browser.WarmCountCache(context.Background())

	browser.countCacheMu.RLock()
	cached, exists := browser.countCache[subDir]
	browser.countCacheMu.RUnlock()

	if !exists {
		t.Fatal("WarmCountCache did not populate entry")
	}
	if cached.state != stateReady {
		t.Errorf("expected ready state, got %s", cached.state)
	}
	if cached.sig.mtime.IsZero() {
		t.Error("expected non-zero mtime in signature")
	}
}

func TestWarmCountCache_PrunesStaleEntries(t *testing.T) {
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	// Pre-populate cache with an entry for a path that doesn't exist
	browser.countCacheMu.Lock()
	browser.countCache[filepath.Join(tmpDir, "deleted_show")] = &dirCount{
		fileCount: 5,
		state:     stateReady,
	}
	browser.countCacheMu.Unlock()

	// Warm cache (walks only dirs that actually exist)
	browser.WarmCountCache(context.Background())

	// The stale entry should be pruned
	browser.countCacheMu.RLock()
	_, exists := browser.countCache[filepath.Join(tmpDir, "deleted_show")]
	browser.countCacheMu.RUnlock()

	if exists {
		t.Error("expected deleted_show entry to be pruned by WarmCountCache")
	}
}

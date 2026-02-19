package browse

import (
	"testing"
	"time"
)

func TestExportCounts_IncludesReadyAndStale(t *testing.T) {
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	now := time.Now()
	browser.countCacheMu.Lock()
	browser.countCache["/media/shows"] = &dirCount{
		fileCount: 42, totalSize: 123456, state: stateReady, updatedAt: now,
	}
	browser.countCache["/media/movies"] = &dirCount{
		fileCount: 10, totalSize: 654321, state: stateStale, updatedAt: now,
	}
	// Error and pending entries should be excluded
	browser.countCache["/media/broken"] = &dirCount{
		fileCount: 5, state: stateError,
	}
	browser.countCache["/media/loading"] = &dirCount{
		state: statePending,
	}
	browser.countCacheMu.Unlock()

	entries := browser.ExportCounts()

	if len(entries) != 2 {
		t.Fatalf("expected 2 exported entries, got %d", len(entries))
	}

	// Build a map for easier lookup
	byPath := make(map[string]DirCountEntry)
	for _, e := range entries {
		byPath[e.Path] = e
	}

	shows, ok := byPath["/media/shows"]
	if !ok {
		t.Fatal("missing /media/shows in export")
	}
	if shows.FileCount != 42 {
		t.Errorf("expected shows count 42, got %d", shows.FileCount)
	}

	movies, ok := byPath["/media/movies"]
	if !ok {
		t.Fatal("missing /media/movies in export")
	}
	if movies.FileCount != 10 {
		t.Errorf("expected movies count 10, got %d", movies.FileCount)
	}
}

func TestImportCounts_LoadsAsStale(t *testing.T) {
	tmpDir := t.TempDir()
	browser := NewBrowser(nil, tmpDir)

	now := time.Now()
	entries := []DirCountEntry{
		{Path: "/media/shows", FileCount: 42, TotalSize: 123456, UpdatedAt: now},
		{Path: "/media/movies", FileCount: 10, TotalSize: 654321, UpdatedAt: now},
	}

	browser.ImportCounts(entries)

	browser.countCacheMu.RLock()
	shows, showsOk := browser.countCache["/media/shows"]
	movies, moviesOk := browser.countCache["/media/movies"]
	browser.countCacheMu.RUnlock()

	if !showsOk || !moviesOk {
		t.Fatal("imported entries missing from cache")
	}
	if shows.fileCount != 42 {
		t.Errorf("expected shows count 42, got %d", shows.fileCount)
	}
	if movies.fileCount != 10 {
		t.Errorf("expected movies count 10, got %d", movies.fileCount)
	}
	if shows.state != stateStale {
		t.Errorf("expected stale state after import, got %s", shows.state)
	}
}

func TestExportImport_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	browser1 := NewBrowser(nil, tmpDir)

	now := time.Now()
	browser1.countCacheMu.Lock()
	browser1.countCache["/media/shows"] = &dirCount{
		fileCount: 42, totalSize: 123456, state: stateReady, updatedAt: now,
	}
	browser1.countCacheMu.Unlock()

	// Export from browser1
	entries := browser1.ExportCounts()

	// Import into fresh browser2
	browser2 := NewBrowser(nil, tmpDir)
	browser2.ImportCounts(entries)

	browser2.countCacheMu.RLock()
	cached, ok := browser2.countCache["/media/shows"]
	browser2.countCacheMu.RUnlock()

	if !ok {
		t.Fatal("round-trip lost the entry")
	}
	if cached.fileCount != 42 {
		t.Errorf("expected count 42 after round-trip, got %d", cached.fileCount)
	}
	if cached.state != stateStale {
		t.Errorf("expected stale after import, got %s", cached.state)
	}
}

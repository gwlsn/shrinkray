package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/gwlsn/shrinkray/internal/browse"
)

func TestSaveDirCounts_RoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	now := time.Now().Truncate(time.Second) // SQLite stores second precision via RFC3339

	entries := []browse.DirCountEntry{
		{Path: "/media/shows", FileCount: 42, TotalSize: 123456, UpdatedAt: now},
		{Path: "/media/movies", FileCount: 10, TotalSize: 654321, UpdatedAt: now},
	}

	if err := store.SaveDirCounts(entries); err != nil {
		t.Fatalf("SaveDirCounts: %v", err)
	}

	loaded, err := store.LoadDirCounts()
	if err != nil {
		t.Fatalf("LoadDirCounts: %v", err)
	}

	if len(loaded) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(loaded))
	}

	byPath := make(map[string]browse.DirCountEntry)
	for _, e := range loaded {
		byPath[e.Path] = e
	}

	shows, ok := byPath["/media/shows"]
	if !ok {
		t.Fatal("missing /media/shows")
	}
	if shows.FileCount != 42 || shows.TotalSize != 123456 {
		t.Errorf("shows: got count=%d size=%d", shows.FileCount, shows.TotalSize)
	}

	movies, ok := byPath["/media/movies"]
	if !ok {
		t.Fatal("missing /media/movies")
	}
	if movies.FileCount != 10 || movies.TotalSize != 654321 {
		t.Errorf("movies: got count=%d size=%d", movies.FileCount, movies.TotalSize)
	}
}

func TestSaveDirCounts_Upsert(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	now := time.Now().Truncate(time.Second)

	// Save initial
	if err := store.SaveDirCounts([]browse.DirCountEntry{
		{Path: "/media/shows", FileCount: 10, TotalSize: 100, UpdatedAt: now},
	}); err != nil {
		t.Fatal(err)
	}

	// Save updated (same path, new values)
	if err := store.SaveDirCounts([]browse.DirCountEntry{
		{Path: "/media/shows", FileCount: 20, TotalSize: 200, UpdatedAt: now},
	}); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.LoadDirCounts()
	if err != nil {
		t.Fatal(err)
	}

	if len(loaded) != 1 {
		t.Fatalf("expected 1 entry after upsert, got %d", len(loaded))
	}
	if loaded[0].FileCount != 20 {
		t.Errorf("expected updated count 20, got %d", loaded[0].FileCount)
	}
}

func TestLoadDirCounts_EmptyTable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	loaded, err := store.LoadDirCounts()
	if err != nil {
		t.Fatalf("LoadDirCounts: %v", err)
	}
	if len(loaded) != 0 {
		t.Errorf("expected 0 entries from empty table, got %d", len(loaded))
	}
}

func TestMigration_V6ToV7_CreatesDirCountsTable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	// Create a v6 database (simulate pre-migration state)
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	// Downgrade version to 6 to test migration path
	_, err = store.db.Exec("UPDATE schema_version SET version = 6 WHERE version = 7")
	if err != nil {
		t.Fatalf("downgrade version: %v", err)
	}
	store.Close()

	// Reopen (should run v6->v7 migration)
	store2, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore after migration: %v", err)
	}
	defer store2.Close()

	// Verify we can use the dir_counts table
	entries := []browse.DirCountEntry{
		{Path: "/media/test", FileCount: 5, TotalSize: 500, UpdatedAt: time.Now().Truncate(time.Second)},
	}
	if err := store2.SaveDirCounts(entries); err != nil {
		t.Fatalf("SaveDirCounts after migration: %v", err)
	}

	loaded, err := store2.LoadDirCounts()
	if err != nil {
		t.Fatalf("LoadDirCounts after migration: %v", err)
	}
	if len(loaded) != 1 || loaded[0].FileCount != 5 {
		t.Errorf("unexpected result after migration: %+v", loaded)
	}
}

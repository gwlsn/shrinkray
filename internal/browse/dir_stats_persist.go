package browse

import "time"

// DirCountEntry is the portable representation of a cached directory count,
// used for persistence (SQLite) and import/export. Decoupled from the
// internal dirCount struct so the store layer doesn't need to know about
// cache state or directory signatures.
type DirCountEntry struct {
	Path      string
	FileCount int
	TotalSize int64
	UpdatedAt time.Time
}

// ExportCounts returns a snapshot of all directory counts in ready or stale
// state. Pending and error entries are excluded since they may hold
// incomplete data. The returned slice is safe to use without holding locks.
func (b *Browser) ExportCounts() []DirCountEntry {
	b.countCacheMu.RLock()
	defer b.countCacheMu.RUnlock()

	entries := make([]DirCountEntry, 0, len(b.countCache))
	for path, dc := range b.countCache {
		if dc.state == stateReady || dc.state == stateStale {
			entries = append(entries, DirCountEntry{
				Path:      path,
				FileCount: dc.fileCount,
				TotalSize: dc.totalSize,
				UpdatedAt: dc.updatedAt,
			})
		}
	}
	return entries
}

// ImportCounts loads directory count entries into the cache as stale. This
// provides instant display of last-known values on startup while
// WarmCountCache refreshes them against the filesystem in the background.
func (b *Browser) ImportCounts(entries []DirCountEntry) {
	b.countCacheMu.Lock()
	defer b.countCacheMu.Unlock()

	for _, e := range entries {
		b.countCache[e.Path] = &dirCount{
			fileCount: e.FileCount,
			totalSize: e.TotalSize,
			state:     stateStale,
			updatedAt: e.UpdatedAt,
		}
	}
}

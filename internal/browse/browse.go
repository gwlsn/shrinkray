package browse

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gwlsn/shrinkray/internal/ffmpeg"
	"github.com/gwlsn/shrinkray/internal/logger"
	"golang.org/x/sync/singleflight"
)

// ProgressCallback is called during file discovery to report progress
type ProgressCallback func(probed, total int)

// Entry represents a file or directory in the browser
type Entry struct {
	Name        string             `json:"name"`
	Path        string             `json:"path"`
	IsDir       bool               `json:"is_dir"`
	Size        int64              `json:"size"`
	ModTime     time.Time          `json:"mod_time"`
	VideoInfo   *ffmpeg.ProbeResult `json:"video_info,omitempty"`
	FileCount   int                `json:"file_count,omitempty"`   // For directories: number of video files
	TotalSize   int64              `json:"total_size,omitempty"`   // For directories: total size of video files
}

// BrowseResult contains the result of browsing a directory
type BrowseResult struct {
	Path       string   `json:"path"`
	Parent     string   `json:"parent,omitempty"`
	Entries    []*Entry `json:"entries"`
	VideoCount int      `json:"video_count"` // Total video files in this directory and subdirs
	TotalSize  int64    `json:"total_size"`  // Total size of video files
}

// Browser handles file system browsing with video metadata
type Browser struct {
	prober    *ffmpeg.Prober
	mediaRoot string

	// Cache for probe results (path -> result)
	cacheMu sync.RWMutex
	cache   map[string]*ffmpeg.ProbeResult

	// Cache for recursive directory video counts
	countCacheMu sync.RWMutex
	countCache   map[string]*dirCount

	// Deduplicates concurrent countVideos calls for the same directory
	countGroup singleflight.Group

	// Limits concurrent directory walks to avoid overwhelming network shares
	countSem chan struct{}

	// Max concurrent file probes in Browse to avoid overwhelming network shares
	maxProbes int

	// Prevents stacking multiple background invalidation sweeps.
	// 0 = idle, 1 = running. If a ClearCache call arrives while running,
	// dirty is set to 1 so the sweep reruns once with a fresh snapshot.
	invalidating int32
	dirty        int32
}

// NewBrowser creates a new Browser with the given prober and media root
func NewBrowser(prober *ffmpeg.Prober, mediaRoot string) *Browser {
	// Convert to absolute path for consistent comparisons
	absRoot, err := filepath.Abs(mediaRoot)
	if err != nil {
		absRoot = mediaRoot
	}
	return &Browser{
		prober:     prober,
		mediaRoot:  absRoot,
		cache:      make(map[string]*ffmpeg.ProbeResult),
		countCache: make(map[string]*dirCount),
		countSem:   make(chan struct{}, 8),
		maxProbes:  16,
	}
}

// isUnderRoot reports whether absPath is equal to or nested under the media root.
// Uses prefix + separator to avoid /media matching /media2.
func (b *Browser) isUnderRoot(absPath string) bool {
	return absPath == b.mediaRoot || strings.HasPrefix(absPath, b.mediaRoot+string(os.PathSeparator))
}

// normalizePath converts a path to an absolute path and ensures it's within the media root.
// If the path is outside the media root, it returns the media root instead.
func (b *Browser) normalizePath(path string) string {
	cleanPath, err := filepath.Abs(path)
	if err != nil {
		cleanPath = filepath.Clean(path)
	}
	if !b.isUnderRoot(cleanPath) {
		return b.mediaRoot
	}
	return cleanPath
}

// Browse returns the contents of a directory
func (b *Browser) Browse(ctx context.Context, path string) (*BrowseResult, error) {
	cleanPath := b.normalizePath(path)

	entries, err := os.ReadDir(cleanPath)
	if err != nil {
		return nil, err
	}

	result := &BrowseResult{
		Path:    cleanPath,
		Entries: make([]*Entry, 0, len(entries)),
	}

	// Set parent path (if not at root)
	if cleanPath != b.mediaRoot {
		result.Parent = filepath.Dir(cleanPath)
	}

	// Process entries — single pass to build entry list and collect work items.
	var uncachedDirs []string
	var videoEntries []*Entry

	for _, e := range entries {
		// Skip hidden files
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}

		entryPath := filepath.Join(cleanPath, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}

		entry := &Entry{
			Name:    e.Name(),
			Path:    entryPath,
			IsDir:   e.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		}

		if e.IsDir() {
			// Return cached counts instantly; collect uncached dirs for
			// bounded background computation.
			b.countCacheMu.RLock()
			cached, isCached := b.countCache[entryPath]
			b.countCacheMu.RUnlock()

			if isCached {
				entry.FileCount = cached.fileCount
				entry.TotalSize = cached.totalSize
			} else {
				uncachedDirs = append(uncachedDirs, entryPath)
			}
		} else if ffmpeg.IsVideoFile(e.Name()) {
			videoEntries = append(videoEntries, entry)
			result.VideoCount++
			result.TotalSize += info.Size()
		}

		result.Entries = append(result.Entries, entry)
	}

	// Probe video files through a bounded worker pool — only maxProbes
	// goroutines are created instead of one per file.
	if len(videoEntries) > 0 {
		work := make(chan *Entry, len(videoEntries))
		for _, ve := range videoEntries {
			work <- ve
		}
		close(work)

		workers := b.maxProbes
		if workers > len(videoEntries) {
			workers = len(videoEntries)
		}
		var wg sync.WaitGroup
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for entry := range work {
					if probeResult := b.getProbeResult(ctx, entry.Path); probeResult != nil {
						entry.VideoInfo = probeResult
						entry.Size = probeResult.Size
					}
				}
			}()
		}
		wg.Wait()
	}

	// Dispatch uncached directory counts through a bounded worker pool.
	// Uses countSem capacity as the pool size since that's the actual concurrency
	// limit for walks — no point spawning more goroutines than can run.
	if len(uncachedDirs) > 0 {
		go func() {
			var countWg sync.WaitGroup
			work := make(chan string, len(uncachedDirs))
			for _, dir := range uncachedDirs {
				work <- dir
			}
			close(work)

			// Spawn workers up to countSem capacity (8)
			workers := cap(b.countSem)
			if workers > len(uncachedDirs) {
				workers = len(uncachedDirs)
			}
			for range workers {
				countWg.Add(1)
				go func() {
					defer countWg.Done()
					for dir := range work {
						b.countVideos(context.Background(), dir)
					}
				}()
			}
			countWg.Wait()
		}()
	}

	// Sort entries: directories first, then by name
	sort.Slice(result.Entries, func(i, j int) bool {
		if result.Entries[i].IsDir != result.Entries[j].IsDir {
			return result.Entries[i].IsDir // Directories first
		}
		return strings.ToLower(result.Entries[i].Name) < strings.ToLower(result.Entries[j].Name)
	})

	return result, nil
}

// countVideos counts video files in a directory recursively.
// Uses three layers of optimization:
//   - Cache: instant return for previously-walked directories
//   - Singleflight: deduplicates concurrent walks for the same directory
//   - WalkDir: avoids stat syscalls on non-video files (big win on network FS)
func (b *Browser) countVideos(ctx context.Context, dirPath string) (int, int64) {
	// Check cache first (fast path, no allocation)
	b.countCacheMu.RLock()
	if cached, ok := b.countCache[dirPath]; ok {
		b.countCacheMu.RUnlock()
		return cached.fileCount, cached.totalSize
	}
	b.countCacheMu.RUnlock()

	// Singleflight: if another goroutine is already walking this directory,
	// wait for its result instead of doing duplicate work
	v, _, _ := b.countGroup.Do(dirPath, func() (interface{}, error) {
		// Double-check cache (another goroutine in the same group may have filled it)
		b.countCacheMu.RLock()
		if cached, ok := b.countCache[dirPath]; ok {
			b.countCacheMu.RUnlock()
			return cached, nil
		}
		b.countCacheMu.RUnlock()

		// Rate-limit concurrent walks to avoid overwhelming network shares
		b.countSem <- struct{}{}
		defer func() { <-b.countSem }()

		var count int
		var totalSize int64
		// WalkDir avoids stat on every entry — only calls Info() for video files
		_ = filepath.WalkDir(dirPath, func(path string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return filepath.SkipAll
			}
			if err != nil || d.IsDir() {
				return nil
			}
			if ffmpeg.IsVideoFile(d.Name()) {
				if info, infoErr := d.Info(); infoErr == nil {
					count++
					totalSize += info.Size()
				}
			}
			return nil
		})

		// Capture the directory's mtime so ClearCache can detect structural changes
		var dirMtime time.Time
		if info, err := os.Stat(dirPath); err == nil {
			dirMtime = info.ModTime()
		}

		dc := &dirCount{fileCount: count, totalSize: totalSize, sig: dirSig{mtime: dirMtime}, state: stateReady, updatedAt: time.Now()}
		// Only cache if context wasn't cancelled (partial results would be wrong)
		if ctx.Err() == nil {
			b.countCacheMu.Lock()
			b.countCache[dirPath] = dc
			b.countCacheMu.Unlock()
		}
		return dc, nil
	})

	if dc, ok := v.(*dirCount); ok {
		return dc.fileCount, dc.totalSize
	}
	return 0, 0
}

// getProbeResult returns a cached or fresh probe result.
// Validates cached entries using inode + size signature to detect file replacement.
// We use inode + size instead of mtime because mtime is deliberately preserved
// after transcoding (via os.Chtimes).
func (b *Browser) getProbeResult(ctx context.Context, path string) *ffmpeg.ProbeResult {
	// Check cache
	b.cacheMu.RLock()
	cached, ok := b.cache[path]
	b.cacheMu.RUnlock()

	if ok {
		// Validate cached signature against current file
		if info, err := os.Stat(path); err == nil {
			currentSize := info.Size()
			var currentInode uint64
			if stat, ok := info.Sys().(*syscall.Stat_t); ok {
				currentInode = stat.Ino
			}

			// Signature match = cache hit
			if cached.Inode == currentInode && cached.Size == currentSize {
				return cached
			}

			// Signature mismatch = invalidate and re-probe
			b.cacheMu.Lock()
			delete(b.cache, path)
			b.cacheMu.Unlock()
		}
	}

	// Cache miss or invalidated: probe and cache
	result, err := b.prober.Probe(ctx, path)
	if err != nil {
		return nil
	}

	b.cacheMu.Lock()
	b.cache[path] = result
	b.cacheMu.Unlock()

	return result
}

// GetVideoFilesWithProgress returns all video files with progress reporting
// The onProgress callback is called periodically with (probed, total) counts
func (b *Browser) GetVideoFilesWithProgress(ctx context.Context, paths []string, onProgress ProgressCallback) ([]*ffmpeg.ProbeResult, error) {
	// First pass: count total video files (fast, no probing)
	var videoPaths []string
	for _, path := range paths {
		cleanPath := b.normalizePath(path)
		// Skip if path was outside media root (normalizePath returns mediaRoot in that case)
		if cleanPath == b.mediaRoot && path != b.mediaRoot && path != "" {
			absPath, _ := filepath.Abs(path)
			if !b.isUnderRoot(absPath) {
				continue
			}
		}

		info, err := os.Stat(cleanPath)
		if err != nil {
			continue
		}

		if info.IsDir() {
			_ = filepath.Walk(cleanPath, func(filePath string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return nil
				}
				if ffmpeg.IsVideoFile(filePath) {
					videoPaths = append(videoPaths, filePath)
				}
				return nil
			})
		} else if ffmpeg.IsVideoFile(cleanPath) {
			videoPaths = append(videoPaths, cleanPath)
		}
	}

	total := len(videoPaths)

	// Report initial count (0 probed)
	if onProgress != nil {
		onProgress(0, total)
	}

	// Second pass: probe files with progress updates
	// Limit concurrent probes to prevent straggler problem and reduce system load
	const maxConcurrent = 50
	sem := make(chan struct{}, maxConcurrent)

	// Pre-allocate indexed slots to preserve original path order after concurrent probing
	indexed := make([]*ffmpeg.ProbeResult, total)
	var wg sync.WaitGroup
	var probed int64

	for i, filePath := range videoPaths {
		wg.Add(1)
		go func(idx int, fp string) {
			defer wg.Done()

			// Acquire semaphore slot (limits concurrent probes)
			sem <- struct{}{}
			defer func() { <-sem }()

			// Write to pre-allocated slot — no mutex needed, each index is unique
			indexed[idx] = b.getProbeResult(ctx, fp)

			// Report progress after each probe completes
			current := atomic.AddInt64(&probed, 1)
			if onProgress != nil {
				onProgress(int(current), total)
			}
		}(i, filePath)
	}

	wg.Wait()

	// Compact: remove nil entries (failed probes) while preserving order
	results := make([]*ffmpeg.ProbeResult, 0, total)
	for _, r := range indexed {
		if r != nil {
			results = append(results, r)
		}
	}

	return results, nil
}

// ClearCache clears the probe cache and selectively invalidates directory
// count caches using two complementary checks:
//  1. Directory mtime: detects structural changes (files added/removed) in any
//     cached directory, even those whose files were never individually probed.
//  2. Probe signatures: detects file content changes (inode/size) for files
//     the user previously browsed into.
//
// This keeps folder sizes visible for unchanged directories while ensuring
// changes trigger a fresh count on next browse.
func (b *Browser) ClearCache() {
	// Snapshot the old probe cache so we can detect stale file entries.
	// This swap is instant (pointer reassignment) so it stays synchronous —
	// the next Browse call immediately gets a fresh probe cache.
	b.cacheMu.Lock()
	oldCache := b.cache
	b.cache = make(map[string]*ffmpeg.ProbeResult)
	b.cacheMu.Unlock()

	// Snapshot the count cache for directory mtime checks.
	b.countCacheMu.RLock()
	oldCountCache := make(map[string]*dirCount, len(b.countCache))
	for k, v := range b.countCache {
		oldCountCache[k] = v
	}
	b.countCacheMu.RUnlock()

	// Run the expensive os.Stat checks in the background so the HTTP handler
	// returns immediately. If a sweep is already running, mark dirty so it
	// reruns once with fresh snapshots after the current sweep finishes.
	if atomic.CompareAndSwapInt32(&b.invalidating, 0, 1) {
		go b.runInvalidationLoop(oldCache, oldCountCache)
	} else {
		// A sweep is already running — signal it to rerun with fresh data.
		atomic.StoreInt32(&b.dirty, 1)
	}
}

// runInvalidationLoop runs the invalidation sweep, then checks if another
// ClearCache call arrived during the sweep (dirty flag). If so, takes fresh
// snapshots and runs one more time to pick up changes that were missed.
func (b *Browser) runInvalidationLoop(oldCache map[string]*ffmpeg.ProbeResult, oldCountCache map[string]*dirCount) {
	// Eventual consistency: a ClearCache between the dirty check and this
	// reset may require one extra refresh to see updated counts.
	defer atomic.StoreInt32(&b.invalidating, 0)

	b.invalidateStaleCounts(oldCache, oldCountCache)

	// If a ClearCache call arrived while we were running, do one more pass
	// with fresh snapshots. Only one retry — further calls will start a new loop.
	if atomic.CompareAndSwapInt32(&b.dirty, 1, 0) {
		b.countCacheMu.RLock()
		freshCountCache := make(map[string]*dirCount, len(b.countCache))
		for k, v := range b.countCache {
			freshCountCache[k] = v
		}
		b.countCacheMu.RUnlock()

		// Probe cache was already swapped by the later ClearCache call,
		// so pass nil — only dir mtime checks matter for the retry.
		b.invalidateStaleCounts(nil, freshCountCache)
	}
}

// invalidateStaleCounts checks directory mtimes and probe signatures to find
// stale count cache entries and removes them. Called in a background goroutine
// by ClearCache to avoid blocking the HTTP handler on os.Stat calls.
func (b *Browser) invalidateStaleCounts(oldCache map[string]*ffmpeg.ProbeResult, oldCountCache map[string]*dirCount) {
	staleDirs := make(map[string]struct{})

	// Check 1: directory mtime — catches new/deleted files in any cached dir,
	// even those whose files were never individually probed.
	for dir, cached := range oldCountCache {
		info, err := os.Stat(dir)
		if err != nil {
			staleDirs[dir] = struct{}{}
			b.markAncestorsStale(dir, staleDirs)
			continue
		}
		if !info.ModTime().Equal(cached.sig.mtime) {
			staleDirs[dir] = struct{}{}
			b.markAncestorsStale(dir, staleDirs)
		}
	}

	// Check 2: probe signatures — catches file content changes (re-encodes)
	// where the file path stays the same but inode/size changed.
	for path, cached := range oldCache {
		info, err := os.Stat(path)
		if err != nil {
			b.markAncestorsStale(filepath.Dir(path), staleDirs)
			continue
		}
		currentSize := info.Size()
		var currentInode uint64
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			currentInode = stat.Ino
		}
		if cached.Inode != currentInode || cached.Size != currentSize {
			b.markAncestorsStale(filepath.Dir(path), staleDirs)
		}
	}

	if len(staleDirs) > 0 {
		b.countCacheMu.Lock()
		for dir := range staleDirs {
			// Only delete if the entry hasn't been refreshed since our snapshot.
			// A concurrent Browse/countVideos may have repopulated this entry
			// with fresh data — deleting it would cause unnecessary flickering.
			if current, ok := b.countCache[dir]; ok && current == oldCountCache[dir] {
				delete(b.countCache, dir)
			}
		}
		b.countCacheMu.Unlock()
	}
}

// markAncestorsStale adds dir and all its ancestors up to mediaRoot into the set.
// Uses the fast lexical check since these paths come from the filesystem/cache.
func (b *Browser) markAncestorsStale(dir string, set map[string]struct{}) {
	for b.isUnderRoot(dir) {
		set[dir] = struct{}{}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
}

// InvalidateCache removes a specific path from the probe cache and clears
// directory count caches for all ancestor directories (since their recursive
// counts include this file).
func (b *Browser) InvalidateCache(path string) {
	b.cacheMu.Lock()
	delete(b.cache, path)
	b.cacheMu.Unlock()

	staleDirs := make(map[string]struct{})
	b.markAncestorsStale(filepath.Dir(path), staleDirs)

	b.countCacheMu.Lock()
	for dir := range staleDirs {
		delete(b.countCache, dir)
	}
	b.countCacheMu.Unlock()
}

// WarmCountCache pre-computes recursive video counts for all directories
// under the media root in a single pass. Call this in a background goroutine
// at startup so counts are ready by the time the user opens the UI.
func (b *Browser) WarmCountCache(ctx context.Context) {
	start := time.Now()
	logger.Info("Warming directory count cache", "media_root", b.mediaRoot)

	dirCounts := make(map[string]*dirCount)
	var videoCount int

	_ = filepath.WalkDir(b.mediaRoot, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return filepath.SkipAll
		}
		if err != nil {
			return nil
		}
		// Skip hidden entries
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			// Ensure every directory gets a cache entry (even if 0 videos)
			// and capture its mtime for staleness detection on refresh.
			if _, ok := dirCounts[path]; !ok {
				dc := &dirCount{state: stateReady, updatedAt: time.Now()}
				if info, infoErr := d.Info(); infoErr == nil {
					dc.sig.mtime = info.ModTime()
				}
				dirCounts[path] = dc
			}
			return nil
		}
		if !ffmpeg.IsVideoFile(d.Name()) {
			return nil
		}

		// Only stat video files (WalkDir skips stat for non-video entries)
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}

		videoCount++

		// Propagate this file's count and size to every ancestor directory
		dir := filepath.Dir(path)
		for b.isUnderRoot(dir) {
			dc := dirCounts[dir] // always present: WalkDir visits parents before children
			dc.fileCount++
			dc.totalSize += info.Size()

			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
		return nil
	})

	// Populate cache in chunks to avoid holding the lock for too long
	// (Browse calls need the read lock to return cached counts)
	if ctx.Err() == nil {
		const chunkSize = 500
		chunk := 0
		for dir, dc := range dirCounts {
			if chunk%chunkSize == 0 {
				if chunk > 0 {
					b.countCacheMu.Unlock()
				}
				b.countCacheMu.Lock()
			}
			b.countCache[dir] = dc
			chunk++
		}
		if chunk > 0 {
			b.countCacheMu.Unlock()
		}

		logger.Info("Directory count cache warmed",
			"directories", len(dirCounts),
			"videos", videoCount,
			"duration", time.Since(start).Round(time.Millisecond),
		)
	}
}

// ProbeFile probes a single file and returns its metadata
func (b *Browser) ProbeFile(ctx context.Context, path string) (*ffmpeg.ProbeResult, error) {
	result, err := b.prober.Probe(ctx, path)
	if err != nil {
		return nil, err
	}
	return result, nil
}

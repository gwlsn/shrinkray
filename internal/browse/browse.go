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
	Name            string              `json:"name"`
	Path            string              `json:"path"`
	IsDir           bool                `json:"is_dir"`
	Size            int64               `json:"size"`
	ModTime         time.Time           `json:"mod_time"`
	VideoInfo       *ffmpeg.ProbeResult `json:"video_info,omitempty"`
	FileCount       int                 `json:"file_count"`
	TotalSize       int64               `json:"total_size"`
	CountsState     string              `json:"counts_state,omitempty"`
	CountsUpdatedAt string              `json:"counts_updated_at,omitempty"`
	CountsError     string              `json:"counts_error,omitempty"`
}

// BrowseResult contains the result of browsing a directory
type BrowseResult struct {
	Path        string   `json:"path"`
	Parent      string   `json:"parent,omitempty"`
	Entries     []*Entry `json:"entries"`
	VideoCount  int      `json:"video_count"`  // Total video files in this directory and subdirs
	TotalSize   int64    `json:"total_size"`   // Total size of video files
	CountsState string   `json:"counts_state"` // State of the recursive counts for this path
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

	// Bounded queue for recompute jobs (consumed by fixed worker pool)
	recomputeQueue chan string

	// Subscribers for directory count update events (SSE)
	browseSubsMu      sync.RWMutex
	browseSubscribers map[chan DirCountEvent]struct{}
}

// NewBrowser creates a new Browser with the given prober and media root
func NewBrowser(prober *ffmpeg.Prober, mediaRoot string) *Browser {
	// Convert to absolute path for consistent comparisons
	absRoot, err := filepath.Abs(mediaRoot)
	if err != nil {
		absRoot = mediaRoot
	}
	b := &Browser{
		prober:            prober,
		mediaRoot:         absRoot,
		cache:             make(map[string]*ffmpeg.ProbeResult),
		countCache:        make(map[string]*dirCount),
		countSem:          make(chan struct{}, 8),
		maxProbes:         16,
		browseSubscribers: make(map[chan DirCountEvent]struct{}),
		recomputeQueue:    make(chan string, 256),
	}
	b.startRecomputeWorkers()
	return b
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
			dc := b.GetDirCount(ctx, entryPath)
			entry.FileCount = dc.FileCount
			entry.TotalSize = dc.TotalSize
			entry.CountsState = string(dc.State)
			if !dc.UpdatedAt.IsZero() {
				entry.CountsUpdatedAt = dc.UpdatedAt.Format(time.RFC3339)
			}
			if dc.Err != "" {
				entry.CountsError = dc.Err
			}
		} else if ffmpeg.IsVideoFile(e.Name()) {
			videoEntries = append(videoEntries, entry)
		}

		result.Entries = append(result.Entries, entry)
	}

	// Populate recursive totals for current path from stats subsystem
	pathDc := b.GetDirCount(ctx, cleanPath)
	result.VideoCount = pathDc.FileCount
	result.TotalSize = pathDc.TotalSize
	result.CountsState = string(pathDc.State)

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

// ClearCache clears the probe cache and marks all directory count entries
// as stale. Enqueues a single root-level recompute rather than one per
// directory (avoids pathological redundant recursive walks).
func (b *Browser) ClearCache() {
	// Swap probe cache (instant, existing behavior)
	b.cacheMu.Lock()
	b.cache = make(map[string]*ffmpeg.ProbeResult)
	b.cacheMu.Unlock()

	// Mark all count entries stale (including error state, which may recover)
	b.countCacheMu.Lock()
	for _, dc := range b.countCache {
		if dc.state == stateReady || dc.state == stateError {
			dc.state = stateStale
			dc.err = ""
		}
	}
	b.countCacheMu.Unlock()

	// Single root-level recompute refreshes all directories efficiently
	// via WarmCountCache-style walk (one walk, many updates)
	go b.WarmCountCache(context.Background())
}

// InvalidateCache removes a specific path from the probe cache and marks
// ancestor directory count entries as stale (triggering background recompute).
// Last-known values are preserved so the UI never shows blanks.
// Also transitions error-state ancestors (they may recover after a transcode).
func (b *Browser) InvalidateCache(path string) {
	b.cacheMu.Lock()
	delete(b.cache, path)
	b.cacheMu.Unlock()

	dir := filepath.Dir(path)
	b.countCacheMu.Lock()
	for b.isUnderRoot(dir) {
		if cached, ok := b.countCache[dir]; ok {
			if cached.state == stateReady || cached.state == stateError {
				cached.state = stateStale
				cached.err = ""
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	b.countCacheMu.Unlock()

	dir = filepath.Dir(path)
	for b.isUnderRoot(dir) {
		b.enqueueRecompute(dir)
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
}

// WarmCountCache pre-computes recursive video counts for all directories
// under the media root in a single pass. Captures full dirSig for each
// directory and prunes entries for directories that no longer exist.
// Pruning is skipped if the walk encountered any errors (safety guard
// for NAS timeouts or permission issues).
func (b *Browser) WarmCountCache(ctx context.Context) {
	start := time.Now()
	logger.Info("Warming directory count cache", "media_root", b.mediaRoot)

	dirCounts := make(map[string]*dirCount)
	var videoCount int
	var walkHadErrors bool

	_ = filepath.WalkDir(b.mediaRoot, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return filepath.SkipAll
		}
		if err != nil {
			walkHadErrors = true
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
			if _, ok := dirCounts[path]; !ok {
				dc := &dirCount{state: stateReady, updatedAt: start}
				if sig, sigErr := getDirSig(path); sigErr == nil {
					dc.sig = sig
				}
				dirCounts[path] = dc
			}
			return nil
		}
		if !ffmpeg.IsVideoFile(d.Name()) {
			return nil
		}

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

	if ctx.Err() == nil {
		// Populate cache in chunks to avoid holding the lock for too long
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

		// Prune entries for directories that no longer exist.
		// Safety guard: if the walk had ANY errors (permissions, NAS
		// timeout, etc.), skip pruning entirely. A transient filesystem
		// error must not cause false deletion of valid cache entries.
		if !walkHadErrors {
			b.countCacheMu.Lock()
			for path := range b.countCache {
				if _, visited := dirCounts[path]; !visited {
					delete(b.countCache, path)
				}
			}
			b.countCacheMu.Unlock()
		} else {
			logger.Warn("Skipping cache pruning due to walk errors")
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

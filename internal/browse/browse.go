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

// dirCount holds cached recursive video counts for a directory
type dirCount struct {
	fileCount int
	totalSize int64
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
	}
}

// normalizePath converts a path to an absolute path and ensures it's within the media root.
// If the path is outside the media root, it returns the media root instead.
func (b *Browser) normalizePath(path string) string {
	cleanPath, err := filepath.Abs(path)
	if err != nil {
		cleanPath = filepath.Clean(path)
	}
	if !strings.HasPrefix(cleanPath, b.mediaRoot) {
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

	// Process entries
	var wg sync.WaitGroup
	var mu sync.Mutex

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
			// Non-blocking: return cached counts instantly, or fire background
			// computation so counts appear on the next browse.
			b.countCacheMu.RLock()
			cached, isCached := b.countCache[entryPath]
			b.countCacheMu.RUnlock()

			if isCached {
				entry.FileCount = cached.fileCount
				entry.TotalSize = cached.totalSize
			} else {
				// Populate cache in background (detached context so it completes
				// even after this HTTP response is sent)
				go b.countVideos(context.Background(), entryPath)
			}
		} else if ffmpeg.IsVideoFile(e.Name()) {
			// For video files, get probe info (with caching)
			wg.Add(1)
			go func(entry *Entry) {
				defer wg.Done()
				if probeResult := b.getProbeResult(ctx, entry.Path); probeResult != nil {
					mu.Lock()
					entry.VideoInfo = probeResult
					entry.Size = probeResult.Size // Use probe size (more accurate)
					mu.Unlock()
				}
			}(entry)

			mu.Lock()
			result.VideoCount++
			result.TotalSize += info.Size()
			mu.Unlock()
		}

		mu.Lock()
		result.Entries = append(result.Entries, entry)
		mu.Unlock()
	}

	wg.Wait()

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

		dc := &dirCount{count, totalSize}
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
			// Check if original path was actually trying to access something outside
			absPath, _ := filepath.Abs(path)
			if !strings.HasPrefix(absPath, b.mediaRoot) {
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

// ClearCache clears the probe cache (useful after transcoding completes).
// Directory count cache is preserved since file counts don't change after transcoding.
func (b *Browser) ClearCache() {
	b.cacheMu.Lock()
	b.cache = make(map[string]*ffmpeg.ProbeResult)
	b.cacheMu.Unlock()
}

// InvalidateCache removes a specific path from the probe cache and clears
// directory count caches for all ancestor directories (since their recursive
// counts include this file).
func (b *Browser) InvalidateCache(path string) {
	b.cacheMu.Lock()
	delete(b.cache, path)
	b.cacheMu.Unlock()

	// Invalidate count cache for every ancestor directory up to media root.
	// Use path-boundary check to avoid matching e.g. /mnt/mediastuff when root is /mnt/media.
	rootPrefix := b.mediaRoot + string(os.PathSeparator)
	b.countCacheMu.Lock()
	dir := filepath.Dir(path)
	for dir == b.mediaRoot || strings.HasPrefix(dir, rootPrefix) {
		delete(b.countCache, dir)
		parent := filepath.Dir(dir)
		if parent == dir {
			break // reached filesystem root
		}
		dir = parent
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
	rootPrefix := b.mediaRoot + string(os.PathSeparator)
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
			if _, ok := dirCounts[path]; !ok {
				dirCounts[path] = &dirCount{}
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
		for dir == b.mediaRoot || strings.HasPrefix(dir, rootPrefix) {
			dc, ok := dirCounts[dir]
			if !ok {
				dc = &dirCount{}
				dirCounts[dir] = dc
			}
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

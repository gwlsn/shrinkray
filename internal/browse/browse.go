package browse

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gwlsn/shrinkray/internal/ffmpeg"
)

// ProgressCallback is called during file discovery to report progress
type ProgressCallback func(probed, total int)

// Entry represents a file or directory in the browser
type Entry struct {
	Name      string              `json:"name"`
	Path      string              `json:"path"`
	IsDir     bool                `json:"is_dir"`
	Size      int64               `json:"size"`
	ModTime   time.Time           `json:"mod_time"`
	VideoInfo *ffmpeg.ProbeResult `json:"video_info,omitempty"`
	FileCount int                 `json:"file_count,omitempty"` // For directories: number of video files
	TotalSize int64               `json:"total_size,omitempty"` // For directories: total size of video files
}

// BrowseResult contains the result of browsing a directory
type BrowseResult struct {
	Path       string   `json:"path"`
	Parent     string   `json:"parent,omitempty"`
	Entries    []*Entry `json:"entries"`
	VideoCount int      `json:"video_count"` // Total video files in this directory and subdirs
	TotalSize  int64    `json:"total_size"`  // Total size of video files
}

// dirInfo contains the file count and total file size for files contained in a directory
// computedAt is the time when the dirInfo was last computed
type dirInfo struct {
	fileCount  int
	totalSize  int64
	computedAt time.Time
}

type dirInfoKey struct {
	path      string
	recursive bool
}

// Browser handles file system browsing with video metadata
type Browser struct {
	prober    *ffmpeg.Prober
	mediaRoot string

	// Cache for probe results (path -> result)
	cacheMu sync.RWMutex
	cache   map[string]*ffmpeg.ProbeResult

	// Cache for dir info on a given directory (dir path -> dir info)
	dirInfoCacheMu sync.RWMutex
	dirInfoCache   map[dirInfoKey]dirInfo

	// Time in minutes before a dir info cache entry goes stale. A value of 0 will never go stale.
	dirInfoTTLMu sync.RWMutex
	dirInfoTTL   time.Duration
}

// NewBrowser creates a new Browser with the given prober and media root
func NewBrowser(prober *ffmpeg.Prober, mediaRoot string) *Browser {
	// Convert to absolute path for consistent comparisons
	absRoot, err := filepath.Abs(mediaRoot)
	if err != nil {
		absRoot = mediaRoot
	}
	return &Browser{
		prober:       prober,
		mediaRoot:    absRoot,
		cache:        make(map[string]*ffmpeg.ProbeResult),
		dirInfoCache: make(map[dirInfoKey]dirInfo),
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
func (b *Browser) Browse(ctx context.Context, path string, recursiveCount bool) (*BrowseResult, error) {
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
			// For directories, count video files
			entry.FileCount, entry.TotalSize = b.countVideos(entryPath, recursiveCount)
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

// countVideos counts video files in a directory
func (b *Browser) countVideos(dirPath string, recursive bool) (totalCount int, totalSize int64) {
	cacheKey := dirInfoKey{dirPath, recursive}
	if cached, ok := b.getDirInfo(cacheKey); ok {
		if !b.isStale(cached) {
			return cached.fileCount, cached.totalSize
		}
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return 0, 0
	}

	for _, e := range entries {
		if e.IsDir() {
			if recursive {
				subCount, subSize := b.countVideos(filepath.Join(dirPath, e.Name()), recursive)
				totalCount += subCount
				totalSize += subSize
			}

			continue
		}

		if ffmpeg.IsVideoFile(e.Name()) {
			totalCount++
			if info, err := e.Info(); err == nil {
				totalSize += info.Size()
			}
		}
	}

	b.setDirInfo(cacheKey, totalCount, totalSize)
	return totalCount, totalSize
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

	var results []*ffmpeg.ProbeResult
	var mu sync.Mutex
	var wg sync.WaitGroup
	var probed int64

	for _, filePath := range videoPaths {
		wg.Add(1)
		go func(fp string) {
			defer wg.Done()

			// Acquire semaphore slot (limits concurrent probes)
			sem <- struct{}{}
			defer func() { <-sem }()

			if result := b.getProbeResult(ctx, fp); result != nil {
				mu.Lock()
				results = append(results, result)
				mu.Unlock()
			}
			// Report progress after each probe completes
			current := atomic.AddInt64(&probed, 1)
			if onProgress != nil {
				onProgress(int(current), total)
			}
		}(filePath)
	}

	wg.Wait()

	// Sort by path for consistent ordering
	sort.Slice(results, func(i, j int) bool {
		return results[i].Path < results[j].Path
	})

	return results, nil
}

func (b *Browser) isStale(info dirInfo) bool {
	ttl := b.getDirInfoTTL()

	if ttl <= 0 { // 0 = never auto refresh
		return false
	}

	return time.Since(info.computedAt) > ttl
}

func (b *Browser) getDirInfo(key dirInfoKey) (dirInfo, bool) {
	b.dirInfoCacheMu.RLock()
	v, ok := b.dirInfoCache[key]
	b.dirInfoCacheMu.RUnlock()

	return v, ok
}

func (b *Browser) setDirInfo(key dirInfoKey, count int, size int64) {
	b.dirInfoCacheMu.Lock()
	b.dirInfoCache[key] = dirInfo{
		fileCount:  count,
		totalSize:  size,
		computedAt: time.Now(),
	}

	b.dirInfoCacheMu.Unlock()
}

func (b *Browser) getDirInfoTTL() time.Duration {
	b.dirInfoTTLMu.RLock()
	ttl := b.dirInfoTTL
	b.dirInfoTTLMu.RUnlock()

	return ttl
}

func (b *Browser) SetDirInfoTTLMinutes(minutes int) {
	if minutes < 0 {
		minutes = 0
	}

	b.dirInfoTTLMu.Lock()
	b.dirInfoTTL = time.Duration(minutes) * time.Minute
	b.dirInfoTTLMu.Unlock()
}

// ClearCache clears the probe cache (useful after transcoding completes)
func (b *Browser) ClearCache() {
	b.cacheMu.Lock()
	b.cache = make(map[string]*ffmpeg.ProbeResult)
	b.cacheMu.Unlock()

	b.dirInfoCacheMu.Lock()
	b.dirInfoCache = make(map[dirInfoKey]dirInfo)
	b.dirInfoCacheMu.Unlock()
}

// InvalidateCache removes a specific path from the cache
func (b *Browser) InvalidateCache(path string) {
	b.cacheMu.Lock()
	delete(b.cache, path)
	b.cacheMu.Unlock()

	b.dirInfoCacheMu.Lock()
	delete(b.dirInfoCache, dirInfoKey{path, true})
	delete(b.dirInfoCache, dirInfoKey{path, false})
	b.dirInfoCacheMu.Unlock()
}

// ProbeFile probes a single file and returns its metadata
func (b *Browser) ProbeFile(ctx context.Context, path string) (*ffmpeg.ProbeResult, error) {
	result, err := b.prober.Probe(ctx, path)
	if err != nil {
		return nil, err
	}
	return result, nil
}

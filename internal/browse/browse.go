package browse

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gwlsn/shrinkray/internal/ffmpeg"
)

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
}

// NewBrowser creates a new Browser with the given prober and media root
func NewBrowser(prober *ffmpeg.Prober, mediaRoot string) *Browser {
	// Convert to absolute path for consistent comparisons
	absRoot, err := filepath.Abs(mediaRoot)
	if err != nil {
		absRoot = mediaRoot
	}
	return &Browser{
		prober:    prober,
		mediaRoot: absRoot,
		cache:     make(map[string]*ffmpeg.ProbeResult),
	}
}

// Browse returns the contents of a directory
func (b *Browser) Browse(ctx context.Context, path string) (*BrowseResult, error) {
	// Convert to absolute path for consistent comparisons
	cleanPath, err := filepath.Abs(path)
	if err != nil {
		cleanPath = filepath.Clean(path)
	}

	// Ensure path is within media root
	if !strings.HasPrefix(cleanPath, b.mediaRoot) {
		cleanPath = b.mediaRoot
	}

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
			// For directories, count video files (non-recursive for speed)
			entry.FileCount, entry.TotalSize = b.countVideos(entryPath)
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

// countVideos counts video files in a directory recursively
func (b *Browser) countVideos(dirPath string) (count int, totalSize int64) {
	filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors
		}
		// Skip hidden files and directories
		if strings.HasPrefix(info.Name(), ".") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if ffmpeg.IsVideoFile(path) {
			count++
			totalSize += info.Size()
		}
		return nil
	})
	return count, totalSize
}

// getProbeResult returns a cached or fresh probe result
func (b *Browser) getProbeResult(ctx context.Context, path string) *ffmpeg.ProbeResult {
	// Check cache
	b.cacheMu.RLock()
	if result, ok := b.cache[path]; ok {
		b.cacheMu.RUnlock()
		return result
	}
	b.cacheMu.RUnlock()

	// Probe the file
	result, err := b.prober.Probe(ctx, path)
	if err != nil {
		return nil
	}

	// Cache the result
	b.cacheMu.Lock()
	b.cache[path] = result
	b.cacheMu.Unlock()

	return result
}

// GetVideoFilesOptions controls how directories are traversed when finding video files
type GetVideoFilesOptions struct {
	// Recursive controls whether to search subdirectories.
	// If false, only files in the immediate directory are included.
	Recursive bool

	// MaxDepth limits how deep to recurse into subdirectories.
	// nil means unlimited depth.
	// 0 means only the current directory (same as Recursive=false).
	// 1 means current directory plus one level of subdirectories.
	// Only used when Recursive is true.
	MaxDepth *int
}

// GetVideoFiles returns all video files in the given paths (files or directories)
// For directories, it recursively finds all video files (backwards-compatible version)
func (b *Browser) GetVideoFiles(ctx context.Context, paths []string) ([]*ffmpeg.ProbeResult, error) {
	// Default to recursive with unlimited depth for backwards compatibility
	return b.GetVideoFilesWithOptions(ctx, paths, GetVideoFilesOptions{Recursive: true, MaxDepth: nil})
}

// GetVideoFilesWithOptions returns all video files in the given paths with recursion control
func (b *Browser) GetVideoFilesWithOptions(ctx context.Context, paths []string, opts GetVideoFilesOptions) ([]*ffmpeg.ProbeResult, error) {
	var results []*ffmpeg.ProbeResult
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, path := range paths {
		// Convert to absolute path for consistent comparisons
		cleanPath, err := filepath.Abs(path)
		if err != nil {
			cleanPath = filepath.Clean(path)
		}

		// Ensure path is within media root
		if !strings.HasPrefix(cleanPath, b.mediaRoot) {
			continue
		}

		info, err := os.Stat(cleanPath)
		if err != nil {
			continue
		}

		if info.IsDir() {
			// Find video files with recursion control
			videoPaths, err := b.discoverMediaFiles(cleanPath, opts.Recursive, opts.MaxDepth)
			if err != nil {
				return nil, err
			}

			for _, fp := range videoPaths {
				wg.Add(1)
				go func(filePath string) {
					defer wg.Done()
					if result := b.getProbeResult(ctx, filePath); result != nil {
						mu.Lock()
						results = append(results, result)
						mu.Unlock()
					}
				}(fp)
			}
		} else if ffmpeg.IsVideoFile(cleanPath) {
			wg.Add(1)
			go func(fp string) {
				defer wg.Done()
				if result := b.getProbeResult(ctx, fp); result != nil {
					mu.Lock()
					results = append(results, result)
					mu.Unlock()
				}
			}(cleanPath)
		}
	}

	wg.Wait()

	// Sort by path for consistent ordering
	sort.Slice(results, func(i, j int) bool {
		return results[i].Path < results[j].Path
	})

	return results, nil
}

// discoverMediaFiles finds all video files in a directory with recursion control.
// If recursive is false, only files in the immediate directory are returned.
// If maxDepth is set, it limits how deep to recurse (0 = current only, 1 = one level, nil = unlimited).
// Returns paths sorted for deterministic ordering.
func (b *Browser) discoverMediaFiles(root string, recursive bool, maxDepth *int) ([]string, error) {
	var paths []string

	// Verify the directory exists
	if _, err := os.Stat(root); err != nil {
		return nil, err
	}

	// If not recursive, just read the immediate directory
	if !recursive || (maxDepth != nil && *maxDepth == 0) {
		entries, err := os.ReadDir(root)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			// Skip hidden files
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			filePath := filepath.Join(root, e.Name())
			if ffmpeg.IsVideoFile(filePath) {
				paths = append(paths, filePath)
			}
		}
		sort.Strings(paths)
		return paths, nil
	}

	// Recursive walk with optional depth limit
	err := filepath.Walk(root, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors
		}

		// Skip hidden files and directories
		if strings.HasPrefix(info.Name(), ".") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Check depth limit if set
		if maxDepth != nil && info.IsDir() && filePath != root {
			// Calculate depth relative to root
			relPath, err := filepath.Rel(root, filePath)
			if err == nil {
				depth := len(strings.Split(relPath, string(filepath.Separator)))
				if depth > *maxDepth {
					return filepath.SkipDir
				}
			}
		}

		if info.IsDir() {
			return nil
		}

		if ffmpeg.IsVideoFile(filePath) {
			paths = append(paths, filePath)
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	sort.Strings(paths)
	return paths, nil
}

// DiscoverMediaFiles is a public wrapper for discovering media files with recursion control.
// This is useful for testing and direct access from other packages.
func DiscoverMediaFiles(root string, recursive bool, maxDepth *int) ([]string, error) {
	// Create a temporary browser just for discovery (no prober or media root needed for this)
	b := &Browser{mediaRoot: root}
	return b.discoverMediaFiles(root, recursive, maxDepth)
}

// ClearCache clears the probe cache (useful after transcoding completes)
func (b *Browser) ClearCache() {
	b.cacheMu.Lock()
	b.cache = make(map[string]*ffmpeg.ProbeResult)
	b.cacheMu.Unlock()
}

// InvalidateCache removes a specific path from the cache
func (b *Browser) InvalidateCache(path string) {
	b.cacheMu.Lock()
	delete(b.cache, path)
	b.cacheMu.Unlock()
}

// ProbeFile probes a single file and returns its metadata
func (b *Browser) ProbeFile(ctx context.Context, path string) (*ffmpeg.ProbeResult, error) {
	result, err := b.prober.Probe(ctx, path)
	if err != nil {
		return nil, err
	}
	return result, nil
}

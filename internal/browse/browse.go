package browse

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/graysonwilson/shrinkray/internal/ffmpeg"
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

// countVideos counts video files in a directory (non-recursive for speed)
func (b *Browser) countVideos(dirPath string) (count int, totalSize int64) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return 0, 0
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if ffmpeg.IsVideoFile(e.Name()) {
			count++
			if info, err := e.Info(); err == nil {
				totalSize += info.Size()
			}
		}
	}
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

// GetVideoFiles returns all video files in the given paths (files or directories)
// For directories, it recursively finds all video files
func (b *Browser) GetVideoFiles(ctx context.Context, paths []string) ([]*ffmpeg.ProbeResult, error) {
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
			// Recursively find video files
			err := filepath.Walk(cleanPath, func(filePath string, info os.FileInfo, err error) error {
				if err != nil {
					return nil // Skip errors
				}
				if info.IsDir() {
					return nil
				}
				if !ffmpeg.IsVideoFile(filePath) {
					return nil
				}

				wg.Add(1)
				go func(fp string) {
					defer wg.Done()
					if result := b.getProbeResult(ctx, fp); result != nil {
						mu.Lock()
						results = append(results, result)
						mu.Unlock()
					}
				}(filePath)

				return nil
			})
			if err != nil {
				return nil, err
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

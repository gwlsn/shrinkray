package browse

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gwlsn/shrinkray/internal/ffmpeg"
)

func BenchmarkCountVideosRecursiveLarge(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping large benchmark in short mode")
	}

	root := b.TempDir()

	// Tune these, 50 * 200 = 10,000 files
	numDirs := 50
	filesPerDir := 200

	// Build a tree
	for i := 0; i < numDirs; i++ {
		dir := filepath.Join(root, "dir", strconv.Itoa(i))
		if err := os.MkdirAll(dir, 0755); err != nil {
			b.Fatalf("mkdir: %v", err)
		}
		for f := 0; f < filesPerDir; f++ {
			// small files to avoid disk bloat
			name := filepath.Join(dir, fmt.Sprintf("f%d.mkv", f))
			if err := os.WriteFile(name, []byte("x"), 0644); err != nil {
				b.Fatalf("write: %v", err)
			}
		}
	}

	browser := NewBrowser(ffmpeg.NewProber("ffprobe"), root)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		browser.ClearCache()
		_, _, _ = browser.countVideos(root, true)
	}
}

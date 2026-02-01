package vmaf

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gwlsn/shrinkray/internal/logger"
)

// lastLines returns the last n non-empty lines from output
func lastLines(output string, n int) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// Sample represents an extracted video sample
type Sample struct {
	Path     string        // Path to extracted sample file
	Position time.Duration // Position in source video
	Duration time.Duration // Sample duration
}

// SamplePositions returns the 3 fixed positions to sample.
// Positions: 25%, 50%, 75% of video duration.
// Using 3 longer samples (20s each) provides better representation than 5 short ones,
// matching the approach used by ab-av1.
func SamplePositions(videoDuration time.Duration) []float64 {
	seconds := videoDuration.Seconds()

	// Handle zero/negative duration
	if seconds <= 0 {
		return []float64{0.5}
	}

	// Very short videos (<60s): single sample at 50%
	if seconds < 60 {
		return []float64{0.5}
	}

	// All other videos: 3 samples at fixed positions
	return []float64{0.25, 0.50, 0.75}
}

// SampleDuration is the fixed duration for each sample (20 seconds).
// Longer samples provide more representative quality measurement.
const SampleDuration = 20

// ExtractSamples extracts video samples at specified positions using stream copy.
// This is fast (remux only, no decode/encode) but results in keyframe-aligned cuts.
// Tonemapping is NOT applied here - it's handled during VMAF scoring instead.
func ExtractSamples(ctx context.Context, ffmpegPath, inputPath, tempDir string,
	videoDuration time.Duration, positions []float64) ([]*Sample, error) {

	samples := make([]*Sample, 0, len(positions))

	for i, pos := range positions {
		startTime := time.Duration(float64(videoDuration) * pos)

		// Ensure we don't go past end of video
		if startTime+time.Duration(SampleDuration)*time.Second > videoDuration {
			startTime = videoDuration - time.Duration(SampleDuration)*time.Second
			if startTime < 0 {
				startTime = 0
			}
		}

		samplePath := filepath.Join(tempDir, fmt.Sprintf("sample_%d.mkv", i))

		// Stream copy extraction - fast, keyframe-aligned
		args := []string{
			"-ss", fmt.Sprintf("%.3f", startTime.Seconds()),
			"-i", inputPath,
			"-t", fmt.Sprintf("%d", SampleDuration),
			"-c:v", "copy",
			"-an", "-sn",
			"-y",
			samplePath,
		}

		// Run with low CPU priority so VMAF analysis yields to other processes
		niceArgs := append([]string{"-n", "19", ffmpegPath}, args...)
		cmd := exec.CommandContext(ctx, "nice", niceArgs...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			logger.Error("FFmpeg sample extraction failed", "sample", i, "error", err, "stderr", lastLines(string(output), 5))
			// Clean up any created samples
			for _, s := range samples {
				os.Remove(s.Path)
			}
			return nil, fmt.Errorf("failed to extract sample %d: %w (%s)", i, err, lastLines(string(output), 3))
		}

		samples = append(samples, &Sample{
			Path:     samplePath,
			Position: startTime,
			Duration: time.Duration(SampleDuration) * time.Second,
		})
	}

	return samples, nil
}

// CleanupSamples removes all sample files
func CleanupSamples(samples []*Sample) {
	for _, s := range samples {
		os.Remove(s.Path)
	}
}

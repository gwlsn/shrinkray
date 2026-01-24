package vmaf

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Sample represents an extracted video sample
type Sample struct {
	Path     string        // Path to extracted sample file
	Position time.Duration // Position in source video
	Duration time.Duration // Sample duration
}

// SamplePositions returns the positions to sample based on video duration
func SamplePositions(videoDuration time.Duration, fastMode bool) []float64 {
	seconds := videoDuration.Seconds()

	// Handle zero/negative duration
	if seconds <= 0 {
		return []float64{0.5}
	}

	// Very short videos (<15s): single sample at 50%
	if seconds < 15 {
		return []float64{0.5}
	}

	// Short videos (15-30s): always full analysis (too short to risk fast mode)
	if seconds < 30 {
		return []float64{0.25, 0.5, 0.75}
	}

	// Normal videos (30s+): respect fast mode setting
	if fastMode {
		return []float64{0.5}
	}

	return []float64{0.25, 0.5, 0.75}
}

// ExtractSamples extracts video samples at specified positions
func ExtractSamples(ctx context.Context, ffmpegPath, inputPath, tempDir string,
	videoDuration time.Duration, sampleDuration int, positions []float64) ([]*Sample, error) {

	samples := make([]*Sample, 0, len(positions))

	for i, pos := range positions {
		startTime := time.Duration(float64(videoDuration) * pos)

		// Ensure we don't go past end of video
		if startTime+time.Duration(sampleDuration)*time.Second > videoDuration {
			startTime = videoDuration - time.Duration(sampleDuration)*time.Second
			if startTime < 0 {
				startTime = 0
			}
		}

		samplePath := filepath.Join(tempDir, fmt.Sprintf("sample_%d.mkv", i))

		// Extract sample as lossless FFV1 for accurate VMAF comparison
		args := []string{
			"-ss", fmt.Sprintf("%.3f", startTime.Seconds()),
			"-i", inputPath,
			"-t", fmt.Sprintf("%d", sampleDuration),
			"-c:v", "ffv1",
			"-an", "-sn", // No audio or subtitles
			"-y",
			samplePath,
		}

		cmd := exec.CommandContext(ctx, ffmpegPath, args...)
		if err := cmd.Run(); err != nil {
			// Clean up any created samples
			for _, s := range samples {
				os.Remove(s.Path)
			}
			return nil, fmt.Errorf("failed to extract sample %d: %w", i, err)
		}

		samples = append(samples, &Sample{
			Path:     samplePath,
			Position: startTime,
			Duration: time.Duration(sampleDuration) * time.Second,
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

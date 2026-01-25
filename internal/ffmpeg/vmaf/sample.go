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
// When tonemap is provided and enabled, samples are tonemapped from HDR to SDR
// so that VMAF comparison is done in the same color space as the encoded output.
func ExtractSamples(ctx context.Context, ffmpegPath, inputPath, tempDir string,
	videoDuration time.Duration, sampleDuration int, positions []float64,
	tonemap *TonemapConfig) ([]*Sample, error) {

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

		// Build FFmpeg args for sample extraction
		args := []string{
			"-ss", fmt.Sprintf("%.3f", startTime.Seconds()),
			"-i", inputPath,
			"-t", fmt.Sprintf("%d", sampleDuration),
		}

		// Apply tonemapping filter if enabled (for HDR content)
		// This ensures reference samples match the color space of encoded output
		if tonemap != nil && tonemap.Enabled {
			algorithm := tonemap.Algorithm
			if algorithm == "" {
				algorithm = "hable"
			}
			// HDR to SDR tonemapping pipeline:
			// 1. Convert to linear light with nominal peak luminance
			// 2. Convert to float for precision
			// 3. Convert primaries to bt709
			// 4. Apply tonemap algorithm
			// 5. Set bt709 transfer and matrix
			// 6. Output as 8-bit yuv420p for SDR
			tonemapFilter := fmt.Sprintf(
				"zscale=t=linear:npl=100,format=gbrpf32le,zscale=p=bt709,tonemap=%s:desat=0:peak=100,zscale=t=bt709:m=bt709,format=yuv420p",
				algorithm,
			)
			args = append(args, "-vf", tonemapFilter)
		}

		// Extract as lossless FFV1 for accurate VMAF comparison
		args = append(args,
			"-c:v", "ffv1",
			"-an", "-sn", // No audio or subtitles
			"-y",
			samplePath,
		)

		cmd := exec.CommandContext(ctx, ffmpegPath, args...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			// Log full output for debugging, return truncated error
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

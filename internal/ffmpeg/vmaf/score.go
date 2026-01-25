package vmaf

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/gwlsn/shrinkray/internal/logger"
)

// Score calculates the VMAF score between reference and distorted videos
func Score(ctx context.Context, ffmpegPath, referencePath, distortedPath string, height int) (float64, error) {
	model := SelectModel(height)

	// Build VMAF filter
	// Input order: [0:v] = distorted (encoded), [1:v] = reference (original)
	// libvmaf compares distorted against reference
	// Use /dev/stdout for log_path as some FFmpeg builds don't support "-"
	vmafFilter := fmt.Sprintf("[0:v][1:v]libvmaf=model=version=%s:log_fmt=json:log_path=/dev/stdout", model)

	args := []string{
		"-i", distortedPath, // Input 0: distorted/encoded sample
		"-i", referencePath, // Input 1: reference/original sample
		"-filter_complex", vmafFilter,
		"-f", "null", "-",
	}

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Error("VMAF scoring failed", "error", err, "stderr", lastLines(string(output), 5))
		return 0, fmt.Errorf("VMAF scoring failed: %w (%s)", err, lastLines(string(output), 3))
	}

	return parseVMAFScore(string(output))
}

// parseVMAFScore extracts the VMAF score from FFmpeg output
func parseVMAFScore(output string) (float64, error) {
	// Look for "VMAF score: XX.XX" or "vmaf.*mean.*: XX.XX" patterns
	patterns := []string{
		`VMAF score:\s*([\d.]+)`,
		`"vmaf"[^}]*"mean":\s*([\d.]+)`,
		`vmaf_v.*mean:\s*([\d.]+)`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindStringSubmatch(output)
		if len(matches) >= 2 {
			score, err := strconv.ParseFloat(strings.TrimSpace(matches[1]), 64)
			if err == nil {
				return score, nil
			}
		}
	}

	return 0, fmt.Errorf("could not parse VMAF score from output")
}

// ScoreSamples calculates VMAF for multiple sample pairs and returns the minimum
func ScoreSamples(ctx context.Context, ffmpegPath string, referenceSamples, distortedSamples []*Sample, height int) (float64, error) {
	if len(referenceSamples) != len(distortedSamples) {
		return 0, fmt.Errorf("sample count mismatch: %d vs %d", len(referenceSamples), len(distortedSamples))
	}

	minScore := 100.0

	for i := range referenceSamples {
		score, err := Score(ctx, ffmpegPath, referenceSamples[i].Path, distortedSamples[i].Path, height)
		if err != nil {
			return 0, fmt.Errorf("scoring sample %d: %w", i, err)
		}
		if score < minScore {
			minScore = score
		}
	}

	return minScore, nil
}

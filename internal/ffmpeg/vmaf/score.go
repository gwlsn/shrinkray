package vmaf

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/gwlsn/shrinkray/internal/logger"
)

// SetMaxConcurrentAnalyses logs the configured concurrent analysis limit.
// Thread count per analysis is fixed at ~50% CPU (numCPU/2) regardless of this setting.
// Multiple concurrent analyses can stack to use more total CPU.
func SetMaxConcurrentAnalyses(n int) {
	if n < 1 {
		n = 1
	}
	if n > 3 {
		n = 3
	}
	logger.Info("VMAF concurrent analyses configured", "max_analyses", n, "threads_per_analysis", GetThreadCount())
}

// GetThreadCount returns the number of threads each VMAF process should use.
// Uses numCPU/2 to limit decoders and filters to ~50% CPU.
// Note: Software encoders (x265, svtav1) ignore this and use all cores.
func GetThreadCount() int {
	numThreads := runtime.NumCPU() / 2
	if numThreads < 1 {
		numThreads = 1
	}
	return numThreads
}

// Score calculates the VMAF score between reference and distorted videos
func Score(ctx context.Context, ffmpegPath, referencePath, distortedPath string, height int) (float64, error) {
	model := SelectModel(height)

	// Get thread count based on configured max concurrent analyses
	// This targets ~50% total CPU usage across all concurrent VMAF processes
	numThreads := GetThreadCount()

	// Build VMAF filter
	// Input order: [0:v] = distorted (encoded), [1:v] = reference (original)
	// libvmaf compares distorted against reference
	// Use /dev/stdout for log_path as some FFmpeg builds don't support "-"
	vmafFilter := fmt.Sprintf("[0:v][1:v]libvmaf=model=version=%s:n_threads=%d:log_fmt=json:log_path=/dev/stdout", model, numThreads)

	args := []string{
		"-threads", fmt.Sprintf("%d", numThreads),        // Limit decoder threads
		"-filter_threads", fmt.Sprintf("%d", numThreads), // Limit filter graph threads
		"-i", distortedPath,                              // Input 0: distorted/encoded sample
		"-i", referencePath,                              // Input 1: reference/original sample
		"-filter_complex", vmafFilter,
		"-f", "null", "-",
	}

	// Run with low CPU priority so VMAF analysis yields to other processes
	niceArgs := append([]string{"-n", "19", ffmpegPath}, args...)
	cmd := exec.CommandContext(ctx, "nice", niceArgs...)
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

// trimmedMean calculates the trimmed mean of VMAF scores.
// Drops the highest and lowest scores, averages the rest.
// For 1-2 scores, returns simple average. For 3 scores, returns median.
func trimmedMean(scores []float64) float64 {
	if len(scores) == 0 {
		return 0
	}
	if len(scores) == 1 {
		return scores[0]
	}
	if len(scores) == 2 {
		return (scores[0] + scores[1]) / 2
	}

	// Sort a copy to avoid modifying original
	sorted := make([]float64, len(scores))
	copy(sorted, scores)
	sort.Float64s(sorted)

	// Drop lowest and highest, average the rest
	sum := 0.0
	for i := 1; i < len(sorted)-1; i++ {
		sum += sorted[i]
	}
	return sum / float64(len(sorted)-2)
}

// ScoreSamples calculates VMAF for multiple sample pairs and returns the trimmed mean.
// Drops the highest and lowest scores, averages the middle scores.
// This is more robust than minimum (too conservative) or average (ignores outliers).
func ScoreSamples(ctx context.Context, ffmpegPath string, referenceSamples, distortedSamples []*Sample, height int) (float64, error) {
	if len(referenceSamples) != len(distortedSamples) {
		return 0, fmt.Errorf("sample count mismatch: %d vs %d", len(referenceSamples), len(distortedSamples))
	}

	scores := make([]float64, 0, len(referenceSamples))

	for i := range referenceSamples {
		score, err := Score(ctx, ffmpegPath, referenceSamples[i].Path, distortedSamples[i].Path, height)
		if err != nil {
			return 0, fmt.Errorf("scoring sample %d: %w", i, err)
		}
		logger.Debug("Sample VMAF score", "sample", i, "score", score)
		scores = append(scores, score)
	}

	result := trimmedMean(scores)
	logger.Info("VMAF trimmed mean", "scores", scores, "result", result)
	return result, nil
}

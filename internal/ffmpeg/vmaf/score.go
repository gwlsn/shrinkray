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

// buildSDRScoringFilter creates a filtergraph for SDR VMAF comparison.
// Both legs are normalized to yuv420p before libvmaf.
func buildSDRScoringFilter(model string, threads int) string {
	return fmt.Sprintf(
		"[0:v]format=yuv420p[dist];[1:v]format=yuv420p[ref];"+
			"[dist][ref]libvmaf=model=version=%s:n_threads=%d:log_fmt=json:log_path=/dev/stdout",
		model, threads)
}

// buildHDRScoringFilter creates a filtergraph for HDR VMAF comparison.
// The reference leg is tonemapped from HDR to SDR to match the distorted leg.
// Explicit color metadata ensures correct HDR interpretation.
func buildHDRScoringFilter(model string, threads int, algorithm string) string {
	// Distorted is already SDR (tonemapped during encoding)
	// Reference is HDR, needs tonemapping before comparison
	return fmt.Sprintf(
		"[0:v]format=yuv420p[dist];"+
			"[1:v]zscale=pin=bt2020:tin=smpte2084:min=bt2020nc:t=linear:npl=1000,"+
			"format=gbrpf32le,"+
			"zscale=p=bt709:t=bt709:m=bt709,"+
			"tonemap=%s:desat=0:peak=100,"+
			"format=yuv420p[ref];"+
			"[dist][ref]libvmaf=model=version=%s:n_threads=%d:log_fmt=json:log_path=/dev/stdout",
		algorithm, model, threads)
}

// SetMaxConcurrentAnalyses configures the concurrent analysis limit and returns the clamped value.
// Thread count per analysis is fixed at ~50% CPU (numCPU/2) regardless of this setting.
// Multiple concurrent analyses can stack to use more total CPU.
// Note: This currently only validates/clamps the value; actual limiting happens elsewhere.
func SetMaxConcurrentAnalyses(n int) int {
	if n < 1 {
		n = 1
	}
	if n > 3 {
		n = 3
	}
	return n
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

// Score calculates the VMAF score between reference and distorted videos.
// When tonemap is provided and enabled, the reference is tonemapped from HDR to SDR.
func Score(ctx context.Context, ffmpegPath, referencePath, distortedPath string, height int, tonemap *TonemapConfig) (float64, error) {
	model := SelectModel(height)
	numThreads := GetThreadCount()

	// Build appropriate filtergraph based on HDR/SDR
	var filterComplex string
	if tonemap != nil && tonemap.Enabled {
		algorithm := tonemap.Algorithm
		if algorithm == "" {
			algorithm = "hable"
		}
		filterComplex = buildHDRScoringFilter(model, numThreads, algorithm)
	} else {
		filterComplex = buildSDRScoringFilter(model, numThreads)
	}

	args := []string{
		"-threads", fmt.Sprintf("%d", numThreads),
		"-filter_threads", fmt.Sprintf("%d", numThreads),
		"-i", distortedPath,
		"-i", referencePath,
		"-filter_complex", filterComplex,
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
// When tonemap is provided and enabled, references are tonemapped from HDR to SDR.
func ScoreSamples(ctx context.Context, ffmpegPath string, referenceSamples, distortedSamples []*Sample, height int, tonemap *TonemapConfig) (float64, error) {
	if len(referenceSamples) != len(distortedSamples) {
		return 0, fmt.Errorf("sample count mismatch: %d vs %d", len(referenceSamples), len(distortedSamples))
	}

	scores := make([]float64, 0, len(referenceSamples))

	for i := range referenceSamples {
		score, err := Score(ctx, ffmpegPath, referenceSamples[i].Path, distortedSamples[i].Path, height, tonemap)
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

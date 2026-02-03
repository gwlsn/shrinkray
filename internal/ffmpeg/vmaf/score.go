package vmaf

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/gwlsn/shrinkray/internal/logger"
	"golang.org/x/sync/errgroup"
)

// buildSDRScoringFilter creates a filtergraph for SDR VMAF comparison.
// Both legs are normalized to yuv420p before libvmaf.
// Score is extracted from FFmpeg's stderr summary line (no JSON logging needed).
func buildSDRScoringFilter(model string, threads int) string {
	return fmt.Sprintf(
		"[0:v]format=yuv420p[dist];[1:v]format=yuv420p[ref];"+
			"[dist][ref]libvmaf=model=version=%s:n_threads=%d",
		model, threads)
}

// buildHDRScoringFilter creates a filtergraph for HDR VMAF comparison.
// The reference leg is tonemapped from HDR to SDR to match the distorted leg.
// Explicit color metadata ensures correct HDR interpretation.
//
// Pipeline order (tonemap requires linear light input):
// 1. Linearize from PQ/HLG with explicit HDR metadata (inputTransfer determines tin=)
// 2. Convert to float format for precision
// 3. Convert primaries to bt709 (color space, still linear)
// 4. Apply tonemap algorithm (operates on linear light)
// 5. Apply bt709 transfer curve and matrix (gamma correction)
// 6. Convert to yuv420p for VMAF
//
// inputTransfer should be "smpte2084" for HDR10/DV or "arib-std-b67" for HLG.
// Falls back to "smpte2084" if empty or unknown.
func buildHDRScoringFilter(model string, threads int, algorithm, inputTransfer string) string {
	// Validate and normalize inputTransfer
	// Known values: smpte2084 (HDR10, DV Profile 8), arib-std-b67 (HLG)
	switch inputTransfer {
	case "smpte2084", "arib-std-b67":
		// Valid, use as-is
	default:
		// Unknown or empty, default to PQ (most common HDR format)
		inputTransfer = "smpte2084"
	}

	// Distorted is already SDR (tonemapped during encoding)
	// Reference is HDR, needs tonemapping before comparison
	// Score is extracted from FFmpeg's stderr summary line (no JSON logging needed).
	return fmt.Sprintf(
		"[0:v]format=yuv420p[dist];"+
			"[1:v]zscale=pin=bt2020:tin=%s:min=bt2020nc:t=linear:npl=1000,"+
			"format=gbrpf32le,"+
			"zscale=p=bt709,"+
			"tonemap=%s:desat=0:peak=100,"+
			"zscale=t=bt709:m=bt709,"+
			"format=yuv420p[ref];"+
			"[dist][ref]libvmaf=model=version=%s:n_threads=%d",
		inputTransfer, algorithm, model, threads)
}

// MaxScoreWorkers is the maximum number of concurrent VMAF scoring workers.
// Matches the number of samples (3) from SamplePositions.
const MaxScoreWorkers = 3

// getThreadsPerWorker calculates threads per scoring worker based on available CPU.
// Uses GOMAXPROCS (container-aware in Go 1.21+) divided by worker count.
// This distributes CPU evenly across concurrent scorers without oversubscription.
func getThreadsPerWorker(workers int) int {
	procs := runtime.GOMAXPROCS(0)
	threads := procs / workers
	if threads < 1 {
		threads = 1
	}
	return threads
}

// GetEncodingThreads returns the number of threads for sample encoding during VMAF search.
// Uses GOMAXPROCS for full CPU utilization since sample encoding is sequential.
func GetEncodingThreads() int {
	procs := runtime.GOMAXPROCS(0)
	if procs < 1 {
		procs = 1
	}
	return procs
}

// Score calculates the VMAF score between reference and distorted videos.
// When tonemap is provided and enabled, the reference is tonemapped from HDR to SDR.
// The threads parameter controls parallelism for FFmpeg and libvmaf.
func Score(ctx context.Context, ffmpegPath, referencePath, distortedPath string, height, threads int, tonemap *TonemapConfig) (float64, error) {
	model := SelectModel(height)

	// Build appropriate filtergraph based on HDR/SDR
	var filterComplex string
	if tonemap != nil && tonemap.Enabled {
		algorithm := tonemap.Algorithm
		if algorithm == "" {
			algorithm = "hable"
		}
		filterComplex = buildHDRScoringFilter(model, threads, algorithm, tonemap.InputTransfer)
	} else {
		filterComplex = buildSDRScoringFilter(model, threads)
	}

	args := []string{
		"-threads", fmt.Sprintf("%d", threads),
		"-filter_threads", fmt.Sprintf("%d", threads),
		"-i", distortedPath,
		"-i", referencePath,
		"-filter_complex", filterComplex,
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

// averageScores returns the mean of VMAF scores.
func averageScores(scores []float64) float64 {
	if len(scores) == 0 {
		return 0
	}
	sum := 0.0
	for _, s := range scores {
		sum += s
	}
	return sum / float64(len(scores))
}

// ScoreSamples calculates VMAF for multiple sample pairs concurrently and returns the average.
// Samples are scored in parallel (up to MaxScoreWorkers) with threads distributed evenly.
// When tonemap is provided and enabled, references are tonemapped from HDR to SDR.
func ScoreSamples(ctx context.Context, ffmpegPath string, referenceSamples, distortedSamples []*Sample, height int, tonemap *TonemapConfig) (float64, error) {
	if len(referenceSamples) != len(distortedSamples) {
		return 0, fmt.Errorf("sample count mismatch: %d vs %d", len(referenceSamples), len(distortedSamples))
	}

	numSamples := len(referenceSamples)
	workers := min(numSamples, MaxScoreWorkers)
	threadsPerWorker := getThreadsPerWorker(workers)

	logger.Debug("Scoring samples concurrently",
		"samples", numSamples,
		"workers", workers,
		"threadsPerWorker", threadsPerWorker,
		"gomaxprocs", runtime.GOMAXPROCS(0))

	// Pre-allocate results slice to collect scores by index (preserves ordering)
	scores := make([]float64, numSamples)

	g, gctx := errgroup.WithContext(ctx)

	for i := range referenceSamples {
		g.Go(func() error {
			score, err := Score(gctx, ffmpegPath, referenceSamples[i].Path, distortedSamples[i].Path, height, threadsPerWorker, tonemap)
			if err != nil {
				return fmt.Errorf("scoring sample %d: %w", i, err)
			}
			logger.Debug("Sample VMAF score", "sample", i, "score", score)
			scores[i] = score
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return 0, err
	}

	result := averageScores(scores)
	logger.Info("VMAF score", "scores", scores, "average", result)
	return result, nil
}

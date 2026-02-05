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

// vmafModel is the VMAF model used for all scoring.
// Always HD model since scoring happens at ≤1080p.
const vmafModel = "vmaf_v0.6.1"

// scoringHeight returns the height used for VMAF scoring.
// Content >1080p is downscaled to 1080; content ≤1080p stays native.
// Unknown/zero height defaults to 1080 as a safety cap against OOM.
func scoringHeight(inputH int) int {
	if inputH <= 0 || inputH > 1080 {
		return 1080
	}
	return inputH &^ 1 // ensure even
}

// buildSDRScoringFilter creates a filtergraph for SDR VMAF comparison.
// Both legs are normalized with setsar=1 and format=yuv420p before libvmaf.
// When needsDownscale is true, both legs are scaled to scoreH before comparison.
func buildSDRScoringFilter(model string, threads, scoreH int, needsDownscale bool) string {
	leg := "setsar=1,"
	if needsDownscale {
		leg += fmt.Sprintf("scale=-2:%d,", scoreH)
	}
	leg += "format=yuv420p"

	return fmt.Sprintf(
		"[0:v]%s[dist];[1:v]%s[ref];"+
			"[dist][ref]libvmaf=model=version=%s:n_threads=%d",
		leg, leg, model, threads)
}

// buildHDRScoringFilter creates a filtergraph for HDR VMAF comparison.
// BOTH legs are tonemapped from HDR to SDR because VMAF is only validated for SDR-to-SDR.
//
// When needsDownscale is true, the downscale is merged into the linearization zscale call
// so that pixel resampling happens in linear light (mathematically correct).
// The combined zscale does: resize + linearize + preserve BT.2020 in one pass.
//
// Pipeline per leg:
//  1. setsar=1 (SAR normalization)
//  2. zscale: linearize from PQ/HLG (+ downscale if needed, resampling in linear light)
//  3. format=gbrpf32le (float precision for tonemap math)
//  4. zscale: convert primaries to bt709
//  5. tonemap algorithm (operates on linear bt709)
//  6. zscale: apply bt709 transfer curve and matrix
//  7. format=yuv420p for VMAF
//
// inputTransfer should be "smpte2084" for HDR10/DV or "arib-std-b67" for HLG.
// Falls back to "smpte2084" if empty or unknown.
func buildHDRScoringFilter(model string, threads int, algorithm, inputTransfer string, scoreH int, needsDownscale bool) string {
	// Validate and normalize inputTransfer
	switch inputTransfer {
	case "smpte2084", "arib-std-b67":
		// Valid, use as-is
	default:
		inputTransfer = "smpte2084"
	}

	// Build the first zscale call: linearize, optionally with resize merged in.
	// When downscaling, adding w/h to the linearization step means zscale
	// converts to linear light first, then resamples at the target resolution.
	var linearizeZscale string
	if needsDownscale {
		linearizeZscale = fmt.Sprintf(
			"zscale=w=-2:h=%d:pin=bt2020:tin=%s:min=bt2020nc:t=linear:npl=1000",
			scoreH, inputTransfer)
	} else {
		linearizeZscale = fmt.Sprintf(
			"zscale=pin=bt2020:tin=%s:min=bt2020nc:t=linear:npl=1000",
			inputTransfer)
	}

	// Full tonemap chain per leg
	tonemapChain := fmt.Sprintf(
		"setsar=1,%s,format=gbrpf32le,zscale=p=bt709,tonemap=%s:desat=0:peak=100,zscale=t=bt709:m=bt709,format=yuv420p",
		linearizeZscale, algorithm)

	return fmt.Sprintf(
		"[0:v]%s[dist];[1:v]%s[ref];[dist][ref]libvmaf=model=version=%s:n_threads=%d",
		tonemapChain, tonemapChain, model, threads)
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
// When tonemap is provided and enabled, both legs are tonemapped from HDR to SDR.
// Content >1080p is downscaled to 1080p before scoring to reduce memory and improve speed.
// The threads parameter controls parallelism for FFmpeg and libvmaf.
func Score(ctx context.Context, ffmpegPath, referencePath, distortedPath string, height, threads int, tonemap *TonemapConfig) (float64, error) {
	scoreH := scoringHeight(height)
	needsDownscale := scoreH < height || height <= 0

	// Build appropriate filtergraph based on HDR/SDR
	var filterComplex string
	if tonemap != nil && tonemap.Enabled {
		algorithm := tonemap.Algorithm
		if algorithm == "" {
			algorithm = "hable"
		}
		filterComplex = buildHDRScoringFilter(vmafModel, threads, algorithm, tonemap.InputTransfer, scoreH, needsDownscale)
	} else {
		filterComplex = buildSDRScoringFilter(vmafModel, threads, scoreH, needsDownscale)
	}

	logger.Debug("VMAF scoring",
		"inputHeight", height,
		"scoringHeight", scoreH,
		"downscale", needsDownscale,
		"hdr", tonemap != nil && tonemap.Enabled,
		"model", vmafModel,
		"filter", filterComplex)

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

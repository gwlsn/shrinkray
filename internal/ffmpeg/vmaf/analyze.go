package vmaf

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gwlsn/shrinkray/internal/logger"
)

// Analyzer orchestrates VMAF analysis for SmartShrink.
// Operates on SDR content only — HDR files are skipped at the worker level.
type Analyzer struct {
	FFmpegPath string
	TempDir    string
}

// NewAnalyzer creates a new VMAF analyzer
func NewAnalyzer(ffmpegPath, tempDir string) *Analyzer {
	return &Analyzer{
		FFmpegPath: ffmpegPath,
		TempDir:    tempDir,
	}
}

// Analyze performs full VMAF analysis on a video
// threshold is the target VMAF score (e.g., 85, 93, or 96)
// encodeSample is a callback that encodes a sample at the given quality
func (a *Analyzer) Analyze(ctx context.Context, inputPath string, videoDuration time.Duration,
	height int, qRange QualityRange, threshold float64, encodeSample EncodeSampleFunc) (*AnalysisResult, error) {

	if !IsAvailable() {
		return nil, fmt.Errorf("VMAF not available")
	}

	// Create temp directory for this analysis
	analysisDir := filepath.Join(a.TempDir, fmt.Sprintf("vmaf_%d", time.Now().UnixNano()))
	if err := os.MkdirAll(analysisDir, 0755); err != nil {
		return nil, fmt.Errorf("creating analysis dir: %w", err)
	}
	defer os.RemoveAll(analysisDir)

	// Get sample positions (3 positions at 25%, 50%, 75%)
	positions := SamplePositions(videoDuration)

	logger.Info("Starting VMAF analysis",
		"input", inputPath,
		"samples", len(positions),
		"threshold", threshold)

	// Extract reference samples using stream copy (fast)
	extractStart := time.Now()
	referenceSamples, err := ExtractSamples(ctx, a.FFmpegPath, inputPath, analysisDir,
		videoDuration, positions)
	if err != nil {
		return nil, fmt.Errorf("extracting samples: %w", err)
	}
	logger.Info("Sample extraction complete", "duration", time.Since(extractStart).String())
	defer CleanupSamples(referenceSamples)

	// Run binary search to find optimal quality
	searchStart := time.Now()
	result, err := BinarySearch(ctx, a.FFmpegPath, referenceSamples, qRange, threshold, height, encodeSample)
	searchDuration := time.Since(searchStart)
	if err != nil {
		return nil, fmt.Errorf("binary search: %w", err)
	}

	// No acceptable quality found
	if result == nil {
		logger.Info("Binary search complete - no acceptable quality found", "duration", searchDuration.String())
		return &AnalysisResult{
			ShouldSkip: true,
			SkipReason: "Already optimized",
		}, nil
	}

	logger.Info("Binary search complete", "duration", searchDuration.String(), "iterations", result.Iterations)

	return &AnalysisResult{
		OptimalCRF: result.Quality,
		QualityMod: result.Modifier,
		VMafScore:  result.VMafScore,
	}, nil
}

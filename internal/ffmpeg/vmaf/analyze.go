package vmaf

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gwlsn/shrinkray/internal/logger"
)

// TonemapConfig holds tonemapping configuration for HDR content
type TonemapConfig struct {
	Enabled       bool   // True if HDR content should be tonemapped to SDR
	Algorithm     string // Tonemapping algorithm (hable, bt2390, etc.)
	InputTransfer string // Input transfer function (smpte2084 for HDR10/DV, arib-std-b67 for HLG)
}

// Analyzer orchestrates VMAF analysis for SmartShrink
type Analyzer struct {
	FFmpegPath string
	TempDir    string
	Tonemap    *TonemapConfig // Optional tonemapping for HDR content
}

// NewAnalyzer creates a new VMAF analyzer
func NewAnalyzer(ffmpegPath, tempDir string) *Analyzer {
	return &Analyzer{
		FFmpegPath: ffmpegPath,
		TempDir:    tempDir,
	}
}

// WithTonemap sets tonemapping configuration for HDR content.
// inputTransfer should be the source transfer function: "smpte2084" for HDR10/DV, "arib-std-b67" for HLG.
func (a *Analyzer) WithTonemap(enabled bool, algorithm, inputTransfer string) *Analyzer {
	a.Tonemap = &TonemapConfig{
		Enabled:       enabled,
		Algorithm:     algorithm,
		InputTransfer: inputTransfer,
	}
	return a
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

	// Extract reference samples using stream copy (fast, no tonemap)
	// Tonemapping for HDR content is handled during VMAF scoring instead
	extractStart := time.Now()
	referenceSamples, err := ExtractSamples(ctx, a.FFmpegPath, inputPath, analysisDir,
		videoDuration, positions)
	if err != nil {
		return nil, fmt.Errorf("extracting samples: %w", err)
	}
	logger.Info("Sample extraction complete", "duration", time.Since(extractStart).String())
	defer CleanupSamples(referenceSamples)

	// Run binary search with tonemap config
	searchStart := time.Now()
	result, err := BinarySearch(ctx, a.FFmpegPath, referenceSamples, qRange, threshold, height, a.Tonemap, encodeSample)
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
		OptimalCRF:  result.Quality,
		QualityMod:  result.Modifier,
		VMafScore:   result.VMafScore,
		SamplesUsed: len(positions),
	}, nil
}

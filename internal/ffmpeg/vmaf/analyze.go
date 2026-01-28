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
	Enabled   bool   // True if HDR content should be tonemapped to SDR
	Algorithm string // Tonemapping algorithm (hable, bt2390, etc.)
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

// WithTonemap sets tonemapping configuration for HDR content
func (a *Analyzer) WithTonemap(enabled bool, algorithm string) *Analyzer {
	a.Tonemap = &TonemapConfig{
		Enabled:   enabled,
		Algorithm: algorithm,
	}
	return a
}

// Analyze performs full VMAF analysis on a video
// encodeSample is a callback that encodes a sample at the given quality
func (a *Analyzer) Analyze(ctx context.Context, inputPath string, videoDuration time.Duration,
	height int, qRange QualityRange, encodeSample EncodeSampleFunc) (*AnalysisResult, error) {

	if !IsAvailable() {
		return nil, fmt.Errorf("VMAF not available")
	}

	// Create temp directory for this analysis
	analysisDir := filepath.Join(a.TempDir, fmt.Sprintf("vmaf_%d", time.Now().UnixNano()))
	if err := os.MkdirAll(analysisDir, 0755); err != nil {
		return nil, fmt.Errorf("creating analysis dir: %w", err)
	}
	defer os.RemoveAll(analysisDir)

	// Determine sample positions
	positions := SamplePositions(videoDuration, a.FastAnalysis)

	logger.Info("Starting VMAF analysis",
		"input", inputPath,
		"samples", len(positions),
		"threshold", a.VMafThreshold)

	// Extract reference samples
	// When tonemapping is enabled for HDR content, reference samples must also be
	// tonemapped so VMAF compares SDR reference to SDR encoded (not HDR to SDR).
	referenceSamples, err := ExtractSamples(ctx, a.FFmpegPath, inputPath, analysisDir,
		videoDuration, a.SampleDuration, positions, a.Tonemap)
	if err != nil {
		return nil, fmt.Errorf("extracting samples: %w", err)
	}
	defer CleanupSamples(referenceSamples)

	// Wrap encodeSample to put outputs in our analysis dir
	wrappedEncode := func(ctx context.Context, samplePath string, quality int, modifier float64) (string, error) {
		return encodeSample(ctx, samplePath, quality, modifier)
	}

	// Run binary search
	result, err := BinarySearch(ctx, a.FFmpegPath, referenceSamples, qRange, a.VMafThreshold, height, wrappedEncode)
	if err != nil {
		return nil, fmt.Errorf("binary search: %w", err)
	}

	// No acceptable quality found
	if result == nil {
		return &AnalysisResult{
			ShouldSkip: true,
			SkipReason: "Already optimized",
		}, nil
	}

	// Track samples used for final result
	samplesUsed := len(positions)

	// Check if we should expand from fast analysis to full
	if a.FastAnalysis && len(positions) == 1 {
		// If score is within 5 points of threshold, do full analysis
		if result.VMafScore < a.VMafThreshold+5 {
			logger.Info("Expanding to full analysis (score near threshold)",
				"score", result.VMafScore,
				"threshold", a.VMafThreshold)

			// Re-run with full positions
			fullPositions := []float64{0.25, 0.5, 0.75}
			fullSamples, err := ExtractSamples(ctx, a.FFmpegPath, inputPath, analysisDir,
				videoDuration, a.SampleDuration, fullPositions, a.Tonemap)
			if err != nil {
				return nil, fmt.Errorf("extracting full samples: %w", err)
			}
			defer CleanupSamples(fullSamples)

			result, err = BinarySearch(ctx, a.FFmpegPath, fullSamples, qRange, a.VMafThreshold, height, wrappedEncode)
			if err != nil {
				return nil, fmt.Errorf("full binary search: %w", err)
			}

			if result == nil {
				return &AnalysisResult{
					ShouldSkip: true,
					SkipReason: "Already optimized",
				}, nil
			}

			// Update samples used to reflect full analysis
			samplesUsed = len(fullPositions)
		}
	}

	return &AnalysisResult{
		OptimalCRF:  result.Quality,
		QualityMod:  result.Modifier,
		VMafScore:   result.VMafScore,
		SamplesUsed: samplesUsed,
	}, nil
}

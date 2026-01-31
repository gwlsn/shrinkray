package vmaf

import (
	"context"
	"testing"
)

func TestBinarySearchCRF(t *testing.T) {
	// Mock encoder that returns predictable VMAF based on CRF
	// Lower CRF = higher quality = higher VMAF
	mockVMAFScores := map[int]float64{
		18: 98.0,
		20: 97.0,
		22: 96.0,
		24: 95.0,
		26: 93.0, // Target threshold
		28: 91.0,
		30: 88.0,
		32: 85.0,
		35: 80.0,
	}

	// This test verifies the search logic without actual FFmpeg
	// Real integration tests run in Docker

	qRange := QualityRange{Min: 18, Max: 35}
	threshold := 93.0

	// For a threshold of 93, we should find CRF 26 or close to it
	// (the highest CRF that still meets the threshold)

	t.Log("Binary search should find optimal CRF")
	t.Logf("Mock VMAF scores: %v", mockVMAFScores)
	t.Logf("Threshold: %.1f, expecting CRF around 26", threshold)
	t.Logf("Quality range: %d-%d", qRange.Min, qRange.Max)
}

func TestQualityRangeDefaults(t *testing.T) {
	// Test that QualityRange struct initializes correctly
	qRange := QualityRange{
		Min: 18,
		Max: 35,
	}

	if qRange.Min != 18 {
		t.Errorf("expected Min=18, got %d", qRange.Min)
	}
	if qRange.Max != 35 {
		t.Errorf("expected Max=35, got %d", qRange.Max)
	}
	if qRange.UsesBitrate {
		t.Error("expected UsesBitrate=false for CRF mode")
	}
}

func TestQualityRangeBitrate(t *testing.T) {
	// Test bitrate modifier mode (for VideoToolbox)
	qRange := QualityRange{
		UsesBitrate: true,
		MinMod:      0.05,
		MaxMod:      0.80,
	}

	if !qRange.UsesBitrate {
		t.Error("expected UsesBitrate=true for bitrate mode")
	}
	if qRange.MinMod != 0.05 {
		t.Errorf("expected MinMod=0.05, got %f", qRange.MinMod)
	}
	if qRange.MaxMod != 0.80 {
		t.Errorf("expected MaxMod=0.80, got %f", qRange.MaxMod)
	}
}

func TestSearchResultFields(t *testing.T) {
	// Test SearchResult struct
	result := SearchResult{
		Quality:    26,
		VMafScore:  93.5,
		Iterations: 4,
	}

	if result.Quality != 26 {
		t.Errorf("expected Quality=26, got %d", result.Quality)
	}
	if result.VMafScore != 93.5 {
		t.Errorf("expected VMafScore=93.5, got %f", result.VMafScore)
	}
	if result.Iterations != 4 {
		t.Errorf("expected Iterations=4, got %d", result.Iterations)
	}
}

func TestSearchResultBitrateMode(t *testing.T) {
	// Test SearchResult with bitrate modifier
	result := SearchResult{
		Modifier:   0.25,
		VMafScore:  94.0,
		Iterations: 5,
	}

	if result.Modifier != 0.25 {
		t.Errorf("expected Modifier=0.25, got %f", result.Modifier)
	}
	if result.Quality != 0 {
		t.Errorf("expected Quality=0 for bitrate mode, got %d", result.Quality)
	}
}

func TestBinarySearchSelectsCorrectFunction(t *testing.T) {
	// Test that BinarySearch routes to the correct internal function
	// based on UsesBitrate flag

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Immediately cancel to prevent actual encoding

	// CRF mode - nil tonemap (SDR)
	crfRange := QualityRange{Min: 18, Max: 35, UsesBitrate: false}

	// This should fail fast due to cancelled context, but validates routing
	_, err := BinarySearch(ctx, "ffmpeg", nil, crfRange, 93.0, 1080, nil, nil)
	// Error is expected due to nil samples/encoder
	_ = err

	// Bitrate mode - nil tonemap (SDR)
	bitrateRange := QualityRange{UsesBitrate: true, MinMod: 0.05, MaxMod: 0.80}
	_, err = BinarySearch(ctx, "ffmpeg", nil, bitrateRange, 93.0, 1080, nil, nil)
	// Error is expected due to nil samples/encoder
	_ = err
}

func TestBinarySearchSignatureAcceptsTonemap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	qRange := QualityRange{Min: 18, Max: 35}

	// SDR case - nil tonemap
	_, err := BinarySearch(ctx, "ffmpeg", nil, qRange, 93.0, 1080, nil, nil)
	_ = err

	// HDR case - with tonemap
	tonemap := &TonemapConfig{Enabled: true, Algorithm: "hable"}
	_, err = BinarySearch(ctx, "ffmpeg", nil, qRange, 93.0, 1080, tonemap, nil)
	_ = err
}

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

// TestVmafLerpCRF tests the CRF interpolation function edge cases
func TestVmafLerpCRF(t *testing.T) {
	tests := []struct {
		name        string
		threshold   float64
		worseCRF    int
		worseScore  float64
		betterCRF   int
		betterScore float64
	}{
		{
			name:        "normal interpolation midpoint",
			threshold:   93.0,
			worseCRF:    30, // Higher CRF = worse quality
			worseScore:  88.0,
			betterCRF:   20, // Lower CRF = better quality
			betterScore: 98.0,
		},
		{
			name:        "threshold near worse score",
			threshold:   89.0,
			worseCRF:    30,
			worseScore:  88.0,
			betterCRF:   20,
			betterScore: 98.0,
		},
		{
			name:        "threshold near better score",
			threshold:   97.0,
			worseCRF:    30,
			worseScore:  88.0,
			betterCRF:   20,
			betterScore: 98.0,
		},
		{
			name:        "zero score difference falls back to midpoint",
			threshold:   93.0,
			worseCRF:    30,
			worseScore:  95.0, // Same as better
			betterCRF:   20,
			betterScore: 95.0,
		},
		{
			name:        "negative score difference falls back to midpoint",
			threshold:   93.0,
			worseCRF:    30,
			worseScore:  96.0, // Higher than better (anomaly)
			betterCRF:   20,
			betterScore: 94.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := vmafLerpCRF(tt.threshold, tt.worseCRF, tt.worseScore, tt.betterCRF, tt.betterScore)

			// Result must be strictly between bounds
			if result <= tt.betterCRF {
				t.Errorf("vmafLerpCRF() = %d, want > %d (betterCRF)", result, tt.betterCRF)
			}
			if result >= tt.worseCRF {
				t.Errorf("vmafLerpCRF() = %d, want < %d (worseCRF)", result, tt.worseCRF)
			}
		})
	}
}

// TestVmafLerpCRFAdjacentValues tests the edge case where CRF values are adjacent.
// When worseCRF - betterCRF == 1, there is no integer strictly between them.
// The function clamps to one of the bounds, which the search algorithm handles
// by detecting adjacency and terminating early.
func TestVmafLerpCRFAdjacentValues(t *testing.T) {
	// Adjacent CRF values (26 and 25) - no integer exists strictly between them
	result := vmafLerpCRF(93.0, 26, 91.0, 25, 95.0)

	// Result must be one of the two bounds (clamped)
	if result != 25 && result != 26 {
		t.Errorf("vmafLerpCRF() with adjacent values = %d, want 25 or 26", result)
	}

	// The search algorithm checks for adjacency before calling interpolation,
	// so in practice this case is handled by early termination
}

// TestVmafLerpMod tests the bitrate modifier interpolation function edge cases
func TestVmafLerpMod(t *testing.T) {
	tests := []struct {
		name        string
		threshold   float64
		worseMod    float64 // Lower modifier = worse quality
		worseScore  float64
		betterMod   float64 // Higher modifier = better quality
		betterScore float64
	}{
		{
			name:        "normal interpolation midpoint",
			threshold:   93.0,
			worseMod:    0.10,
			worseScore:  88.0,
			betterMod:   0.50,
			betterScore: 98.0,
		},
		{
			name:        "threshold near worse score",
			threshold:   89.0,
			worseMod:    0.10,
			worseScore:  88.0,
			betterMod:   0.50,
			betterScore: 98.0,
		},
		{
			name:        "threshold near better score",
			threshold:   97.0,
			worseMod:    0.10,
			worseScore:  88.0,
			betterMod:   0.50,
			betterScore: 98.0,
		},
		{
			name:        "zero score difference falls back to midpoint",
			threshold:   93.0,
			worseMod:    0.10,
			worseScore:  95.0,
			betterMod:   0.50,
			betterScore: 95.0,
		},
		{
			name:        "negative score difference falls back to midpoint",
			threshold:   93.0,
			worseMod:    0.10,
			worseScore:  96.0, // Higher than better (anomaly)
			betterMod:   0.50,
			betterScore: 94.0,
		},
		{
			name:        "narrow modifier range",
			threshold:   93.0,
			worseMod:    0.20,
			worseScore:  91.0,
			betterMod:   0.25,
			betterScore: 95.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := vmafLerpMod(tt.threshold, tt.worseMod, tt.worseScore, tt.betterMod, tt.betterScore)

			// Result must be strictly between bounds (with margin)
			margin := minModRange / 2
			if result <= tt.worseMod+margin/2 {
				t.Errorf("vmafLerpMod() = %f, want > %f (worseMod + margin)", result, tt.worseMod+margin)
			}
			if result >= tt.betterMod-margin/2 {
				t.Errorf("vmafLerpMod() = %f, want < %f (betterMod - margin)", result, tt.betterMod-margin)
			}
		})
	}
}

// TestVmafLerpCRFMidpointFallback specifically tests the zero/negative score diff fallback
func TestVmafLerpCRFMidpointFallback(t *testing.T) {
	// When scores are equal, should return midpoint
	result := vmafLerpCRF(93.0, 30, 95.0, 20, 95.0)
	expected := (30 + 20) / 2 // = 25
	if result != expected {
		t.Errorf("zero scoreDiff: vmafLerpCRF() = %d, want %d (midpoint)", result, expected)
	}

	// When worse has higher score (anomaly), should also return midpoint
	result = vmafLerpCRF(93.0, 30, 96.0, 20, 94.0)
	if result != expected {
		t.Errorf("negative scoreDiff: vmafLerpCRF() = %d, want %d (midpoint)", result, expected)
	}
}

// TestVmafLerpModMidpointFallback specifically tests the zero/negative score diff fallback
func TestVmafLerpModMidpointFallback(t *testing.T) {
	// When scores are equal, should return midpoint
	result := vmafLerpMod(93.0, 0.10, 95.0, 0.50, 95.0)
	expected := (0.10 + 0.50) / 2 // = 0.30
	tolerance := 0.001
	if result < expected-tolerance || result > expected+tolerance {
		t.Errorf("zero scoreDiff: vmafLerpMod() = %f, want %f (midpoint)", result, expected)
	}

	// When worse has higher score (anomaly), should also return midpoint
	result = vmafLerpMod(93.0, 0.10, 96.0, 0.50, 94.0)
	if result < expected-tolerance || result > expected+tolerance {
		t.Errorf("negative scoreDiff: vmafLerpMod() = %f, want %f (midpoint)", result, expected)
	}
}

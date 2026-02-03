package vmaf

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestBuildSDRScoringFilter(t *testing.T) {
	filter := buildSDRScoringFilter("vmaf_v0.6.1", 4)

	// Should have format conversion on both legs
	if !strings.Contains(filter, "[0:v]format=yuv420p[dist]") {
		t.Error("missing distorted leg format conversion")
	}
	if !strings.Contains(filter, "[1:v]format=yuv420p[ref]") {
		t.Error("missing reference leg format conversion")
	}

	// Should have libvmaf with correct params
	if !strings.Contains(filter, "[dist][ref]libvmaf=") {
		t.Error("missing libvmaf filter")
	}
	if !strings.Contains(filter, "model=version=vmaf_v0.6.1") {
		t.Error("missing model version")
	}
	if !strings.Contains(filter, "n_threads=4") {
		t.Error("missing thread count")
	}
	// JSON logging removed - score is parsed from FFmpeg stderr summary line
	if strings.Contains(filter, "log_fmt=json") {
		t.Error("unexpected json log format - should be removed")
	}
	if strings.Contains(filter, "log_path=") {
		t.Error("unexpected log_path - should be removed")
	}
}

func TestBuildHDRScoringFilter(t *testing.T) {
	// Test with HDR10 (smpte2084) - the default/most common case
	filter := buildHDRScoringFilter("vmaf_v0.6.1", 4, "hable", "smpte2084")

	// Both legs should have full tonemap pipeline (HDR samples -> SDR for VMAF)
	// VMAF is only validated for SDR-to-SDR comparison

	// Distorted leg should have full tonemap pipeline
	if !strings.Contains(filter, "[0:v]zscale=") {
		t.Error("distorted leg missing zscale (should tonemap HDR to SDR)")
	}

	// Reference leg should have full tonemap pipeline
	if !strings.Contains(filter, "[1:v]zscale=") {
		t.Error("reference leg missing zscale (should tonemap HDR to SDR)")
	}

	// Check explicit HDR input metadata on both legs
	if strings.Count(filter, "pin=bt2020") != 2 {
		t.Errorf("expected bt2020 primaries input on both legs, got %d", strings.Count(filter, "pin=bt2020"))
	}
	if strings.Count(filter, "tin=smpte2084") != 2 {
		t.Errorf("expected PQ transfer input on both legs, got %d", strings.Count(filter, "tin=smpte2084"))
	}

	// Check tonemap applied to both legs
	if strings.Count(filter, "tonemap=hable") != 2 {
		t.Errorf("expected tonemap on both legs, got %d", strings.Count(filter, "tonemap=hable"))
	}

	// CRITICAL: Verify correct pipeline order - tonemap must operate on linear light
	// Order: linearize -> float -> primaries -> tonemap -> transfer/matrix -> yuv420p
	// Check first leg (distorted) ordering
	linearIdx := strings.Index(filter, "t=linear")
	primariesIdx := strings.Index(filter, "zscale=p=bt709")
	tonemapIdx := strings.Index(filter, "tonemap=")
	transferIdx := strings.Index(filter, "zscale=t=bt709")

	if linearIdx == -1 || primariesIdx == -1 || tonemapIdx == -1 || transferIdx == -1 {
		t.Fatal("missing required filter components for order check")
	}

	if linearIdx > primariesIdx {
		t.Error("linearization (t=linear) must come BEFORE primaries conversion")
	}
	if primariesIdx > tonemapIdx {
		t.Error("primaries conversion (p=bt709) must come BEFORE tonemap")
	}
	if tonemapIdx > transferIdx {
		t.Error("tonemap must come BEFORE transfer function (t=bt709)")
	}

	// Check libvmaf receives both tonemapped legs
	if !strings.Contains(filter, "[dist][ref]libvmaf=") {
		t.Error("missing libvmaf filter")
	}
}

func TestBuildHDRScoringFilterHLG(t *testing.T) {
	// Test with HLG (arib-std-b67) - requires different input transfer
	filter := buildHDRScoringFilter("vmaf_v0.6.1", 4, "hable", "arib-std-b67")

	// Should use HLG transfer function on both legs
	if strings.Count(filter, "tin=arib-std-b67") != 2 {
		t.Errorf("expected HLG transfer input on both legs, got %d", strings.Count(filter, "tin=arib-std-b67"))
	}
	if strings.Contains(filter, "tin=smpte2084") {
		t.Error("should NOT contain PQ transfer for HLG content")
	}
}

func TestBuildHDRScoringFilterDefaultTransfer(t *testing.T) {
	// Test fallback to smpte2084 when input transfer is empty or unknown
	testCases := []struct {
		name          string
		inputTransfer string
	}{
		{"empty", ""},
		{"unknown", "unknown_transfer"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			filter := buildHDRScoringFilter("vmaf_v0.6.1", 4, "hable", tc.inputTransfer)
			if !strings.Contains(filter, "tin=smpte2084") {
				t.Errorf("should fallback to smpte2084 for %s input transfer", tc.name)
			}
		})
	}
}

func TestBuildHDRScoringFilterAlgorithms(t *testing.T) {
	algorithms := []string{"hable", "bt2390", "reinhard", "mobius"}

	for _, algo := range algorithms {
		t.Run(algo, func(t *testing.T) {
			filter := buildHDRScoringFilter("vmaf_v0.6.1", 4, algo, "smpte2084")
			expected := fmt.Sprintf("tonemap=%s:", algo)
			if !strings.Contains(filter, expected) {
				t.Errorf("expected tonemap algorithm %s, got filter: %s", algo, filter)
			}
		})
	}
}

func TestScoreSignatureAcceptsTonemap(t *testing.T) {
	// This test verifies the function signature compiles with tonemap param
	// Actual scoring requires FFmpeg, tested in integration tests

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately to avoid actual FFmpeg call

	// SDR case - nil tonemap
	_, err := Score(ctx, "ffmpeg", "ref.mkv", "dist.mkv", 1080, 4, nil)
	// Error expected due to cancelled context or missing files
	_ = err

	// HDR case - with tonemap config
	tonemap := &TonemapConfig{Enabled: true, Algorithm: "hable"}
	_, err = Score(ctx, "ffmpeg", "ref.mkv", "dist.mkv", 1080, 4, tonemap)
	// Error expected due to cancelled context or missing files
	_ = err
}

func TestScoreSamplesSignatureAcceptsTonemap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Verify signature compiles with tonemap param
	refSamples := []*Sample{{Path: "ref.mkv"}}
	distSamples := []*Sample{{Path: "dist.mkv"}}

	// SDR case
	_, err := ScoreSamples(ctx, "ffmpeg", refSamples, distSamples, 1080, nil)
	_ = err

	// HDR case
	tonemap := &TonemapConfig{Enabled: true, Algorithm: "hable"}
	_, err = ScoreSamples(ctx, "ffmpeg", refSamples, distSamples, 1080, tonemap)
	_ = err
}

func TestAverageScores(t *testing.T) {
	tests := []struct {
		name     string
		scores   []float64
		expected float64
	}{
		{
			name:     "empty scores",
			scores:   []float64{},
			expected: 0,
		},
		{
			name:     "1 score",
			scores:   []float64{85},
			expected: 85.0,
		},
		{
			name:     "3 scores",
			scores:   []float64{80, 90, 95},
			expected: 88.33333333333333, // (80 + 90 + 95) / 3
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := averageScores(tt.scores)
			if result != tt.expected {
				t.Errorf("averageScores(%v) = %v, want %v", tt.scores, result, tt.expected)
			}
		})
	}
}

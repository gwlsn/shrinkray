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
	if !strings.Contains(filter, "log_fmt=json") {
		t.Error("missing json log format")
	}
	if !strings.Contains(filter, "log_path=/dev/stdout") {
		t.Error("missing stdout log path")
	}
}

func TestBuildHDRScoringFilter(t *testing.T) {
	filter := buildHDRScoringFilter("vmaf_v0.6.1", 4, "hable")

	// Distorted leg should be simple format conversion (already SDR)
	if !strings.Contains(filter, "[0:v]format=yuv420p[dist]") {
		t.Error("missing distorted leg format conversion")
	}

	// Reference leg should have full tonemap pipeline
	if !strings.Contains(filter, "[1:v]") {
		t.Error("missing reference leg start")
	}

	// Check explicit HDR input metadata
	if !strings.Contains(filter, "pin=bt2020") {
		t.Error("missing bt2020 primaries input")
	}
	if !strings.Contains(filter, "tin=smpte2084") {
		t.Error("missing PQ transfer input")
	}
	if !strings.Contains(filter, "min=bt2020nc") {
		t.Error("missing bt2020nc matrix input")
	}

	// Check linearization
	if !strings.Contains(filter, "t=linear") {
		t.Error("missing linear transfer")
	}
	if !strings.Contains(filter, "npl=1000") {
		t.Error("missing nominal peak luminance")
	}

	// Check float format for precision
	if !strings.Contains(filter, "format=gbrpf32le") {
		t.Error("missing float format conversion")
	}

	// Check SDR output metadata
	if !strings.Contains(filter, "p=bt709") {
		t.Error("missing bt709 primaries output")
	}
	if !strings.Contains(filter, "m=bt709") {
		t.Error("missing bt709 matrix output")
	}

	// Check tonemap with algorithm
	if !strings.Contains(filter, "tonemap=hable") {
		t.Error("missing tonemap filter with algorithm")
	}

	// Check libvmaf
	if !strings.Contains(filter, "[dist][ref]libvmaf=") {
		t.Error("missing libvmaf filter")
	}
}

func TestBuildHDRScoringFilterAlgorithms(t *testing.T) {
	algorithms := []string{"hable", "bt2390", "reinhard", "mobius"}

	for _, algo := range algorithms {
		t.Run(algo, func(t *testing.T) {
			filter := buildHDRScoringFilter("vmaf_v0.6.1", 4, algo)
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
	_, err := Score(ctx, "ffmpeg", "ref.mkv", "dist.mkv", 1080, nil)
	// Error expected due to cancelled context or missing files
	_ = err

	// HDR case - with tonemap config
	tonemap := &TonemapConfig{Enabled: true, Algorithm: "hable"}
	_, err = Score(ctx, "ffmpeg", "ref.mkv", "dist.mkv", 1080, tonemap)
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

func TestTrimmedMean(t *testing.T) {
	tests := []struct {
		name     string
		scores   []float64
		expected float64
	}{
		{
			name:     "5 scores - drops highest and lowest",
			scores:   []float64{80, 85, 90, 95, 100},
			expected: 90.0, // (85 + 90 + 95) / 3
		},
		{
			name:     "5 scores - unordered input",
			scores:   []float64{95, 80, 100, 85, 90},
			expected: 90.0, // sorted: 80,85,90,95,100 → (85+90+95)/3
		},
		{
			name:     "3 scores - returns middle",
			scores:   []float64{80, 90, 100},
			expected: 90.0, // just the middle value
		},
		{
			name:     "1 score - returns that score",
			scores:   []float64{85},
			expected: 85.0,
		},
		{
			name:     "2 scores - returns average",
			scores:   []float64{80, 90},
			expected: 85.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := trimmedMean(tt.scores)
			if result != tt.expected {
				t.Errorf("trimmedMean(%v) = %v, want %v", tt.scores, result, tt.expected)
			}
		})
	}
}

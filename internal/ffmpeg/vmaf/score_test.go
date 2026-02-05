package vmaf

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestBuildSDRScoringFilter(t *testing.T) {
	t.Run("native resolution (no downscale)", func(t *testing.T) {
		filter := buildSDRScoringFilter("vmaf_v0.6.1", 4, 720, false)

		// Should have setsar + format on both legs, no scale
		if !strings.Contains(filter, "[0:v]setsar=1,format=yuv420p[dist]") {
			t.Errorf("wrong distorted leg, got: %s", filter)
		}
		if !strings.Contains(filter, "[1:v]setsar=1,format=yuv420p[ref]") {
			t.Errorf("wrong reference leg, got: %s", filter)
		}
		if strings.Contains(filter, "scale=") {
			t.Error("should NOT have scale filter when not downscaling")
		}
		if !strings.Contains(filter, "[dist][ref]libvmaf=") {
			t.Error("missing libvmaf filter")
		}
		if !strings.Contains(filter, "model=version=vmaf_v0.6.1") {
			t.Error("missing model version")
		}
		if !strings.Contains(filter, "n_threads=4") {
			t.Error("missing thread count")
		}
	})

	t.Run("downscale to 1080p", func(t *testing.T) {
		filter := buildSDRScoringFilter("vmaf_v0.6.1", 4, 1080, true)

		// Should have setsar + scale + format on both legs
		if !strings.Contains(filter, "[0:v]setsar=1,scale=-2:1080,format=yuv420p[dist]") {
			t.Errorf("wrong distorted leg, got: %s", filter)
		}
		if !strings.Contains(filter, "[1:v]setsar=1,scale=-2:1080,format=yuv420p[ref]") {
			t.Errorf("wrong reference leg, got: %s", filter)
		}
		if !strings.Contains(filter, "[dist][ref]libvmaf=") {
			t.Error("missing libvmaf filter")
		}
	})
}

func TestBuildHDRScoringFilter(t *testing.T) {
	t.Run("native resolution (no downscale)", func(t *testing.T) {
		filter := buildHDRScoringFilter("vmaf_v0.6.1", 4, "hable", "smpte2084", 1080, false)

		// Should have setsar=1 on both legs
		if strings.Count(filter, "setsar=1") != 2 {
			t.Errorf("expected setsar=1 on both legs, got %d", strings.Count(filter, "setsar=1"))
		}

		// Should NOT have w= or h= in any zscale (no downscale)
		if strings.Contains(filter, "w=-2") || strings.Contains(filter, "h=") {
			t.Error("should NOT have resize params when not downscaling")
		}

		// Check standard tonemap pipeline on both legs
		if strings.Count(filter, "pin=bt2020") != 2 {
			t.Errorf("expected bt2020 primaries on both legs, got %d", strings.Count(filter, "pin=bt2020"))
		}
		if strings.Count(filter, "tin=smpte2084") != 2 {
			t.Errorf("expected PQ transfer on both legs, got %d", strings.Count(filter, "tin=smpte2084"))
		}
		if strings.Count(filter, "tonemap=hable") != 2 {
			t.Errorf("expected tonemap on both legs, got %d", strings.Count(filter, "tonemap=hable"))
		}
		if !strings.Contains(filter, "[dist][ref]libvmaf=") {
			t.Error("missing libvmaf filter")
		}
	})

	t.Run("downscale merges with linearization", func(t *testing.T) {
		filter := buildHDRScoringFilter("vmaf_v0.6.1", 4, "hable", "smpte2084", 1080, true)

		// Should have setsar=1 on both legs
		if strings.Count(filter, "setsar=1") != 2 {
			t.Errorf("expected setsar=1 on both legs, got %d", strings.Count(filter, "setsar=1"))
		}

		// The first zscale should combine resize + linearize
		if strings.Count(filter, "w=-2:h=1080") != 2 {
			t.Errorf("expected resize on both legs, got %d", strings.Count(filter, "w=-2:h=1080"))
		}

		// Verify pipeline order: the combined zscale (with w=) must come before
		// the primaries conversion and tonemap
		resizeIdx := strings.Index(filter, "w=-2:h=1080")
		primariesIdx := strings.Index(filter, "zscale=p=bt709")
		tonemapIdx := strings.Index(filter, "tonemap=")

		if resizeIdx == -1 || primariesIdx == -1 || tonemapIdx == -1 {
			t.Fatal("missing required filter components")
		}
		if resizeIdx > primariesIdx {
			t.Error("resize+linearize must come BEFORE primaries conversion")
		}
		if primariesIdx > tonemapIdx {
			t.Error("primaries conversion must come BEFORE tonemap")
		}

		// Should still have full pipeline
		if strings.Count(filter, "tonemap=hable") != 2 {
			t.Errorf("expected tonemap on both legs, got %d", strings.Count(filter, "tonemap=hable"))
		}
		if !strings.Contains(filter, "[dist][ref]libvmaf=") {
			t.Error("missing libvmaf filter")
		}
	})
}

func TestBuildHDRScoringFilterHLG(t *testing.T) {
	// Test with HLG (arib-std-b67) - requires different input transfer
	filter := buildHDRScoringFilter("vmaf_v0.6.1", 4, "hable", "arib-std-b67", 1080, false)

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
			filter := buildHDRScoringFilter("vmaf_v0.6.1", 4, "hable", tc.inputTransfer, 1080, false)
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
			filter := buildHDRScoringFilter("vmaf_v0.6.1", 4, algo, "smpte2084", 1080, false)
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

func TestScoringHeight(t *testing.T) {
	tests := []struct {
		name   string
		inputH int
		wantH  int
	}{
		{"4K downscales to 1080", 2160, 1080},
		{"1440p downscales to 1080", 1440, 1080},
		{"1082 downscales to 1080", 1082, 1080},
		{"1081 downscales to 1080", 1081, 1080},
		{"1080 stays native", 1080, 1080},
		{"720 stays native", 720, 720},
		{"480 stays native", 480, 480},
		{"odd height 719 gets even-clamped", 719, 718},
		{"odd height 1079 gets even-clamped", 1079, 1078},
		{"zero defaults to 1080", 0, 1080},
		{"negative defaults to 1080", -1, 1080},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scoringHeight(tt.inputH)
			if got != tt.wantH {
				t.Errorf("scoringHeight(%d) = %d, want %d", tt.inputH, got, tt.wantH)
			}
		})
	}
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

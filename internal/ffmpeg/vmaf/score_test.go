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

func TestBuildSDRScoringFilterThreadCount(t *testing.T) {
	threadCounts := []int{1, 2, 4, 8}
	for _, threads := range threadCounts {
		t.Run(fmt.Sprintf("%d_threads", threads), func(t *testing.T) {
			filter := buildSDRScoringFilter("vmaf_v0.6.1", threads, 1080, false)
			expected := fmt.Sprintf("n_threads=%d", threads)
			if !strings.Contains(filter, expected) {
				t.Errorf("expected %s in filter, got: %s", expected, filter)
			}
		})
	}
}

func TestScoreSignature(t *testing.T) {
	// Verifies the function signature compiles without tonemap param
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately to avoid actual FFmpeg call

	_, err := Score(ctx, "ffmpeg", "ref.mkv", "dist.mkv", 1080, 4)
	// Error expected due to cancelled context or missing files
	_ = err
}

func TestScoreSamplesSignature(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	refSamples := []*Sample{{Path: "ref.mkv"}}
	distSamples := []*Sample{{Path: "dist.mkv"}}

	_, err := ScoreSamples(ctx, "ffmpeg", refSamples, distSamples, 1080)
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

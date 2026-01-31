package vmaf

import (
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

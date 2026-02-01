package vmaf

import (
	"context"
	"testing"
	"time"
)

func TestExtractSamplesSignatureNoTonemap(t *testing.T) {
	// Verify the new signature without tonemap parameter compiles
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Should compile without tonemap parameter (6 args, not 7)
	_, err := ExtractSamples(ctx, "ffmpeg", "input.mkv", "/tmp", 60*time.Second, []float64{0.5})
	// Error expected due to cancelled context
	_ = err
}

func TestSamplePositions(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     []float64
	}{
		{"very short", 10 * time.Second, []float64{0.5}},
		{"short video", 45 * time.Second, []float64{0.5}},
		{"normal video", 120 * time.Second, []float64{0.25, 0.50, 0.75}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SamplePositions(tt.duration)
			if len(got) != len(tt.want) {
				t.Errorf("SamplePositions() len = %d, want %d", len(got), len(tt.want))
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("SamplePositions()[%d] = %v, want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSamplePositionsEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     []float64
	}{
		{"zero duration", 0, []float64{0.5}},
		{"negative duration", -5 * time.Second, []float64{0.5}},
		{"exactly 59s", 59 * time.Second, []float64{0.5}},
		{"exactly 60s", 60 * time.Second, []float64{0.25, 0.50, 0.75}},
		{"very long video", 3600 * time.Second, []float64{0.25, 0.50, 0.75}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SamplePositions(tt.duration)
			if len(got) != len(tt.want) {
				t.Errorf("SamplePositions(%v) = %v, want %v", tt.duration, got, tt.want)
			}
		})
	}
}

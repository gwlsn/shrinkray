package vmaf

import (
	"testing"
	"time"
)

func TestSamplePositions(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     []float64
	}{
		{"very short", 10 * time.Second, []float64{0.5}},
		{"short video", 25 * time.Second, []float64{0.10, 0.30, 0.50, 0.70, 0.90}},
		{"normal video", 60 * time.Second, []float64{0.10, 0.30, 0.50, 0.70, 0.90}},
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
		{"exactly 14s", 14 * time.Second, []float64{0.5}},
		{"exactly 15s", 15 * time.Second, []float64{0.10, 0.30, 0.50, 0.70, 0.90}},
		{"very long video", 3600 * time.Second, []float64{0.10, 0.30, 0.50, 0.70, 0.90}},
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

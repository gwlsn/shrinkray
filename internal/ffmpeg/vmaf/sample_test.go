package vmaf

import (
	"testing"
	"time"
)

func TestSamplePositions(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		fastMode bool
		want     []float64
	}{
		{"very short", 10 * time.Second, false, []float64{0.5}},
		{"short no fast", 25 * time.Second, false, []float64{0.25, 0.5, 0.75}},
		{"short with fast", 25 * time.Second, true, []float64{0.25, 0.5, 0.75}}, // fast mode ignored for short
		{"normal fast", 60 * time.Second, true, []float64{0.5}},
		{"normal full", 60 * time.Second, false, []float64{0.25, 0.5, 0.75}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SamplePositions(tt.duration, tt.fastMode)
			if len(got) != len(tt.want) {
				t.Errorf("SamplePositions() = %v, want %v", got, tt.want)
			}
		})
	}
}

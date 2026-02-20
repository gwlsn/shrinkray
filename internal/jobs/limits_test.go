package jobs_test

import (
	"testing"

	"github.com/gwlsn/shrinkray/internal/jobs"
)

func TestClampWorkerCount(t *testing.T) {
	// ClampWorkerCount constrains the input to [MinWorkers, MaxWorkers].
	// We test three categories: below minimum (should clamp up), above
	// maximum (should clamp down), and values within range (pass through).
	tests := []struct {
		name  string
		input int
		want  int
	}{
		{"below minimum (zero)", 0, jobs.MinWorkers},
		{"below minimum (negative)", -5, jobs.MinWorkers},
		{"at minimum", jobs.MinWorkers, jobs.MinWorkers},
		{"within range (3)", 3, 3},
		{"within range (middle)", (jobs.MinWorkers + jobs.MaxWorkers) / 2, (jobs.MinWorkers + jobs.MaxWorkers) / 2},
		{"at maximum", jobs.MaxWorkers, jobs.MaxWorkers},
		{"above maximum", jobs.MaxWorkers + 10, jobs.MaxWorkers},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := jobs.ClampWorkerCount(tt.input)
			if got != tt.want {
				t.Errorf("ClampWorkerCount(%d) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestClampAnalysisCount(t *testing.T) {
	// Same pattern as ClampWorkerCount but for the VMAF analysis concurrency
	// range [MinConcurrentAnalyses, MaxConcurrentAnalyses].
	tests := []struct {
		name  string
		input int
		want  int
	}{
		{"below minimum (zero)", 0, jobs.MinConcurrentAnalyses},
		{"below minimum (negative)", -1, jobs.MinConcurrentAnalyses},
		{"at minimum", jobs.MinConcurrentAnalyses, jobs.MinConcurrentAnalyses},
		{"within range (2)", 2, 2},
		{"at maximum", jobs.MaxConcurrentAnalyses, jobs.MaxConcurrentAnalyses},
		{"above maximum", jobs.MaxConcurrentAnalyses + 5, jobs.MaxConcurrentAnalyses},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := jobs.ClampAnalysisCount(tt.input)
			if got != tt.want {
				t.Errorf("ClampAnalysisCount(%d) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsValidAnalysisCount(t *testing.T) {
	// IsValidAnalysisCount returns true only when the value falls within
	// [MinConcurrentAnalyses, MaxConcurrentAnalyses]. We specifically test
	// the boundary values (min-1, min, max, max+1) because off-by-one errors
	// are the most common bug in range checks.
	tests := []struct {
		name  string
		input int
		want  bool
	}{
		{"below minimum", jobs.MinConcurrentAnalyses - 1, false},
		{"at minimum", jobs.MinConcurrentAnalyses, true},
		{"within range (2)", 2, true},
		{"at maximum", jobs.MaxConcurrentAnalyses, true},
		{"above maximum", jobs.MaxConcurrentAnalyses + 1, false},
		{"zero", 0, false},
		{"negative", -1, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := jobs.IsValidAnalysisCount(tt.input)
			if got != tt.want {
				t.Errorf("IsValidAnalysisCount(%d) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsValidSmartShrinkQuality(t *testing.T) {
	// IsValidSmartShrinkQuality checks against the three recognized quality
	// tiers: "acceptable", "good", and "excellent". We test all valid values
	// plus several invalid ones to confirm proper rejection.
	tests := []struct {
		name    string
		quality string
		want    bool
	}{
		{"acceptable", "acceptable", true},
		{"good", "good", true},
		{"excellent", "excellent", true},
		{"empty string", "", false},
		{"invalid quality", "best", false},
		{"uppercase (case sensitive)", "Good", false},
		{"extra whitespace", " good ", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := jobs.IsValidSmartShrinkQuality(tt.quality)
			if got != tt.want {
				t.Errorf("IsValidSmartShrinkQuality(%q) = %v, want %v", tt.quality, got, tt.want)
			}
		})
	}
}

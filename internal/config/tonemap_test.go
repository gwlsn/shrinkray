package config

import (
	"testing"
)

func TestIsValidTonemapAlgorithm(t *testing.T) {
	// IsValidTonemapAlgorithm checks the input against ValidTonemapAlgorithms.
	// These map to FFmpeg's zscale tonemap filter options, so getting the
	// validation right prevents invalid filter strings from reaching FFmpeg.
	tests := []struct {
		name      string
		algorithm string
		want      bool
	}{
		// All valid algorithms (one test per entry in ValidTonemapAlgorithms)
		{"hable", "hable", true},
		{"bt2390", "bt2390", true},
		{"reinhard", "reinhard", true},
		{"mobius", "mobius", true},
		{"clip", "clip", true},
		{"linear", "linear", true},
		{"gamma", "gamma", true},

		// Invalid inputs
		{"empty string", "", false},
		{"unknown algorithm", "filmic", false},
		{"uppercase (case sensitive)", "Hable", false},
		{"extra whitespace", " hable ", false},
		{"partial match", "hab", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsValidTonemapAlgorithm(tt.algorithm)
			if got != tt.want {
				t.Errorf("IsValidTonemapAlgorithm(%q) = %v, want %v", tt.algorithm, got, tt.want)
			}
		})
	}
}

func TestValidateTonemapAlgorithm(t *testing.T) {
	// ValidateTonemapAlgorithm returns the input if valid, or the default
	// ("hable") if invalid. This is a "sanitize" pattern: the caller always
	// gets back a usable algorithm string, never an empty or bogus one.
	tests := []struct {
		name      string
		algorithm string
		want      string
	}{
		// Valid inputs should pass through unchanged
		{"valid hable", "hable", "hable"},
		{"valid bt2390", "bt2390", "bt2390"},
		{"valid reinhard", "reinhard", "reinhard"},
		{"valid mobius", "mobius", "mobius"},

		// Invalid inputs should return the default algorithm
		{"empty string returns default", "", DefaultTonemapAlgorithm},
		{"unknown returns default", "filmic", DefaultTonemapAlgorithm},
		{"case mismatch returns default", "Hable", DefaultTonemapAlgorithm},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ValidateTonemapAlgorithm(tt.algorithm)
			if got != tt.want {
				t.Errorf("ValidateTonemapAlgorithm(%q) = %q, want %q", tt.algorithm, got, tt.want)
			}
		})
	}
}

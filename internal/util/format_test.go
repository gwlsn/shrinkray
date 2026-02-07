package util

import (
	"testing"
	"time"
)

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		input    time.Duration
		expected string
	}{
		{5 * time.Second, "5s"},
		{65 * time.Second, "1m 5s"},
		{3600 * time.Second, "1h 0m"},
		{3665 * time.Second, "1h 1m"},
		{-1 * time.Second, ""},
	}

	for _, tt := range tests {
		result := FormatDuration(tt.input)
		if result != tt.expected {
			t.Errorf("FormatDuration(%v) = %s, expected %s", tt.input, result, tt.expected)
		}
	}
}

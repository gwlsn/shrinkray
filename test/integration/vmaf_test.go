//go:build integration

package integration

import (
	"testing"

	"github.com/gwlsn/shrinkray/internal/ffmpeg/vmaf"
)

func TestVMAFDetection(t *testing.T) {
	vmaf.DetectVMAF("ffmpeg")

	if !vmaf.IsAvailable() {
		t.Skip("VMAF not available in this FFmpeg build")
	}

	models := vmaf.GetModels()
	if len(models) == 0 {
		t.Error("Expected at least one VMAF model")
	}

	t.Logf("VMAF available: %v", vmaf.IsAvailable())
	t.Logf("Available models: %v", models)
}

func TestVMAFModelSelection(t *testing.T) {
	vmaf.DetectVMAF("ffmpeg")

	if !vmaf.IsAvailable() {
		t.Skip("VMAF not available in this FFmpeg build")
	}

	// Test model selection for different resolutions
	tests := []struct {
		name   string
		height int
	}{
		{"1080p", 1080},
		{"720p", 720},
		{"4K", 2160},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := vmaf.SelectModel(tt.height)
			if model == "" {
				t.Error("SelectModel returned empty string")
			}
			t.Logf("Selected model for %s: %s", tt.name, model)
		})
	}
}

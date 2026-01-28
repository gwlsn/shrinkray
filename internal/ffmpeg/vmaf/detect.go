package vmaf

import (
	"os/exec"
	"strings"
)

var (
	vmafAvailable bool
	vmafModels    []string
)

// DetectVMAF probes FFmpeg for libvmaf support and available models.
// Must be called at startup after FFmpeg path is known.
func DetectVMAF(ffmpegPath string) {
	// Reset state before detection
	vmafAvailable = false
	vmafModels = nil

	// Check if libvmaf filter is available
	cmd := exec.Command(ffmpegPath, "-filters")
	output, err := cmd.Output()
	if err != nil {
		return
	}

	vmafAvailable = strings.Contains(string(output), "libvmaf")
	if !vmafAvailable {
		return
	}

	// Detect available models
	vmafModels = detectModels(ffmpegPath)
}

// IsAvailable returns true if libvmaf filter is available
func IsAvailable() bool {
	return vmafAvailable
}

// GetModels returns available VMAF model names
func GetModels() []string {
	return vmafModels
}

// SelectModel returns the appropriate model for the given resolution
func SelectModel(height int) string {
	// Prefer 4K model for >1080p content if available
	if height > 1080 {
		for _, m := range vmafModels {
			if strings.Contains(m, "4k") {
				return m
			}
		}
	}
	// Fall back to default model
	for _, m := range vmafModels {
		if strings.Contains(m, "vmaf_v0.6.1") && !strings.Contains(m, "4k") {
			return m
		}
	}
	// Last resort: return first available
	if len(vmafModels) > 0 {
		return vmafModels[0]
	}
	return "vmaf_v0.6.1" // Default, may fail if not present
}

func detectModels(ffmpegPath string) []string {
	// Try to run a VMAF filter to see what models are available
	// This is a heuristic - models are compiled into libvmaf
	models := []string{"vmaf_v0.6.1"}

	// Try 4K model
	cmd := exec.Command(ffmpegPath, "-h", "filter=libvmaf")
	output, _ := cmd.Output()
	if strings.Contains(string(output), "vmaf_4k") {
		models = append(models, "vmaf_4k_v0.6.1")
	}

	return models
}

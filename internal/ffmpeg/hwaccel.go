package ffmpeg

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// HWAccel represents a hardware acceleration method
type HWAccel string

const (
	HWAccelNone        HWAccel = "none"        // Software encoding (libx265)
	HWAccelVideoToolbox HWAccel = "videotoolbox" // Apple Silicon / Intel Mac
	HWAccelNVENC       HWAccel = "nvenc"       // NVIDIA GPU
	HWAccelQSV         HWAccel = "qsv"         // Intel Quick Sync
	HWAccelVAAPI       HWAccel = "vaapi"       // Linux VA-API (Intel/AMD)
)

// HWEncoder contains info about a hardware encoder
type HWEncoder struct {
	Accel       HWAccel `json:"accel"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Encoder     string  `json:"encoder"`     // FFmpeg encoder name (e.g., hevc_videotoolbox)
	Available   bool    `json:"available"`
}

// AvailableEncoders holds the detected hardware encoders
type AvailableEncoders struct {
	mu       sync.RWMutex
	encoders map[HWAccel]*HWEncoder
	detected bool
}

// Global encoder detection cache
var availableEncoders = &AvailableEncoders{
	encoders: make(map[HWAccel]*HWEncoder),
}

// AllEncoders returns the list of all possible encoders with their status
var allEncoderDefs = []*HWEncoder{
	{
		Accel:       HWAccelVideoToolbox,
		Name:        "VideoToolbox",
		Description: "Apple Silicon / Intel Mac hardware encoding",
		Encoder:     "hevc_videotoolbox",
	},
	{
		Accel:       HWAccelNVENC,
		Name:        "NVENC",
		Description: "NVIDIA GPU hardware encoding",
		Encoder:     "hevc_nvenc",
	},
	{
		Accel:       HWAccelQSV,
		Name:        "Quick Sync",
		Description: "Intel Quick Sync hardware encoding",
		Encoder:     "hevc_qsv",
	},
	{
		Accel:       HWAccelVAAPI,
		Name:        "VAAPI",
		Description: "Linux VA-API hardware encoding (Intel/AMD)",
		Encoder:     "hevc_vaapi",
	},
	{
		Accel:       HWAccelNone,
		Name:        "Software",
		Description: "CPU-based encoding (slower but always available)",
		Encoder:     "libx265",
		Available:   true, // Software is always available
	},
}

// DetectEncoders probes FFmpeg to detect available hardware encoders
func DetectEncoders(ffmpegPath string) map[HWAccel]*HWEncoder {
	availableEncoders.mu.Lock()
	defer availableEncoders.mu.Unlock()

	// Return cached results if already detected
	if availableEncoders.detected {
		return copyEncoders(availableEncoders.encoders)
	}

	// Get list of available encoders from ffmpeg
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffmpegPath, "-encoders", "-hide_banner")
	output, err := cmd.Output()
	if err != nil {
		// Fallback to software only
		availableEncoders.encoders[HWAccelNone] = &HWEncoder{
			Accel:       HWAccelNone,
			Name:        "Software",
			Description: "CPU-based encoding (slower but always available)",
			Encoder:     "libx265",
			Available:   true,
		}
		availableEncoders.detected = true
		return copyEncoders(availableEncoders.encoders)
	}

	encoderList := string(output)

	// Check each encoder
	for _, enc := range allEncoderDefs {
		encCopy := *enc
		if enc.Accel == HWAccelNone {
			encCopy.Available = true
		} else {
			encCopy.Available = strings.Contains(encoderList, enc.Encoder)
		}
		availableEncoders.encoders[enc.Accel] = &encCopy
	}

	availableEncoders.detected = true
	return copyEncoders(availableEncoders.encoders)
}

// GetAvailableEncoders returns detected encoders (must call DetectEncoders first)
func GetAvailableEncoders() map[HWAccel]*HWEncoder {
	availableEncoders.mu.RLock()
	defer availableEncoders.mu.RUnlock()
	return copyEncoders(availableEncoders.encoders)
}

// GetEncoder returns a specific encoder by accel type
func GetEncoder(accel HWAccel) *HWEncoder {
	availableEncoders.mu.RLock()
	defer availableEncoders.mu.RUnlock()
	if enc, ok := availableEncoders.encoders[accel]; ok {
		encCopy := *enc
		return &encCopy
	}
	return nil
}

// IsEncoderAvailable checks if a specific encoder is available
func IsEncoderAvailable(accel HWAccel) bool {
	enc := GetEncoder(accel)
	return enc != nil && enc.Available
}

// GetBestEncoder returns the best available encoder (prefer hardware)
func GetBestEncoder() *HWEncoder {
	// Priority: VideoToolbox > NVENC > QSV > VAAPI > Software
	priority := []HWAccel{HWAccelVideoToolbox, HWAccelNVENC, HWAccelQSV, HWAccelVAAPI, HWAccelNone}

	for _, accel := range priority {
		if IsEncoderAvailable(accel) {
			return GetEncoder(accel)
		}
	}

	// Fallback to software
	return &HWEncoder{
		Accel:       HWAccelNone,
		Name:        "Software",
		Description: "CPU-based encoding",
		Encoder:     "libx265",
		Available:   true,
	}
}

// ListAvailableEncoders returns a slice of available encoders
func ListAvailableEncoders() []*HWEncoder {
	availableEncoders.mu.RLock()
	defer availableEncoders.mu.RUnlock()

	var result []*HWEncoder
	// Return in priority order
	priority := []HWAccel{HWAccelVideoToolbox, HWAccelNVENC, HWAccelQSV, HWAccelVAAPI, HWAccelNone}
	for _, accel := range priority {
		if enc, ok := availableEncoders.encoders[accel]; ok && enc.Available {
			encCopy := *enc
			result = append(result, &encCopy)
		}
	}
	return result
}

func copyEncoders(src map[HWAccel]*HWEncoder) map[HWAccel]*HWEncoder {
	dst := make(map[HWAccel]*HWEncoder)
	for k, v := range src {
		encCopy := *v
		dst[k] = &encCopy
	}
	return dst
}

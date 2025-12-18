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
	HWAccelNone         HWAccel = "none"         // Software encoding
	HWAccelVideoToolbox HWAccel = "videotoolbox" // Apple Silicon / Intel Mac
	HWAccelNVENC        HWAccel = "nvenc"        // NVIDIA GPU
	HWAccelQSV          HWAccel = "qsv"          // Intel Quick Sync
	HWAccelVAAPI        HWAccel = "vaapi"        // Linux VA-API (Intel/AMD)
)

// Codec represents the target video codec
type Codec string

const (
	CodecHEVC Codec = "hevc"
	CodecAV1  Codec = "av1"
)

// HWEncoder contains info about a hardware encoder
type HWEncoder struct {
	Accel       HWAccel `json:"accel"`
	Codec       Codec   `json:"codec"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Encoder     string  `json:"encoder"` // FFmpeg encoder name (e.g., hevc_videotoolbox)
	Available   bool    `json:"available"`
}

// EncoderKey uniquely identifies an encoder by accel + codec
type EncoderKey struct {
	Accel HWAccel
	Codec Codec
}

// AvailableEncoders holds the detected hardware encoders
type AvailableEncoders struct {
	mu       sync.RWMutex
	encoders map[EncoderKey]*HWEncoder
	detected bool
}

// Global encoder detection cache
var availableEncoders = &AvailableEncoders{
	encoders: make(map[EncoderKey]*HWEncoder),
}

// allEncoderDefs defines all possible encoders (HEVC and AV1 variants)
var allEncoderDefs = []*HWEncoder{
	// HEVC encoders
	{
		Accel:       HWAccelVideoToolbox,
		Codec:       CodecHEVC,
		Name:        "VideoToolbox HEVC",
		Description: "Apple Silicon / Intel Mac hardware HEVC encoding",
		Encoder:     "hevc_videotoolbox",
	},
	{
		Accel:       HWAccelNVENC,
		Codec:       CodecHEVC,
		Name:        "NVENC HEVC",
		Description: "NVIDIA GPU hardware HEVC encoding",
		Encoder:     "hevc_nvenc",
	},
	{
		Accel:       HWAccelQSV,
		Codec:       CodecHEVC,
		Name:        "Quick Sync HEVC",
		Description: "Intel Quick Sync hardware HEVC encoding",
		Encoder:     "hevc_qsv",
	},
	{
		Accel:       HWAccelVAAPI,
		Codec:       CodecHEVC,
		Name:        "VAAPI HEVC",
		Description: "Linux VA-API hardware HEVC encoding (Intel/AMD)",
		Encoder:     "hevc_vaapi",
	},
	{
		Accel:       HWAccelNone,
		Codec:       CodecHEVC,
		Name:        "Software HEVC",
		Description: "CPU-based HEVC encoding (libx265)",
		Encoder:     "libx265",
		Available:   true, // Software is always available
	},
	// AV1 encoders
	{
		Accel:       HWAccelVideoToolbox,
		Codec:       CodecAV1,
		Name:        "VideoToolbox AV1",
		Description: "Apple Silicon (M3+) hardware AV1 encoding",
		Encoder:     "av1_videotoolbox",
	},
	{
		Accel:       HWAccelNVENC,
		Codec:       CodecAV1,
		Name:        "NVENC AV1",
		Description: "NVIDIA GPU (RTX 40+) hardware AV1 encoding",
		Encoder:     "av1_nvenc",
	},
	{
		Accel:       HWAccelQSV,
		Codec:       CodecAV1,
		Name:        "Quick Sync AV1",
		Description: "Intel Arc hardware AV1 encoding",
		Encoder:     "av1_qsv",
	},
	{
		Accel:       HWAccelVAAPI,
		Codec:       CodecAV1,
		Name:        "VAAPI AV1",
		Description: "Linux VA-API hardware AV1 encoding (Intel/AMD)",
		Encoder:     "av1_vaapi",
	},
	{
		Accel:       HWAccelNone,
		Codec:       CodecAV1,
		Name:        "Software AV1",
		Description: "CPU-based AV1 encoding (SVT-AV1)",
		Encoder:     "libsvtav1",
		Available:   true, // Software is always available (if ffmpeg has it)
	},
}

// DetectEncoders probes FFmpeg to detect available hardware encoders
func DetectEncoders(ffmpegPath string) map[EncoderKey]*HWEncoder {
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
		availableEncoders.encoders[EncoderKey{HWAccelNone, CodecHEVC}] = &HWEncoder{
			Accel:       HWAccelNone,
			Codec:       CodecHEVC,
			Name:        "Software HEVC",
			Description: "CPU-based HEVC encoding",
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
		key := EncoderKey{enc.Accel, enc.Codec}

		if enc.Accel == HWAccelNone {
			// Software encoders - check if available in ffmpeg
			encCopy.Available = strings.Contains(encoderList, enc.Encoder)
		} else {
			encCopy.Available = strings.Contains(encoderList, enc.Encoder)
		}
		availableEncoders.encoders[key] = &encCopy
	}

	availableEncoders.detected = true
	return copyEncoders(availableEncoders.encoders)
}

// GetAvailableEncoders returns all detected encoders (must call DetectEncoders first)
func GetAvailableEncoders() map[EncoderKey]*HWEncoder {
	availableEncoders.mu.RLock()
	defer availableEncoders.mu.RUnlock()
	return copyEncoders(availableEncoders.encoders)
}

// GetEncoderByKey returns a specific encoder by accel type and codec
func GetEncoderByKey(accel HWAccel, codec Codec) *HWEncoder {
	availableEncoders.mu.RLock()
	defer availableEncoders.mu.RUnlock()
	key := EncoderKey{accel, codec}
	if enc, ok := availableEncoders.encoders[key]; ok {
		encCopy := *enc
		return &encCopy
	}
	return nil
}

// GetEncoder returns a specific HEVC encoder by accel type (for backward compatibility)
func GetEncoder(accel HWAccel) *HWEncoder {
	return GetEncoderByKey(accel, CodecHEVC)
}

// IsEncoderAvailable checks if a specific encoder is available
func IsEncoderAvailable(accel HWAccel) bool {
	enc := GetEncoder(accel)
	return enc != nil && enc.Available
}

// IsEncoderAvailableForCodec checks if a specific encoder is available for a codec
func IsEncoderAvailableForCodec(accel HWAccel, codec Codec) bool {
	enc := GetEncoderByKey(accel, codec)
	return enc != nil && enc.Available
}

// GetBestEncoderForCodec returns the best available encoder for a given codec (prefer hardware)
func GetBestEncoderForCodec(codec Codec) *HWEncoder {
	// Priority: VideoToolbox > NVENC > QSV > VAAPI > Software
	priority := []HWAccel{HWAccelVideoToolbox, HWAccelNVENC, HWAccelQSV, HWAccelVAAPI, HWAccelNone}

	for _, accel := range priority {
		if IsEncoderAvailableForCodec(accel, codec) {
			return GetEncoderByKey(accel, codec)
		}
	}

	// Fallback to software
	if codec == CodecAV1 {
		return &HWEncoder{
			Accel:       HWAccelNone,
			Codec:       CodecAV1,
			Name:        "Software AV1",
			Description: "CPU-based AV1 encoding",
			Encoder:     "libsvtav1",
			Available:   true,
		}
	}
	return &HWEncoder{
		Accel:       HWAccelNone,
		Codec:       CodecHEVC,
		Name:        "Software HEVC",
		Description: "CPU-based HEVC encoding",
		Encoder:     "libx265",
		Available:   true,
	}
}

// GetBestEncoder returns the best available HEVC encoder (for backward compatibility)
func GetBestEncoder() *HWEncoder {
	return GetBestEncoderForCodec(CodecHEVC)
}

// ListAvailableEncoders returns a slice of available encoders for all codecs
func ListAvailableEncoders() []*HWEncoder {
	availableEncoders.mu.RLock()
	defer availableEncoders.mu.RUnlock()

	var result []*HWEncoder
	// Return in priority order (HEVC first, then AV1)
	priority := []HWAccel{HWAccelVideoToolbox, HWAccelNVENC, HWAccelQSV, HWAccelVAAPI, HWAccelNone}
	codecs := []Codec{CodecHEVC, CodecAV1}

	for _, codec := range codecs {
		for _, accel := range priority {
			key := EncoderKey{accel, codec}
			if enc, ok := availableEncoders.encoders[key]; ok && enc.Available {
				encCopy := *enc
				result = append(result, &encCopy)
			}
		}
	}
	return result
}

// ListAvailableEncodersForCodec returns a slice of available encoders for a specific codec
func ListAvailableEncodersForCodec(codec Codec) []*HWEncoder {
	availableEncoders.mu.RLock()
	defer availableEncoders.mu.RUnlock()

	var result []*HWEncoder
	priority := []HWAccel{HWAccelVideoToolbox, HWAccelNVENC, HWAccelQSV, HWAccelVAAPI, HWAccelNone}

	for _, accel := range priority {
		key := EncoderKey{accel, codec}
		if enc, ok := availableEncoders.encoders[key]; ok && enc.Available {
			encCopy := *enc
			result = append(result, &encCopy)
		}
	}
	return result
}

func copyEncoders(src map[EncoderKey]*HWEncoder) map[EncoderKey]*HWEncoder {
	dst := make(map[EncoderKey]*HWEncoder)
	for k, v := range src {
		encCopy := *v
		dst[k] = &encCopy
	}
	return dst
}

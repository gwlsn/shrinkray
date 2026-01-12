package ffmpeg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
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
	mu          sync.RWMutex
	encoders    map[EncoderKey]*HWEncoder
	detected    bool
	vaapiDevice string // Auto-detected VAAPI device path (e.g., /dev/dri/renderD128)
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

		// First check if encoder exists in ffmpeg
		if !strings.Contains(encoderList, enc.Encoder) {
			encCopy.Available = false
			availableEncoders.encoders[key] = &encCopy
			continue
		}

		if enc.Accel == HWAccelNone {
			// Software encoders - just check if listed in ffmpeg
			encCopy.Available = true
		} else {
			// Hardware encoders - actually test if they work
			encCopy.Available = testEncoder(ffmpegPath, enc.Encoder)
		}
		availableEncoders.encoders[key] = &encCopy
	}

	availableEncoders.detected = true
	return copyEncoders(availableEncoders.encoders)
}

// detectVAAPIDevice finds the first available VAAPI render device
func detectVAAPIDevice() string {
	driPath := "/dev/dri"
	entries, err := os.ReadDir(driPath)
	if err != nil {
		return ""
	}

	// Collect all renderD* devices
	var devices []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "renderD") {
			devices = append(devices, filepath.Join(driPath, entry.Name()))
		}
	}

	// Sort to get consistent ordering (renderD128, renderD129, etc.)
	sort.Strings(devices)

	// Return the first one found
	if len(devices) > 0 {
		return devices[0]
	}
	return ""
}

// testEncoder tries a quick test encode to verify hardware encoder actually works
func testEncoder(ffmpegPath string, encoder string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var args []string

	// QSV on Linux: Must derive from VAAPI (this is the Jellyfin approach)
	// This tests the actual pipeline we use in production
	if strings.Contains(encoder, "qsv") && runtime.GOOS == "linux" {
		device := detectVAAPIDevice()
		if device == "" {
			return false // No VAAPI device found - QSV won't work on Linux
		}
		// Store the detected device for later use
		availableEncoders.vaapiDevice = device
		// Test QSV with VAAPI derivation - matches presets.go pipeline
		args = []string{
			"-init_hw_device", "vaapi=va:" + device,
			"-init_hw_device", "qsv=qs@va",
			"-filter_hw_device", "qs",
			"-f", "lavfi",
			"-i", "color=c=black:s=256x256:d=0.1",
			"-vf", "format=nv12,hwupload=extra_hw_frames=64",
			"-frames:v", "1",
			"-c:v", encoder,
			"-f", "null",
			"-",
		}
	} else if strings.Contains(encoder, "vaapi") {
		// VAAPI: Use modern init_hw_device style (matches presets.go)
		device := detectVAAPIDevice()
		if device == "" {
			return false // No VAAPI device found
		}
		// Store the detected device for later use
		availableEncoders.vaapiDevice = device
		args = []string{
			"-init_hw_device", "vaapi=va:" + device,
			"-filter_hw_device", "va",
			"-f", "lavfi",
			"-i", "color=c=black:s=256x256:d=0.1",
			"-vf", "format=nv12,hwupload",
			"-frames:v", "1",
			"-c:v", encoder,
			"-f", "null",
			"-",
		}
	} else {
		// Non-VAAPI/QSV encoders (NVENC, VideoToolbox) can accept software frames directly
		args = []string{
			"-f", "lavfi",
			"-i", "color=c=black:s=256x256:d=0.1",
			"-frames:v", "1",
			"-c:v", encoder,
			"-f", "null",
			"-",
		}
	}

	// Try to encode a single frame from a test pattern
	// This will fail fast if the hardware doesn't actually support the encoder
	// Note: Use 256x256 resolution - some hardware encoders (QSV) have minimum resolution requirements
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)

	// We don't care about output, just whether it succeeds
	err := cmd.Run()
	return err == nil
}

// GetVAAPIDevice returns the auto-detected VAAPI device path, or a default
func GetVAAPIDevice() string {
	availableEncoders.mu.RLock()
	defer availableEncoders.mu.RUnlock()
	if availableEncoders.vaapiDevice != "" {
		return availableEncoders.vaapiDevice
	}
	// Fallback to common default
	return "/dev/dri/renderD128"
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

func copyEncoders(src map[EncoderKey]*HWEncoder) map[EncoderKey]*HWEncoder {
	dst := make(map[EncoderKey]*HWEncoder)
	for k, v := range src {
		encCopy := *v
		dst[k] = &encCopy
	}
	return dst
}

// RequiresSoftwareDecode returns true if the video cannot be hardware decoded
// by the given encoder's associated hardware decoder. This allows proactive
// detection of unsupported formats before wasting time on a failed attempt.
func RequiresSoftwareDecode(codec, profile string, bitDepth int, encoder HWAccel) bool {
	// Software encoder has no hardware decode - no fallback needed
	if encoder == HWAccelNone {
		return false
	}

	codec = strings.ToLower(codec)
	profile = strings.ToLower(profile)

	// H.264/AVC 10-bit (High 10 profile) - NO GPU supports this
	// This is a universal hardware limitation, not driver-specific
	if (codec == "h264" || codec == "avc") && bitDepth >= 10 {
		return true
	}

	// Encoder-specific limitations
	switch encoder {
	case HWAccelQSV:
		// VC-1 decode is spotty on Intel QSV
		if codec == "vc1" || codec == "wmv3" {
			return true
		}
		// MPEG-4 ASP (DivX, XviD) not reliably supported
		// Simple Profile is supported, but Advanced Simple is not
		if codec == "mpeg4" && !strings.HasPrefix(profile, "simple") {
			return true
		}
	case HWAccelVAAPI:
		// VC-1 support varies by driver/hardware
		if codec == "vc1" || codec == "wmv3" {
			return true
		}
	case HWAccelNVENC:
		// NVDEC has broader codec support but still no H.264 10-bit
		// VC-1 decode was dropped in newer drivers
		if codec == "vc1" {
			return true
		}
	case HWAccelVideoToolbox:
		// VideoToolbox has good codec coverage on Apple Silicon
		// but still no H.264 10-bit (already caught above)
	}

	return false
}

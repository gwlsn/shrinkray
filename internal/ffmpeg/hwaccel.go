package ffmpeg

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
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

	log.Println("[encoder-detect] Starting hardware encoder detection...")

	// Get list of available encoders from ffmpeg
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffmpegPath, "-encoders", "-hide_banner")
	output, err := cmd.Output()
	if err != nil {
		log.Printf("[encoder-detect] Failed to query ffmpeg encoders: %v", err)
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
			log.Printf("[encoder-detect] %s: not listed in ffmpeg", enc.Encoder)
			encCopy.Available = false
			availableEncoders.encoders[key] = &encCopy
			continue
		}

		if enc.Accel == HWAccelNone {
			// Software encoders - just check if listed in ffmpeg
			log.Printf("[encoder-detect] %s: available (software)", enc.Encoder)
			encCopy.Available = true
		} else {
			// Hardware encoders - actually test if they work
			available := testEncoder(ffmpegPath, enc.Encoder)
			if available {
				log.Printf("[encoder-detect] %s: AVAILABLE (test encode passed)", enc.Encoder)
			} else {
				log.Printf("[encoder-detect] %s: not available (test encode failed)", enc.Encoder)
			}
			encCopy.Available = available
		}
		availableEncoders.encoders[key] = &encCopy
	}

	// Log summary of detected encoders
	log.Println("[encoder-detect] Detection complete. Available encoders:")
	for _, codec := range []Codec{CodecHEVC, CodecAV1} {
		best := getBestEncoderForCodecInternal(availableEncoders.encoders, codec)
		if best != nil {
			log.Printf("[encoder-detect]   %s: %s (%s)", codec, best.Name, best.Encoder)
		}
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

	// For NVENC encoders, first check if NVIDIA GPU is actually present
	// This prevents false positives when CUDA libraries are installed but no GPU exists
	if strings.Contains(encoder, "nvenc") {
		if !hasNVIDIADevice() {
			log.Printf("[encoder-detect] %s: skipped (no NVIDIA device found)", encoder)
			return false
		}
	}

	var args []string

	// For VAAPI encoders, we need to specify the device and upload frames to VAAPI memory
	if strings.Contains(encoder, "vaapi") {
		device := detectVAAPIDevice()
		if device == "" {
			return false // No VAAPI device found
		}
		// Store the detected device for later use
		availableEncoders.vaapiDevice = device
		// Build VAAPI-specific test command with hardware frame upload
		// The filter chain converts to nv12 and uploads to VAAPI memory
		args = []string{
			"-vaapi_device", device,
			"-f", "lavfi",
			"-i", "color=c=black:s=256x256:d=0.1",
			"-frames:v", "1",
			"-vf", "format=nv12,hwupload",
			"-c:v", encoder,
			"-f", "null",
			"-",
		}
	} else {
		// Build base args for non-VAAPI encoders
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

	// Capture stderr to check for specific errors
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Log the failure reason for debugging
		log.Printf("[encoder-detect] %s test failed: %v (output: %s)",
			encoder, err, truncateOutput(string(output), 200))
		return false
	}
	return true
}

// hasNVIDIADevice checks if an NVIDIA GPU is present in the system
func hasNVIDIADevice() bool {
	// Check for NVIDIA device files - this is the most reliable check
	if _, err := os.Stat("/dev/nvidia0"); err == nil {
		log.Println("[encoder-detect] Found /dev/nvidia0 - NVIDIA GPU present")
		return true
	}

	// Check for nvidia-smi and verify it actually lists a GPU
	if smiPath, err := exec.LookPath("nvidia-smi"); err == nil {
		cmd := exec.Command(smiPath, "-L")
		output, err := cmd.Output()
		if err != nil {
			log.Printf("[encoder-detect] nvidia-smi -L failed: %v", err)
			return false
		}
		// nvidia-smi -L outputs lines like "GPU 0: NVIDIA GeForce RTX 3080 (UUID: ...)"
		// If no GPU, it outputs nothing or an error message
		outputStr := strings.TrimSpace(string(output))
		if outputStr == "" || !strings.Contains(strings.ToLower(outputStr), "gpu") {
			log.Printf("[encoder-detect] nvidia-smi found but no GPU listed: %q", outputStr)
			return false
		}
		log.Printf("[encoder-detect] nvidia-smi found GPU: %s", strings.Split(outputStr, "\n")[0])
		return true
	}

	log.Println("[encoder-detect] No NVIDIA device or nvidia-smi found")
	return false
}

// truncateOutput truncates a string to maxLen characters for logging
func truncateOutput(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

// GetAvailableEncoders returns all detected encoders (must call DetectEncoders first)
func GetAvailableEncoders() map[EncoderKey]*HWEncoder {
	availableEncoders.mu.RLock()
	defer availableEncoders.mu.RUnlock()
	return copyEncoders(availableEncoders.encoders)
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

// getBestEncoderForCodecInternal returns the best encoder from a given map (for internal use)
func getBestEncoderForCodecInternal(encoders map[EncoderKey]*HWEncoder, codec Codec) *HWEncoder {
	// Priority: VideoToolbox > NVENC > VAAPI > QSV > Software
	// VAAPI is prioritized over QSV on Linux because:
	// - VAAPI is the native Linux hardware acceleration API
	// - QSV on Linux uses VAAPI as its decode backend anyway
	// - VAAPI provides a full GPU pipeline (decode -> encode) without CPU frame transfers
	priority := []HWAccel{HWAccelVideoToolbox, HWAccelNVENC, HWAccelVAAPI, HWAccelQSV, HWAccelNone}

	for _, accel := range priority {
		key := EncoderKey{accel, codec}
		if enc, ok := encoders[key]; ok && enc.Available {
			return enc
		}
	}
	return nil
}

// GetBestEncoderForCodec returns the best available encoder for a given codec (prefer hardware)
func GetBestEncoderForCodec(codec Codec) *HWEncoder {
	availableEncoders.mu.RLock()
	defer availableEncoders.mu.RUnlock()

	enc := getBestEncoderForCodecInternal(availableEncoders.encoders, codec)
	if enc != nil {
		encCopy := *enc
		return &encCopy
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
	// VAAPI prioritized over QSV for full GPU pipeline on Linux
	priority := []HWAccel{HWAccelVideoToolbox, HWAccelNVENC, HWAccelVAAPI, HWAccelQSV, HWAccelNone}
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

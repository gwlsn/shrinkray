package ffmpeg

import (
	"fmt"
	"strings"
)

// Preset defines a transcoding preset with its FFmpeg parameters
type Preset struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Encoder     HWAccel `json:"encoder"`    // Which encoder to use
	Codec       Codec   `json:"codec"`      // Target codec (HEVC or AV1)
	MaxHeight   int     `json:"max_height"` // 0 = no scaling, 1080, 720, etc.
}

// encoderSettings defines FFmpeg settings for each encoder
type encoderSettings struct {
	encoder     string   // FFmpeg encoder name
	qualityFlag string   // -crf, -b:v, -global_quality, etc.
	quality     string   // Quality value (CRF or bitrate modifier)
	extraArgs   []string // Additional encoder-specific args
	usesBitrate bool     // If true, quality value is a bitrate modifier (0.0-1.0)
	hwaccelArgs []string // Args to prepend before -i for hardware decoding
	scaleFilter string   // Hardware-specific scale filter (e.g., "scale_qsv", "scale_cuda")
	baseFilter  string   // Filter to prepend before scale (e.g., "format=nv12,hwupload" for VAAPI)
}

// Bitrate constraints for dynamic bitrate calculation (VideoToolbox).
// These bounds prevent extreme compression artifacts or excessive file sizes.
const (
	// minBitrateKbps prevents artifacts from over-compression.
	// 500 kbps is roughly equivalent to 480p DVD quality - going lower
	// produces noticeable blocking artifacts in most content.
	minBitrateKbps = 500

	// maxBitrateKbps caps output bitrate to prevent larger-than-source files.
	// 15000 kbps (15 Mbps) is typical for 4K streaming services.
	// Higher bitrates rarely improve perceptual quality with modern codecs.
	maxBitrateKbps = 15000
)

var encoderConfigs = map[EncoderKey]encoderSettings{
	// HEVC encoders
	{HWAccelNone, CodecHEVC}: {
		encoder:     "libx265",
		qualityFlag: "-crf",
		quality:     "26",
		extraArgs:   []string{"-preset", "medium"},
		scaleFilter: "scale",
	},
	{HWAccelVideoToolbox, CodecHEVC}: {
		// VideoToolbox uses bitrate control (-b:v) with dynamic calculation.
		// Target bitrate = source bitrate * modifier.
		// Unlike CRF-based encoders, VideoToolbox requires explicit bitrate targets.
		encoder:     "hevc_videotoolbox",
		qualityFlag: "-b:v",
		// 0.35 = 35% of source bitrate, typically yields 50-60% smaller files.
		// This value was chosen empirically to balance quality vs compression:
		// - 0.50 = minimal compression, near-transparent quality
		// - 0.35 = good balance (default) - comparable to x265 CRF 22-24
		// - 0.25 = aggressive compression, some quality loss on detailed scenes
		quality:     "0.35",
		extraArgs:   []string{"-allow_sw", "1"},
		usesBitrate: true,
		hwaccelArgs: []string{"-hwaccel", "videotoolbox"},
		scaleFilter: "scale", // VideoToolbox doesn't have a HW scaler, use CPU
	},
	{HWAccelNVENC, CodecHEVC}: {
		encoder:     "hevc_nvenc",
		qualityFlag: "-cq",
		quality:     "28",
		extraArgs:   []string{"-preset", "p4", "-tune", "hq", "-rc", "vbr"},
		// hwaccelArgs generated dynamically by getHwaccelInputArgs()
		scaleFilter: "scale_cuda",
		baseFilter:  "scale_cuda=format=nv12", // Explicit format for compatibility
	},
	{HWAccelQSV, CodecHEVC}: {
		encoder:     "hevc_qsv",
		qualityFlag: "-global_quality",
		quality:     "27",
		extraArgs:   []string{"-preset", "medium"},
		// hwaccelArgs generated dynamically by getHwaccelInputArgs() - QSV derived from VAAPI on Linux
		scaleFilter: "scale_qsv",
		baseFilter:  "format=nv12|qsv,hwupload=extra_hw_frames=64,scale_qsv=format=nv12", // Added scale_qsv for format compatibility
	},
	{HWAccelVAAPI, CodecHEVC}: {
		encoder:     "hevc_vaapi",
		qualityFlag: "-qp",
		quality:     "27",
		extraArgs:   []string{},
		// hwaccelArgs generated dynamically by getHwaccelInputArgs()
		scaleFilter: "scale_vaapi",
		baseFilter:  "format=nv12|vaapi,hwupload,scale_vaapi=format=nv12", // Added scale_vaapi for format compatibility
	},

	// AV1 encoders
	// More aggressive compression than HEVC - AV1 handles lower bitrates better
	{HWAccelNone, CodecAV1}: {
		encoder:     "libsvtav1",
		qualityFlag: "-crf",
		quality:     "35",
		extraArgs:   []string{"-preset", "6"},
		scaleFilter: "scale",
	},
	{HWAccelVideoToolbox, CodecAV1}: {
		// VideoToolbox AV1 (M3+ chips) uses bitrate control.
		// AV1 is more efficient than HEVC, so we use a lower bitrate multiplier.
		encoder:     "av1_videotoolbox",
		qualityFlag: "-b:v",
		// 0.25 = 25% of source bitrate.
		// AV1 achieves better quality at lower bitrates than HEVC, so this
		// more aggressive setting produces comparable visual quality to
		// HEVC at 0.35. Roughly equivalent to SVT-AV1 CRF 30-32.
		quality:     "0.25",
		extraArgs:   []string{"-allow_sw", "1"},
		usesBitrate: true,
		hwaccelArgs: []string{"-hwaccel", "videotoolbox"},
		scaleFilter: "scale", // VideoToolbox doesn't have a HW scaler, use CPU
	},
	{HWAccelNVENC, CodecAV1}: {
		encoder:     "av1_nvenc",
		qualityFlag: "-cq",
		quality:     "32",
		extraArgs:   []string{"-preset", "p4", "-tune", "hq", "-rc", "vbr"},
		// hwaccelArgs generated dynamically by getHwaccelInputArgs()
		scaleFilter: "scale_cuda",
		baseFilter:  "scale_cuda=format=nv12", // Explicit format for compatibility
	},
	{HWAccelQSV, CodecAV1}: {
		encoder:     "av1_qsv",
		qualityFlag: "-global_quality",
		quality:     "32",
		extraArgs:   []string{"-preset", "medium"},
		// hwaccelArgs generated dynamically by getHwaccelInputArgs() - QSV derived from VAAPI on Linux
		scaleFilter: "scale_qsv",
		baseFilter:  "format=nv12|qsv,hwupload=extra_hw_frames=64,scale_qsv=format=nv12", // Added scale_qsv for format compatibility
	},
	{HWAccelVAAPI, CodecAV1}: {
		encoder:     "av1_vaapi",
		qualityFlag: "-qp",
		quality:     "32",
		extraArgs:   []string{},
		// hwaccelArgs generated dynamically by getHwaccelInputArgs()
		scaleFilter: "scale_vaapi",
		baseFilter:  "format=nv12|vaapi,hwupload,scale_vaapi=format=nv12", // Added scale_vaapi for format compatibility
	},
}

// BasePresets defines the core presets
var BasePresets = []struct {
	ID          string
	Name        string
	Description string
	Codec       Codec
	MaxHeight   int
}{
	{"compress-hevc", "Compress (HEVC)", "Reduce size with HEVC encoding", CodecHEVC, 0},
	{"compress-av1", "Compress (AV1)", "Maximum compression with AV1 encoding", CodecAV1, 0},
	{"1080p", "Downscale to 1080p", "Downscale to 1080p max (HEVC)", CodecHEVC, 1080},
	{"720p", "Downscale to 720p", "Downscale to 720p (big savings)", CodecHEVC, 720},
}

// GetEncoderDefaults returns the default quality values for a given encoder.
// For bitrate-based encoders (VideoToolbox), returns 0 to indicate "use software defaults".
func GetEncoderDefaults(encoder HWAccel) (hevcDefault, av1Default int) {
	hevcConfig := encoderConfigs[EncoderKey{encoder, CodecHEVC}]
	av1Config := encoderConfigs[EncoderKey{encoder, CodecAV1}]

	// Parse defaults (skip bitrate-based encoders - they return 0)
	if !hevcConfig.usesBitrate {
		fmt.Sscanf(hevcConfig.quality, "%d", &hevcDefault)
	}
	if !av1Config.usesBitrate {
		fmt.Sscanf(av1Config.quality, "%d", &av1Default)
	}
	return
}

// crfToBitrateModifier converts a CRF value to a VideoToolbox bitrate modifier.
// This allows users to set CRF values (like Handbrake) even when using VideoToolbox,
// which only supports bitrate-based encoding.
//
// Formula: modifier = 0.8 - (crf * 0.02)
//
// The formula was derived empirically to approximate CRF behavior:
//   - CRF 15 → 0.50 (50% of source) - near-lossless, large files
//   - CRF 22 → 0.36 (36% of source) - high quality, good balance
//   - CRF 26 → 0.28 (28% of source) - typical "compress" setting
//   - CRF 35 → 0.10 (10% of source) - aggressive, smaller files
//
// The 0.02 multiplier creates a roughly linear mapping where each CRF unit
// reduces bitrate by ~2%, matching the typical CRF behavior where +6 CRF
// halves the bitrate.
func crfToBitrateModifier(crf int) float64 {
	modifier := 0.8 - (float64(crf) * 0.02)
	// Clamp to reasonable range to prevent extreme values
	if modifier < 0.05 {
		modifier = 0.05 // Never go below 5% - prevents unusable quality
	}
	if modifier > 0.80 {
		modifier = 0.80 // Never exceed 80% - prevents larger-than-source files
	}
	return modifier
}

// getHwaccelInputArgs returns the FFmpeg input arguments for hardware acceleration.
// This generates the correct device initialization and hwaccel flags for each encoder type.
// softwareDecode: if true, skip -hwaccel flags but keep device init for the encoder.
func getHwaccelInputArgs(encoder HWAccel, softwareDecode bool) []string {
	switch encoder {
	case HWAccelNVENC:
		// NVIDIA CUDA - simple init
		// Per NVIDIA FFmpeg guide: https://developer.nvidia.com/blog/nvidia-ffmpeg-transcoding-guide/
		args := []string{
			"-init_hw_device", "cuda=cu:0",
			"-filter_hw_device", "cu",
		}
		if !softwareDecode {
			args = append(args, "-hwaccel", "cuda", "-hwaccel_output_format", "cuda")
		}
		return args

	case HWAccelQSV:
		// Intel QSV - MUST derive from VAAPI on Linux
		// Per Jellyfin: https://github.com/jellyfin/jellyfin/blob/master/MediaBrowser.Controller/MediaEncoding/EncodingHelper.cs
		device := GetVAAPIDevice()
		args := []string{
			"-init_hw_device", "vaapi=va:" + device,
			"-init_hw_device", "qsv=qs@va",
			"-filter_hw_device", "qs",
		}
		if !softwareDecode {
			args = append(args, "-hwaccel", "qsv", "-hwaccel_output_format", "qsv")
		}
		return args

	case HWAccelVAAPI:
		// Linux VAAPI (Intel/AMD)
		device := GetVAAPIDevice()
		args := []string{
			"-init_hw_device", "vaapi=va:" + device,
			"-filter_hw_device", "va",
		}
		if !softwareDecode {
			args = append(args, "-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi")
		}
		return args

	case HWAccelVideoToolbox:
		// macOS VideoToolbox - simple, encoder handles CPU frames directly
		if !softwareDecode {
			return []string{"-hwaccel", "videotoolbox"}
		}
		return nil

	default:
		// Software encoder - no hwaccel args needed
		return nil
	}
}

// BuildPresetArgs builds FFmpeg arguments for a preset with the specified encoder
// sourceBitrate is the source video bitrate in bits/second (used for dynamic bitrate calculation)
// sourceWidth/sourceHeight are the source video dimensions (for calculating scaled output)
// qualityHEVC/qualityAV1 are optional CRF overrides (0 = use default)
// softwareDecode: if true, skip hardware decode args and use software decode filter
// outputFormat: "mkv" preserves audio/subs, "mp4" transcodes to AAC and strips subtitles
// Returns (inputArgs, outputArgs) - inputArgs go before -i, outputArgs go after
func BuildPresetArgs(preset *Preset, sourceBitrate int64, sourceWidth, sourceHeight int, qualityHEVC, qualityAV1 int, softwareDecode bool, outputFormat string) (inputArgs []string, outputArgs []string) {
	key := EncoderKey{preset.Encoder, preset.Codec}
	config, ok := encoderConfigs[key]
	if !ok {
		// Fallback to software encoder for the target codec
		config = encoderConfigs[EncoderKey{HWAccelNone, preset.Codec}]
	}

	// Input args: hardware acceleration for decoding
	// Generated dynamically based on encoder type
	inputArgs = getHwaccelInputArgs(preset.Encoder, softwareDecode)

	// Output args
	outputArgs = []string{}

	// Build video filter chain
	var filterParts []string
	if softwareDecode {
		// Use software decode filter for the encoder
		swFilter := getSoftwareDecodeFilter(preset.Encoder)
		if swFilter != "" {
			filterParts = append(filterParts, swFilter)
		}
	} else if config.baseFilter != "" {
		// Use normal hardware decode filter
		filterParts = append(filterParts, config.baseFilter)
	}

	// Add scaling filter if needed
	if preset.MaxHeight > 0 && sourceHeight > preset.MaxHeight {
		scaleFilter := config.scaleFilter
		if scaleFilter == "" {
			scaleFilter = "scale"
		}
		filterParts = append(filterParts, fmt.Sprintf("%s=-2:'min(ih,%d)'", scaleFilter, preset.MaxHeight))
	}

	// Apply filter chain if we have any filters
	if len(filterParts) > 0 {
		outputArgs = append(outputArgs, "-vf", strings.Join(filterParts, ","))
	}

	// Add encoder
	outputArgs = append(outputArgs, "-c:v", config.encoder)

	// Determine quality value - use override if provided, otherwise use default
	var qualityStr string
	qualityOverride := 0
	if preset.Codec == CodecHEVC && qualityHEVC > 0 {
		qualityOverride = qualityHEVC
	} else if preset.Codec == CodecAV1 && qualityAV1 > 0 {
		qualityOverride = qualityAV1
	}

	// For encoders that use dynamic bitrate calculation (VideoToolbox)
	if config.usesBitrate && sourceBitrate > 0 {
		var modifier float64
		if qualityOverride > 0 {
			// Convert CRF override to bitrate modifier
			modifier = crfToBitrateModifier(qualityOverride)
		} else {
			// Parse default modifier from config (e.g., "0.35")
			modifier = 0.5
			fmt.Sscanf(config.quality, "%f", &modifier)
		}

		// Calculate target bitrate in kbps
		targetKbps := int64(float64(sourceBitrate) * modifier / 1000)

		// Apply min/max constraints
		if targetKbps < minBitrateKbps {
			targetKbps = minBitrateKbps
		}
		if targetKbps > maxBitrateKbps {
			targetKbps = maxBitrateKbps
		}

		qualityStr = fmt.Sprintf("%dk", targetKbps)
	} else if qualityOverride > 0 {
		// Use override quality directly (for CRF/CQ/QP based encoders)
		qualityStr = fmt.Sprintf("%d", qualityOverride)
	} else {
		// Use default from config
		qualityStr = config.quality
	}

	outputArgs = append(outputArgs, config.qualityFlag, qualityStr)

	// Add encoder-specific extra args
	outputArgs = append(outputArgs, config.extraArgs...)

	// Add stream mapping and handle audio/subtitles based on output format
	// Use explicit stream selection to skip attached pictures (cover art)
	// that cause hardware encoders to fail (issue #40)
	outputArgs = append(outputArgs,
		"-map", "0:v:0", // First video stream only
		"-map", "0:a?",  // All audio streams (optional)
	)

	if outputFormat == "mp4" {
		// MP4: Transcode audio to AAC for web compatibility, strip subtitles (PGS breaks MP4)
		outputArgs = append(outputArgs,
			"-c:a", "aac",
			"-b:a", "192k",
			"-ac", "2", // Stereo for wide compatibility
			"-sn",      // Strip subtitles
		)
	} else {
		// MKV: Copy all streams as-is
		outputArgs = append(outputArgs,
			"-map", "0:s?", // All subtitle streams (optional)
			"-c:a", "copy",
			"-c:s", "copy",
		)
	}

	return inputArgs, outputArgs
}

// GeneratePresets creates presets using the best available encoder for each codec
func GeneratePresets() map[string]*Preset {
	presets := make(map[string]*Preset)

	for _, base := range BasePresets {
		// Get the best available encoder for this preset's target codec
		bestEncoder := GetBestEncoderForCodec(base.Codec)

		presets[base.ID] = &Preset{
			ID:          base.ID,
			Name:        base.Name,
			Description: base.Description,
			Encoder:     bestEncoder.Accel,
			Codec:       base.Codec,
			MaxHeight:   base.MaxHeight,
		}
	}

	return presets
}

// Presets cache - populated after encoder detection
var generatedPresets map[string]*Preset
var presetsInitialized bool

// InitPresets initializes presets based on available encoders
// Must be called after DetectEncoders
func InitPresets() {
	generatedPresets = GeneratePresets()
	presetsInitialized = true
}

// GetPreset returns a preset by ID
func GetPreset(id string) *Preset {
	if !presetsInitialized {
		// Fallback to software-only presets
		return getSoftwarePreset(id)
	}
	return generatedPresets[id]
}

// getSoftwarePreset returns a software-only preset (fallback)
func getSoftwarePreset(id string) *Preset {
	for _, base := range BasePresets {
		if base.ID == id {
			return &Preset{
				ID:          base.ID,
				Name:        base.Name,
				Description: base.Description,
				Encoder:     HWAccelNone,
				Codec:       base.Codec,
				MaxHeight:   base.MaxHeight,
			}
		}
	}
	return nil
}

// getSoftwareDecodeFilter returns the filter chain for software decode + hardware encode.
// These are simplified filters that avoid problematic hardware post-processing filters
// like vpp_qsv which can cause -38 errors near end of stream.
func getSoftwareDecodeFilter(encoder HWAccel) string {
	switch encoder {
	case HWAccelQSV:
		// Simple hwupload - no vpp_qsv (causes -38 errors near EOF)
		// Per Jellyfin PR #5534: hwupload is sufficient for QSV encoding
		return "format=nv12,hwupload=extra_hw_frames=64"
	case HWAccelVAAPI:
		return "format=nv12,hwupload"
	case HWAccelNVENC:
		return "" // NVENC auto-handles CPU frames
	case HWAccelVideoToolbox:
		return "" // VideoToolbox encoder handles CPU frames directly
	default:
		return ""
	}
}

// ListPresets returns all available presets
func ListPresets() []*Preset {
	if !presetsInitialized {
		// Return software-only presets as fallback
		var presets []*Preset
		for _, base := range BasePresets {
			presets = append(presets, &Preset{
				ID:          base.ID,
				Name:        base.Name,
				Description: base.Description,
				Encoder:     HWAccelNone,
				Codec:       base.Codec,
				MaxHeight:   base.MaxHeight,
			})
		}
		return presets
	}

	// Return presets in order
	var result []*Preset
	for _, base := range BasePresets {
		if preset, ok := generatedPresets[base.ID]; ok {
			result = append(result, preset)
		}
	}

	return result
}


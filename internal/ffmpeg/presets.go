package ffmpeg

import (
	"fmt"
	"strings"

	"github.com/gwlsn/shrinkray/internal/ffmpeg/vmaf"
)

// Preset defines a transcoding preset with its FFmpeg parameters
type Preset struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Description     string  `json:"description"`
	Encoder         HWAccel `json:"encoder"`          // Which encoder to use
	Codec           Codec   `json:"codec"`            // Target codec (HEVC or AV1)
	MaxHeight       int     `json:"max_height"`       // 0 = no scaling, 1080, 720, etc.
	IsSmartShrink   bool    `json:"is_smart_shrink"`  // True for VMAF-based presets
	SkipsCodecCheck bool    `json:"skips_codec_check"` // Bypass same-codec skip
}

// WithEncoder returns a copy of the preset with a different encoder.
// Used for encoder fallback - preserves all other preset settings.
func (p *Preset) WithEncoder(encoder HWAccel) *Preset {
	copy := *p
	copy.Encoder = encoder
	return &copy
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
	qualityMin  int      // Minimum quality (best quality, lowest compression)
	qualityMax  int      // Maximum quality (most compression)
	modMin      float64  // Min bitrate modifier (for VideoToolbox)
	modMax      float64  // Max bitrate modifier (for VideoToolbox)
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
		qualityMin:  18,
		qualityMax:  35,
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
		modMin:      0.05,
		modMax:      0.80,
	},
	{HWAccelNVENC, CodecHEVC}: {
		encoder:     "hevc_nvenc",
		qualityFlag: "-cq",
		quality:     "28",
		extraArgs:   []string{"-preset", "p4", "-tune", "hq", "-rc", "vbr"},
		// hwaccelArgs generated dynamically by getHwaccelInputArgs()
		scaleFilter: "scale_cuda",
		baseFilter:  "scale_cuda=format=nv12", // Explicit format for compatibility
		qualityMin:  18,
		qualityMax:  35,
	},
	{HWAccelQSV, CodecHEVC}: {
		encoder:     "hevc_qsv",
		qualityFlag: "-global_quality",
		quality:     "27",
		extraArgs:   []string{"-preset", "medium"},
		// hwaccelArgs generated dynamically by getHwaccelInputArgs() - QSV derived from VAAPI on Linux
		scaleFilter: "scale_qsv",
		baseFilter:  "format=nv12|qsv,hwupload=extra_hw_frames=64,scale_qsv=format=nv12", // Added scale_qsv for format compatibility
		qualityMin:  18,
		qualityMax:  35,
	},
	{HWAccelVAAPI, CodecHEVC}: {
		encoder:     "hevc_vaapi",
		qualityFlag: "-qp",
		quality:     "27",
		extraArgs:   []string{},
		// hwaccelArgs generated dynamically by getHwaccelInputArgs()
		scaleFilter: "scale_vaapi",
		baseFilter:  "format=nv12|vaapi,hwupload,scale_vaapi=format=nv12", // Added scale_vaapi for format compatibility
		qualityMin:  18,
		qualityMax:  35,
	},

	// AV1 encoders
	// More aggressive compression than HEVC - AV1 handles lower bitrates better
	{HWAccelNone, CodecAV1}: {
		encoder:     "libsvtav1",
		qualityFlag: "-crf",
		quality:     "35",
		extraArgs:   []string{"-preset", "6"},
		scaleFilter: "scale",
		qualityMin:  20,
		qualityMax:  45,
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
		modMin:      0.05,
		modMax:      0.70,
	},
	{HWAccelNVENC, CodecAV1}: {
		encoder:     "av1_nvenc",
		qualityFlag: "-cq",
		quality:     "32",
		extraArgs:   []string{"-preset", "p4", "-tune", "hq", "-rc", "vbr"},
		// hwaccelArgs generated dynamically by getHwaccelInputArgs()
		scaleFilter: "scale_cuda",
		baseFilter:  "scale_cuda=format=nv12", // Explicit format for compatibility
		qualityMin:  20,
		qualityMax:  40,
	},
	{HWAccelQSV, CodecAV1}: {
		encoder:     "av1_qsv",
		qualityFlag: "-global_quality",
		quality:     "32",
		extraArgs:   []string{"-preset", "medium"},
		// hwaccelArgs generated dynamically by getHwaccelInputArgs() - QSV derived from VAAPI on Linux
		scaleFilter: "scale_qsv",
		baseFilter:  "format=nv12|qsv,hwupload=extra_hw_frames=64,scale_qsv=format=nv12", // Added scale_qsv for format compatibility
		qualityMin:  20,
		qualityMax:  40,
	},
	{HWAccelVAAPI, CodecAV1}: {
		encoder:     "av1_vaapi",
		qualityFlag: "-qp",
		quality:     "32",
		extraArgs:   []string{},
		// hwaccelArgs generated dynamically by getHwaccelInputArgs()
		scaleFilter: "scale_vaapi",
		baseFilter:  "format=nv12|vaapi,hwupload,scale_vaapi=format=nv12", // Added scale_vaapi for format compatibility
		qualityMin:  20,
		qualityMax:  40,
	},
}

// BasePresets defines the core presets
var BasePresets = []struct {
	ID              string
	Name            string
	Description     string
	Codec           Codec
	MaxHeight       int
	IsSmartShrink   bool
	SkipsCodecCheck bool
}{
	{"compress-hevc", "Compress (HEVC)", "Reduce size with HEVC encoding", CodecHEVC, 0, false, false},
	{"compress-av1", "Compress (AV1)", "Maximum compression with AV1 encoding", CodecAV1, 0, false, false},
	{"1080p", "Downscale to 1080p", "Downscale to 1080p max (HEVC)", CodecHEVC, 1080, false, false},
	{"720p", "Downscale to 720p", "Downscale to 720p (big savings)", CodecHEVC, 720, false, false},
	// SmartShrink presets - VMAF-based auto-optimization
	{"smartshrink-hevc", "SmartShrink (HEVC)", "Auto-optimize with VMAF analysis", CodecHEVC, 0, true, true},
	{"smartshrink-av1", "SmartShrink (AV1)", "Auto-optimize with VMAF analysis", CodecAV1, 0, true, true},
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

// GetQualityRange returns the quality search range for an encoder
func GetQualityRange(hwaccel HWAccel, codec Codec) vmaf.QualityRange {
	key := EncoderKey{hwaccel, codec}
	config, ok := encoderConfigs[key]
	if !ok {
		// Fallback defaults
		if codec == CodecAV1 {
			return vmaf.QualityRange{Min: 20, Max: 45}
		}
		return vmaf.QualityRange{Min: 18, Max: 35}
	}

	return vmaf.QualityRange{
		Min:         config.qualityMin,
		Max:         config.qualityMax,
		UsesBitrate: config.usesBitrate,
		MinMod:      config.modMin,
		MaxMod:      config.modMax,
	}
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
		// NVIDIA CUDA - use the init mode detected at startup
		// Simple init works on most Docker setups
		// Explicit init required for CUDA filters on bare metal
		if GetNVENCInitMode() == NVENCInitExplicit {
			args := []string{
				"-init_hw_device", "cuda=cu:0",
				"-filter_hw_device", "cu",
			}
			if !softwareDecode {
				args = append(args, "-hwaccel", "cuda", "-hwaccel_output_format", "cuda")
			}
			return args
		}
		// Simple init (default, works on Docker)
		if !softwareDecode {
			return []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda"}
		}
		return nil

	case HWAccelQSV:
		// Intel QSV - use the init mode detected at startup
		// Direct init works on most Docker/Unraid setups
		// VAAPI-derived works on bare metal Linux (Jellyfin approach)
		if GetQSVInitMode() == QSVInitVAAPI {
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
		}
		// Direct QSV init (default, works on Docker)
		args := []string{
			"-init_hw_device", "qsv=qsv",
			"-filter_hw_device", "qsv",
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

// TonemapParams holds parameters for HDR to SDR tonemapping
type TonemapParams struct {
	IsHDR          bool   // True if source is HDR content
	EnableTonemap  bool   // True if tonemapping should be applied
	Algorithm      string // Tonemapping algorithm: hable, bt2390, reinhard, etc.
}

// BuildPresetArgs builds FFmpeg arguments for a preset with the specified encoder
// sourceBitrate is the source video bitrate in bits/second (used for dynamic bitrate calculation)
// sourceWidth/sourceHeight are the source video dimensions (for calculating scaled output)
// qualityHEVC/qualityAV1 are optional CRF overrides (0 = use default)
// qualityMod is an optional bitrate modifier for VideoToolbox (0 = use default)
// softwareDecode: if true, skip hardware decode args and use software decode filter
// outputFormat: "mkv" preserves audio/subs, "mp4" transcodes to AAC and strips subtitles
// tonemap: optional tonemapping parameters (nil = no tonemapping)
// Returns (inputArgs, outputArgs) - inputArgs go before -i, outputArgs go after
func BuildPresetArgs(preset *Preset, sourceBitrate int64, sourceWidth, sourceHeight int, qualityHEVC, qualityAV1 int, qualityMod float64, softwareDecode bool, outputFormat string, tonemap *TonemapParams) (inputArgs []string, outputArgs []string) {
	key := EncoderKey{preset.Encoder, preset.Codec}
	config, ok := encoderConfigs[key]
	if !ok {
		// Fallback to software encoder for the target codec
		config = encoderConfigs[EncoderKey{HWAccelNone, preset.Codec}]
	}

	// Check if we need tonemapping
	needsTonemap := tonemap != nil && tonemap.IsHDR && tonemap.EnableTonemap
	// Check if we need to preserve HDR (HDR source, tonemapping disabled)
	preserveHDR := tonemap != nil && tonemap.IsHDR && !tonemap.EnableTonemap
	var tonemapFilter string
	if needsTonemap {
		algorithm := tonemap.Algorithm
		if algorithm == "" {
			algorithm = "hable"
		}
		tonemapFilter, _ = BuildTonemapFilter(algorithm)
		// Software tonemapping requires software decode
		softwareDecode = true
	}

	// Input args: hardware acceleration for decoding
	// Generated dynamically based on encoder type
	inputArgs = getHwaccelInputArgs(preset.Encoder, softwareDecode)

	// Output args
	outputArgs = []string{}

	// Build video filter chain
	var filterParts []string

	// First add any base filters for decode pipeline
	if softwareDecode {
		// Use software decode filter for the encoder
		// Pass preserveHDR=false when tonemapping (SW tonemap outputs 8-bit SDR)
		swFilter := getSoftwareDecodeFilter(preset.Encoder, preserveHDR && !needsTonemap)
		if swFilter != "" {
			filterParts = append(filterParts, swFilter)
		}
	} else if config.baseFilter != "" {
		// Use normal hardware decode filter
		// For HDR preservation, replace nv12 with p010 in the filter chain
		baseFilter := config.baseFilter
		if preserveHDR && !needsTonemap {
			baseFilter = strings.ReplaceAll(baseFilter, "format=nv12", "format=p010")
			baseFilter = strings.ReplaceAll(baseFilter, "=format=nv12", "=format=p010")
		}
		filterParts = append(filterParts, baseFilter)
	}

	// Add tonemapping filter if needed
	// Software tonemap (zscale) goes before hwupload since it requires CPU frames
	if needsTonemap && tonemapFilter != "" {
		// Software tonemap - insert at beginning (before hwupload)
		// The zscale chain expects CPU frames and outputs CPU frames
		filterParts = []string{tonemapFilter}

		// Add CPU scaling if needed - must be done BEFORE hwupload since
		// software tonemapping outputs CPU frames. Hardware scalers (scale_qsv,
		// scale_cuda, etc.) don't work after hwupload from software tonemap.
		if preset.MaxHeight > 0 && sourceHeight > preset.MaxHeight {
			filterParts = append(filterParts, fmt.Sprintf("scale=-2:'min(ih,%d)'", preset.MaxHeight))
		}

		// Re-add hwupload after tonemap (and optional scale) if encoder needs it
		// Tonemap outputs 8-bit SDR, so preserveHDR=false
		swFilter := getSoftwareDecodeFilter(preset.Encoder, false)
		if swFilter != "" {
			filterParts = append(filterParts, swFilter)
		}
	}

	// Add scaling filter if needed (non-tonemapping path only)
	// When tonemapping, scaling is already handled above with CPU scale
	if !needsTonemap && preset.MaxHeight > 0 && sourceHeight > preset.MaxHeight {
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
	if config.usesBitrate {
		// Derive modifier from qualityMod, CRF conversion, or default
		var modifier float64
		if qualityMod > 0 {
			// Use VMAF-optimized bitrate modifier directly
			modifier = qualityMod
		} else if qualityOverride > 0 {
			// Convert CRF override to bitrate modifier
			modifier = crfToBitrateModifier(qualityOverride)
		} else {
			// Parse default modifier from config (e.g., "0.35")
			modifier = 0.5
			fmt.Sscanf(config.quality, "%f", &modifier)
		}

		// Clamp modifier to encoder's valid range
		if config.modMin > 0 && modifier < config.modMin {
			modifier = config.modMin
		}
		if config.modMax > 0 && modifier > config.modMax {
			modifier = config.modMax
		}

		// Use source bitrate if available, otherwise use 10Mbps reference
		// (consistent with BuildSampleEncodeArgs behavior)
		refKbps := int64(sourceBitrate / 1000)
		if sourceBitrate <= 0 {
			refKbps = 10000 // 10 Mbps reference bitrate
		}

		// Calculate target bitrate in kbps
		targetKbps := int64(float64(refKbps) * modifier)

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

	// Add HDR preservation flags when preserving HDR content
	// Per FFmpeg docs and Jellyfin implementation:
	// - Main10 profile for 10-bit HEVC/AV1
	// - Color metadata for HDR10 (BT.2020 colorspace, PQ transfer)
	if preserveHDR && !needsTonemap {
		// Set 10-bit profile for HEVC encoders
		// Most HW encoders auto-detect, but explicit is safer
		if preset.Codec == CodecHEVC {
			switch preset.Encoder {
			case HWAccelNVENC:
				outputArgs = append(outputArgs, "-profile:v", "main10")
			case HWAccelQSV, HWAccelVAAPI:
				outputArgs = append(outputArgs, "-profile:v", "main10")
			case HWAccelVideoToolbox:
				outputArgs = append(outputArgs, "-profile:v", "main10")
			case HWAccelNone:
				// libx265 uses x265-params for profile
				outputArgs = append(outputArgs, "-profile:v", "main10")
			}
		}
		// Add HDR10 color metadata to preserve HDR signaling
		outputArgs = append(outputArgs,
			"-color_primaries", "bt2020",
			"-color_trc", "smpte2084",
			"-colorspace", "bt2020nc",
		)
	}

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

// BuildSampleEncodeArgs builds FFmpeg arguments for encoding a sample.
// Similar to BuildPresetArgs but video-only (no audio/subtitles).
// For VideoToolbox (bitrate-based encoders), modifierOverride sets the bitrate
// as a fraction of a reference bitrate (e.g., 0.35 = 35% of 10Mbps = 3.5Mbps).
func BuildSampleEncodeArgs(preset *Preset, sourceWidth, sourceHeight int,
	qualityOverride int, modifierOverride float64, softwareDecode bool,
	tonemap *TonemapParams) (inputArgs []string, outputArgs []string) {

	// Get base args from BuildPresetArgs
	// We pass modifierOverride as qualityMod. For bitrate-based encoders (VideoToolbox),
	// BuildPresetArgs uses a 10Mbps reference when sourceBitrate=0 and applies the modifier.
	// When modifierOverride > 0, we also replace -b:v below for explicit control.
	inputArgs, outputArgs = BuildPresetArgs(preset, 0, sourceWidth, sourceHeight,
		qualityOverride, qualityOverride, modifierOverride, softwareDecode, "mkv", tonemap)

	// Remove audio/subtitle mapping and replace with video-only
	filteredArgs := make([]string, 0, len(outputArgs))
	skipNext := false
	for i, arg := range outputArgs {
		if skipNext {
			skipNext = false
			continue
		}
		// Skip audio/subtitle related args
		if arg == "-map" {
			if i+1 < len(outputArgs) && (strings.Contains(outputArgs[i+1], ":a") || strings.Contains(outputArgs[i+1], ":s")) {
				skipNext = true
				continue
			}
		}
		if arg == "-c:a" || arg == "-c:s" || arg == "-b:a" || arg == "-ac" || arg == "-sn" {
			skipNext = true
			continue
		}
		filteredArgs = append(filteredArgs, arg)
	}

	// For bitrate-based encoders (VideoToolbox), replace bitrate when modifierOverride > 0
	// BuildPresetArgs already calculated a bitrate, but we replace it here for explicit control.
	if modifierOverride > 0 {
		key := EncoderKey{preset.Encoder, preset.Codec}
		if config, ok := encoderConfigs[key]; ok && config.usesBitrate {
			// Clamp modifier to encoder's valid range (consistent with BuildPresetArgs)
			modifier := modifierOverride
			if config.modMin > 0 && modifier < config.modMin {
				modifier = config.modMin
			}
			if config.modMax > 0 && modifier > config.modMax {
				modifier = config.modMax
			}

			// Use a reference bitrate of 10Mbps for sample encoding
			// This gives reasonable quality for VMAF comparison
			const referenceBitrateKbps = 10000
			targetKbps := int64(float64(referenceBitrateKbps) * modifier)

			// Apply min/max constraints
			if targetKbps < minBitrateKbps {
				targetKbps = minBitrateKbps
			}
			if targetKbps > maxBitrateKbps {
				targetKbps = maxBitrateKbps
			}

			// Replace the -b:v value in filteredArgs
			for i, arg := range filteredArgs {
				if arg == "-b:v" && i+1 < len(filteredArgs) {
					filteredArgs[i+1] = fmt.Sprintf("%dk", targetKbps)
					break
				}
			}
		}
	}

	// Add explicit no audio/subtitles
	filteredArgs = append(filteredArgs, "-an", "-sn")

	return inputArgs, filteredArgs
}

// GeneratePresets creates presets using the best available encoder for each codec
func GeneratePresets() map[string]*Preset {
	presets := make(map[string]*Preset)

	for _, base := range BasePresets {
		// Skip SmartShrink presets if VMAF not available
		if base.IsSmartShrink && !vmaf.IsAvailable() {
			continue
		}

		// Get the best available encoder for this preset's target codec
		bestEncoder := GetBestEncoderForCodec(base.Codec)

		// Add HW/SW suffix to name
		suffix := " [SW]"
		if bestEncoder.Accel != HWAccelNone {
			suffix = " [HW]"
		}

		presets[base.ID] = &Preset{
			ID:              base.ID,
			Name:            base.Name + suffix,
			Description:     base.Description,
			Encoder:         bestEncoder.Accel,
			Codec:           base.Codec,
			MaxHeight:       base.MaxHeight,
			IsSmartShrink:   base.IsSmartShrink,
			SkipsCodecCheck: base.SkipsCodecCheck,
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
			// Skip SmartShrink presets if VMAF not available
			if base.IsSmartShrink && !vmaf.IsAvailable() {
				return nil
			}
			return &Preset{
				ID:              base.ID,
				Name:            base.Name + " [SW]",
				Description:     base.Description,
				Encoder:         HWAccelNone,
				Codec:           base.Codec,
				MaxHeight:       base.MaxHeight,
				IsSmartShrink:   base.IsSmartShrink,
				SkipsCodecCheck: base.SkipsCodecCheck,
			}
		}
	}
	return nil
}

// getSoftwareDecodeFilter returns the filter chain for software decode + hardware encode.
// These are simplified filters that avoid problematic hardware post-processing filters
// like vpp_qsv which can cause -38 errors near end of stream.
// When preserveHDR is true, uses p010 (10-bit) format to preserve HDR color depth.
func getSoftwareDecodeFilter(encoder HWAccel, preserveHDR bool) string {
	pixFmt := "nv12"
	if preserveHDR {
		pixFmt = "p010"
	}
	switch encoder {
	case HWAccelQSV:
		// Simple hwupload - no vpp_qsv (causes -38 errors near EOF)
		// Per Jellyfin PR #5534: hwupload is sufficient for QSV encoding
		return fmt.Sprintf("format=%s,hwupload=extra_hw_frames=64", pixFmt)
	case HWAccelVAAPI:
		return fmt.Sprintf("format=%s,hwupload", pixFmt)
	case HWAccelNVENC:
		return "" // NVENC auto-handles CPU frames
	case HWAccelVideoToolbox:
		return "" // VideoToolbox encoder handles CPU frames directly
	default:
		return ""
	}
}

// BuildTonemapFilter returns the FFmpeg filter chain for HDR to SDR tonemapping.
// Returns the filter string and whether it requires software decode.
// Uses software tonemapping (zscale) for universal compatibility across all hardware.
// The algorithm parameter should be one of: hable, bt2390, reinhard, mobius, clip, linear, gamma.
func BuildTonemapFilter(algorithm string) (filter string, requiresSoftwareDecode bool) {
	// Software tonemapping via zscale - works universally with all encoders
	// Pipeline: HDR (BT.2020 PQ) -> linear light -> tonemap -> SDR (BT.709)
	// Encoding is still hardware-accelerated; only tonemapping uses CPU
	return fmt.Sprintf("zscale=t=linear:npl=100,format=gbrpf32le,zscale=p=bt709,tonemap=%s:desat=0:peak=100,zscale=t=bt709:m=bt709,format=yuv420p", algorithm), true
}

// ListPresets returns all available presets
func ListPresets() []*Preset {
	if !presetsInitialized {
		// Return software-only presets as fallback
		var presets []*Preset
		for _, base := range BasePresets {
			// Skip SmartShrink presets if VMAF not available
			if base.IsSmartShrink && !vmaf.IsAvailable() {
				continue
			}
			presets = append(presets, &Preset{
				ID:              base.ID,
				Name:            base.Name + " [SW]",
				Description:     base.Description,
				Encoder:         HWAccelNone,
				Codec:           base.Codec,
				MaxHeight:       base.MaxHeight,
				IsSmartShrink:   base.IsSmartShrink,
				SkipsCodecCheck: base.SkipsCodecCheck,
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

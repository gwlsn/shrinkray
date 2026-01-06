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

// Bitrate constraints for dynamic bitrate calculation (VideoToolbox)
const (
	minBitrateKbps = 500   // Minimum target bitrate in kbps
	maxBitrateKbps = 15000 // Maximum target bitrate in kbps
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
		// VideoToolbox uses bitrate control (-b:v) with dynamic calculation
		// Target bitrate = source bitrate * modifier
		encoder:     "hevc_videotoolbox",
		qualityFlag: "-b:v",
		quality:     "0.35", // 35% of source bitrate (~50-60% smaller files)
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
		hwaccelArgs: []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda"},
		scaleFilter: "scale_cuda",
	},
	{HWAccelQSV, CodecHEVC}: {
		encoder:     "hevc_qsv",
		qualityFlag: "-global_quality",
		quality:     "27",
		extraArgs:   []string{"-preset", "medium"},
		hwaccelArgs: []string{"-hwaccel", "qsv", "-hwaccel_output_format", "qsv"},
		scaleFilter: "scale_qsv",
	},
	{HWAccelVAAPI, CodecHEVC}: {
		encoder:     "hevc_vaapi",
		qualityFlag: "-qp",
		quality:     "27",
		extraArgs:   []string{},
		hwaccelArgs: []string{"-vaapi_device", "", "-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi"}, // Device path filled dynamically
		scaleFilter: "scale_vaapi",
		baseFilter:  "format=nv12,hwupload", // Required for AMD GPUs when hw decode falls back to software
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
		// VideoToolbox AV1 (M3+ chips) uses bitrate control
		encoder:     "av1_videotoolbox",
		qualityFlag: "-b:v",
		quality:     "0.25", // 25% of source bitrate
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
		hwaccelArgs: []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda"},
		scaleFilter: "scale_cuda",
	},
	{HWAccelQSV, CodecAV1}: {
		encoder:     "av1_qsv",
		qualityFlag: "-global_quality",
		quality:     "32",
		extraArgs:   []string{"-preset", "medium"},
		hwaccelArgs: []string{"-hwaccel", "qsv", "-hwaccel_output_format", "qsv"},
		scaleFilter: "scale_qsv",
	},
	{HWAccelVAAPI, CodecAV1}: {
		encoder:     "av1_vaapi",
		qualityFlag: "-qp",
		quality:     "32",
		extraArgs:   []string{},
		hwaccelArgs: []string{"-vaapi_device", "", "-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi"}, // Device path filled dynamically
		scaleFilter: "scale_vaapi",
		baseFilter:  "format=nv12,hwupload", // Required for AMD GPUs when hw decode falls back to software
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
// This allows users to set CRF (like Handbrake) even when using VideoToolbox.
// Formula: modifier = 0.8 - (crf * 0.02)
// CRF 15 → 0.50, CRF 22 → 0.36, CRF 26 → 0.28, CRF 35 → 0.10
func crfToBitrateModifier(crf int) float64 {
	modifier := 0.8 - (float64(crf) * 0.02)
	// Clamp to reasonable range
	if modifier < 0.05 {
		modifier = 0.05
	}
	if modifier > 0.80 {
		modifier = 0.80
	}
	return modifier
}

// BuildPresetArgs builds FFmpeg arguments for a preset with the specified encoder
// sourceBitrate is the source video bitrate in bits/second (used for dynamic bitrate calculation)
// qualityHEVC/qualityAV1 are optional CRF overrides (0 = use default)
// Returns (inputArgs, outputArgs) - inputArgs go before -i, outputArgs go after
func BuildPresetArgs(preset *Preset, sourceBitrate int64, qualityHEVC, qualityAV1 int) (inputArgs []string, outputArgs []string) {
	key := EncoderKey{preset.Encoder, preset.Codec}
	config, ok := encoderConfigs[key]
	if !ok {
		// Fallback to software encoder for the target codec
		config = encoderConfigs[EncoderKey{HWAccelNone, preset.Codec}]
	}

	// Input args: hardware acceleration for decoding
	// Make a copy to avoid modifying the original config
	for _, arg := range config.hwaccelArgs {
		// Fill in VAAPI device path dynamically
		if arg == "" && len(inputArgs) > 0 && inputArgs[len(inputArgs)-1] == "-vaapi_device" {
			arg = GetVAAPIDevice()
		}
		inputArgs = append(inputArgs, arg)
	}

	// Output args
	outputArgs = []string{}

	// Build video filter chain
	// baseFilter is used for VAAPI to handle software-decoded frames (hwupload)
	var filterParts []string
	if config.baseFilter != "" {
		filterParts = append(filterParts, config.baseFilter)
	}

	// Add scaling filter if needed
	if preset.MaxHeight > 0 {
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

	// Add stream mapping and copy audio/subtitles
	outputArgs = append(outputArgs,
		"-map", "0",
		"-c:a", "copy",
		"-c:s", "copy",
	)

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


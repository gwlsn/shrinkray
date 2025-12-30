package ffmpeg

import "fmt"

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

// BuildPresetArgs builds FFmpeg arguments for a preset with the specified encoder
// sourceBitrate is the source video bitrate in bits/second (used for dynamic bitrate calculation)
// Returns (inputArgs, outputArgs) - inputArgs go before -i, outputArgs go after
func BuildPresetArgs(preset *Preset, sourceBitrate int64) (inputArgs []string, outputArgs []string) {
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

	// Add scaling filter if needed
	if preset.MaxHeight > 0 {
		scaleFilter := config.scaleFilter
		if scaleFilter == "" {
			scaleFilter = "scale"
		}
		outputArgs = append(outputArgs,
			"-vf", fmt.Sprintf("%s=-2:'min(ih,%d)'", scaleFilter, preset.MaxHeight),
		)
	}

	// Add encoder
	outputArgs = append(outputArgs, "-c:v", config.encoder)

	// Get quality setting
	qualityStr := config.quality

	// For encoders that use dynamic bitrate calculation
	if config.usesBitrate && sourceBitrate > 0 {
		// Parse modifier (e.g., "0.5" = 50% of source bitrate)
		modifier := 0.5 // default
		fmt.Sscanf(qualityStr, "%f", &modifier)

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

	// Increase muxing queue size to prevent "Too many packets buffered" errors.
	// Default is 8192 packets which is insufficient for some inputs with unusual
	// timing or long keyframe intervals. 4096 is a safe middle ground.
	outputArgs = append(outputArgs, "-max_muxing_queue_size", "4096")

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


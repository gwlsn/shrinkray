package ffmpeg

import "fmt"

// Preset defines a transcoding preset with its FFmpeg parameters
type Preset struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Encoder     HWAccel `json:"encoder"`      // Which encoder to use
	Quality     string  `json:"quality"`      // "standard" or "smaller"
	MaxHeight   int     `json:"max_height"`   // 0 = no scaling, 1080, 720, etc.
}

// Quality settings for each encoder
// These are tuned to produce similar quality/size results across encoders
type encoderSettings struct {
	encoder       string   // FFmpeg encoder name
	qualityFlag   string   // -crf, -b:v, -global_quality, etc.
	standardQual  string   // Quality value for "standard"
	smallerQual   string   // Quality value for "smaller"
	extraArgs     []string // Additional encoder-specific args
	usesBitrate   bool     // If true, quality values are bitrate modifiers (0.0-1.0)
}

// Bitrate constraints for dynamic bitrate calculation (VideoToolbox)
const (
	minBitrateKbps = 500   // Minimum target bitrate in kbps
	maxBitrateKbps = 15000 // Maximum target bitrate in kbps
)

var encoderConfigs = map[HWAccel]encoderSettings{
	HWAccelNone: {
		encoder:      "libx265",
		qualityFlag:  "-crf",
		standardQual: "22",
		smallerQual:  "26",
		extraArgs:    []string{"-preset", "medium"},
	},
	HWAccelVideoToolbox: {
		// VideoToolbox uses bitrate control (-b:v) with dynamic calculation
		// Following Tdarr's approach: target_bitrate = source_bitrate * modifier
		// HEVC is roughly 50% more efficient than H.264, so 0.5 = same quality
		// We use slightly lower to get actual compression
		encoder:      "hevc_videotoolbox",
		qualityFlag:  "-b:v",
		standardQual: "0.5",  // 50% of source bitrate (good quality, ~30-40% smaller)
		smallerQual:  "0.35", // 35% of source bitrate (more aggressive, ~50-60% smaller)
		extraArgs:    []string{"-allow_sw", "1"}, // Allow software fallback
		usesBitrate:  true,
	},
	HWAccelNVENC: {
		encoder:      "hevc_nvenc",
		qualityFlag:  "-cq",
		standardQual: "24",
		smallerQual:  "28",
		extraArgs:    []string{"-preset", "p4", "-tune", "hq", "-rc", "vbr"},
	},
	HWAccelQSV: {
		encoder:      "hevc_qsv",
		qualityFlag:  "-global_quality",
		standardQual: "23",
		smallerQual:  "27",
		extraArgs:    []string{"-preset", "medium"},
	},
	HWAccelVAAPI: {
		encoder:      "hevc_vaapi",
		qualityFlag:  "-qp",
		standardQual: "23",
		smallerQual:  "27",
		extraArgs:    []string{},
	},
}

// BasePresets defines the core presets
var BasePresets = []struct {
	ID          string
	Name        string
	Description string
	Quality     string
	MaxHeight   int
}{
	{"compress", "Compress", "Reduce size while keeping quality", "standard", 0},
	{"compress-small", "Compress (Smaller)", "Prioritize size over quality", "smaller", 0},
	{"1080p", "1080p", "Downscale to 1080p max", "standard", 1080},
	{"720p", "720p", "Downscale to 720p (big savings)", "standard", 720},
}

// BuildPresetArgs builds FFmpeg arguments for a preset with the specified encoder
// sourceBitrate is the source video bitrate in bits/second (used for dynamic bitrate calculation)
func BuildPresetArgs(preset *Preset, sourceBitrate int64) []string {
	config, ok := encoderConfigs[preset.Encoder]
	if !ok {
		config = encoderConfigs[HWAccelNone]
	}

	args := []string{}

	// Add scaling filter if needed
	if preset.MaxHeight > 0 {
		// For VAAPI, we need to use a different filter chain
		if preset.Encoder == HWAccelVAAPI {
			args = append(args,
				"-vf", fmt.Sprintf("format=nv12,hwupload,scale_vaapi=-2:'min(ih,%d)'", preset.MaxHeight),
			)
		} else {
			args = append(args,
				"-vf", fmt.Sprintf("scale=-2:'min(ih,%d)'", preset.MaxHeight),
			)
		}
	}

	// Add encoder
	args = append(args, "-c:v", config.encoder)

	// Add quality setting
	qualityStr := config.standardQual
	if preset.Quality == "smaller" {
		qualityStr = config.smallerQual
	}

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

	args = append(args, config.qualityFlag, qualityStr)

	// Add encoder-specific extra args
	args = append(args, config.extraArgs...)

	// Add stream mapping and copy audio/subtitles
	args = append(args,
		"-map", "0",
		"-c:a", "copy",
		"-c:s", "copy",
	)

	// For VAAPI, need to specify the device
	if preset.Encoder == HWAccelVAAPI {
		// Prepend hardware device initialization
		args = append([]string{"-vaapi_device", "/dev/dri/renderD128"}, args...)
	}

	return args
}

// GeneratePresets creates presets using the best available encoder
func GeneratePresets() map[string]*Preset {
	presets := make(map[string]*Preset)

	// Always use the best available encoder
	bestEncoder := GetBestEncoder()

	for _, base := range BasePresets {
		presets[base.ID] = &Preset{
			ID:          base.ID,
			Name:        base.Name,
			Description: base.Description,
			Encoder:     bestEncoder.Accel,
			Quality:     base.Quality,
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
				Quality:     base.Quality,
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
				Quality:     base.Quality,
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

// ListPresetsForEncoder returns presets for a specific encoder
func ListPresetsForEncoder(accel HWAccel) []*Preset {
	var result []*Preset

	for _, base := range BasePresets {
		var presetID string
		if accel == HWAccelNone {
			presetID = base.ID
		} else {
			presetID = fmt.Sprintf("%s-%s", base.ID, string(accel))
		}

		if presetsInitialized {
			if preset, ok := generatedPresets[presetID]; ok {
				result = append(result, preset)
			}
		} else if accel == HWAccelNone {
			result = append(result, &Preset{
				ID:          base.ID,
				Name:        base.Name,
				Description: base.Description,
				Encoder:     HWAccelNone,
				Quality:     base.Quality,
				MaxHeight:   base.MaxHeight,
			})
		}
	}

	return result
}

// GetRecommendedPreset returns the best preset for the user's hardware
func GetRecommendedPreset() *Preset {
	bestEncoder := GetBestEncoder()
	presets := ListPresetsForEncoder(bestEncoder.Accel)
	if len(presets) > 0 {
		return presets[0] // First preset (compress) for best encoder
	}
	return getSoftwarePreset("compress")
}

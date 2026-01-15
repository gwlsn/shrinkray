package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	// MediaPath is the root directory to browse for media files
	MediaPath string `yaml:"media_path"`

	// TempPath is where temp files are written during transcoding
	// If empty, temp files go in the same directory as the source
	TempPath string `yaml:"temp_path"`

	// OriginalHandling determines what happens to original files after transcoding
	// Options: "replace" (rename original to .old), "keep" (keep original, new file replaces)
	OriginalHandling string `yaml:"original_handling"`

	// Workers is the number of concurrent transcode jobs (default 1)
	Workers int `yaml:"workers"`

	// FFmpegPath is the path to ffmpeg binary (default: "ffmpeg")
	FFmpegPath string `yaml:"ffmpeg_path"`

	// FFprobePath is the path to ffprobe binary (default: "ffprobe")
	FFprobePath string `yaml:"ffprobe_path"`

	// QueueFile is where the job queue is persisted (default: config dir + queue.json)
	QueueFile string `yaml:"queue_file"`

	// PushoverUserKey is the Pushover user key for notifications
	PushoverUserKey string `yaml:"pushover_user_key"`

	// PushoverAppToken is the Pushover application token for notifications
	PushoverAppToken string `yaml:"pushover_app_token"`

	// NotifyOnComplete triggers a Pushover notification when all jobs finish
	NotifyOnComplete bool `yaml:"notify_on_complete"`

	// QualityHEVC is the CRF value for HEVC encoding (lower = higher quality, default 26)
	QualityHEVC int `yaml:"quality_hevc"`

	// QualityAV1 is the CRF value for AV1 encoding (lower = higher quality, default 35)
	QualityAV1 int `yaml:"quality_av1"`

	// ScheduleEnabled enables time-based scheduling for transcoding
	ScheduleEnabled bool `yaml:"schedule_enabled"`

	// ScheduleStartHour is when transcoding is allowed to start (0-23, default 22 = 10 PM)
	ScheduleStartHour int `yaml:"schedule_start_hour"`

	// ScheduleEndHour is when transcoding must stop (0-23, default 6 = 6 AM)
	ScheduleEndHour int `yaml:"schedule_end_hour"`

	// LogLevel controls logging verbosity: debug, info, warn, error (default: info)
	LogLevel string `yaml:"log_level"`

	// KeepLargerFiles keeps transcoded files even if they're larger than the original
	// Useful for users who want codec consistency across their library
	KeepLargerFiles bool `yaml:"keep_larger_files"`

	// OutputFormat is the container format for transcoded files: "mkv" or "mp4"
	// MKV preserves all streams; MP4 transcodes audio to AAC and strips subtitles
	OutputFormat string `yaml:"output_format"`

	// TonemapHDR enables automatic HDR to SDR conversion (default: false)
	// When enabled, HDR content (HDR10, HLG) is tonemapped to SDR using CPU.
	// When disabled, HDR metadata is preserved for HDR-capable displays.
	TonemapHDR bool `yaml:"tonemap_hdr"`

	// TonemapAlgorithm is the tonemapping algorithm to use: "hable", "bt2390", "reinhard"
	// Default is "hable" (filmic, good for movies)
	TonemapAlgorithm string `yaml:"tonemap_algorithm"`
}

// DefaultConfig returns a config with sensible defaults
func DefaultConfig() *Config {
	return &Config{
		MediaPath:         "/media",
		TempPath:          "", // same directory as source
		OriginalHandling:  "replace",
		Workers:           1,
		FFmpegPath:        "ffmpeg",
		FFprobePath:       "ffprobe",
		QueueFile:         "/config/queue.json",
		QualityHEVC:       0, // 0 = use encoder-specific default
		QualityAV1:        0, // 0 = use encoder-specific default
		ScheduleEnabled:   false,
		ScheduleStartHour: 22, // 10 PM
		ScheduleEndHour:   6,  // 6 AM
		LogLevel:          "info",
		OutputFormat:      "mkv",
		TonemapHDR:        false,   // HDR passthrough by default; enable for SDR conversion (uses CPU)
		TonemapAlgorithm:  "hable", // Filmic tonemapping, good for movies
	}
}

// Load reads config from a YAML file, applying defaults for missing values
func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// No config file - create one with defaults
			if saveErr := cfg.Save(path); saveErr != nil {
				fmt.Printf("Warning: Could not create config file: %v\n", saveErr)
			}
			return cfg, nil
		}
		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	// Apply defaults for empty values
	if cfg.FFmpegPath == "" {
		cfg.FFmpegPath = "ffmpeg"
	}
	if cfg.FFprobePath == "" {
		cfg.FFprobePath = "ffprobe"
	}
	if cfg.Workers < 1 {
		cfg.Workers = 1
	}
	// Note: QualityHEVC/QualityAV1 of 0 means "use encoder-specific default"
	// The API handler will determine the actual default based on detected encoder

	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}

	if cfg.OutputFormat == "" {
		cfg.OutputFormat = "mkv"
	}

	// Validate tonemapping algorithm
	if cfg.TonemapAlgorithm == "" {
		cfg.TonemapAlgorithm = "hable"
	}
	// Ensure it's a valid algorithm
	switch cfg.TonemapAlgorithm {
	case "hable", "bt2390", "reinhard", "mobius", "clip", "linear", "gamma":
		// Valid algorithms
	default:
		// Unknown algorithm - fall back to hable
		cfg.TonemapAlgorithm = "hable"
	}

	return cfg, nil
}

// Save writes the config to a YAML file
func (c *Config) Save(path string) error {
	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// GetTempDir returns the directory for temp files
// If TempPath is set, returns that; otherwise returns the directory of the source file
func (c *Config) GetTempDir(sourcePath string) string {
	if c.TempPath != "" {
		return c.TempPath
	}
	return filepath.Dir(sourcePath)
}

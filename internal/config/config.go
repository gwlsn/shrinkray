package config

import (
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"
)

// FeatureFlags controls experimental features for phased rollout
type FeatureFlags struct {
	// VirtualScroll enables virtual scrolling in the UI (render only visible jobs)
	// When OFF: all jobs are rendered to DOM (current behavior)
	// When ON: only visible jobs + overscan are rendered
	VirtualScroll bool `yaml:"virtual_scroll"`

	// DeferredProbing enables streaming job discovery without blocking on ffprobe
	// When OFF: all files are probed before jobs are added (current behavior)
	// When ON: jobs are added as "pending_probe" and probed by workers on demand
	DeferredProbing bool `yaml:"deferred_probing"`

	// PaginatedInit enables paginated SSE init and job API responses
	// When OFF: SSE init sends all jobs at once (current behavior)
	// When ON: SSE init sends first page, frontend lazy-loads more
	PaginatedInit bool `yaml:"paginated_init"`

	// BatchedSSE is already implemented - kept for consistency
	BatchedSSE bool `yaml:"batched_sse"`

	// DeltaProgress is already implemented - kept for consistency
	DeltaProgress bool `yaml:"delta_progress"`
}

// DefaultFeatureFlags returns feature flags with safe defaults (all new features OFF)
func DefaultFeatureFlags() FeatureFlags {
	return FeatureFlags{
		VirtualScroll:   false, // Safe default: use current rendering
		DeferredProbing: false, // Safe default: probe all files first
		PaginatedInit:   false, // Safe default: send all jobs on init
		BatchedSSE:      true,  // Already implemented and safe
		DeltaProgress:   true,  // Already implemented and safe
	}
}

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

	// Features contains feature flags for phased rollout of new functionality
	Features FeatureFlags `yaml:"features"`
}

// DefaultConfig returns a config with sensible defaults
func DefaultConfig() *Config {
	return &Config{
		MediaPath:        "/media",
		TempPath:         "", // same directory as source
		OriginalHandling: "replace",
		Workers:          1,
		FFmpegPath:       "ffmpeg",
		FFprobePath:      "ffprobe",
		QueueFile:        "",
		Features:         DefaultFeatureFlags(),
	}
}

// Load reads config from a YAML file, applying defaults for missing values
func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// No config file - use defaults
			applyFeatureFlagEnvOverrides(cfg)
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

	// Apply environment variable overrides for feature flags
	// This allows toggling features without modifying config files
	applyFeatureFlagEnvOverrides(cfg)

	return cfg, nil
}

// applyFeatureFlagEnvOverrides checks environment variables for feature flag overrides
// Environment variables take precedence over YAML config
// Use: SHRINKRAY_FEATURE_VIRTUAL_SCROLL=1 to enable, =0 to disable
func applyFeatureFlagEnvOverrides(cfg *Config) {
	if v := os.Getenv("SHRINKRAY_FEATURE_VIRTUAL_SCROLL"); v != "" {
		cfg.Features.VirtualScroll = envBool(v)
	}
	if v := os.Getenv("SHRINKRAY_FEATURE_DEFERRED_PROBING"); v != "" {
		cfg.Features.DeferredProbing = envBool(v)
	}
	if v := os.Getenv("SHRINKRAY_FEATURE_PAGINATED_INIT"); v != "" {
		cfg.Features.PaginatedInit = envBool(v)
	}
	if v := os.Getenv("SHRINKRAY_FEATURE_BATCHED_SSE"); v != "" {
		cfg.Features.BatchedSSE = envBool(v)
	}
	if v := os.Getenv("SHRINKRAY_FEATURE_DELTA_PROGRESS"); v != "" {
		cfg.Features.DeltaProgress = envBool(v)
	}
}

// envBool parses a boolean from an environment variable value
// Accepts: "1", "true", "yes", "on" for true; anything else is false
func envBool(v string) bool {
	b, err := strconv.ParseBool(v)
	if err != nil {
		// Also accept "1" as true
		return v == "1"
	}
	return b
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

package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

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

// DefaultFeatureFlags returns feature flags with performance features enabled by default
func DefaultFeatureFlags() FeatureFlags {
	return FeatureFlags{
		VirtualScroll:   true,  // Render only visible items for large queues
		DeferredProbing: true,  // Add jobs instantly, probe when worker picks up
		PaginatedInit:   false, // Not implemented yet
		BatchedSSE:      true,  // Batch add events to reduce SSE flood
		DeltaProgress:   true,  // Small progress payloads
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

	// NtfyServer is the ntfy server URL for notifications
	NtfyServer string `yaml:"ntfy_server"`

	// NtfyTopic is the ntfy topic for notifications
	NtfyTopic string `yaml:"ntfy_topic"`

	// NtfyToken is the ntfy access token (optional)
	NtfyToken string `yaml:"ntfy_token"`

	// NotifyOnComplete triggers a notification when all jobs finish
	NotifyOnComplete bool `yaml:"notify_on_complete"`

	// Features contains feature flags for phased rollout of new functionality
	Features FeatureFlags `yaml:"features"`

	// Auth contains authentication configuration.
	Auth AuthConfig `yaml:"auth"`
}

// AuthConfig configures authentication providers.
type AuthConfig struct {
	// Enabled controls whether authentication is required.
	Enabled bool `yaml:"enabled"`
	// Provider selects the auth provider name.
	Provider string `yaml:"provider"`
	// Secret signs session cookies.
	Secret string `yaml:"secret"`
	// BypassPaths lists endpoints that bypass auth enforcement.
	BypassPaths []string `yaml:"bypass_paths"`
	// Password configures password-based auth.
	Password PasswordAuthConfig `yaml:"password"`
	// OIDC configures OpenID Connect auth.
	OIDC OIDCAuthConfig `yaml:"oidc"`
}

// PasswordAuthConfig configures password auth.
type PasswordAuthConfig struct {
	// Users maps usernames to password hashes.
	Users map[string]string `yaml:"users"`
	// HashAlgo specifies the expected password hash algorithm.
	HashAlgo string `yaml:"hash_algo"`
}

// OIDCAuthConfig configures OpenID Connect auth.
type OIDCAuthConfig struct {
	// Issuer is the OIDC issuer URL.
	Issuer string `yaml:"issuer"`
	// ClientID is the OIDC client ID.
	ClientID string `yaml:"client_id"`
	// ClientSecret is the OIDC client secret.
	ClientSecret string `yaml:"client_secret"`
	// RedirectURL is the callback URL registered with the IdP.
	RedirectURL string `yaml:"redirect_url"`
	// Scopes are extra OAuth scopes (openid is always enforced).
	Scopes []string `yaml:"scopes"`
	// GroupClaim is the claim name that holds group membership.
	GroupClaim string `yaml:"group_claim"`
	// AllowedGroups restricts access to matching group values.
	AllowedGroups []string `yaml:"allowed_groups"`
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
		NtfyServer:       "https://ntfy.sh",
		Features:         DefaultFeatureFlags(),
		Auth: AuthConfig{
			Enabled:  false,
			Provider: "noop",
			Password: PasswordAuthConfig{HashAlgo: "auto"},
			OIDC: OIDCAuthConfig{
				Scopes: []string{"openid", "profile", "email"},
			},
		},
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
			applyAuthEnvOverrides(cfg)
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
	applyAuthEnvOverrides(cfg)

	return cfg, nil
}

func applyAuthEnvOverrides(cfg *Config) {
	if v := os.Getenv("SHRINKRAY_AUTH_ENABLED"); v != "" {
		cfg.Auth.Enabled = envBool(v)
	}
	if v := os.Getenv("SHRINKRAY_AUTH_PROVIDER"); v != "" {
		cfg.Auth.Provider = v
	}
	if v := os.Getenv("SHRINKRAY_AUTH_SECRET"); v != "" {
		cfg.Auth.Secret = v
	}
	if v := os.Getenv("SHRINKRAY_AUTH_BYPASS_PATHS"); v != "" {
		cfg.Auth.BypassPaths = splitCommaList(v)
	}
	if v := os.Getenv("SHRINKRAY_AUTH_HASH_ALGO"); v != "" {
		cfg.Auth.Password.HashAlgo = v
	}
	if v := os.Getenv("SHRINKRAY_AUTH_USERS"); v != "" {
		cfg.Auth.Password.Users = parseUsersEnv(v)
	}
	if v := os.Getenv("SHRINKRAY_AUTH_OIDC_ISSUER"); v != "" {
		cfg.Auth.OIDC.Issuer = v
	}
	if v := os.Getenv("SHRINKRAY_AUTH_OIDC_CLIENT_ID"); v != "" {
		cfg.Auth.OIDC.ClientID = v
	}
	if v := os.Getenv("SHRINKRAY_AUTH_OIDC_CLIENT_SECRET"); v != "" {
		cfg.Auth.OIDC.ClientSecret = v
	}
	if v := os.Getenv("SHRINKRAY_AUTH_OIDC_REDIRECT_URL"); v != "" {
		cfg.Auth.OIDC.RedirectURL = v
	}
	if v := os.Getenv("SHRINKRAY_AUTH_OIDC_SCOPES"); v != "" {
		cfg.Auth.OIDC.Scopes = splitCommaList(v)
	}
	if v := os.Getenv("SHRINKRAY_AUTH_OIDC_GROUP_CLAIM"); v != "" {
		cfg.Auth.OIDC.GroupClaim = v
	}
	if v := os.Getenv("SHRINKRAY_AUTH_OIDC_ALLOWED_GROUPS"); v != "" {
		cfg.Auth.OIDC.AllowedGroups = splitCommaList(v)
	}
}

func splitCommaList(value string) []string {
	parts := []string{}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		parts = append(parts, item)
	}
	return parts
}

func parseUsersEnv(value string) map[string]string {
	users := make(map[string]string)
	for _, entry := range splitCommaList(value) {
		parts := strings.SplitN(entry, ":", 2)
		if len(parts) != 2 {
			continue
		}
		username := strings.TrimSpace(parts[0])
		hash := strings.TrimSpace(parts[1])
		if username == "" || hash == "" {
			continue
		}
		users[username] = hash
	}
	return users
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

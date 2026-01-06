package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.MediaPath != "/media" {
		t.Errorf("expected MediaPath /media, got %s", cfg.MediaPath)
	}
	if cfg.Workers != 1 {
		t.Errorf("expected Workers 1, got %d", cfg.Workers)
	}
	if cfg.OriginalHandling != "replace" {
		t.Errorf("expected OriginalHandling replace, got %s", cfg.OriginalHandling)
	}
	if cfg.KeepLargerResult != "discard" {
		t.Errorf("expected KeepLargerResult discard, got %s", cfg.KeepLargerResult)
	}
	if cfg.FFmpegPath != "ffmpeg" {
		t.Errorf("expected FFmpegPath ffmpeg, got %s", cfg.FFmpegPath)
	}
}

func TestLoadNonExistent(t *testing.T) {
	cfg, err := Load("/nonexistent/path/config.yaml")
	if err != nil {
		t.Fatalf("expected no error for missing file, got %v", err)
	}

	// Should return defaults
	if cfg.MediaPath != "/media" {
		t.Errorf("expected default MediaPath, got %s", cfg.MediaPath)
	}
}

func TestLoadAndSave(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Create a config
	cfg := &Config{
		MediaPath:        "/my/media",
		TempPath:         "/tmp/shrinkray",
		OriginalHandling: "keep",
		Workers:          2,
		FFmpegPath:       "/usr/bin/ffmpeg",
		FFprobePath:      "/usr/bin/ffprobe",
	}

	// Save it
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// Load it back
	loaded, err := Load(configPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if loaded.MediaPath != cfg.MediaPath {
		t.Errorf("MediaPath mismatch: %s vs %s", loaded.MediaPath, cfg.MediaPath)
	}
	if loaded.TempPath != cfg.TempPath {
		t.Errorf("TempPath mismatch: %s vs %s", loaded.TempPath, cfg.TempPath)
	}
	if loaded.Workers != cfg.Workers {
		t.Errorf("Workers mismatch: %d vs %d", loaded.Workers, cfg.Workers)
	}
}

func TestGetTempDir(t *testing.T) {
	// With TempPath set
	cfg := &Config{TempPath: "/tmp/shrinkray"}
	if dir := cfg.GetTempDir("/media/video.mkv"); dir != "/tmp/shrinkray" {
		t.Errorf("expected /tmp/shrinkray, got %s", dir)
	}

	// Without TempPath set
	cfg = &Config{TempPath: ""}
	if dir := cfg.GetTempDir("/media/movies/video.mkv"); dir != "/media/movies" {
		t.Errorf("expected /media/movies, got %s", dir)
	}
}

func TestLoadWithPartialConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Write partial config
	content := `media_path: /custom/media
workers: 4`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	// Custom values should be set
	if cfg.MediaPath != "/custom/media" {
		t.Errorf("expected /custom/media, got %s", cfg.MediaPath)
	}
	if cfg.Workers != 4 {
		t.Errorf("expected 4 workers, got %d", cfg.Workers)
	}

	// Defaults should apply for unset values
	if cfg.FFmpegPath != "ffmpeg" {
		t.Errorf("expected default ffmpeg path, got %s", cfg.FFmpegPath)
	}
}

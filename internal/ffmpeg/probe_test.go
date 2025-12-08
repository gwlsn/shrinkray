package ffmpeg

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func getTestdataPath() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "..", "testdata")
}

func TestProbe(t *testing.T) {
	testFile := filepath.Join(getTestdataPath(), "test_x264.mkv")

	prober := NewProber("ffprobe")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := prober.Probe(ctx, testFile)
	if err != nil {
		t.Fatalf("Probe failed: %v", err)
	}

	// Verify basic metadata
	if result.Path != testFile {
		t.Errorf("expected path %s, got %s", testFile, result.Path)
	}

	if result.Size == 0 {
		t.Error("expected non-zero size")
	}

	if result.Duration < 9*time.Second || result.Duration > 11*time.Second {
		t.Errorf("expected duration ~10s, got %v", result.Duration)
	}

	if result.VideoCodec != "h264" {
		t.Errorf("expected video codec h264, got %s", result.VideoCodec)
	}

	if result.AudioCodec != "aac" {
		t.Errorf("expected audio codec aac, got %s", result.AudioCodec)
	}

	if result.Width != 1280 {
		t.Errorf("expected width 1280, got %d", result.Width)
	}

	if result.Height != 720 {
		t.Errorf("expected height 720, got %d", result.Height)
	}

	if result.IsHEVC {
		t.Error("expected IsHEVC to be false for h264 content")
	}

	if result.FrameRate < 29 || result.FrameRate > 31 {
		t.Errorf("expected frame rate ~30, got %f", result.FrameRate)
	}

	t.Logf("Probe result: %+v", result)
}

func TestProbeNonExistent(t *testing.T) {
	prober := NewProber("ffprobe")
	ctx := context.Background()

	_, err := prober.Probe(ctx, "/nonexistent/file.mkv")
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}

func TestIsHEVCCodec(t *testing.T) {
	tests := []struct {
		codec    string
		expected bool
	}{
		{"hevc", true},
		{"HEVC", true},
		{"h265", true},
		{"H265", true},
		{"x265", true},
		{"h264", false},
		{"x264", false},
		{"vp9", false},
		{"av1", false},
	}

	for _, tt := range tests {
		result := isHEVCCodec(tt.codec)
		if result != tt.expected {
			t.Errorf("isHEVCCodec(%s) = %v, expected %v", tt.codec, result, tt.expected)
		}
	}
}

func TestParseFrameRate(t *testing.T) {
	tests := []struct {
		input    string
		expected float64
	}{
		{"30/1", 30.0},
		{"30000/1001", 29.97002997},
		{"24/1", 24.0},
		{"25/1", 25.0},
		{"0/0", 0},
		{"", 0},
		{"60", 60.0},
	}

	for _, tt := range tests {
		result := parseFrameRate(tt.input)
		// Allow small floating point differences
		diff := result - tt.expected
		if diff < 0 {
			diff = -diff
		}
		if diff > 0.01 {
			t.Errorf("parseFrameRate(%s) = %f, expected %f", tt.input, result, tt.expected)
		}
	}
}

func TestIsVideoFile(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		{"/media/movie.mkv", true},
		{"/media/movie.mp4", true},
		{"/media/movie.avi", true},
		{"/media/movie.mov", true},
		{"/media/movie.MKV", true},
		{"/media/movie.MP4", true},
		{"/media/document.pdf", false},
		{"/media/image.jpg", false},
		{"/media/audio.mp3", false},
		{"/media/subtitle.srt", false},
	}

	for _, tt := range tests {
		result := IsVideoFile(tt.path)
		if result != tt.expected {
			t.Errorf("IsVideoFile(%s) = %v, expected %v", tt.path, result, tt.expected)
		}
	}
}

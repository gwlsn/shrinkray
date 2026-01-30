//go:build integration

package ffmpeg

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSubtitleFiltering_MovText verifies that mov_text subtitles are filtered out
// and the transcode succeeds (previously failed with "Subtitle codec 94213 is not supported")
func TestSubtitleFiltering_MovText(t *testing.T) {
	testFile := filepath.Join(getTestdataPath(), "test_mov_text_subs.mp4")
	if _, err := os.Stat(testFile); os.IsNotExist(err) {
		t.Skipf("test file not found: %s (run generate script or create manually)", testFile)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Step 1: Probe subtitles
	prober := NewProber("ffprobe")
	subs, err := prober.ProbeSubtitles(ctx, testFile)
	if err != nil {
		t.Fatalf("ProbeSubtitles failed: %v", err)
	}

	if len(subs) == 0 {
		t.Fatal("expected at least one subtitle stream")
	}

	// Verify we detected mov_text
	foundMovText := false
	for _, s := range subs {
		t.Logf("Found subtitle: index=%d codec=%s", s.Index, s.CodecName)
		if s.CodecName == "mov_text" {
			foundMovText = true
		}
	}
	if !foundMovText {
		t.Error("expected to find mov_text codec in test file")
	}

	// Step 2: Filter for MKV compatibility
	compatible, dropped := FilterMKVCompatible(subs)

	// All mov_text should be dropped
	if len(compatible) != 0 {
		t.Errorf("expected 0 compatible streams, got %d", len(compatible))
	}
	if len(dropped) == 0 {
		t.Error("expected dropped codecs, got none")
	}
	for _, codec := range dropped {
		if codec != "mov_text" {
			t.Errorf("expected dropped codec 'mov_text', got '%s'", codec)
		}
	}

	// Step 3: Verify that FFmpeg would succeed with our filtered mapping
	// Build args with empty subtitle indices (all filtered out)
	preset := &Preset{
		ID:      "test",
		Encoder: HWAccelNone,
		Codec:   CodecHEVC,
	}
	_, outputArgs := BuildPresetArgs(preset, 0, 640, 360, 28, 28, 0, false, "mkv", nil, compatible)

	// Verify no subtitle mapping in args (empty slice = no subtitles)
	argsStr := strings.Join(outputArgs, " ")
	if strings.Contains(argsStr, "0:s?") {
		t.Errorf("expected no subtitle mapping with empty indices, got: %s", argsStr)
	}
	if strings.Contains(argsStr, "-c:s") {
		t.Errorf("expected no -c:s with empty indices, got: %s", argsStr)
	}

	t.Log("Subtitle filtering test passed - mov_text correctly filtered out")
}

// TestSubtitleFiltering_Compatible verifies that compatible subtitles are preserved
func TestSubtitleFiltering_Compatible(t *testing.T) {
	testFile := filepath.Join(getTestdataPath(), "comp_h264_subtitles.mkv")
	if _, err := os.Stat(testFile); os.IsNotExist(err) {
		t.Skipf("test file not found: %s", testFile)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	prober := NewProber("ffprobe")
	subs, err := prober.ProbeSubtitles(ctx, testFile)
	if err != nil {
		t.Fatalf("ProbeSubtitles failed: %v", err)
	}

	if len(subs) == 0 {
		t.Skip("test file has no subtitles")
	}

	// Verify we have compatible codecs (subrip)
	for _, s := range subs {
		t.Logf("Found subtitle: index=%d codec=%s", s.Index, s.CodecName)
		if !IsMKVCompatible(s.CodecName) {
			t.Errorf("expected compatible codec, got incompatible: %s", s.CodecName)
		}
	}

	// Filter should preserve all
	compatible, dropped := FilterMKVCompatible(subs)
	if len(dropped) != 0 {
		t.Errorf("expected no dropped codecs, got %v", dropped)
	}
	if len(compatible) != len(subs) {
		t.Errorf("expected %d compatible, got %d", len(subs), len(compatible))
	}

	t.Log("Compatible subtitle test passed - subrip correctly preserved")
}

// TestSubtitleFiltering_EndToEnd performs an actual transcode with filtering
func TestSubtitleFiltering_EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping end-to-end test in short mode")
	}

	testFile := filepath.Join(getTestdataPath(), "test_mov_text_subs.mp4")
	if _, err := os.Stat(testFile); os.IsNotExist(err) {
		t.Skipf("test file not found: %s", testFile)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Create temp output file
	tmpDir := t.TempDir()
	outputFile := filepath.Join(tmpDir, "output.mkv")

	// Probe and filter subtitles
	prober := NewProber("ffprobe")
	subs, err := prober.ProbeSubtitles(ctx, testFile)
	if err != nil {
		t.Fatalf("ProbeSubtitles failed: %v", err)
	}

	compatible, dropped := FilterMKVCompatible(subs)
	t.Logf("Filtering result: %d compatible, %d dropped (%v)", len(compatible), len(dropped), dropped)

	// Build FFmpeg command with filtered subtitles
	// Using copy codecs for speed
	args := []string{
		"-i", testFile,
		"-c:v", "copy",
		"-c:a", "copy",
		"-map", "0:v:0",
		"-map", "0:a?",
	}

	// Add subtitle mapping based on filter result
	if len(compatible) > 0 {
		for _, idx := range compatible {
			args = append(args, "-map", fmt.Sprintf("0:%d?", idx))
		}
		args = append(args, "-c:s", "copy")
	}
	// If compatible is empty, no subtitle args (they get filtered out)

	args = append(args, "-y", outputFile)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	output, err := cmd.CombinedOutput()

	if err != nil {
		t.Fatalf("FFmpeg failed (this is the bug we're fixing!): %v\nOutput: %s", err, output)
	}

	// Verify output file exists and has no subtitle streams
	outputSubs, err := prober.ProbeSubtitles(ctx, outputFile)
	if err != nil {
		t.Fatalf("Failed to probe output: %v", err)
	}

	if len(outputSubs) != 0 {
		t.Errorf("expected 0 subtitle streams in output (all filtered), got %d", len(outputSubs))
	}

	t.Log("End-to-end transcode succeeded - mov_text correctly filtered, MKV muxing succeeded")
}

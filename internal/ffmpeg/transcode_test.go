package ffmpeg

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildTempPath(t *testing.T) {
	tests := []struct {
		input    string
		tempDir  string
		expected string
	}{
		{
			"/media/movie.mkv",
			"/tmp",
			"/tmp/movie.shrinkray.tmp.mkv",
		},
		{
			"/media/tv/show/episode.mp4",
			"/media/tv/show",
			"/media/tv/show/episode.shrinkray.tmp.mkv",
		},
		{
			"/data/video.avi",
			"/data",
			"/data/video.shrinkray.tmp.mkv",
		},
	}

	for _, tt := range tests {
		result := BuildTempPath(tt.input, tt.tempDir)
		if result != tt.expected {
			t.Errorf("BuildTempPath(%s, %s) = %s, expected %s",
				tt.input, tt.tempDir, result, tt.expected)
		}
	}
}

func TestTranscode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping transcode test in short mode")
	}

	testFile := filepath.Join(getTestdataPath(), "test_x264.mkv")
	if _, err := os.Stat(testFile); os.IsNotExist(err) {
		t.Skipf("test file not found: %s", testFile)
	}

	// Probe the input file first
	prober := NewProber("ffprobe")
	ctx := context.Background()
	probeResult, err := prober.Probe(ctx, testFile)
	if err != nil {
		t.Fatalf("failed to probe test file: %v", err)
	}

	// Create temp output directory
	tmpDir := t.TempDir()
	outputPath := BuildTempPath(testFile, tmpDir)

	// Create transcoder and progress channel
	transcoder := NewTranscoder("ffmpeg")
	progressCh := make(chan Progress, 100)

	// Run transcode with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	preset := GetPreset("compress")

	// Collect progress in a goroutine
	var progressUpdates []Progress
	done := make(chan struct{})
	go func() {
		for p := range progressCh {
			progressUpdates = append(progressUpdates, p)
			t.Logf("Progress: %.1f%% (speed=%.2fx, eta=%v)", p.Percent, p.Speed, p.ETA)
		}
		close(done)
	}()

	result, err := transcoder.Transcode(ctx, testFile, outputPath, preset, probeResult.Duration, probeResult.Bitrate, progressCh)
	<-done

	if err != nil {
		t.Fatalf("transcode failed: %v", err)
	}

	// Verify result
	if result.InputSize == 0 {
		t.Error("expected non-zero input size")
	}

	if result.OutputSize == 0 {
		t.Error("expected non-zero output size")
	}

	// Verify output file exists
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		t.Error("output file was not created")
	}

	// Verify output is valid video
	outputProbe, err := prober.Probe(ctx, outputPath)
	if err != nil {
		t.Fatalf("failed to probe output file: %v", err)
	}

	if outputProbe.VideoCodec != "hevc" {
		t.Errorf("expected output codec hevc, got %s", outputProbe.VideoCodec)
	}

	// Should have received some progress updates
	if len(progressUpdates) == 0 {
		t.Error("expected to receive progress updates")
	}

	t.Logf("Transcode result: %d → %d bytes (%.1f%% reduction) in %v",
		result.InputSize, result.OutputSize,
		float64(result.SpaceSaved)/float64(result.InputSize)*100,
		result.Duration)
}

func TestFinalizeTranscodeReplace(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a fake "original" file
	originalPath := filepath.Join(tmpDir, "video.mkv")
	if err := os.WriteFile(originalPath, []byte("original content"), 0644); err != nil {
		t.Fatalf("failed to create original: %v", err)
	}

	// Create a fake "temp" file (transcoded output)
	tempPath := filepath.Join(tmpDir, "video.shrinkray.tmp.mkv")
	if err := os.WriteFile(tempPath, []byte("transcoded content"), 0644); err != nil {
		t.Fatalf("failed to create temp: %v", err)
	}

	// Finalize with replace=true
	finalPath, err := FinalizeTranscode(originalPath, tempPath, true)
	if err != nil {
		t.Fatalf("FinalizeTranscode failed: %v", err)
	}

	// Original should be renamed to .old
	oldPath := originalPath + ".old"
	if _, err := os.Stat(oldPath); os.IsNotExist(err) {
		t.Error("original was not renamed to .old")
	}

	// Final path should contain transcoded content
	content, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("failed to read final file: %v", err)
	}
	if string(content) != "transcoded content" {
		t.Error("final file has wrong content")
	}

	// Temp file should be gone
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Error("temp file still exists")
	}

	t.Logf("Replace mode: original→%s, final=%s", oldPath, finalPath)
}

func TestFinalizeTranscodeKeep(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a fake "original" file
	originalPath := filepath.Join(tmpDir, "video.mp4")
	if err := os.WriteFile(originalPath, []byte("original content"), 0644); err != nil {
		t.Fatalf("failed to create original: %v", err)
	}

	// Create a fake "temp" file
	tempPath := filepath.Join(tmpDir, "video.shrinkray.tmp.mkv")
	if err := os.WriteFile(tempPath, []byte("transcoded content"), 0644); err != nil {
		t.Fatalf("failed to create temp: %v", err)
	}

	// Finalize with replace=false (keep both)
	finalPath, err := FinalizeTranscode(originalPath, tempPath, false)
	if err != nil {
		t.Fatalf("FinalizeTranscode failed: %v", err)
	}

	// Original should still exist with original content
	content, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatalf("failed to read original: %v", err)
	}
	if string(content) != "original content" {
		t.Error("original file was modified")
	}

	// Final file should exist with transcoded content
	content, err = os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("failed to read final: %v", err)
	}
	if string(content) != "transcoded content" {
		t.Error("final file has wrong content")
	}

	// Temp file should be gone
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Error("temp file still exists")
	}

	t.Logf("Keep mode: original=%s (kept), final=%s", originalPath, finalPath)
}

func TestParseProgressLine(t *testing.T) {
	line := "frame=  123 fps=45.6 q=28.0 size=    1234kB time=00:01:23.45 bitrate= 123.4kbits/s speed=1.5x"
	duration := 10 * time.Minute

	progress := ParseProgressLine(line, duration)
	if progress == nil {
		t.Fatal("failed to parse progress line")
	}

	if progress.Frame != 123 {
		t.Errorf("expected frame 123, got %d", progress.Frame)
	}

	if progress.FPS != 45.6 {
		t.Errorf("expected fps 45.6, got %f", progress.FPS)
	}

	if progress.Size != 1234*1024 {
		t.Errorf("expected size %d, got %d", 1234*1024, progress.Size)
	}

	expectedTime := 1*time.Minute + 23*time.Second + 450*time.Millisecond
	if progress.Time != expectedTime {
		t.Errorf("expected time %v, got %v", expectedTime, progress.Time)
	}

	if progress.Speed != 1.5 {
		t.Errorf("expected speed 1.5, got %f", progress.Speed)
	}

	t.Logf("Parsed progress: %+v", progress)
}

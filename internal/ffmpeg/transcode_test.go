package ffmpeg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildTempPath(t *testing.T) {
	tests := []struct {
		input   string
		tempDir string
		format  string
		wantDir string
		prefix  string
		suffix  string
	}{
		{"/media/movie.mkv", "/tmp", "mkv", "/tmp", "movie.", ".shrinkray.tmp.mkv"},
		{"/media/tv/show/episode.mp4", "/media/tv/show", "mkv", "/media/tv/show", "episode.", ".shrinkray.tmp.mkv"},
		{"/data/video.avi", "/data", "mp4", "/data", "video.", ".shrinkray.tmp.mp4"},
	}

	for _, tt := range tests {
		result := BuildTempPath(tt.input, tt.tempDir, tt.format)
		dir := filepath.Dir(result)
		base := filepath.Base(result)
		if dir != tt.wantDir {
			t.Errorf("BuildTempPath(%s, %s, %s): dir = %s, want %s", tt.input, tt.tempDir, tt.format, dir, tt.wantDir)
		}
		if !strings.HasPrefix(base, tt.prefix) {
			t.Errorf("BuildTempPath(%s, %s, %s): base %s missing prefix %s", tt.input, tt.tempDir, tt.format, base, tt.prefix)
		}
		if !strings.HasSuffix(base, tt.suffix) {
			t.Errorf("BuildTempPath(%s, %s, %s): base %s missing suffix %s", tt.input, tt.tempDir, tt.format, base, tt.suffix)
		}
	}

	// Verify uniqueness: same input produces different paths (random suffix)
	a := BuildTempPath("/media/movie.mkv", "/tmp", "mkv")
	b := BuildTempPath("/media/movie.mkv", "/tmp", "mkv")
	if a == b {
		t.Errorf("expected unique paths, got identical: %s", a)
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
	outputPath := BuildTempPath(testFile, tmpDir, "mkv")

	// Create transcoder and progress channel
	transcoder := NewTranscoder("ffmpeg")
	progressCh := make(chan Progress, 100)

	// Run transcode with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	preset := GetPreset("compress-hevc")

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

	totalFrames := int64(probeResult.Duration.Seconds() * probeResult.FrameRate)
	result, err := transcoder.Transcode(ctx, testFile, outputPath, preset, probeResult.Duration, probeResult.Bitrate, probeResult.Width, probeResult.Height, 0, 0, 0, totalFrames, progressCh, false, "mkv", nil, nil)
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
	finalPath, err := FinalizeTranscode(originalPath, tempPath, "mkv", true)
	if err != nil {
		t.Fatalf("FinalizeTranscode failed: %v", err)
	}

	// Files are now written to completed/ subdirectory
	expectedFinalPath := filepath.Join(tmpDir, "completed", "video.mkv")
	if finalPath != expectedFinalPath {
		t.Errorf("expected final path %s, got %s", expectedFinalPath, finalPath)
	}

	// .old file should NOT exist in replace mode
	oldPath := originalPath + ".old"
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Error(".old file exists, but replace mode should delete original")
	}

	// Final path should contain transcoded content (original was replaced)
	content, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("failed to read final file: %v", err)
	}
	if string(content) != "transcoded content" {
		t.Error("final file has wrong content - original content should have been replaced")
	}

	// Temp file should be gone
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Error("temp file still exists")
	}

	// Original should be deleted
	if _, err := os.Stat(originalPath); !os.IsNotExist(err) {
		t.Error("original file should be deleted in replace mode")
	}

	t.Logf("Replace mode (mkv→mkv): original replaced, final=%s", finalPath)
}

func TestFinalizeTranscodeReplaceDifferentExt(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a fake "original" mp4 file
	originalPath := filepath.Join(tmpDir, "video.mp4")
	if err := os.WriteFile(originalPath, []byte("original content"), 0644); err != nil {
		t.Fatalf("failed to create original: %v", err)
	}

	// Create a fake "temp" file (transcoded output)
	tempPath := filepath.Join(tmpDir, "video.shrinkray.tmp.mkv")
	if err := os.WriteFile(tempPath, []byte("transcoded content"), 0644); err != nil {
		t.Fatalf("failed to create temp: %v", err)
	}

	// Finalize with replace=true
	finalPath, err := FinalizeTranscode(originalPath, tempPath, "mkv", true)
	if err != nil {
		t.Fatalf("FinalizeTranscode failed: %v", err)
	}

	// Original mp4 should be deleted
	if _, err := os.Stat(originalPath); !os.IsNotExist(err) {
		t.Error("original mp4 file still exists, should have been deleted")
	}

	// Final path should be in completed/ subdirectory with .mkv extension
	expectedFinal := filepath.Join(tmpDir, "completed", "video.mkv")
	if finalPath != expectedFinal {
		t.Errorf("expected final path %s, got %s", expectedFinal, finalPath)
	}

	content, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("failed to read final file: %v", err)
	}
	if string(content) != "transcoded content" {
		t.Error("final file has wrong content")
	}

	t.Logf("Replace mode (mp4→mkv): original deleted, final=%s", finalPath)
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

	// Finalize with replace=false (keep original as .old)
	finalPath, err := FinalizeTranscode(originalPath, tempPath, "mkv", false)
	if err != nil {
		t.Fatalf("FinalizeTranscode failed: %v", err)
	}

	// Original should be renamed to .old with original content
	oldPath := originalPath + ".old"
	content, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatalf("failed to read .old file: %v", err)
	}
	if string(content) != "original content" {
		t.Error(".old file has wrong content")
	}

	// Original path should no longer exist (renamed to .old)
	if _, err := os.Stat(originalPath); !os.IsNotExist(err) {
		t.Error("original file still exists at original path, should have been renamed to .old")
	}

	// Final file should exist in completed/ subdirectory with transcoded content
	expectedFinal := filepath.Join(tmpDir, "completed", "video.mkv")
	if finalPath != expectedFinal {
		t.Errorf("expected final path %s, got %s", expectedFinal, finalPath)
	}

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

	t.Logf("Keep mode: original→%s, final=%s", oldPath, finalPath)
}

func TestTranscode_ClosesChannelOnEarlyError(t *testing.T) {
	transcoder := NewTranscoder("ffmpeg")
	progressCh := make(chan Progress, 10)

	// Call with nonexistent file to trigger early os.Stat error
	ctx := context.Background()
	_, err := transcoder.Transcode(
		ctx,
		"/nonexistent/path/to/file.mp4",
		"/tmp/out.mkv",
		&Preset{Encoder: HWAccelNone, Codec: CodecHEVC},
		time.Minute,
		0,
		1920, 1080,
		0, 0, 0,
		1000,
		progressCh,
		false,
		"mkv",
		nil,
		nil,
	)

	// Should error
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}

	// Channel should be closed (not leak the consumer goroutine)
	select {
	case _, open := <-progressCh:
		if open {
			t.Error("channel should be closed after early error")
		}
		// Good - channel is closed
	case <-time.After(100 * time.Millisecond):
		t.Error("channel was not closed - goroutine would leak")
	}
}

func TestFrameBasedSpeedAndETA(t *testing.T) {
	// Simulate: 1000 frame video, 100 seconds duration
	// After 10 seconds wall time, 200 frames encoded (20%)
	totalFrames := int64(1000)
	duration := 100 * time.Second
	currentFrame := int64(200)
	elapsedWallTime := 10 * time.Second

	// Expected:
	// - Video time encoded = 100s * (200/1000) = 20s
	// - Speed = 20s / 10s = 2.0x (encoding 2x realtime)
	// - Frames remaining = 800
	// - ETA = 10s * (800/200) = 40s

	videoTimeEncoded := time.Duration(float64(duration) * float64(currentFrame) / float64(totalFrames))
	speed := float64(videoTimeEncoded) / float64(elapsedWallTime)
	framesRemaining := totalFrames - currentFrame
	eta := time.Duration(float64(elapsedWallTime) * float64(framesRemaining) / float64(currentFrame))

	if speed < 1.99 || speed > 2.01 {
		t.Errorf("expected speed ~2.0x, got %.2fx", speed)
	}
	if eta < 39*time.Second || eta > 41*time.Second {
		t.Errorf("expected ETA ~40s, got %v", eta)
	}

	t.Logf("Frame-based calculation: speed=%.2fx, eta=%v", speed, eta)
}

// TestTranscodeError_Unwrap tests that TranscodeError properly wraps errors
func TestTranscodeError_Unwrap(t *testing.T) {
	innerErr := fmt.Errorf("ffmpeg process failed")
	transcodeErr := &TranscodeError{
		Err:    innerErr,
		Stderr: "some error output",
		Frames: 100,
	}

	// Test Error() method
	if transcodeErr.Error() != innerErr.Error() {
		t.Errorf("Error() = %q, want %q", transcodeErr.Error(), innerErr.Error())
	}

	// Test Unwrap() method
	unwrapped := transcodeErr.Unwrap()
	if unwrapped != innerErr {
		t.Errorf("Unwrap() returned wrong error")
	}

	// Test that errors.Is works with wrapped error
	if !strings.Contains(transcodeErr.Error(), "ffmpeg process failed") {
		t.Error("TranscodeError should contain inner error message")
	}
}

// TestTranscodeError_Fields tests TranscodeError field access
func TestTranscodeError_Fields(t *testing.T) {
	stderr := "hwaccel not available"
	frames := int64(0)
	err := &TranscodeError{
		Err:    fmt.Errorf("hardware decode failed"),
		Stderr: stderr,
		Frames: frames,
	}

	if err.Stderr != stderr {
		t.Errorf("Stderr = %q, want %q", err.Stderr, stderr)
	}
	if err.Frames != frames {
		t.Errorf("Frames = %d, want %d", err.Frames, frames)
	}
}

// TestFinalizeTranscode_CompletedDirectory tests completed/ subdirectory creation
func TestFinalizeTranscode_CompletedDirectory(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a fake "original" file
	originalPath := filepath.Join(tmpDir, "video.mkv")
	if err := os.WriteFile(originalPath, []byte("original content"), 0644); err != nil {
		t.Fatalf("failed to create original: %v", err)
	}

	// Create a fake "temp" file
	tempPath := filepath.Join(tmpDir, "video.shrinkray.tmp.mkv")
	if err := os.WriteFile(tempPath, []byte("transcoded content"), 0644); err != nil {
		t.Fatalf("failed to create temp: %v", err)
	}

	// Finalize with replace=true
	finalPath, err := FinalizeTranscode(originalPath, tempPath, "mkv", true)
	if err != nil {
		t.Fatalf("FinalizeTranscode failed: %v", err)
	}

	// Final path should be in completed/ subdirectory
	expectedDir := filepath.Join(tmpDir, "completed")
	expectedPath := filepath.Join(expectedDir, "video.mkv")

	if finalPath != expectedPath {
		t.Errorf("finalPath = %q, want %q", finalPath, expectedPath)
	}

	// Verify completed/ directory was created
	if stat, err := os.Stat(expectedDir); err != nil || !stat.IsDir() {
		t.Errorf("completed/ directory not created: %v", err)
	}

	// Verify final file exists in completed/ with correct content
	content, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("failed to read final file: %v", err)
	}
	if string(content) != "transcoded content" {
		t.Error("final file has wrong content")
	}

	// Original should be deleted
	if _, err := os.Stat(originalPath); !os.IsNotExist(err) {
		t.Error("original file should be deleted in replace mode")
	}
}

// TestFinalizeTranscode_CompletedDirectoryKeepMode tests completed/ with keep mode
func TestFinalizeTranscode_CompletedDirectoryKeepMode(t *testing.T) {
	tmpDir := t.TempDir()

	originalPath := filepath.Join(tmpDir, "video.mp4")
	if err := os.WriteFile(originalPath, []byte("original content"), 0644); err != nil {
		t.Fatalf("failed to create original: %v", err)
	}

	tempPath := filepath.Join(tmpDir, "video.shrinkray.tmp.mkv")
	if err := os.WriteFile(tempPath, []byte("transcoded content"), 0644); err != nil {
		t.Fatalf("failed to create temp: %v", err)
	}

	// Finalize with replace=false (keep mode)
	finalPath, err := FinalizeTranscode(originalPath, tempPath, "mkv", false)
	if err != nil {
		t.Fatalf("FinalizeTranscode failed: %v", err)
	}

	// Final path should be in completed/ subdirectory
	expectedDir := filepath.Join(tmpDir, "completed")
	expectedPath := filepath.Join(expectedDir, "video.mkv")

	if finalPath != expectedPath {
		t.Errorf("finalPath = %q, want %q", finalPath, expectedPath)
	}

	// Original should be renamed to .old
	oldPath := originalPath + ".old"
	content, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatalf("failed to read .old file: %v", err)
	}
	if string(content) != "original content" {
		t.Error(".old file has wrong content")
	}

	// Final file should exist with transcoded content
	content, err = os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("failed to read final file: %v", err)
	}
	if string(content) != "transcoded content" {
		t.Error("final file has wrong content")
	}
}

// TestBuildTempPath_Uniqueness tests that BuildTempPath generates unique paths
func TestBuildTempPath_Uniqueness(t *testing.T) {
	paths := make(map[string]bool)
	inputPath := "/media/movies/test.mkv"
	tempDir := "/tmp"

	// Generate 100 temp paths and ensure they're all unique
	for i := 0; i < 100; i++ {
		path := BuildTempPath(inputPath, tempDir, "mkv")
		if paths[path] {
			t.Errorf("duplicate path generated: %s", path)
		}
		paths[path] = true

		// Verify path structure
		if !strings.Contains(path, "test.") {
			t.Errorf("path missing original filename prefix: %s", path)
		}
		if !strings.Contains(path, ".shrinkray.tmp.mkv") {
			t.Errorf("path missing temp marker: %s", path)
		}
	}
}

// TestBuildTempPath_FormatSelection tests different output formats
func TestBuildTempPath_FormatSelection(t *testing.T) {
	tests := []struct {
		format   string
		wantExt  string
	}{
		{"mkv", ".shrinkray.tmp.mkv"},
		{"mp4", ".shrinkray.tmp.mp4"},
	}

	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			path := BuildTempPath("/media/test.mkv", "/tmp", tt.format)
			if !strings.HasSuffix(path, tt.wantExt) {
				t.Errorf("path %q does not end with %q", path, tt.wantExt)
			}
		})
	}
}

// TestProgress_Fields tests Progress struct field types
func TestProgress_Fields(t *testing.T) {
	p := Progress{
		Frame:   1000,
		FPS:     30.5,
		Size:    1024000,
		Time:    60 * time.Second,
		Bitrate: 2500.5,
		Speed:   1.5,
		Percent: 50.25,
		ETA:     120 * time.Second,
	}

	// Verify types are accessible
	if p.Frame != 1000 {
		t.Errorf("Frame = %d, want 1000", p.Frame)
	}
	if p.FPS != 30.5 {
		t.Errorf("FPS = %.1f, want 30.5", p.FPS)
	}
	if p.Size != 1024000 {
		t.Errorf("Size = %d, want 1024000", p.Size)
	}
	if p.Time != 60*time.Second {
		t.Errorf("Time = %v, want 1m0s", p.Time)
	}
	if p.Bitrate != 2500.5 {
		t.Errorf("Bitrate = %.1f, want 2500.5", p.Bitrate)
	}
	if p.Speed != 1.5 {
		t.Errorf("Speed = %.1f, want 1.5", p.Speed)
	}
	if p.Percent != 50.25 {
		t.Errorf("Percent = %.2f, want 50.25", p.Percent)
	}
	if p.ETA != 120*time.Second {
		t.Errorf("ETA = %v, want 2m0s", p.ETA)
	}
}

// TestTranscodeResult_Fields tests TranscodeResult struct
func TestTranscodeResult_Fields(t *testing.T) {
	result := TranscodeResult{
		InputPath:  "/input/video.mkv",
		OutputPath: "/output/video.mkv",
		InputSize:  1000000,
		OutputSize: 500000,
		SpaceSaved: 500000,
		Duration:   5 * time.Minute,
	}

	if result.InputPath != "/input/video.mkv" {
		t.Errorf("InputPath = %q, want /input/video.mkv", result.InputPath)
	}
	if result.OutputPath != "/output/video.mkv" {
		t.Errorf("OutputPath = %q, want /output/video.mkv", result.OutputPath)
	}
	if result.InputSize != 1000000 {
		t.Errorf("InputSize = %d, want 1000000", result.InputSize)
	}
	if result.OutputSize != 500000 {
		t.Errorf("OutputSize = %d, want 500000", result.OutputSize)
	}
	if result.SpaceSaved != 500000 {
		t.Errorf("SpaceSaved = %d, want 500000", result.SpaceSaved)
	}
	if result.Duration != 5*time.Minute {
		t.Errorf("Duration = %v, want 5m0s", result.Duration)
	}
}
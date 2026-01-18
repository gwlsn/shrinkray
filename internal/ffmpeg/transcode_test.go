package ffmpeg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildTempPath(t *testing.T) {
	tests := []struct {
		input    string
		tempDir  string
		format   string
		expected string
	}{
		{
			"/media/movie.mkv",
			"/tmp",
			"mkv",
			"/tmp/movie.shrinkray.tmp.mkv",
		},
		{
			"/media/tv/show/episode.mp4",
			"/media/tv/show",
			"mkv",
			"/media/tv/show/episode.shrinkray.tmp.mkv",
		},
		{
			"/data/video.avi",
			"/data",
			"mp4",
			"/data/video.shrinkray.tmp.mp4",
		},
	}

	for _, tt := range tests {
		result := BuildTempPath(tt.input, tt.tempDir, tt.format)
		if result != tt.expected {
			t.Errorf("BuildTempPath(%s, %s, %s) = %s, expected %s",
				tt.input, tt.tempDir, tt.format, result, tt.expected)
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
	outputPath := BuildTempPath(testFile, tmpDir, "mkv")

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

	totalFrames := int64(probeResult.Duration.Seconds() * probeResult.FrameRate)
	result, err := transcoder.Transcode(ctx, testFile, outputPath, preset, probeResult.Duration, probeResult.Bitrate, probeResult.Width, probeResult.Height, 0, 0, totalFrames, progressCh, false, "mkv", nil)
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

	// For mkv→mkv, finalPath == originalPath, so the file should exist
	// but contain transcoded content (original was replaced)
	if finalPath != originalPath {
		t.Errorf("expected final path %s to equal original path %s for mkv→mkv", finalPath, originalPath)
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

	t.Logf("Replace mode (mkv→mkv): original replaced in-place, final=%s", finalPath)
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

	// Final path should be .mkv and contain transcoded content
	expectedFinal := filepath.Join(tmpDir, "video.mkv")
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

	t.Logf("Keep mode: original→%s, final=%s", oldPath, finalPath)
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

// TestTranscodeCorruptMPEG2 verifies that Shrinkray can handle corrupt MPEG2
// transport streams without failing. This is a regression test for issue #74.
// See: https://github.com/gwlsn/shrinkray/issues/74
func TestTranscodeCorruptMPEG2(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping corrupt MPEG2 test in short mode")
	}

	tmpDir := t.TempDir()

	// Step 1: Generate a clean MPEG2 transport stream test file
	// Use 5 seconds at 25fps = 125 frames for meaningful corruption testing.
	// This gives us enough frames that partial corruption (like issue #74)
	// will still allow progress before hitting bad frames.
	cleanPath := filepath.Join(tmpDir, "test_clean.ts")
	genCmd := []string{
		"-f", "lavfi",
		"-i", "testsrc=duration=5:size=320x240:rate=25",
		"-c:v", "mpeg2video",
		"-b:v", "1M",
		"-y", cleanPath,
	}
	t.Logf("Generating clean MPEG2 test file: ffmpeg %v", genCmd)

	ctx := context.Background()
	genCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := execCommandContext(genCtx, "ffmpeg", genCmd...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to generate test file: %v\nOutput: %s", err, output)
	}

	// Verify the clean file was created
	cleanInfo, err := os.Stat(cleanPath)
	if err != nil {
		t.Fatalf("failed to stat clean file: %v", err)
	}
	t.Logf("Generated clean MPEG2 file: %d bytes", cleanInfo.Size())

	// Step 2: Create multiple corrupt versions with different corruption patterns
	// These patterns simulate real-world corruption found in broadcast recordings:
	// - Random byte injection (bitflips, transmission errors)
	// - Zeroed chunks (dropped packets, storage issues)
	// - Multiple scattered corruptions (typical of damaged .ts files)
	corruptTests := []struct {
		name      string
		corruptFn func(path string) error
	}{
		{
			name: "random_bytes_middle",
			corruptFn: func(path string) error {
				// Single corruption in the middle - tests basic error handling
				return corruptFileAtOffset(path, cleanInfo.Size()/2, 500)
			},
		},
		{
			name: "zeroed_chunks",
			corruptFn: func(path string) error {
				// Zero out several small chunks (simulates dropped packets)
				// Keep chunks small to avoid destroying entire GOPs
				f, err := os.OpenFile(path, os.O_WRONLY, 0644)
				if err != nil {
					return err
				}
				defer f.Close()
				zeros := make([]byte, 100)
				for offset := int64(10000); offset < cleanInfo.Size()-1000; offset += 25000 {
					if _, err := f.WriteAt(zeros, offset); err != nil {
						return err
					}
				}
				return nil
			},
		},
		{
			name: "scattered_corruption",
			corruptFn: func(path string) error {
				// Scattered small corruptions throughout the file
				// This simulates the typical .ts corruption pattern where
				// some frames are corrupted but most remain decodable
				// (like issue #74 where transcode reached 44% before failure)
				for offset := int64(15000); offset < cleanInfo.Size()-5000; offset += 20000 {
					if err := corruptFileAtOffset(path, offset, 80); err != nil {
						return err
					}
				}
				return nil
			},
		},
	}

	// Initialize presets for software encoding
	InitPresets()
	preset := getSoftwarePreset("compress-hevc")
	if preset == nil {
		t.Fatal("failed to get software HEVC preset")
	}

	for _, tt := range corruptTests {
		t.Run(tt.name, func(t *testing.T) {
			// Copy clean file to corrupt test file
			corruptPath := filepath.Join(tmpDir, tt.name+".ts")
			if err := copyFile(cleanPath, corruptPath); err != nil {
				t.Fatalf("failed to copy file: %v", err)
			}

			// Apply corruption
			if err := tt.corruptFn(corruptPath); err != nil {
				t.Fatalf("failed to corrupt file: %v", err)
			}

			// Probe the corrupt file (should still work - probing is lenient)
			prober := NewProber("ffprobe")
			probeResult, err := prober.Probe(ctx, corruptPath)
			if err != nil {
				t.Fatalf("failed to probe corrupt file: %v", err)
			}

			// Step 3: Transcode the corrupt file - this should NOT fail
			outputPath := filepath.Join(tmpDir, tt.name+"_output.mkv")
			transcoder := NewTranscoder("ffmpeg")
			progressCh := make(chan Progress, 100)

			// Drain progress channel in background
			go func() {
				for range progressCh {
				}
			}()

			transcodeCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()

			totalFrames := int64(probeResult.Duration.Seconds() * probeResult.FrameRate)
			result, err := transcoder.Transcode(
				transcodeCtx,
				corruptPath,
				outputPath,
				preset,
				probeResult.Duration,
				probeResult.Bitrate,
				probeResult.Width,
				probeResult.Height,
				0, 0, // qualityHEVC, qualityAV1 (use defaults)
				totalFrames,
				progressCh,
				false, // softwareDecode (test primary path first)
				"mkv",
				nil, // tonemap
			)

			// The key assertion: transcode should succeed despite corruption
			if err != nil {
				t.Errorf("CRITICAL: Transcode of corrupt MPEG2 failed: %v", err)
				t.Errorf("This is the bug from issue #74 - corrupt frames causing -38/ENOSYS errors")
				t.FailNow()
			}

			// Verify output was created
			if result.OutputSize == 0 {
				t.Error("output size is 0, transcode may have partially failed")
			}

			// Verify output is a valid video file
			outputProbe, err := prober.Probe(ctx, outputPath)
			if err != nil {
				t.Errorf("failed to probe output file: %v", err)
			} else if outputProbe.VideoCodec != "hevc" {
				t.Errorf("expected hevc codec in output, got %s", outputProbe.VideoCodec)
			}

			t.Logf("SUCCESS: Corrupt MPEG2 (%s) transcoded: %d → %d bytes",
				tt.name, result.InputSize, result.OutputSize)
		})
	}
}

// corruptFileAtOffset writes random bytes at the specified offset in a file
func corruptFileAtOffset(path string, offset, count int64) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	// Use a deterministic "random" pattern for reproducibility
	garbage := make([]byte, count)
	for i := range garbage {
		garbage[i] = byte((i * 17 + int(offset)) % 256)
	}

	_, err = f.WriteAt(garbage, offset)
	return err
}

// copyFile copies a file from src to dst
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

// execCommandContext wraps exec.CommandContext for testing
func execCommandContext(ctx context.Context, name string, args ...string) *execCmd {
	return &execCmd{ctx: ctx, name: name, args: args}
}

type execCmd struct {
	ctx  context.Context
	name string
	args []string
}

func (c *execCmd) CombinedOutput() ([]byte, error) {
	cmd := exec.CommandContext(c.ctx, c.name, c.args...)
	return cmd.CombinedOutput()
}

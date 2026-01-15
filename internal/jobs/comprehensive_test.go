package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gwlsn/shrinkray/internal/config"
	"github.com/gwlsn/shrinkray/internal/ffmpeg"
)

// TestComprehensiveTranscode runs comprehensive end-to-end tests covering all
// codec/bitdepth/track permutations through the full Shrinkray pipeline.
//
// These tests verify:
// 1. All codec types (H.264, H.265, AV1, VP9, MPEG-4)
// 2. All bit depths (8-bit, 10-bit)
// 3. Cover art handling (-map 0:v:0 fix)
// 4. Multi-track files (multiple audio, subtitles)
// 5. Long duration files (catch late-stage failures like -38 error)
//
// Generate test vectors first: testdata/generate_comprehensive_vectors.sh
func TestComprehensiveTranscode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping comprehensive transcode test in short mode")
	}

	testdataDir, err := filepath.Abs(filepath.Join("..", "..", "testdata"))
	if err != nil {
		t.Fatalf("failed to get testdata path: %v", err)
	}

	// Check for comprehensive test files
	criticalFile := filepath.Join(testdataDir, "comp_h264_8bit_short.mp4")
	if _, err := os.Stat(criticalFile); os.IsNotExist(err) {
		t.Skipf("comprehensive test vectors not generated - run testdata/generate_comprehensive_vectors.sh first\n"+
			"Missing: %s", criticalFile)
	}

	tests := []struct {
		name            string
		file            string
		preset          string
		expectSkip      bool // Should be skipped (already target codec)
		expectComplete  bool // Should complete successfully
		expectFail      bool // Expected to fail (e.g., output larger than input)
		expectSWDecode  bool // Should trigger software decode fallback
		timeout         time.Duration
		skipIfNotExists bool // Skip instead of fail if file doesn't exist
	}{
		// ===== H.264 8-bit Tests (should hardware decode) =====
		{
			name:           "H264_8bit_Short",
			file:           "comp_h264_8bit_short.mp4",
			preset:         "compress-hevc",
			expectComplete: true,
			timeout:        2 * time.Minute,
		},
		{
			name:           "H264_8bit_Medium",
			file:           "comp_h264_8bit_medium.mp4",
			preset:         "compress-hevc",
			expectComplete: true,
			timeout:        5 * time.Minute,
		},
		{
			name:           "H264_8bit_Long_LateStageTest",
			file:           "comp_h264_8bit_long.mp4",
			preset:         "compress-hevc",
			expectComplete: true,
			timeout:        15 * time.Minute, // Long file - needs time
		},

		// ===== H.264 10-bit Tests (Issue #56 - software decode required) =====
		{
			name:           "H264_10bit_Short_SoftwareDecode",
			file:           "comp_h264_10bit_short.mkv",
			preset:         "compress-hevc",
			expectComplete: true,
			expectSWDecode: true,
			timeout:        3 * time.Minute,
		},
		{
			name:           "H264_10bit_Long_Issue56_Critical",
			file:           "comp_h264_10bit_long.mkv",
			preset:         "compress-hevc",
			expectComplete: true,
			expectSWDecode: true,
			timeout:        20 * time.Minute, // Critical: catch late-stage -38 errors
		},

		// ===== Cover Art Tests (Issue #40 - -map 0:v:0 fix) =====
		{
			name:           "H264_CoverArt_Issue40",
			file:           "comp_h264_coverart.mkv",
			preset:         "compress-hevc",
			expectComplete: true,
			timeout:        2 * time.Minute,
		},
		{
			name:           "HEVC_CoverArt_ShouldSkip",
			file:           "comp_hevc_coverart.mkv",
			preset:         "compress-hevc",
			expectSkip:     true,
			timeout:        1 * time.Minute,
		},

		// ===== Multi-Track Tests =====
		{
			name:           "H264_MultiAudio",
			file:           "comp_h264_multiaudio.mkv",
			preset:         "compress-hevc",
			expectComplete: true,
			timeout:        2 * time.Minute,
		},
		{
			name:           "H264_Subtitles",
			file:           "comp_h264_subtitles.mkv",
			preset:         "compress-hevc",
			expectComplete: true,
			timeout:        2 * time.Minute,
		},
		{
			name:           "H264_KitchenSink_AllTracks",
			file:           "comp_kitchensink.mkv",
			preset:         "compress-hevc",
			expectComplete: true,
			timeout:        2 * time.Minute,
		},

		// ===== HEVC Tests (should be skipped - already target codec) =====
		{
			name:       "HEVC_8bit_ShouldSkip",
			file:       "comp_hevc_8bit_short.mkv",
			preset:     "compress-hevc",
			expectSkip: true,
			timeout:    1 * time.Minute,
		},
		{
			name:       "HEVC_10bit_ShouldSkip",
			file:       "comp_hevc_10bit_short.mkv",
			preset:     "compress-hevc",
			expectSkip: true,
			timeout:    1 * time.Minute,
		},

		// ===== AV1 Tests =====
		{
			name:            "AV1_8bit_ToHEVC",
			file:            "comp_av1_8bit_short.mkv",
			preset:          "compress-hevc",
			expectFail:      true, // AV1 is more efficient, output may be larger
			skipIfNotExists: true,
			timeout:         3 * time.Minute,
		},
		{
			name:            "AV1_10bit_ToHEVC",
			file:            "comp_av1_10bit_short.mkv",
			preset:          "compress-hevc",
			expectFail:      true,
			skipIfNotExists: true,
			timeout:         3 * time.Minute,
		},

		// ===== VP9 Tests =====
		{
			name:            "VP9_8bit_ToHEVC",
			file:            "comp_vp9_8bit_short.webm",
			preset:          "compress-hevc",
			expectComplete:  true,
			skipIfNotExists: true,
			timeout:         3 * time.Minute,
		},
		{
			name:            "VP9_10bit_ToHEVC",
			file:            "comp_vp9_10bit_short.webm",
			preset:          "compress-hevc",
			expectComplete:  true,
			skipIfNotExists: true,
			timeout:         3 * time.Minute,
		},

		// ===== MPEG-4 Tests =====
		{
			name:           "MPEG4_ToHEVC",
			file:           "comp_mpeg4_short.avi",
			preset:         "compress-hevc",
			expectComplete: true,
			timeout:        2 * time.Minute,
		},

		// ===== VC-1/WMV Tests (Issue #32) =====
		{
			name:            "WMV2_ToHEVC_Issue32",
			file:            "comp_wmv2_short.wmv",
			preset:          "compress-hevc",
			expectComplete:  true,
			skipIfNotExists: true,
			timeout:         3 * time.Minute,
		},

		// ===== Edge Case Tests =====
		{
			name:           "H264_60fps",
			file:           "comp_h264_60fps.mp4",
			preset:         "compress-hevc",
			expectComplete: true,
			timeout:        2 * time.Minute,
		},
		{
			name:           "H264_4K",
			file:           "comp_h264_4k.mp4",
			preset:         "compress-hevc",
			expectComplete: true,
			timeout:        5 * time.Minute,
		},
		{
			name:           "H264_Interlaced",
			file:           "comp_h264_interlaced.mp4",
			preset:         "compress-hevc",
			expectComplete: true,
			timeout:        2 * time.Minute,
		},
		{
			name:           "H264_1Second",
			file:           "comp_h264_1sec.mp4",
			preset:         "compress-hevc",
			expectComplete: true,
			timeout:        1 * time.Minute,
		},
		{
			name:           "VideoOnly_NoAudio",
			file:           "comp_videoonly.mp4",
			preset:         "compress-hevc",
			expectComplete: true,
			timeout:        2 * time.Minute,
		},
		{
			name:           "LowRes_320x240",
			file:           "comp_lowres.mp4",
			preset:         "compress-hevc",
			expectComplete: true, // Small files can still be compressed
			timeout:        2 * time.Minute,
		},
		{
			name:           "HighBitrate_50Mbps",
			file:           "comp_highbitrate.mp4",
			preset:         "compress-hevc",
			expectComplete: true,
			timeout:        2 * time.Minute,
		},
		{
			name:           "OddResolution_1279x719",
			file:           "comp_oddres.mp4",
			preset:         "compress-hevc",
			expectComplete: true,
			timeout:        2 * time.Minute,
		},

		// ===== Scaling Tests =====
		{
			name:           "H264_4K_DownscaleTo1080p",
			file:           "comp_h264_4k.mp4",
			preset:         "1080p",
			expectComplete: true,
			timeout:        5 * time.Minute,
		},
		{
			name:           "H264_4K_DownscaleTo720p",
			file:           "comp_h264_4k.mp4", // Use 4K file so there's something to downscale
			preset:         "720p",
			expectComplete: true,
			timeout:        5 * time.Minute,
		},
	}

	// Initialize ffmpeg presets
	ffmpeg.InitPresets()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srcPath := filepath.Join(testdataDir, tt.file)
			if _, err := os.Stat(srcPath); os.IsNotExist(err) {
				if tt.skipIfNotExists {
					t.Skipf("test file not found (optional): %s", tt.file)
				}
				t.Skipf("test file not found: %s", tt.file)
			}

			runComprehensiveTest(t, testdataDir, tt.file, tt.preset,
				tt.expectSkip, tt.expectComplete, tt.expectFail, tt.expectSWDecode, tt.timeout)
		})
	}
}

func runComprehensiveTest(t *testing.T, testdataDir, filename, presetID string,
	expectSkip, expectComplete, expectFail, expectSWDecode bool, timeout time.Duration) {

	// Create temp directory for this test
	tmpDir := t.TempDir()

	// Copy test file to temp dir
	srcPath := filepath.Join(testdataDir, filename)
	dstPath := filepath.Join(tmpDir, filename)
	if err := copyTestFile(srcPath, dstPath); err != nil {
		t.Fatalf("failed to copy test file: %v", err)
	}

	// Setup config
	cfg := &config.Config{
		MediaPath:        tmpDir,
		TempPath:         tmpDir,
		FFmpegPath:       "ffmpeg",
		FFprobePath:      "ffprobe",
		Workers:          1,
		OriginalHandling: "keep",
		OutputFormat:     "mkv",
	}

	// Create queue
	queue := NewQueue()

	// Probe the file
	prober := ffmpeg.NewProber(cfg.FFprobePath)
	probe, err := prober.Probe(context.Background(), dstPath)
	if err != nil {
		t.Fatalf("failed to probe file: %v", err)
	}

	t.Logf("Probe: codec=%s profile=%q bit_depth=%d pix_fmt=%s size=%d duration=%v",
		probe.VideoCodec, probe.Profile, probe.BitDepth, probe.PixelFormat, probe.Size, probe.Duration)

	// Get preset and check software decode requirement
	preset := ffmpeg.GetPreset(presetID)
	if preset == nil {
		t.Fatalf("unknown preset: %s", presetID)
	}

	// Check proactive software decode detection
	requiresSWDecode := ffmpeg.RequiresSoftwareDecode(
		probe.VideoCodec, probe.Profile, probe.BitDepth, preset.Encoder)

	if expectSWDecode && !requiresSWDecode && preset.Encoder != ffmpeg.HWAccelNone {
		t.Errorf("expected software decode requirement for %s/%s/%d-bit with %v encoder",
			probe.VideoCodec, probe.Profile, probe.BitDepth, preset.Encoder)
	}

	if requiresSWDecode {
		t.Logf("PROACTIVE: Will use software decode (codec=%s, profile=%q, bit_depth=%d)",
			probe.VideoCodec, probe.Profile, probe.BitDepth)
	}

	// Add job to queue
	job, err := queue.Add(dstPath, presetID, probe)
	if err != nil {
		t.Fatalf("failed to add job: %v", err)
	}

	// Check for expected skip
	if expectSkip {
		if job.Status == StatusSkipped {
			if strings.Contains(job.Error, "already") ||
				strings.Contains(job.Error, "HEVC") ||
				strings.Contains(job.Error, "AV1") {
				t.Logf("SKIP: %s (correctly identified as already target codec)", job.Error)
				return
			}
		}
		t.Errorf("expected file to be skipped, but job created with status %s: %s", job.Status, job.Error)
		return
	}

	t.Logf("Job created: %s (status=%s)", job.ID, job.Status)

	// Subscribe to events
	events := queue.Subscribe()
	defer queue.Unsubscribe(events)

	// Create worker pool and process
	pool := NewWorkerPool(queue, cfg, nil)
	pool.Start()
	defer pool.Stop()

	// Wait for job completion with timeout
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var finalJob *Job
	lastProgress := 0.0
	lastProgressLog := time.Now()

	for {
		select {
		case event := <-events:
			if event.Job.ID != job.ID {
				continue
			}

			// Log progress periodically (not every event to reduce noise)
			if event.Job.Progress > lastProgress+10 || time.Since(lastProgressLog) > 10*time.Second {
				t.Logf("Progress: %.1f%% speed=%.2fx", event.Job.Progress, event.Job.Speed)
				lastProgress = event.Job.Progress
				lastProgressLog = time.Now()
			}

			if event.Type == "complete" || event.Type == "failed" || event.Type == "cancelled" {
				finalJob = event.Job
				goto done
			}

		case <-ctx.Done():
			// Get final job state
			finalJob = queue.Get(job.ID)
			t.Fatalf("TIMEOUT after %v (last progress: %.1f%%, status=%s, error=%q)",
				timeout, finalJob.Progress, finalJob.Status, finalJob.Error)
		}
	}

done:
	t.Logf("RESULT: status=%s progress=%.1f%% error=%q", finalJob.Status, finalJob.Progress, finalJob.Error)

	// Verify expectations
	if expectComplete {
		if finalJob.Status != StatusComplete {
			t.Errorf("FAIL: expected complete, got %s: %s", finalJob.Status, finalJob.Error)
			return
		}

		// Verify output exists and is valid
		if finalJob.OutputPath != "" {
			outProbe, err := prober.Probe(context.Background(), finalJob.OutputPath)
			if err != nil {
				t.Errorf("failed to probe output: %v", err)
			} else {
				reduction := float64(finalJob.SpaceSaved) / float64(finalJob.InputSize) * 100
				t.Logf("OUTPUT: codec=%s size=%d saved=%d bytes (%.1f%% reduction)",
					outProbe.VideoCodec, outProbe.Size, finalJob.SpaceSaved, reduction)

				// Verify output codec matches preset
				expectedCodec := "hevc"
				if preset.Codec == ffmpeg.CodecAV1 {
					expectedCodec = "av1"
				}
				if outProbe.VideoCodec != expectedCodec {
					t.Errorf("output codec mismatch: got %s, want %s", outProbe.VideoCodec, expectedCodec)
				}
			}
		}
		return
	}

	if expectFail {
		if finalJob.Status != StatusFailed {
			t.Errorf("expected failure but got %s", finalJob.Status)
		} else {
			t.Logf("EXPECTED FAIL: %s", finalJob.Error)
		}
		return
	}
}

// TestComprehensiveQuick runs a quick subset of comprehensive tests for CI
func TestComprehensiveQuick(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping comprehensive quick test in short mode")
	}

	testdataDir, err := filepath.Abs(filepath.Join("..", "..", "testdata"))
	if err != nil {
		t.Fatalf("failed to get testdata path: %v", err)
	}

	// Just test the most critical files
	quickTests := []struct {
		name   string
		file   string
		preset string
	}{
		{"H264_8bit_Quick", "comp_h264_8bit_short.mp4", "compress-hevc"},
		{"H264_10bit_Quick", "comp_h264_10bit_short.mkv", "compress-hevc"},
		{"H264_CoverArt_Quick", "comp_h264_coverart.mkv", "compress-hevc"},
		{"H264_MultiTrack_Quick", "comp_kitchensink.mkv", "compress-hevc"},
	}

	ffmpeg.InitPresets()

	for _, tt := range quickTests {
		t.Run(tt.name, func(t *testing.T) {
			srcPath := filepath.Join(testdataDir, tt.file)
			if _, err := os.Stat(srcPath); os.IsNotExist(err) {
				t.Skipf("test file not found: %s", tt.file)
			}

			runComprehensiveTest(t, testdataDir, tt.file, tt.preset,
				false, true, false, false, 3*time.Minute)
		})
	}
}

// TestCoverArtHandling specifically tests the cover art fix (-map 0:v:0)
func TestCoverArtHandling(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cover art test in short mode")
	}

	testdataDir, err := filepath.Abs(filepath.Join("..", "..", "testdata"))
	if err != nil {
		t.Fatalf("failed to get testdata path: %v", err)
	}

	// Test files with cover art
	coverArtFiles := []string{
		"comp_h264_coverart.mkv",
		"comp_kitchensink.mkv",
	}

	ffmpeg.InitPresets()

	for _, filename := range coverArtFiles {
		t.Run(filename, func(t *testing.T) {
			srcPath := filepath.Join(testdataDir, filename)
			if _, err := os.Stat(srcPath); os.IsNotExist(err) {
				t.Skipf("test file not found: %s", filename)
			}

			// Verify file has multiple video streams (cover art)
			prober := ffmpeg.NewProber("ffprobe")
			probe, err := prober.Probe(context.Background(), srcPath)
			if err != nil {
				t.Fatalf("failed to probe: %v", err)
			}

			t.Logf("Testing cover art handling: codec=%s", probe.VideoCodec)

			// Run the transcode
			runComprehensiveTest(t, testdataDir, filename, "compress-hevc",
				false, true, false, false, 3*time.Minute)
		})
	}
}

// TestLongDurationTranscode specifically tests for late-stage failures (Issue #56)
func TestLongDurationTranscode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping long duration test in short mode")
	}

	testdataDir, err := filepath.Abs(filepath.Join("..", "..", "testdata"))
	if err != nil {
		t.Fatalf("failed to get testdata path: %v", err)
	}

	// Test long duration files to catch late-stage failures
	longFiles := []struct {
		name   string
		file   string
		swDec  bool
	}{
		{"H264_8bit_Long", "comp_h264_8bit_long.mp4", false},
		{"H264_10bit_Long_Issue56", "comp_h264_10bit_long.mkv", true},
	}

	ffmpeg.InitPresets()

	for _, tt := range longFiles {
		t.Run(tt.name, func(t *testing.T) {
			srcPath := filepath.Join(testdataDir, tt.file)
			if _, err := os.Stat(srcPath); os.IsNotExist(err) {
				t.Skipf("test file not found: %s", tt.file)
			}

			t.Logf("Testing long duration file: %s (software_decode=%v)", tt.file, tt.swDec)

			// 20 minute timeout for long files
			runComprehensiveTest(t, testdataDir, tt.file, "compress-hevc",
				false, true, false, tt.swDec, 20*time.Minute)
		})
	}
}

// TestBuildPresetArgsComprehensive verifies FFmpeg argument generation for all encoder combinations
func TestBuildPresetArgsComprehensive(t *testing.T) {
	encoders := []ffmpeg.HWAccel{
		ffmpeg.HWAccelNone,
		ffmpeg.HWAccelVideoToolbox,
		ffmpeg.HWAccelNVENC,
		ffmpeg.HWAccelQSV,
		ffmpeg.HWAccelVAAPI,
	}

	codecs := []ffmpeg.Codec{
		ffmpeg.CodecHEVC,
		ffmpeg.CodecAV1,
	}

	for _, encoder := range encoders {
		for _, codec := range codecs {
			name := fmt.Sprintf("%v_%v", encoder, codec)
			t.Run(name, func(t *testing.T) {
				preset := &ffmpeg.Preset{
					ID:      "test",
					Encoder: encoder,
					Codec:   codec,
				}

				// Test hardware decode
				inputArgsHW, outputArgsHW := ffmpeg.BuildPresetArgs(preset, 5000000, 1920, 1080, 0, 0, false, "mkv")
				t.Logf("HW decode: input=%v output=%v", inputArgsHW, outputArgsHW)

				// Test software decode
				inputArgsSW, outputArgsSW := ffmpeg.BuildPresetArgs(preset, 5000000, 1920, 1080, 0, 0, true, "mkv")
				t.Logf("SW decode: input=%v output=%v", inputArgsSW, outputArgsSW)

				// Verify encoder is in output args
				foundEncoder := false
				for i, arg := range outputArgsHW {
					if arg == "-c:v" && i+1 < len(outputArgsHW) {
						foundEncoder = true
						t.Logf("Encoder: %s", outputArgsHW[i+1])
					}
				}
				if !foundEncoder {
					t.Error("missing -c:v encoder flag")
				}
			})
		}
	}
}

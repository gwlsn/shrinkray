package ffmpeg

import (
	"context"
	"os"
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

	// Skip if test file doesn't exist
	if _, err := os.Stat(testFile); os.IsNotExist(err) {
		t.Skipf("test file not found: %s", testFile)
	}

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

// TestInferBitDepth verifies bit depth inference from pixel format strings
func TestInferBitDepth(t *testing.T) {
	tests := []struct {
		pixFmt   string
		expected int
	}{
		// 8-bit formats
		{"yuv420p", 8},
		{"yuv422p", 8},
		{"yuv444p", 8},
		{"nv12", 8},
		{"rgb24", 8},
		{"bgr24", 8},

		// 10-bit formats
		{"yuv420p10le", 10},
		{"yuv420p10be", 10},
		{"yuv422p10le", 10},
		{"yuv444p10le", 10},
		{"p010le", 10},
		{"p010be", 10},

		// 12-bit formats
		{"yuv420p12le", 12},
		{"yuv420p12be", 12},
		{"yuv422p12le", 12},
		{"yuv444p12le", 12},

		// Edge cases
		{"", 8},           // Empty defaults to 8
		{"unknown", 8},    // Unknown defaults to 8
		{"p016le", 8},     // 16-bit not explicitly matched, defaults to 8
		{"gbrp10le", 10},  // RGB 10-bit
	}

	for _, tt := range tests {
		t.Run(tt.pixFmt, func(t *testing.T) {
			result := inferBitDepth(tt.pixFmt)
			if result != tt.expected {
				t.Errorf("inferBitDepth(%q) = %d, want %d", tt.pixFmt, result, tt.expected)
			}
		})
	}
}

// TestProbe_BitDepthDetection tests that probing extracts correct profile and bit depth
func TestProbe_BitDepthDetection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping probe integration test in short mode")
	}

	testdataDir := getTestdataPath()

	tests := []struct {
		file           string
		expectCodec    string
		expectProfile  string
		expectBitDepth int
		expectPixFmt   string
	}{
		{"test_h264_8bit_high.mp4", "h264", "Constrained Baseline", 8, "yuv420p"},
		{"test_h264_10bit_high10.mkv", "h264", "High 10", 10, "yuv420p10le"},
		{"test_hevc_8bit_main.mkv", "hevc", "Main", 8, "yuv420p"},
		{"test_hevc_10bit_main10.mkv", "hevc", "Main 10", 10, "yuv420p10le"},
	}

	prober := NewProber("ffprobe")
	ctx := context.Background()

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			path := filepath.Join(testdataDir, tt.file)
			if _, err := os.Stat(path); os.IsNotExist(err) {
				t.Skipf("test file not found: %s (run testdata/generate_test_vectors.sh)", tt.file)
			}

			result, err := prober.Probe(ctx, path)
			if err != nil {
				t.Fatalf("probe failed: %v", err)
			}

			if result.VideoCodec != tt.expectCodec {
				t.Errorf("codec = %q, want %q", result.VideoCodec, tt.expectCodec)
			}
			if result.Profile != tt.expectProfile {
				t.Errorf("profile = %q, want %q", result.Profile, tt.expectProfile)
			}
			if result.BitDepth != tt.expectBitDepth {
				t.Errorf("bit_depth = %d, want %d", result.BitDepth, tt.expectBitDepth)
			}
			if result.PixelFormat != tt.expectPixFmt {
				t.Errorf("pix_fmt = %q, want %q", result.PixelFormat, tt.expectPixFmt)
			}

			t.Logf("Probe result: codec=%s profile=%s bit_depth=%d pix_fmt=%s",
				result.VideoCodec, result.Profile, result.BitDepth, result.PixelFormat)
		})
	}
}

// TestIsAV1Codec verifies AV1 codec detection
func TestIsAV1Codec(t *testing.T) {
	tests := []struct {
		codec    string
		expected bool
	}{
		{"av1", true},
		{"AV1", true},
		{"libaom-av1", true},
		{"libsvtav1", true},
		{"h264", false},
		{"hevc", false},
		{"vp9", false},
	}

	for _, tt := range tests {
		result := isAV1Codec(tt.codec)
		if result != tt.expected {
			t.Errorf("isAV1Codec(%s) = %v, expected %v", tt.codec, result, tt.expected)
		}
	}
}

// TestProbe_HDRDetection verifies HDR detection on real test files
func TestProbe_HDRDetection(t *testing.T) {
	testdata := getTestdataPath()
	prober := NewProber("ffprobe")

	tests := []struct {
		filename  string
		expectHDR bool
		hdrType   string // "HDR10", "HLG", "fallback", or ""
	}{
		// HDR10 files (smpte2084 PQ transfer)
		{"comp_h264_hdr10_short.mkv", true, "HDR10"},
		{"comp_hevc_hdr10_short.mkv", true, "HDR10"},
		{"comp_hevc_hdr10_4k.mkv", true, "HDR10"},
		{"comp_hevc_hdr10_coverart.mkv", true, "HDR10"},
		{"comp_vp9_hdr10_short.mkv", true, "HDR10"},
		{"comp_av1_hdr10_short.mkv", true, "HDR10"},

		// HLG file (arib-std-b67 transfer)
		{"comp_hevc_hlg_short.mkv", true, "HLG"},

		// Fallback detection (10-bit bt2020, no explicit transfer)
		{"comp_h264_bt2020_notransfer.mkv", true, "fallback"},

		// SDR files (should NOT be detected as HDR)
		{"comp_h264_8bit_short.mp4", false, ""},
		{"comp_hevc_8bit_short.mkv", false, ""},
		{"comp_hevc_10bit_short.mkv", false, ""}, // 10-bit but NOT bt2020
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			testFile := filepath.Join(testdata, tt.filename)

			if _, err := os.Stat(testFile); os.IsNotExist(err) {
				t.Skipf("test file not found: %s (run testdata/generate_comprehensive_vectors.sh)", tt.filename)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			result, err := prober.Probe(ctx, testFile)
			if err != nil {
				t.Fatalf("Probe failed: %v", err)
			}

			t.Logf("Probe result: codec=%s transfer=%s primaries=%s bit_depth=%d is_hdr=%v",
				result.VideoCodec, result.ColorTransfer, result.ColorPrimaries, result.BitDepth, result.IsHDR)

			if result.IsHDR != tt.expectHDR {
				t.Errorf("expected IsHDR=%v, got %v (transfer=%s primaries=%s bit_depth=%d)",
					tt.expectHDR, result.IsHDR, result.ColorTransfer, result.ColorPrimaries, result.BitDepth)
			}

			// Verify HDR type detection
			if tt.expectHDR && tt.hdrType != "" {
				switch tt.hdrType {
				case "HDR10":
					if result.ColorTransfer != "smpte2084" {
						t.Errorf("expected HDR10 (smpte2084), got transfer=%s", result.ColorTransfer)
					}
				case "HLG":
					if result.ColorTransfer != "arib-std-b67" {
						t.Errorf("expected HLG (arib-std-b67), got transfer=%s", result.ColorTransfer)
					}
				case "fallback":
					// Fallback: no explicit transfer but bt2020 + 10-bit
					if result.ColorPrimaries != "bt2020" || result.BitDepth < 10 {
						t.Errorf("expected fallback HDR (bt2020 10-bit), got primaries=%s bit_depth=%d",
							result.ColorPrimaries, result.BitDepth)
					}
				}
			}
		})
	}
}

// TestDetectHDR verifies HDR detection based on color metadata
func TestDetectHDR(t *testing.T) {
	tests := []struct {
		name           string
		colorTransfer  string
		colorPrimaries string
		bitDepth       int
		expected       bool
	}{
		// HDR10 (PQ transfer function)
		{"HDR10 with smpte2084", "smpte2084", "bt2020", 10, true},
		{"HDR10 uppercase", "SMPTE2084", "bt2020", 10, true},
		{"HDR10 with 8-bit still HDR", "smpte2084", "bt2020", 8, true},

		// HLG
		{"HLG with arib-std-b67", "arib-std-b67", "bt2020", 10, true},
		{"HLG uppercase", "ARIB-STD-B67", "bt2020", 10, true},

		// Fallback heuristic: 10-bit + bt2020 without explicit transfer
		{"Fallback: 10-bit bt2020 no transfer", "", "bt2020", 10, true},
		{"Fallback: 10-bit BT2020 uppercase", "", "BT2020", 10, true},

		// SDR content
		{"SDR with bt709", "bt709", "bt709", 8, false},
		{"SDR 10-bit without bt2020", "", "bt709", 10, false},
		{"SDR 8-bit with bt2020 primaries", "", "bt2020", 8, false},
		{"SDR empty metadata", "", "", 8, false},
		{"SDR with srgb", "iec61966-2-1", "bt709", 8, false},

		// Edge cases
		{"Unknown transfer with bt2020 8-bit", "unknown", "bt2020", 8, false},
		{"bt2020 primaries but bt709 transfer", "bt709", "bt2020", 10, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detectHDR(tt.colorTransfer, tt.colorPrimaries, tt.bitDepth)
			if result != tt.expected {
				t.Errorf("detectHDR(%q, %q, %d) = %v, want %v",
					tt.colorTransfer, tt.colorPrimaries, tt.bitDepth, result, tt.expected)
			}
		})
	}
}

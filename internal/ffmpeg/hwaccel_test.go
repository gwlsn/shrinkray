package ffmpeg

import (
	"testing"
)

// TestRequiresSoftwareDecode verifies the proactive codec detection function
// correctly identifies all known hardware decode limitations.
func TestRequiresSoftwareDecode(t *testing.T) {
	tests := []struct {
		name     string
		codec    string
		profile  string
		bitDepth int
		encoder  HWAccel
		expected bool
	}{
		// === H.264 10-bit - Universal hardware limitation ===
		// NO GPU on any platform supports H.264 10-bit (High 10 profile) decode
		{"H264_10bit_QSV", "h264", "High 10", 10, HWAccelQSV, true},
		{"H264_10bit_VAAPI", "h264", "High 10", 10, HWAccelVAAPI, true},
		{"H264_10bit_NVENC", "h264", "High 10", 10, HWAccelNVENC, true},
		{"H264_10bit_VideoToolbox", "h264", "High 10", 10, HWAccelVideoToolbox, true},
		{"AVC_10bit_QSV", "avc", "High 10", 10, HWAccelQSV, true},
		{"AVC_10bit_VAAPI", "avc", "High 10", 10, HWAccelVAAPI, true},

		// H.264 10-bit with various bit depths >= 10
		{"H264_12bit", "h264", "High 10", 12, HWAccelQSV, true},

		// === H.264 8-bit - Hardware decode supported ===
		{"H264_8bit_High_QSV", "h264", "High", 8, HWAccelQSV, false},
		{"H264_8bit_High_VAAPI", "h264", "High", 8, HWAccelVAAPI, false},
		{"H264_8bit_High_NVENC", "h264", "High", 8, HWAccelNVENC, false},
		{"H264_8bit_High_VideoToolbox", "h264", "High", 8, HWAccelVideoToolbox, false},
		{"H264_8bit_Main", "h264", "Main", 8, HWAccelQSV, false},
		{"H264_8bit_Baseline", "h264", "Baseline", 8, HWAccelQSV, false},

		// === HEVC - All bit depths hardware supported ===
		{"HEVC_8bit_Main_QSV", "hevc", "Main", 8, HWAccelQSV, false},
		{"HEVC_10bit_Main10_QSV", "hevc", "Main 10", 10, HWAccelQSV, false},
		{"HEVC_8bit_Main_VAAPI", "hevc", "Main", 8, HWAccelVAAPI, false},
		{"HEVC_10bit_Main10_VAAPI", "hevc", "Main 10", 10, HWAccelVAAPI, false},
		{"HEVC_8bit_Main_NVENC", "hevc", "Main", 8, HWAccelNVENC, false},
		{"HEVC_10bit_Main10_NVENC", "hevc", "Main 10", 10, HWAccelNVENC, false},
		{"HEVC_12bit", "hevc", "Main 12", 12, HWAccelQSV, false},

		// === AV1 - Hardware supported ===
		{"AV1_8bit_QSV", "av1", "Main", 8, HWAccelQSV, false},
		{"AV1_10bit_QSV", "av1", "Main", 10, HWAccelQSV, false},
		{"AV1_8bit_VAAPI", "av1", "Main", 8, HWAccelVAAPI, false},

		// === VP9 - Hardware supported ===
		{"VP9_8bit_QSV", "vp9", "Profile 0", 8, HWAccelQSV, false},
		{"VP9_10bit_QSV", "vp9", "Profile 2", 10, HWAccelQSV, false},

		// === VC-1/WMV - Spotty hardware support ===
		{"VC1_QSV", "vc1", "", 8, HWAccelQSV, true},
		{"VC1_VAAPI", "vc1", "", 8, HWAccelVAAPI, true},
		{"VC1_NVENC", "vc1", "", 8, HWAccelNVENC, true},
		{"WMV3_QSV", "wmv3", "", 8, HWAccelQSV, true},
		{"WMV3_VAAPI", "wmv3", "", 8, HWAccelVAAPI, true},

		// === MPEG-4 ===
		// Advanced Simple Profile not supported on QSV
		{"MPEG4_ASP_QSV", "mpeg4", "Advanced Simple", 8, HWAccelQSV, true},
		// Simple Profile is OK (ffprobe returns "Simple Profile", not just "Simple")
		{"MPEG4_Simple_QSV", "mpeg4", "Simple", 8, HWAccelQSV, false},
		{"MPEG4_SimpleProfile_QSV", "mpeg4", "Simple Profile", 8, HWAccelQSV, false},
		{"MPEG4_Simple_VAAPI", "mpeg4", "Simple", 8, HWAccelVAAPI, false},
		{"MPEG4_SimpleProfile_VAAPI", "mpeg4", "Simple Profile", 8, HWAccelVAAPI, false},
		{"MPEG4_Simple_NVENC", "mpeg4", "Simple", 8, HWAccelNVENC, false},

		// === Software encoder - No fallback needed ===
		{"H264_10bit_Software", "h264", "High 10", 10, HWAccelNone, false},
		{"VC1_Software", "vc1", "", 8, HWAccelNone, false},
		{"WMV3_Software", "wmv3", "", 8, HWAccelNone, false},
		{"MPEG4_ASP_Software", "mpeg4", "Advanced Simple", 8, HWAccelNone, false},

		// === Case insensitivity ===
		{"H264_Uppercase", "H264", "High 10", 10, HWAccelQSV, true},
		{"AVC_Uppercase", "AVC", "HIGH 10", 10, HWAccelVAAPI, true},
		{"VC1_Uppercase", "VC1", "", 8, HWAccelQSV, true},
		{"HEVC_Uppercase", "HEVC", "Main", 8, HWAccelQSV, false},

		// === Edge cases ===
		{"EmptyCodec", "", "", 8, HWAccelQSV, false},
		{"EmptyProfile", "h264", "", 10, HWAccelQSV, true}, // Still 10-bit
		{"ZeroBitDepth", "h264", "High 10", 0, HWAccelQSV, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RequiresSoftwareDecode(tt.codec, tt.profile, tt.bitDepth, tt.encoder)
			if result != tt.expected {
				t.Errorf("RequiresSoftwareDecode(%q, %q, %d, %v) = %v, want %v",
					tt.codec, tt.profile, tt.bitDepth, tt.encoder, result, tt.expected)
			}
		})
	}
}

// TestRequiresSoftwareDecode_AllEncoders ensures each encoder type is tested
func TestRequiresSoftwareDecode_AllEncoders(t *testing.T) {
	encoders := []HWAccel{
		HWAccelNone,
		HWAccelVideoToolbox,
		HWAccelNVENC,
		HWAccelQSV,
		HWAccelVAAPI,
	}

	// H.264 10-bit should require software decode on ALL hardware encoders
	for _, enc := range encoders {
		result := RequiresSoftwareDecode("h264", "High 10", 10, enc)
		if enc == HWAccelNone {
			// Software encoder doesn't need fallback
			if result {
				t.Errorf("HWAccelNone should not require software decode, got true")
			}
		} else {
			// All hardware encoders should require software decode
			if !result {
				t.Errorf("%v should require software decode for H.264 10-bit, got false", enc)
			}
		}
	}
}

// TestHWAccelConstants verifies the HWAccel constants are distinct
func TestHWAccelConstants(t *testing.T) {
	accels := map[HWAccel]string{
		HWAccelNone:         "none",
		HWAccelVideoToolbox: "videotoolbox",
		HWAccelNVENC:        "nvenc",
		HWAccelQSV:          "qsv",
		HWAccelVAAPI:        "vaapi",
	}

	// Verify each maps to expected string
	for accel, expected := range accels {
		if string(accel) != expected {
			t.Errorf("HWAccel constant %v should be %q, got %q", accel, expected, string(accel))
		}
	}

	// Verify all are distinct
	seen := make(map[HWAccel]bool)
	for accel := range accels {
		if seen[accel] {
			t.Errorf("duplicate HWAccel constant: %v", accel)
		}
		seen[accel] = true
	}
}

// TestCodecConstants verifies the Codec constants are distinct
func TestCodecConstants(t *testing.T) {
	codecs := map[Codec]string{
		CodecHEVC: "hevc",
		CodecAV1:  "av1",
	}

	for codec, expected := range codecs {
		if string(codec) != expected {
			t.Errorf("Codec constant %v should be %q, got %q", codec, expected, string(codec))
		}
	}
}

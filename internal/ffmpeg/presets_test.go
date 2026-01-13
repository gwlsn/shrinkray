package ffmpeg

import (
	"strings"
	"testing"
)

func TestBuildPresetArgsDynamicBitrate(t *testing.T) {
	// Test that VideoToolbox presets calculate dynamic bitrate correctly

	// Source bitrate: 3481000 bits/s (3481 kbps)
	sourceBitrate := int64(3481000)

	// Create a VideoToolbox preset (0.35 modifier for HEVC)
	preset := &Preset{
		ID:      "test-hevc",
		Encoder: HWAccelVideoToolbox,
		Codec:   CodecHEVC,
	}

	inputArgs, outputArgs := BuildPresetArgs(preset, sourceBitrate, 0, 0, 0, 0, false, "mkv")

	// Should have hwaccel input args
	if len(inputArgs) == 0 {
		t.Error("expected hwaccel input args for VideoToolbox")
	}
	t.Logf("Input args: %v", inputArgs)

	// Should contain -b:v with calculated bitrate
	// Expected: 3481 * 0.35 = ~1218k
	found := false
	for i, arg := range outputArgs {
		if arg == "-b:v" && i+1 < len(outputArgs) {
			found = true
			bitrate := outputArgs[i+1]
			if !strings.HasSuffix(bitrate, "k") {
				t.Errorf("expected bitrate to end in 'k', got %s", bitrate)
			}
			t.Logf("HEVC VideoToolbox: source=%dkbps → target=%s", sourceBitrate/1000, bitrate)

			// Should be around 1218k (3481 * 0.35)
			if bitrate != "1218k" {
				t.Errorf("expected ~1218k, got %s", bitrate)
			}
		}
	}
	if !found {
		t.Error("expected to find -b:v flag in args")
	}
}

func TestBuildPresetArgsDynamicBitrateAV1(t *testing.T) {
	sourceBitrate := int64(3481000)

	// Create a VideoToolbox AV1 preset (0.25 modifier - more aggressive for AV1)
	preset := &Preset{
		ID:      "test-av1",
		Encoder: HWAccelVideoToolbox,
		Codec:   CodecAV1,
	}

	inputArgs, outputArgs := BuildPresetArgs(preset, sourceBitrate, 0, 0, 0, 0, false, "mkv")

	// Should have hwaccel input args
	if len(inputArgs) == 0 {
		t.Error("expected hwaccel input args for VideoToolbox")
	}
	t.Logf("Input args: %v", inputArgs)

	// Expected: 3481 * 0.25 = ~870k
	for i, arg := range outputArgs {
		if arg == "-b:v" && i+1 < len(outputArgs) {
			bitrate := outputArgs[i+1]
			t.Logf("AV1 VideoToolbox: source=%dkbps → target=%s", sourceBitrate/1000, bitrate)

			if bitrate != "870k" {
				t.Errorf("expected ~870k, got %s", bitrate)
			}
		}
	}
}

func TestBuildPresetArgsBitrateConstraints(t *testing.T) {
	// Test min/max bitrate constraints

	// Very low source bitrate (should hit minimum)
	// 500 kbps * 0.35 = 175k, should clamp to 500k
	lowBitrate := int64(500000)
	presetLow := &Preset{
		ID:      "test-low",
		Encoder: HWAccelVideoToolbox,
		Codec:   CodecHEVC,
	}

	_, outputArgs := BuildPresetArgs(presetLow, lowBitrate, 0, 0, 0, 0, false, "mkv")
	for i, arg := range outputArgs {
		if arg == "-b:v" && i+1 < len(outputArgs) {
			bitrate := outputArgs[i+1]
			t.Logf("Low bitrate source: %dkbps → target=%s", lowBitrate/1000, bitrate)

			if bitrate != "500k" {
				t.Errorf("expected min 500k, got %s", bitrate)
			}
		}
	}

	// Very high source bitrate (should hit maximum)
	// 50000 kbps * 0.35 = 17500k, should clamp to 15000k
	highBitrate := int64(50000000)
	presetHigh := &Preset{
		ID:      "test-high",
		Encoder: HWAccelVideoToolbox,
		Codec:   CodecHEVC,
	}

	_, outputArgs = BuildPresetArgs(presetHigh, highBitrate, 0, 0, 0, 0, false, "mkv")
	for i, arg := range outputArgs {
		if arg == "-b:v" && i+1 < len(outputArgs) {
			bitrate := outputArgs[i+1]
			t.Logf("High bitrate source: %dkbps → target=%s", highBitrate/1000, bitrate)

			if bitrate != "15000k" {
				t.Errorf("expected max 15000k, got %s", bitrate)
			}
		}
	}
}

func TestBuildPresetArgsNonBitrateEncoder(t *testing.T) {
	// Test that non-bitrate encoders (like software x265) don't use dynamic calculation
	sourceBitrate := int64(3481000)

	presetSoftware := &Preset{
		ID:      "test-software",
		Encoder: HWAccelNone,
		Codec:   CodecHEVC,
	}

	inputArgs, outputArgs := BuildPresetArgs(presetSoftware, sourceBitrate, 0, 0, 0, 0, false, "mkv")

	// Software encoder should have no hwaccel input args
	if len(inputArgs) != 0 {
		t.Errorf("expected no hwaccel input args for software encoder, got %v", inputArgs)
	}

	// Should use -crf not -b:v
	foundCRF := false
	foundBv := false
	for i, arg := range outputArgs {
		if arg == "-crf" {
			foundCRF = true
			// Verify CRF value is 26
			if i+1 < len(outputArgs) && outputArgs[i+1] != "26" {
				t.Errorf("expected CRF 26, got %s", outputArgs[i+1])
			}
		}
		if arg == "-b:v" {
			foundBv = true
		}
	}

	if !foundCRF {
		t.Error("expected software encoder to use -crf")
	}
	if foundBv {
		t.Error("software encoder should not use -b:v")
	}

	t.Logf("Software encoder args: %v", outputArgs)
}

func TestBuildPresetArgsZeroBitrate(t *testing.T) {
	// When source bitrate is 0, should use default behavior
	presetVT := &Preset{
		ID:      "test-vt-zero",
		Encoder: HWAccelVideoToolbox,
		Codec:   CodecHEVC,
	}

	inputArgs, outputArgs := BuildPresetArgs(presetVT, 0, 0, 0, 0, 0, false, "mkv")

	// Should still have hwaccel input args
	if len(inputArgs) == 0 {
		t.Error("expected hwaccel input args for VideoToolbox")
	}

	// Should still have -b:v but with raw modifier value
	for i, arg := range outputArgs {
		if arg == "-b:v" && i+1 < len(outputArgs) {
			bitrate := outputArgs[i+1]
			t.Logf("Zero bitrate source → target=%s", bitrate)
			// Should fall back to the raw modifier value "0.35"
			if bitrate != "0.35" {
				t.Errorf("expected fallback to '0.35', got %s", bitrate)
			}
		}
	}
}

func TestGetEncoderDefaults(t *testing.T) {
	tests := []struct {
		encoder      HWAccel
		name         string
		expectedHEVC int
		expectedAV1  int
	}{
		{HWAccelNone, "Software", 26, 35},
		{HWAccelVideoToolbox, "VideoToolbox", 0, 0}, // Bitrate-based, returns 0
		{HWAccelNVENC, "NVENC", 28, 32},
		{HWAccelQSV, "QSV", 27, 32},
		{HWAccelVAAPI, "VAAPI", 27, 32},
	}

	for _, tt := range tests {
		hevc, av1 := GetEncoderDefaults(tt.encoder)
		t.Logf("%s: HEVC=%d, AV1=%d", tt.name, hevc, av1)

		if hevc != tt.expectedHEVC {
			t.Errorf("%s HEVC: got %d, want %d", tt.name, hevc, tt.expectedHEVC)
		}
		if av1 != tt.expectedAV1 {
			t.Errorf("%s AV1: got %d, want %d", tt.name, av1, tt.expectedAV1)
		}
	}
}

func TestQSVPresetFilterChain(t *testing.T) {
	// Test that QSV presets have the correct filter chain for software decode fallback
	// The filter chain must use "format=nv12|qsv" to accept either CPU or GPU frames
	tests := []struct {
		name  string
		codec Codec
	}{
		{"QSV HEVC", CodecHEVC},
		{"QSV AV1", CodecAV1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preset := &Preset{
				ID:      "test",
				Encoder: HWAccelQSV,
				Codec:   tt.codec,
			}

			_, outputArgs := BuildPresetArgs(preset, 1000000, 1920, 1080, 0, 0, false, "mkv")

			// Find -vf argument
			for i, arg := range outputArgs {
				if arg == "-vf" && i+1 < len(outputArgs) {
					filter := outputArgs[i+1]
					if !strings.Contains(filter, "format=nv12|qsv") {
						t.Errorf("QSV preset missing 'format=nv12|qsv' in filter chain, got: %s", filter)
					}
					if !strings.Contains(filter, "hwupload=extra_hw_frames=64") {
						t.Errorf("QSV preset missing 'hwupload=extra_hw_frames=64' in filter chain, got: %s", filter)
					}
					t.Logf("Filter chain: %s", filter)
					return
				}
			}
			t.Error("QSV preset missing -vf argument")
		})
	}
}

func TestBuildPresetArgsSoftwareDecode(t *testing.T) {
	// Test that softwareDecode=true strips hwaccel args and uses correct filter
	preset := &Preset{
		ID:      "test",
		Encoder: HWAccelQSV,
		Codec:   CodecHEVC,
	}

	// Hardware decode (softwareDecode=false)
	inputArgsHW, _ := BuildPresetArgs(preset, 1000000, 1920, 1080, 0, 0, false, "mkv")

	// Software decode (softwareDecode=true)
	inputArgsSW, outputArgsSW := BuildPresetArgs(preset, 1000000, 1920, 1080, 0, 0, true, "mkv")

	// Hardware decode should have -hwaccel
	hasHwaccelHW := false
	for _, arg := range inputArgsHW {
		if arg == "-hwaccel" {
			hasHwaccelHW = true
			break
		}
	}
	if !hasHwaccelHW {
		t.Error("Hardware decode args should contain -hwaccel")
	}

	// Software decode should NOT have -hwaccel
	hasHwaccelSW := false
	for _, arg := range inputArgsSW {
		if arg == "-hwaccel" {
			hasHwaccelSW = true
			break
		}
	}
	if hasHwaccelSW {
		t.Error("Software decode args should NOT contain -hwaccel")
	}

	// Software decode should still have device init args
	hasInitDevice := false
	for _, arg := range inputArgsSW {
		if arg == "-init_hw_device" {
			hasInitDevice = true
			break
		}
	}
	if !hasInitDevice {
		t.Error("Software decode args should still contain -init_hw_device")
	}

	// Software decode output should have the software decode filter
	hasVF := false
	for i, arg := range outputArgsSW {
		if arg == "-vf" && i+1 < len(outputArgsSW) {
			filter := outputArgsSW[i+1]
			// QSV software decode filter should have hwupload (vpp_qsv removed - causes -38 errors)
			if strings.Contains(filter, "hwupload") {
				hasVF = true
			}
			break
		}
	}
	if !hasVF {
		t.Error("Software decode output args should have software decode filter with hwupload")
	}
}

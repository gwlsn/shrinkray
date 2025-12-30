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

	inputArgs, outputArgs := BuildPresetArgs(preset, sourceBitrate)

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

	inputArgs, outputArgs := BuildPresetArgs(preset, sourceBitrate)

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

	_, outputArgs := BuildPresetArgs(presetLow, lowBitrate)
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

	_, outputArgs = BuildPresetArgs(presetHigh, highBitrate)
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

	inputArgs, outputArgs := BuildPresetArgs(presetSoftware, sourceBitrate)

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

	inputArgs, outputArgs := BuildPresetArgs(presetVT, 0)

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

func TestBuildPresetArgsMuxingQueueSize(t *testing.T) {
	// Verify -max_muxing_queue_size is present for all encoder types
	testCases := []struct {
		name    string
		encoder HWAccel
		codec   Codec
	}{
		{"software HEVC", HWAccelNone, CodecHEVC},
		{"software AV1", HWAccelNone, CodecAV1},
		{"VideoToolbox HEVC", HWAccelVideoToolbox, CodecHEVC},
		{"NVENC HEVC", HWAccelNVENC, CodecHEVC},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			preset := &Preset{
				ID:      "test",
				Encoder: tc.encoder,
				Codec:   tc.codec,
			}

			_, outputArgs := BuildPresetArgs(preset, 5000000)

			found := false
			for i, arg := range outputArgs {
				if arg == "-max_muxing_queue_size" && i+1 < len(outputArgs) {
					found = true
					if outputArgs[i+1] != "4096" {
						t.Errorf("expected queue size 4096, got %s", outputArgs[i+1])
					}
				}
			}
			if !found {
				t.Errorf("expected -max_muxing_queue_size flag in output args: %v", outputArgs)
			}
		})
	}
}

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

	_, outputArgs := BuildPresetArgs(preset, sourceBitrate, nil, "convert")

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

	_, outputArgs := BuildPresetArgs(preset, sourceBitrate, nil, "convert")

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

	_, outputArgs := BuildPresetArgs(presetLow, lowBitrate, nil, "convert")
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

	_, outputArgs = BuildPresetArgs(presetHigh, highBitrate, nil, "convert")
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

	inputArgs, outputArgs := BuildPresetArgs(presetSoftware, sourceBitrate, nil, "convert")

	// Software encoder should have probesize/analyzeduration but no hwaccel input args
	// Expected: [-probesize 50M -analyzeduration 10M]
	if len(inputArgs) != 4 {
		t.Errorf("expected 4 input args (probesize + analyzeduration), got %v", inputArgs)
	}
	if len(inputArgs) >= 2 && inputArgs[0] != "-probesize" {
		t.Errorf("expected first input arg to be -probesize, got %v", inputArgs)
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

	_, outputArgs := BuildPresetArgs(presetVT, 0, nil, "convert")

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

func TestBuildPresetArgsSubtitleHandling(t *testing.T) {
	preset := &Preset{
		ID:      "test-hevc",
		Encoder: HWAccelNone,
		Codec:   CodecHEVC,
	}

	_, outputArgs := BuildPresetArgs(preset, 0, []string{"mov_text"}, "convert")
	if !containsArgPair(outputArgs, "-c:s", "srt") {
		t.Errorf("expected -c:s srt when mov_text present and convert enabled, got %v", outputArgs)
	}

	_, outputArgs = BuildPresetArgs(preset, 0, []string{"mov_text"}, "drop")
	if !containsArg(outputArgs, "-sn") {
		t.Errorf("expected -sn when mov_text present and drop enabled, got %v", outputArgs)
	}

	_, outputArgs = BuildPresetArgs(preset, 0, []string{"srt"}, "convert")
	if !containsArgPair(outputArgs, "-c:s", "copy") {
		t.Errorf("expected -c:s copy when mov_text absent, got %v", outputArgs)
	}
}

func containsArg(args []string, target string) bool {
	for _, arg := range args {
		if arg == target {
			return true
		}
	}
	return false
}

func containsArgPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

// TestBuildPresetArgsMuxingQueueSize removed - the -max_muxing_queue_size flag
// was removed because it caused memory issues with concurrent QSV encoding

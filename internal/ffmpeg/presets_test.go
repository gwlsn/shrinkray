package ffmpeg

import (
	"strings"
	"testing"
)

func TestBuildPresetArgsDynamicBitrate(t *testing.T) {
	// Test that VideoToolbox presets calculate dynamic bitrate correctly

	// Source bitrate: 3481000 bits/s (3481 kbps)
	sourceBitrate := int64(3481000)

	// Create a VideoToolbox preset with standard quality (0.5 modifier)
	presetStandard := &Preset{
		ID:      "test-standard",
		Encoder: HWAccelVideoToolbox,
		Quality: "standard",
	}

	args := BuildPresetArgs(presetStandard, sourceBitrate)

	// Should contain -b:v with calculated bitrate
	// Expected: 3481 * 0.5 = ~1740k
	found := false
	for i, arg := range args {
		if arg == "-b:v" && i+1 < len(args) {
			found = true
			bitrate := args[i+1]
			if !strings.HasSuffix(bitrate, "k") {
				t.Errorf("expected bitrate to end in 'k', got %s", bitrate)
			}
			t.Logf("Standard quality: source=%dkbps → target=%s", sourceBitrate/1000, bitrate)

			// Should be around 1740k (within reasonable range)
			if bitrate != "1740k" {
				t.Errorf("expected ~1740k, got %s", bitrate)
			}
		}
	}
	if !found {
		t.Error("expected to find -b:v flag in args")
	}
}

func TestBuildPresetArgsDynamicBitrateSmallerQuality(t *testing.T) {
	sourceBitrate := int64(3481000)

	// Create a VideoToolbox preset with smaller quality (0.35 modifier)
	presetSmaller := &Preset{
		ID:      "test-smaller",
		Encoder: HWAccelVideoToolbox,
		Quality: "smaller",
	}

	args := BuildPresetArgs(presetSmaller, sourceBitrate)

	// Expected: 3481 * 0.35 = ~1218k
	for i, arg := range args {
		if arg == "-b:v" && i+1 < len(args) {
			bitrate := args[i+1]
			t.Logf("Smaller quality: source=%dkbps → target=%s", sourceBitrate/1000, bitrate)

			if bitrate != "1218k" {
				t.Errorf("expected ~1218k, got %s", bitrate)
			}
		}
	}
}

func TestBuildPresetArgsBitrateConstraints(t *testing.T) {
	// Test min/max bitrate constraints

	// Very low source bitrate (should hit minimum)
	lowBitrate := int64(500000) // 500 kbps * 0.5 = 250k, should clamp to 500k
	presetLow := &Preset{
		ID:      "test-low",
		Encoder: HWAccelVideoToolbox,
		Quality: "standard",
	}

	args := BuildPresetArgs(presetLow, lowBitrate)
	for i, arg := range args {
		if arg == "-b:v" && i+1 < len(args) {
			bitrate := args[i+1]
			t.Logf("Low bitrate source: %dkbps → target=%s", lowBitrate/1000, bitrate)

			if bitrate != "500k" {
				t.Errorf("expected min 500k, got %s", bitrate)
			}
		}
	}

	// Very high source bitrate (should hit maximum)
	highBitrate := int64(50000000) // 50000 kbps * 0.5 = 25000k, should clamp to 15000k
	presetHigh := &Preset{
		ID:      "test-high",
		Encoder: HWAccelVideoToolbox,
		Quality: "standard",
	}

	args = BuildPresetArgs(presetHigh, highBitrate)
	for i, arg := range args {
		if arg == "-b:v" && i+1 < len(args) {
			bitrate := args[i+1]
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
		Quality: "standard",
	}

	args := BuildPresetArgs(presetSoftware, sourceBitrate)

	// Should use -crf not -b:v
	foundCRF := false
	foundBv := false
	for _, arg := range args {
		if arg == "-crf" {
			foundCRF = true
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

	t.Logf("Software encoder args: %v", args)
}

func TestBuildPresetArgsZeroBitrate(t *testing.T) {
	// When source bitrate is 0, should use default behavior
	presetVT := &Preset{
		ID:      "test-vt-zero",
		Encoder: HWAccelVideoToolbox,
		Quality: "standard",
	}

	args := BuildPresetArgs(presetVT, 0)

	// Should still have -b:v but with raw modifier value
	for i, arg := range args {
		if arg == "-b:v" && i+1 < len(args) {
			bitrate := args[i+1]
			t.Logf("Zero bitrate source → target=%s", bitrate)
			// Should fall back to the raw modifier value "0.5"
			if bitrate != "0.5" {
				t.Errorf("expected fallback to '0.5', got %s", bitrate)
			}
		}
	}
}

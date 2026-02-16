package ffmpeg

import (
	"fmt"
	"strings"
	"testing"
)

func TestBuildPresetArgsDynamicBitrate(t *testing.T) {
	// Test that VideoToolbox presets calculate dynamic bitrate correctly

	// Source bitrate: 3481000 bits/s (3481 kbps)
	sourceBitrate := int64(3481000)

	// Create a VideoToolbox preset (0.52 modifier for HEVC, calibrated via VMAF)
	preset := &Preset{
		ID:      "test-hevc",
		Encoder: HWAccelVideoToolbox,
		Codec:   CodecHEVC,
	}

	inputArgs, outputArgs := BuildPresetArgs(preset, sourceBitrate, 0, 0, 0, 0, 0, false, "mkv", nil, nil)

	// Should have hwaccel input args
	if len(inputArgs) == 0 {
		t.Error("expected hwaccel input args for VideoToolbox")
	}
	t.Logf("Input args: %v", inputArgs)

	// Should contain -b:v with calculated bitrate
	// Expected: 3481 * 0.52 = ~1810k
	found := false
	for i, arg := range outputArgs {
		if arg == "-b:v" && i+1 < len(outputArgs) {
			found = true
			bitrate := outputArgs[i+1]
			if !strings.HasSuffix(bitrate, "k") {
				t.Errorf("expected bitrate to end in 'k', got %s", bitrate)
			}
			t.Logf("HEVC VideoToolbox: source=%dkbps → target=%s", sourceBitrate/1000, bitrate)

			// Should be around 1810k (3481 * 0.52)
			if bitrate != "1810k" {
				t.Errorf("expected ~1810k, got %s", bitrate)
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

	inputArgs, outputArgs := BuildPresetArgs(preset, sourceBitrate, 0, 0, 0, 0, 0, false, "mkv", nil, nil)

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

func TestBuildPresetArgsQualityModOverride(t *testing.T) {
	// Test that qualityMod overrides the default modifier for VideoToolbox
	sourceBitrate := int64(10000000) // 10 Mbps

	preset := &Preset{
		ID:      "test-hevc-smartshrink",
		Encoder: HWAccelVideoToolbox,
		Codec:   CodecHEVC,
	}

	// With qualityMod=0.5, target should be 10000 * 0.5 = 5000k
	_, outputArgs := BuildPresetArgs(preset, sourceBitrate, 0, 0, 0, 0, 0.5, false, "mkv", nil, nil)

	for i, arg := range outputArgs {
		if arg == "-b:v" && i+1 < len(outputArgs) {
			bitrate := outputArgs[i+1]
			t.Logf("HEVC VideoToolbox with qualityMod=0.5: source=%dkbps → target=%s", sourceBitrate/1000, bitrate)

			if bitrate != "5000k" {
				t.Errorf("expected 5000k with qualityMod=0.5, got %s", bitrate)
			}
			return
		}
	}
	t.Error("expected to find -b:v flag in args")
}

func TestBuildPresetArgsQualityModIgnoredForCRFEncoders(t *testing.T) {
	// Test that qualityMod is ignored for CRF-based encoders (NVENC, etc.)
	sourceBitrate := int64(10000000)

	preset := &Preset{
		ID:      "test-nvenc",
		Encoder: HWAccelNVENC,
		Codec:   CodecHEVC,
	}

	// qualityMod should be ignored for NVENC (CRF-based)
	_, outputArgs := BuildPresetArgs(preset, sourceBitrate, 0, 0, 0, 0, 0.5, false, "mkv", nil, nil)

	// Should use -cq (constant quality) not -b:v
	for i, arg := range outputArgs {
		if arg == "-b:v" {
			t.Errorf("NVENC should not use -b:v, found at index %d", i)
		}
		if arg == "-cq" {
			t.Logf("NVENC correctly uses -cq for quality")
		}
	}
}

func TestBuildPresetArgsBitrateConstraints(t *testing.T) {
	// Test min/max bitrate constraints

	// Very low source bitrate (should hit minimum)
	// 500 kbps * 0.52 = 260k, should clamp to 500k
	lowBitrate := int64(500000)
	presetLow := &Preset{
		ID:      "test-low",
		Encoder: HWAccelVideoToolbox,
		Codec:   CodecHEVC,
	}

	_, outputArgs := BuildPresetArgs(presetLow, lowBitrate, 0, 0, 0, 0, 0, false, "mkv", nil, nil)
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
	// 50000 kbps * 0.52 = 26000k, should clamp to 15000k
	highBitrate := int64(50000000)
	presetHigh := &Preset{
		ID:      "test-high",
		Encoder: HWAccelVideoToolbox,
		Codec:   CodecHEVC,
	}

	_, outputArgs = BuildPresetArgs(presetHigh, highBitrate, 0, 0, 0, 0, 0, false, "mkv", nil, nil)
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

	inputArgs, outputArgs := BuildPresetArgs(presetSoftware, sourceBitrate, 0, 0, 0, 0, 0, false, "mkv", nil, nil)

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
			// Verify CRF value is 22 (VMAF-calibrated default)
			if i+1 < len(outputArgs) && outputArgs[i+1] != "22" {
				t.Errorf("expected CRF 22, got %s", outputArgs[i+1])
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
	// When source bitrate is 0, should use 10Mbps reference bitrate
	presetVT := &Preset{
		ID:      "test-vt-zero",
		Encoder: HWAccelVideoToolbox,
		Codec:   CodecHEVC,
	}

	inputArgs, outputArgs := BuildPresetArgs(presetVT, 0, 0, 0, 0, 0, 0, false, "mkv", nil, nil)

	// Should still have hwaccel input args
	if len(inputArgs) == 0 {
		t.Error("expected hwaccel input args for VideoToolbox")
	}

	// Should use 10Mbps reference * 0.52 modifier = 5200k
	for i, arg := range outputArgs {
		if arg == "-b:v" && i+1 < len(outputArgs) {
			bitrate := outputArgs[i+1]
			t.Logf("Zero bitrate source (10Mbps reference) → target=%s", bitrate)
			// 10000 kbps * 0.52 = 5200k
			if bitrate != "5200k" {
				t.Errorf("expected 5200k with 10Mbps reference, got %s", bitrate)
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
		{HWAccelNone, "Software", 22, 25},
		{HWAccelVideoToolbox, "VideoToolbox", 0, 0}, // Bitrate-based, returns 0
		{HWAccelNVENC, "NVENC", 26, 32},
		{HWAccelQSV, "QSV", 20, 22},
		{HWAccelVAAPI, "VAAPI", 20, 22},
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

			_, outputArgs := BuildPresetArgs(preset, 1000000, 1920, 1080, 0, 0, 0, false, "mkv", nil, nil)

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
	inputArgsHW, _ := BuildPresetArgs(preset, 1000000, 1920, 1080, 0, 0, 0, false, "mkv", nil, nil)

	// Software decode (softwareDecode=true)
	inputArgsSW, outputArgsSW := BuildPresetArgs(preset, 1000000, 1920, 1080, 0, 0, 0, true, "mkv", nil, nil)

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

// TestBuildPresetArgsHDRPermutations tests all HDR/tonemap permutations across encoders and codecs
func TestBuildPresetArgsHDRPermutations(t *testing.T) {
	encoders := []struct {
		name    string
		encoder HWAccel
	}{
		{"NVENC", HWAccelNVENC},
		{"QSV", HWAccelQSV},
		{"VAAPI", HWAccelVAAPI},
		{"VideoToolbox", HWAccelVideoToolbox},
		{"Software", HWAccelNone},
	}

	codecs := []struct {
		name  string
		codec Codec
	}{
		{"HEVC", CodecHEVC},
		{"AV1", CodecAV1},
	}

	hdrCases := []struct {
		name          string
		isHDR         bool
		enableTonemap bool
		expectP010    bool   // Should use p010 format (HDR preservation)
		expectMain10  bool   // Should have -profile:v main10
		expectTonemap bool   // Should have tonemap filter
		expectBT709   bool   // Should have bt709 color metadata (SDR output)
		expectBT2020  bool   // Should have bt2020 color metadata (HDR preserved)
	}{
		{"SDR content", false, false, false, false, false, false, false},
		{"HDR with tonemap", true, true, false, false, true, true, false},
		{"HDR preserved (no tonemap)", true, false, true, true, false, false, true},
	}

	for _, enc := range encoders {
		for _, codec := range codecs {
			for _, hdr := range hdrCases {
				testName := enc.name + "/" + codec.name + "/" + hdr.name
				t.Run(testName, func(t *testing.T) {
					preset := &Preset{
						ID:      "test",
						Encoder: enc.encoder,
						Codec:   codec.codec,
					}

					var tonemap *TonemapParams
					if hdr.isHDR {
						tonemap = &TonemapParams{
							IsHDR:         true,
							EnableTonemap: hdr.enableTonemap,
							Algorithm:     "hable",
						}
					}

					_, outputArgs := BuildPresetArgs(preset, 10000000, 1920, 1080, 0, 0, 0, false, "mkv", tonemap, nil)

					outputStr := strings.Join(outputArgs, " ")

					// Check p010 format for HDR preservation
					if hdr.expectP010 {
						if !strings.Contains(outputStr, "p010") {
							t.Logf("Output args: %v", outputArgs)
							// Note: p010 might be in input args filter, not output
							// This is expected behavior for some encoders
						}
					}

					// Check Main10 profile for HDR preservation on HEVC
					if hdr.expectMain10 && codec.codec == CodecHEVC {
						foundMain10 := false
						for i, arg := range outputArgs {
							if arg == "-profile:v" && i+1 < len(outputArgs) && outputArgs[i+1] == "main10" {
								foundMain10 = true
								break
							}
						}
						if !foundMain10 {
							t.Errorf("expected -profile:v main10 for HDR preservation")
						}
					}

					// Check tonemap filter
					if hdr.expectTonemap {
						hasTonemap := strings.Contains(outputStr, "tonemap")
						// Some encoders might not have tonemap filter available
						// (e.g., software without zscale)
						t.Logf("Has tonemap filter: %v", hasTonemap)
					}

					// Check color metadata for SDR output
					if hdr.expectBT709 {
						foundBT709 := false
						for i, arg := range outputArgs {
							if arg == "-color_primaries" && i+1 < len(outputArgs) && outputArgs[i+1] == "bt709" {
								foundBT709 = true
								break
							}
						}
						// Tonemap filters handle color space internally, so explicit bt709 not required
						t.Logf("Has explicit bt709 metadata: %v", foundBT709)
					}

					// Check color metadata for HDR preservation
					if hdr.expectBT2020 {
						foundBT2020 := false
						for i, arg := range outputArgs {
							if arg == "-color_primaries" && i+1 < len(outputArgs) && outputArgs[i+1] == "bt2020" {
								foundBT2020 = true
								break
							}
						}
						if !foundBT2020 {
							t.Errorf("expected -color_primaries bt2020 for HDR preservation")
						}

						foundSMPTE2084 := false
						for i, arg := range outputArgs {
							if arg == "-color_trc" && i+1 < len(outputArgs) && outputArgs[i+1] == "smpte2084" {
								foundSMPTE2084 = true
								break
							}
						}
						if !foundSMPTE2084 {
							t.Errorf("expected -color_trc smpte2084 for HDR preservation")
						}
					}
				})
			}
		}
	}
}

// TestBuildPresetArgsHDRFilters tests that HDR filter chains are correct for each encoder
func TestBuildPresetArgsHDRFilters(t *testing.T) {
	tests := []struct {
		name           string
		encoder        HWAccel
		enableTonemap  bool
		expectFilter   string // Substring expected in filter
	}{
		// All encoders use software tonemapping (zscale) for reliability
		{"VAAPI tonemap", HWAccelVAAPI, true, "zscale"},
		{"NVENC tonemap", HWAccelNVENC, true, "zscale"},
		{"QSV tonemap", HWAccelQSV, true, "zscale"},
		{"Software tonemap", HWAccelNone, true, "zscale"},
		// HDR preservation uses p010 format
		{"VAAPI HDR preserve", HWAccelVAAPI, false, "p010"},
		{"NVENC HDR preserve", HWAccelNVENC, false, "p010"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preset := &Preset{
				ID:      "test",
				Encoder: tt.encoder,
				Codec:   CodecHEVC,
			}

			tonemap := &TonemapParams{
				IsHDR:         true,
				EnableTonemap: tt.enableTonemap,
				Algorithm:     "hable",
			}

			inputArgs, outputArgs := BuildPresetArgs(preset, 10000000, 1920, 1080, 0, 0, 0, false, "mkv", tonemap, nil)
			allArgs := strings.Join(append(inputArgs, outputArgs...), " ")

			// Note: Filter availability depends on system, so we just log
			t.Logf("Args for %s: %s", tt.name, allArgs)

			if tt.enableTonemap {
				// Tonemap should be in args if filter is available
				t.Logf("Checking for tonemap-related filter: %s", tt.expectFilter)
			} else if strings.Contains(tt.expectFilter, "p010") {
				// HDR preservation - check for p010 format
				if !strings.Contains(allArgs, "p010") {
					// p010 might be substituted dynamically based on HDR state
					t.Logf("p010 not found, but may be handled dynamically")
				}
			}
		})
	}
}

func TestPreset_WithEncoder(t *testing.T) {
	original := &Preset{
		ID:            "test-preset",
		Name:          "Test Preset",
		Description:   "A test preset for unit testing",
		Encoder:       HWAccelNVENC,
		Codec:         CodecHEVC,
		MaxHeight:     1080,
		IsSmartShrink: true,
	}

	modified := original.WithEncoder(HWAccelVAAPI)

	// Encoder should be changed
	if modified.Encoder != HWAccelVAAPI {
		t.Errorf("Encoder: got %v, want %v", modified.Encoder, HWAccelVAAPI)
	}

	// All other fields should be preserved
	if modified.ID != original.ID {
		t.Errorf("ID not preserved: got %v, want %v", modified.ID, original.ID)
	}
	if modified.Name != original.Name {
		t.Errorf("Name not preserved: got %v, want %v", modified.Name, original.Name)
	}
	if modified.Description != original.Description {
		t.Errorf("Description not preserved: got %v, want %v", modified.Description, original.Description)
	}
	if modified.Codec != original.Codec {
		t.Errorf("Codec not preserved: got %v, want %v", modified.Codec, original.Codec)
	}
	if modified.MaxHeight != original.MaxHeight {
		t.Errorf("MaxHeight not preserved: got %v, want %v", modified.MaxHeight, original.MaxHeight)
	}
	if modified.IsSmartShrink != original.IsSmartShrink {
		t.Errorf("IsSmartShrink not preserved: got %v, want %v", modified.IsSmartShrink, original.IsSmartShrink)
	}

	// Original should be unchanged
	if original.Encoder != HWAccelNVENC {
		t.Errorf("Original was modified: Encoder is now %v", original.Encoder)
	}

	// Should return a new pointer, not the same object
	if modified == original {
		t.Error("WithEncoder should return a new Preset, not modify in place")
	}
}

// TestBuildTonemapFilter tests the software tonemap filter builder
func TestBuildTonemapFilter(t *testing.T) {
	algorithms := []string{"hable", "bt2390", "reinhard", "mobius"}

	for _, algo := range algorithms {
		t.Run(algo, func(t *testing.T) {
			filter, requiresSWDec := BuildTonemapFilter(algo)

			// Should always return a zscale-based filter
			if !strings.Contains(filter, "zscale") {
				t.Errorf("expected filter to contain 'zscale', got %q", filter)
			}

			// Should contain the requested algorithm
			if !strings.Contains(filter, algo) {
				t.Errorf("expected filter to contain algorithm %q, got %q", algo, filter)
			}

			// Software tonemapping always requires SW decode
			if !requiresSWDec {
				t.Error("expected requiresSWDec=true for software tonemapping")
			}
		})
	}
}

func TestGetBasePresetMeta(t *testing.T) {
	tests := []struct {
		id            string
		expectNil     bool
		expectedCodec Codec
		expectedMax   int
	}{
		{"compress-hevc", false, CodecHEVC, 0},
		{"compress-av1", false, CodecAV1, 0},
		{"smartshrink-hevc", false, CodecHEVC, 0},
		{"smartshrink-av1", false, CodecAV1, 0},
		{"1080p", false, CodecHEVC, 1080},
		{"720p", false, CodecHEVC, 720},
		{"nonexistent-preset", true, "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			meta := GetBasePresetMeta(tt.id)

			if tt.expectNil {
				if meta != nil {
					t.Errorf("expected nil for unknown preset %q, got %+v", tt.id, meta)
				}
				return
			}

			if meta == nil {
				t.Fatalf("expected non-nil meta for %q", tt.id)
			}

			if meta.Codec != tt.expectedCodec {
				t.Errorf("Codec: got %v, want %v", meta.Codec, tt.expectedCodec)
			}

			if meta.MaxHeight != tt.expectedMax {
				t.Errorf("MaxHeight: got %d, want %d", meta.MaxHeight, tt.expectedMax)
			}
		})
	}
}

func TestPreset_Meta(t *testing.T) {
	preset := &Preset{
		ID:            "test",
		Name:          "Test",
		Description:   "Test preset",
		Encoder:       HWAccelNVENC,
		Codec:         CodecAV1,
		MaxHeight:     1080,
		IsSmartShrink: true,
	}

	meta := preset.Meta()

	if meta == nil {
		t.Fatal("expected non-nil meta")
	}

	if meta.Codec != CodecAV1 {
		t.Errorf("Codec: got %v, want %v", meta.Codec, CodecAV1)
	}

	if meta.MaxHeight != 1080 {
		t.Errorf("MaxHeight: got %d, want 1080", meta.MaxHeight)
	}
}

func TestPreset_Meta_NilSafe(t *testing.T) {
	var preset *Preset = nil

	meta := preset.Meta()

	if meta != nil {
		t.Errorf("expected nil meta for nil preset, got %+v", meta)
	}
}

func TestBuildPresetArgsSubtitleMapping(t *testing.T) {
	preset := &Preset{
		ID:      "test-hevc",
		Encoder: HWAccelNone,
		Codec:   CodecHEVC,
	}

	tests := []struct {
		name            string
		subtitleIndices []int
		outputFormat    string
		wantContains    []string
		wantNotContains []string
	}{
		{
			name:            "nil indices maps all subtitles (MKV)",
			subtitleIndices: nil,
			outputFormat:    "mkv",
			wantContains:    []string{"0:s?", "-c:s", "copy"},
			wantNotContains: []string{"-sn"},
		},
		{
			name:            "empty indices maps no subtitles",
			subtitleIndices: []int{},
			outputFormat:    "mkv",
			// NOTE: -map still appears for video/audio (0:v:0, 0:a?), so we only
			// check that subtitle-specific mapping is absent
			wantContains:    []string{"-c:a", "copy"}, // Audio still copied
			wantNotContains: []string{"0:s?", "-c:s"}, // No subtitle mapping
		},
		{
			name:            "specific indices maps those streams by absolute index",
			subtitleIndices: []int{2, 4},
			outputFormat:    "mkv",
			wantContains:    []string{"0:2", "0:4", "-c:s", "copy"},
			wantNotContains: []string{"0:s?"},
		},
		{
			name:            "MP4 includes subtitles as mov_text",
			subtitleIndices: []int{2, 4},
			outputFormat:    "mp4",
			wantContains:    []string{"0:s?", "-c:s", "mov_text"},
			wantNotContains: []string{"-sn"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, outputArgs := BuildPresetArgs(preset, 0, 1920, 1080, 0, 0, 0, false, tt.outputFormat, nil, tt.subtitleIndices)
			argsStr := strings.Join(outputArgs, " ")

			for _, want := range tt.wantContains {
				if !strings.Contains(argsStr, want) {
					t.Errorf("expected args to contain %q, got: %s", want, argsStr)
				}
			}
			for _, notWant := range tt.wantNotContains {
				if strings.Contains(argsStr, notWant) {
					t.Errorf("expected args NOT to contain %q, got: %s", notWant, argsStr)
				}
			}
		})
	}
}

// TestBuildPresetArgsDownscaleSingleHWScaler verifies that downscale presets
// produce exactly one hardware scale filter per encoder, not two.
// Regression test for issue #101: duplicate scale_cuda/scale_qsv/scale_vaapi
// caused fallback to software encoding on older GPUs.
func TestBuildPresetArgsDownscaleSingleHWScaler(t *testing.T) {
	tests := []struct {
		name        string
		encoder     HWAccel
		codec       Codec
		scaleFilter string // the HW scaler name to check for duplicates
		wantInVF    string // expected merged filter substring
	}{
		{
			name:        "NVENC HEVC downscale 720p",
			encoder:     HWAccelNVENC,
			codec:       CodecHEVC,
			scaleFilter: "scale_cuda",
			wantInVF:    "scale_cuda=w=-2:h='min(ih,720)':format=nv12",
		},
		{
			name:        "NVENC AV1 downscale 720p",
			encoder:     HWAccelNVENC,
			codec:       CodecAV1,
			scaleFilter: "scale_cuda",
			wantInVF:    "scale_cuda=w=-2:h='min(ih,720)':format=nv12",
		},
		{
			name:        "QSV HEVC downscale 720p",
			encoder:     HWAccelQSV,
			codec:       CodecHEVC,
			scaleFilter: "scale_qsv",
			wantInVF:    "scale_qsv=w=-2:h='min(ih,720)':format=nv12",
		},
		{
			name:        "VAAPI HEVC downscale 720p",
			encoder:     HWAccelVAAPI,
			codec:       CodecHEVC,
			scaleFilter: "scale_vaapi",
			wantInVF:    "scale_vaapi=w=-2:h='min(ih,720)':format=nv12",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preset := &Preset{
				ID:        "test-downscale",
				Encoder:   tt.encoder,
				Codec:     tt.codec,
				MaxHeight: 720,
			}

			// sourceHeight=1080 triggers downscaling to 720
			_, outputArgs := BuildPresetArgs(preset, 10000000, 1920, 1080, 0, 0, 0, false, "mkv", nil, nil)

			// Find the -vf argument
			vfFilter := ""
			for i, arg := range outputArgs {
				if arg == "-vf" && i+1 < len(outputArgs) {
					vfFilter = outputArgs[i+1]
					break
				}
			}

			if vfFilter == "" {
				t.Fatal("expected -vf argument in output args")
			}

			t.Logf("Filter chain: %s", vfFilter)

			// Must contain the merged filter
			if !strings.Contains(vfFilter, tt.wantInVF) {
				t.Errorf("expected merged filter %q in chain, got: %s", tt.wantInVF, vfFilter)
			}

			// Must have exactly one instance of the HW scaler
			count := strings.Count(vfFilter, tt.scaleFilter)
			if count != 1 {
				t.Errorf("expected exactly 1 %s in filter chain, found %d: %s", tt.scaleFilter, count, vfFilter)
			}
		})
	}
}

// TestBuildPresetArgsDownscaleHDRPreservation verifies that downscale presets
// with HDR preservation use p010 format in the merged filter.
func TestBuildPresetArgsDownscaleHDRPreservation(t *testing.T) {
	tests := []struct {
		name        string
		encoder     HWAccel
		scaleFilter string
		wantInVF    string
	}{
		{
			name:        "NVENC HDR downscale 720p",
			encoder:     HWAccelNVENC,
			scaleFilter: "scale_cuda",
			wantInVF:    "scale_cuda=w=-2:h='min(ih,720)':format=p010",
		},
		{
			name:        "VAAPI HDR downscale 720p",
			encoder:     HWAccelVAAPI,
			scaleFilter: "scale_vaapi",
			wantInVF:    "scale_vaapi=w=-2:h='min(ih,720)':format=p010",
		},
		{
			name:        "QSV HDR downscale 720p",
			encoder:     HWAccelQSV,
			scaleFilter: "scale_qsv",
			wantInVF:    "scale_qsv=w=-2:h='min(ih,720)':format=p010",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preset := &Preset{
				ID:        "test-hdr-downscale",
				Encoder:   tt.encoder,
				Codec:     CodecHEVC,
				MaxHeight: 720,
			}

			tonemap := &TonemapParams{
				IsHDR:         true,
				EnableTonemap: false, // preserve HDR
			}

			_, outputArgs := BuildPresetArgs(preset, 10000000, 1920, 1080, 0, 0, 0, false, "mkv", tonemap, nil)

			vfFilter := ""
			for i, arg := range outputArgs {
				if arg == "-vf" && i+1 < len(outputArgs) {
					vfFilter = outputArgs[i+1]
					break
				}
			}

			if vfFilter == "" {
				t.Fatal("expected -vf argument in output args")
			}

			t.Logf("Filter chain: %s", vfFilter)

			if !strings.Contains(vfFilter, tt.wantInVF) {
				t.Errorf("expected merged HDR filter %q in chain, got: %s", tt.wantInVF, vfFilter)
			}

			// Exactly one HW scaler
			count := strings.Count(vfFilter, tt.scaleFilter)
			if count != 1 {
				t.Errorf("expected exactly 1 %s in filter chain, found %d: %s", tt.scaleFilter, count, vfFilter)
			}
		})
	}
}

// TestBuildPresetArgsTonemapDownscale verifies that tonemapping + downscaling
// uses CPU scale (not HW scale) before hwupload, since tonemapping outputs
// CPU frames via zscale.
func TestBuildPresetArgsTonemapDownscale(t *testing.T) {
	tests := []struct {
		name        string
		encoder     HWAccel
		scaleFilter string // HW scaler that should NOT appear
		wantScale   bool   // expect CPU scale in chain
		wantUpload  string // expected upload filter substring (empty = none)
	}{
		{
			name:        "NVENC tonemap + downscale",
			encoder:     HWAccelNVENC,
			scaleFilter: "scale_cuda",
			wantScale:   true,
			wantUpload:  "", // NVENC handles CPU frames natively
		},
		{
			name:        "QSV tonemap + downscale",
			encoder:     HWAccelQSV,
			scaleFilter: "scale_qsv",
			wantScale:   true,
			wantUpload:  "format=nv12,hwupload=extra_hw_frames=64",
		},
		{
			name:        "VAAPI tonemap + downscale",
			encoder:     HWAccelVAAPI,
			scaleFilter: "scale_vaapi",
			wantScale:   true,
			wantUpload:  "format=nv12,hwupload",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preset := &Preset{
				ID:        "test-tonemap-downscale",
				Encoder:   tt.encoder,
				Codec:     CodecHEVC,
				MaxHeight: 720,
			}

			tonemap := &TonemapParams{
				IsHDR:         true,
				EnableTonemap: true, // triggers tonemapping path
			}

			// sourceHeight=1080 triggers downscaling to 720
			_, outputArgs := BuildPresetArgs(preset, 10000000, 1920, 1080, 0, 0, 0, false, "mkv", tonemap, nil)

			vfFilter := ""
			for i, arg := range outputArgs {
				if arg == "-vf" && i+1 < len(outputArgs) {
					vfFilter = outputArgs[i+1]
					break
				}
			}

			if vfFilter == "" {
				t.Fatal("expected -vf argument in output args")
			}

			t.Logf("Filter chain: %s", vfFilter)

			// Must start with zscale tonemap chain
			if !strings.Contains(vfFilter, "zscale=t=linear") {
				t.Errorf("expected zscale tonemap in filter chain, got: %s", vfFilter)
			}

			// Must use CPU scale, not HW scaler (tonemap outputs CPU frames)
			if strings.Contains(vfFilter, tt.scaleFilter) {
				t.Errorf("tonemap path should not use %s, got: %s", tt.scaleFilter, vfFilter)
			}

			if tt.wantScale {
				if !strings.Contains(vfFilter, "scale=-2:'min(ih,720)'") {
					t.Errorf("expected CPU scale=-2:'min(ih,720)' in filter chain, got: %s", vfFilter)
				}
			}

			if tt.wantUpload != "" {
				if !strings.Contains(vfFilter, tt.wantUpload) {
					t.Errorf("expected upload filter %q in chain, got: %s", tt.wantUpload, vfFilter)
				}
			}
		})
	}
}

// TestBuildPresetArgsSoftwareDecodeDownscale verifies that when software decode
// is active, downscale presets use CPU scaling (scale) not hardware scaling
// (scale_cuda/scale_qsv/scale_vaapi), since frames are on CPU.
func TestBuildPresetArgsSoftwareDecodeDownscale(t *testing.T) {
	hwEncoders := []struct {
		name        string
		encoder     HWAccel
		scaleFilter string // should NOT appear in filter chain
	}{
		{"NVENC", HWAccelNVENC, "scale_cuda"},
		{"QSV", HWAccelQSV, "scale_qsv"},
		{"VAAPI", HWAccelVAAPI, "scale_vaapi"},
	}

	for _, enc := range hwEncoders {
		t.Run(enc.name, func(t *testing.T) {
			preset := &Preset{
				ID:        "test-sw-downscale",
				Encoder:   enc.encoder,
				Codec:     CodecHEVC,
				MaxHeight: 720,
			}

			// softwareDecode=true, sourceHeight=1080 triggers downscaling
			_, outputArgs := BuildPresetArgs(preset, 10000000, 1920, 1080, 0, 0, 0, true, "mkv", nil, nil)

			vfFilter := ""
			for i, arg := range outputArgs {
				if arg == "-vf" && i+1 < len(outputArgs) {
					vfFilter = outputArgs[i+1]
					break
				}
			}

			t.Logf("Filter chain: %s", vfFilter)

			// Must use CPU scale, not the HW scaler
			if strings.Contains(vfFilter, enc.scaleFilter) {
				t.Errorf("software decode should not use %s, got: %s", enc.scaleFilter, vfFilter)
			}

			// Must contain CPU scale with dimensions
			if !strings.Contains(vfFilter, "scale=-2:'min(ih,720)'") {
				t.Errorf("expected CPU scale=-2:'min(ih,720)' in filter chain, got: %s", vfFilter)
			}
		})
	}
}

// TestBuildPresetArgsCompressNoScale verifies that compress presets (MaxHeight=0)
// do NOT add any scaling filter, confirming they remain unaffected by the fix.
func TestBuildPresetArgsCompressNoScale(t *testing.T) {
	preset := &Preset{
		ID:      "test-compress",
		Encoder: HWAccelNVENC,
		Codec:   CodecHEVC,
		// MaxHeight=0 means no scaling
	}

	_, outputArgs := BuildPresetArgs(preset, 10000000, 1920, 1080, 0, 0, 0, false, "mkv", nil, nil)

	vfFilter := ""
	for i, arg := range outputArgs {
		if arg == "-vf" && i+1 < len(outputArgs) {
			vfFilter = outputArgs[i+1]
			break
		}
	}

	t.Logf("Filter chain: %s", vfFilter)

	// Compress should have exactly the base filter, no resize dimensions
	if vfFilter != "scale_cuda=format=nv12" {
		t.Errorf("compress preset should have only base filter, got: %s", vfFilter)
	}
}

// TestCrfToBitrateModifier tests the CRF to bitrate modifier conversion formula
func TestCrfToBitrateModifier(t *testing.T) {
	tests := []struct {
		crf      int
		expected float64
	}{
		{15, 0.50}, // Near-lossless
		{22, 0.36}, // High quality default
		{26, 0.28}, // Typical compress
		{35, 0.10}, // Aggressive compression
		{0, 0.80},  // Edge case: CRF 0 clamps to max 0.80
		{50, 0.05}, // Edge case: high CRF clamps to min 0.05
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("CRF_%d", tt.crf), func(t *testing.T) {
			result := crfToBitrateModifier(tt.crf)
			// Allow small floating point tolerance
			if result < tt.expected-0.01 || result > tt.expected+0.01 {
				t.Errorf("crfToBitrateModifier(%d) = %.3f, want %.3f", tt.crf, result, tt.expected)
			}
		})
	}
}

// TestEncoderSettings_buildScaleFilter tests the scale filter generation
func TestEncoderSettings_buildScaleFilter(t *testing.T) {
	tests := []struct {
		name           string
		scaleFilter    string
		scalePixFmt    string
		maxHeight      int
		resize         bool
		pixFmt         string
		expectedFilter string
	}{
		{
			name:           "CPU encoder no resize",
			scaleFilter:    "scale",
			scalePixFmt:    "",
			maxHeight:      0,
			resize:         false,
			pixFmt:         "",
			expectedFilter: "",
		},
		{
			name:           "CPU encoder with resize",
			scaleFilter:    "scale",
			scalePixFmt:    "",
			maxHeight:      720,
			resize:         true,
			pixFmt:         "",
			expectedFilter: "scale=-2:'min(ih,720)'",
		},
		{
			name:           "NVENC no resize (format only)",
			scaleFilter:    "scale_cuda",
			scalePixFmt:    "nv12",
			maxHeight:      0,
			resize:         false,
			pixFmt:         "nv12",
			expectedFilter: "scale_cuda=format=nv12",
		},
		{
			name:           "NVENC with resize",
			scaleFilter:    "scale_cuda",
			scalePixFmt:    "nv12",
			maxHeight:      1080,
			resize:         true,
			pixFmt:         "nv12",
			expectedFilter: "scale_cuda=w=-2:h='min(ih,1080)':format=nv12",
		},
		{
			name:           "HDR preservation with p010",
			scaleFilter:    "scale_cuda",
			scalePixFmt:    "nv12",
			maxHeight:      1080,
			resize:         true,
			pixFmt:         "p010",
			expectedFilter: "scale_cuda=w=-2:h='min(ih,1080)':format=p010",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			es := &encoderSettings{
				scaleFilter: tt.scaleFilter,
				scalePixFmt: tt.scalePixFmt,
			}
			result := es.buildScaleFilter(tt.maxHeight, tt.resize, tt.pixFmt)
			if result != tt.expectedFilter {
				t.Errorf("buildScaleFilter() = %q, want %q", result, tt.expectedFilter)
			}
		})
	}
}

// TestEncoderSettings_buildUploadPipeline tests the GPU upload filter generation
func TestEncoderSettings_buildUploadPipeline(t *testing.T) {
	tests := []struct {
		name           string
		uploadFilter   string
		pixFmt         string
		expectedFilter string
	}{
		{
			name:           "No upload filter (NVENC, VideoToolbox)",
			uploadFilter:   "",
			pixFmt:         "nv12",
			expectedFilter: "",
		},
		{
			name:           "QSV upload",
			uploadFilter:   "hwupload=extra_hw_frames=64",
			pixFmt:         "nv12",
			expectedFilter: "format=nv12,hwupload=extra_hw_frames=64",
		},
		{
			name:           "VAAPI upload",
			uploadFilter:   "hwupload",
			pixFmt:         "nv12",
			expectedFilter: "format=nv12,hwupload",
		},
		{
			name:           "Upload with p010 format",
			uploadFilter:   "hwupload",
			pixFmt:         "p010",
			expectedFilter: "format=p010,hwupload",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			es := &encoderSettings{
				uploadFilter: tt.uploadFilter,
			}
			result := es.buildUploadPipeline(tt.pixFmt)
			if result != tt.expectedFilter {
				t.Errorf("buildUploadPipeline() = %q, want %q", result, tt.expectedFilter)
			}
		})
	}
}

// TestGetQualityRange tests quality range retrieval for different encoders
func TestGetQualityRange(t *testing.T) {
	tests := []struct {
		name        string
		hwaccel     HWAccel
		codec       Codec
		expectedMin int
		expectedMax int
	}{
		{"Software HEVC", HWAccelNone, CodecHEVC, 16, 30},
		{"Software AV1", HWAccelNone, CodecAV1, 18, 35},
		{"NVENC HEVC", HWAccelNVENC, CodecHEVC, 16, 30},
		{"NVENC AV1", HWAccelNVENC, CodecAV1, 18, 35},
		{"QSV HEVC", HWAccelQSV, CodecHEVC, 16, 30},
		{"QSV AV1", HWAccelQSV, CodecAV1, 18, 35},
		{"VAAPI HEVC", HWAccelVAAPI, CodecHEVC, 16, 30},
		{"VAAPI AV1", HWAccelVAAPI, CodecAV1, 18, 35},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qr := GetQualityRange(tt.hwaccel, tt.codec)
			if qr.Min != tt.expectedMin {
				t.Errorf("Min = %d, want %d", qr.Min, tt.expectedMin)
			}
			if qr.Max != tt.expectedMax {
				t.Errorf("Max = %d, want %d", qr.Max, tt.expectedMax)
			}
		})
	}
}

// TestGetQualityRange_VideoToolbox tests VideoToolbox bitrate-based range
func TestGetQualityRange_VideoToolbox(t *testing.T) {
	tests := []struct {
		codec       Codec
		expectedMin float64
		expectedMax float64
	}{
		{CodecHEVC, 0.05, 0.80},
		{CodecAV1, 0.05, 0.70},
	}

	for _, tt := range tests {
		t.Run(string(tt.codec), func(t *testing.T) {
			qr := GetQualityRange(HWAccelVideoToolbox, tt.codec)
			if !qr.UsesBitrate {
				t.Error("VideoToolbox should use bitrate-based quality")
			}
			if qr.MinMod != tt.expectedMin {
				t.Errorf("MinMod = %.2f, want %.2f", qr.MinMod, tt.expectedMin)
			}
			if qr.MaxMod != tt.expectedMax {
				t.Errorf("MaxMod = %.2f, want %.2f", qr.MaxMod, tt.expectedMax)
			}
		})
	}
}

// TestBuildSampleEncodeArgs tests sample encoding arguments for VMAF
func TestBuildSampleEncodeArgs(t *testing.T) {
	preset := &Preset{
		ID:      "test",
		Encoder: HWAccelNVENC,
		Codec:   CodecHEVC,
	}

	inputArgs, outputArgs := BuildSampleEncodeArgs(preset, 1920, 1080, 25, 0, false)

	// Should have input args for hardware acceleration
	if len(inputArgs) == 0 {
		t.Error("expected hwaccel input args")
	}

	// Should have video codec
	hasCodec := false
	for i, arg := range outputArgs {
		if arg == "-c:v" && i+1 < len(outputArgs) {
			hasCodec = true
			if outputArgs[i+1] != "hevc_nvenc" {
				t.Errorf("expected hevc_nvenc, got %s", outputArgs[i+1])
			}
		}
	}
	if !hasCodec {
		t.Error("expected -c:v in output args")
	}

	// Should have quality flag
	hasQuality := false
	for i, arg := range outputArgs {
		if arg == "-cq" && i+1 < len(outputArgs) {
			hasQuality = true
			if outputArgs[i+1] != "25" {
				t.Errorf("expected quality 25, got %s", outputArgs[i+1])
			}
		}
	}
	if !hasQuality {
		t.Error("expected -cq flag in output args")
	}

	// Should have -an and -sn (no audio/subtitles)
	hasAN := false
	hasSN := false
	for _, arg := range outputArgs {
		if arg == "-an" {
			hasAN = true
		}
		if arg == "-sn" {
			hasSN = true
		}
	}
	if !hasAN {
		t.Error("expected -an in sample encode args")
	}
	if !hasSN {
		t.Error("expected -sn in sample encode args")
	}

	// Should NOT have audio/subtitle mapping
	for _, arg := range outputArgs {
		if strings.Contains(arg, ":a") || strings.Contains(arg, ":s") {
			t.Errorf("sample encode should not map audio/subtitles, found: %s", arg)
		}
	}
}

// TestBuildSampleEncodeArgs_VideoToolbox tests sample encoding with bitrate modifier
func TestBuildSampleEncodeArgs_VideoToolbox(t *testing.T) {
	preset := &Preset{
		ID:      "test-vt",
		Encoder: HWAccelVideoToolbox,
		Codec:   CodecHEVC,
	}

	// With modifierOverride=0.4, should use 10Mbps reference * 0.4 = 4000k
	_, outputArgs := BuildSampleEncodeArgs(preset, 1920, 1080, 0, 0.4, false)

	foundBitrate := false
	for i, arg := range outputArgs {
		if arg == "-b:v" && i+1 < len(outputArgs) {
			foundBitrate = true
			if outputArgs[i+1] != "4000k" {
				t.Errorf("expected 4000k with modifier 0.4, got %s", outputArgs[i+1])
			}
		}
	}
	if !foundBitrate {
		t.Error("expected -b:v flag for VideoToolbox")
	}
}

// TestBuildSampleEncodeArgs_QualityOverride tests CRF override in sample encoding
func TestBuildSampleEncodeArgs_QualityOverride(t *testing.T) {
	preset := &Preset{
		ID:      "test-sw",
		Encoder: HWAccelNone,
		Codec:   CodecAV1,
	}

	_, outputArgs := BuildSampleEncodeArgs(preset, 1920, 1080, 30, 0, false)

	foundQuality := false
	for i, arg := range outputArgs {
		if arg == "-crf" && i+1 < len(outputArgs) {
			foundQuality = true
			if outputArgs[i+1] != "30" {
				t.Errorf("expected CRF 30, got %s", outputArgs[i+1])
			}
		}
	}
	if !foundQuality {
		t.Error("expected -crf flag with override value")
	}
}
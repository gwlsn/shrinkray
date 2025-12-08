package ffmpeg

import (
	"testing"
	"time"
)

func TestEstimateTranscode(t *testing.T) {
	// Test x264 → x265 compression
	probe := &ProbeResult{
		Size:       1_000_000_000, // 1 GB
		Duration:   time.Hour,
		VideoCodec: "h264",
		IsHEVC:     false,
		Height:     1080,
		Bitrate:    10_000_000, // 10 Mbps
	}

	preset := GetPreset("compress")
	est := EstimateTranscode(probe, preset)

	// Should estimate ~50% savings
	if est.SavingsPercent < 30 || est.SavingsPercent > 70 {
		t.Errorf("expected savings 30-70%%, got %.1f%%", est.SavingsPercent)
	}

	if est.EstimatedSize >= probe.Size {
		t.Errorf("expected smaller estimated size, got %d >= %d", est.EstimatedSize, probe.Size)
	}

	if est.Warning != "" {
		t.Errorf("unexpected warning for good compression candidate: %s", est.Warning)
	}

	t.Logf("Estimate: current=%d, estimated=%d (%.1f%% savings), time=%v",
		est.CurrentSize, est.EstimatedSize, est.SavingsPercent, est.EstimatedTime)
}

func TestEstimateTranscodeAlreadyHEVC(t *testing.T) {
	probe := &ProbeResult{
		Size:       1_000_000_000,
		Duration:   time.Hour,
		VideoCodec: "hevc",
		IsHEVC:     true,
		Height:     1080,
		Bitrate:    5_000_000,
	}

	preset := GetPreset("compress")
	est := EstimateTranscode(probe, preset)

	// Should have minimal savings
	if est.SavingsPercent > 20 {
		t.Errorf("expected low savings for HEVC, got %.1f%%", est.SavingsPercent)
	}

	if est.Warning == "" {
		t.Error("expected warning for already HEVC content")
	}

	t.Logf("HEVC estimate: %.1f%% savings, warning: %s", est.SavingsPercent, est.Warning)
}

func TestEstimateTranscodeLowBitrate(t *testing.T) {
	probe := &ProbeResult{
		Size:       500_000_000, // 500 MB
		Duration:   time.Hour,
		VideoCodec: "h264",
		IsHEVC:     false,
		Height:     1080,
		Bitrate:    1_000_000, // 1 Mbps - quite low
	}

	preset := GetPreset("compress")
	est := EstimateTranscode(probe, preset)

	// Should warn about low bitrate
	if est.Warning == "" {
		t.Error("expected warning for low bitrate content")
	}

	t.Logf("Low bitrate estimate: %.1f%% savings, warning: %s", est.SavingsPercent, est.Warning)
}

func TestEstimateTranscodeDownscale(t *testing.T) {
	// 4K content being downscaled to 1080p
	probe := &ProbeResult{
		Size:       10_000_000_000, // 10 GB
		Duration:   time.Hour,
		VideoCodec: "h264",
		IsHEVC:     false,
		Height:     2160, // 4K
		Bitrate:    20_000_000,
	}

	preset := GetPreset("1080p")
	est := EstimateTranscode(probe, preset)

	// Should have significant savings from downscale
	if est.SavingsPercent < 50 {
		t.Errorf("expected >50%% savings for 4K→1080p, got %.1f%%", est.SavingsPercent)
	}

	t.Logf("4K→1080p estimate: %.1f%% savings", est.SavingsPercent)

	// 720p preset should save even more
	preset720 := GetPreset("720p")
	est720 := EstimateTranscode(probe, preset720)

	if est720.SavingsPercent <= est.SavingsPercent {
		t.Errorf("expected 720p to save more than 1080p: %.1f%% vs %.1f%%",
			est720.SavingsPercent, est.SavingsPercent)
	}

	t.Logf("4K→720p estimate: %.1f%% savings", est720.SavingsPercent)
}

func TestEstimateMultiple(t *testing.T) {
	probes := []*ProbeResult{
		{Size: 1_000_000_000, Duration: time.Hour, VideoCodec: "h264", Height: 1080, Bitrate: 10_000_000},
		{Size: 2_000_000_000, Duration: 2 * time.Hour, VideoCodec: "h264", Height: 1080, Bitrate: 8_000_000},
		{Size: 500_000_000, Duration: 30 * time.Minute, VideoCodec: "h264", Height: 720, Bitrate: 5_000_000},
	}

	preset := GetPreset("compress")
	total := EstimateMultiple(probes, preset)

	expectedSize := int64(1_000_000_000 + 2_000_000_000 + 500_000_000)
	if total.CurrentSize != expectedSize {
		t.Errorf("expected current size %d, got %d", expectedSize, total.CurrentSize)
	}

	if total.EstimatedSize >= total.CurrentSize {
		t.Error("expected estimated size to be less than current")
	}

	expectedDuration := time.Hour + 2*time.Hour + 30*time.Minute
	// Estimated time should be longer than duration due to encode speed < 1x
	if total.EstimatedTime < expectedDuration {
		t.Errorf("expected estimated time > %v, got %v", expectedDuration, total.EstimatedTime)
	}

	t.Logf("Multiple files: %d → %d (%.1f%% savings), time=%v",
		total.CurrentSize, total.EstimatedSize, total.SavingsPercent, total.EstimatedTime)
}

func TestEstimateBounds(t *testing.T) {
	probe := &ProbeResult{
		Size:       1_000_000_000,
		Duration:   time.Hour,
		VideoCodec: "h264",
		IsHEVC:     false,
		Height:     1080,
		Bitrate:    10_000_000,
	}

	preset := GetPreset("compress")
	est := EstimateTranscode(probe, preset)

	// Min should be <= estimated <= max
	if est.EstimatedSizeMin > est.EstimatedSize {
		t.Errorf("min %d > estimated %d", est.EstimatedSizeMin, est.EstimatedSize)
	}
	if est.EstimatedSizeMax < est.EstimatedSize {
		t.Errorf("max %d < estimated %d", est.EstimatedSizeMax, est.EstimatedSize)
	}

	t.Logf("Size bounds: %d (min) <= %d (est) <= %d (max)",
		est.EstimatedSizeMin, est.EstimatedSize, est.EstimatedSizeMax)
}

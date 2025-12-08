package ffmpeg

import (
	"time"
)

// Estimate contains size and time estimates for transcoding
type Estimate struct {
	CurrentSize      int64         `json:"current_size"`
	EstimatedSize    int64         `json:"estimated_size"`
	EstimatedSizeMin int64         `json:"estimated_size_min"` // Lower bound
	EstimatedSizeMax int64         `json:"estimated_size_max"` // Upper bound
	SpaceSaved       int64         `json:"space_saved"`
	SavingsPercent   float64       `json:"savings_percent"`
	EstimatedTime    time.Duration `json:"estimated_time"`
	Warning          string        `json:"warning,omitempty"`
}

// EstimateTranscode estimates the output size and time for transcoding
func EstimateTranscode(probe *ProbeResult, preset *Preset) *Estimate {
	est := &Estimate{
		CurrentSize: probe.Size,
	}

	// Estimate compression ratio based on source codec and preset
	var compressionRatio float64
	var compressionMin, compressionMax float64

	if probe.IsHEVC {
		// Already HEVC - minimal gains from re-encoding
		compressionRatio = 0.95 // 5% savings typical
		compressionMin = 0.90
		compressionMax = 1.05 // Could actually get larger
		est.Warning = "Already encoded in x265/HEVC. Re-encoding will save minimal space and may reduce quality."
	} else {
		// x264 or other codec → x265
		// Typical savings: 40-60% for x264 → x265
		switch preset.ID {
		case "compress":
			compressionRatio = 0.50 // 50% of original (50% savings)
			compressionMin = 0.40
			compressionMax = 0.65
		case "compress-hard":
			compressionRatio = 0.35 // 65% savings
			compressionMin = 0.25
			compressionMax = 0.50
		case "1080p":
			// If already <= 1080p, similar to compress
			// If > 1080p, additional savings from downscale
			if probe.Height > 1080 {
				compressionRatio = 0.35
				compressionMin = 0.25
				compressionMax = 0.50
			} else {
				compressionRatio = 0.50
				compressionMin = 0.40
				compressionMax = 0.65
			}
		case "720p":
			if probe.Height > 720 {
				compressionRatio = 0.25
				compressionMin = 0.15
				compressionMax = 0.40
			} else {
				compressionRatio = 0.50
				compressionMin = 0.40
				compressionMax = 0.65
			}
		default:
			compressionRatio = 0.50
			compressionMin = 0.40
			compressionMax = 0.65
		}

		// Adjust for already low bitrate content
		// If bitrate is already low, compression gains are reduced
		if probe.Bitrate > 0 && probe.Bitrate < 2_000_000 { // < 2 Mbps
			compressionRatio = compressionRatio * 1.3 // Less savings
			compressionMin = compressionMin * 1.3
			compressionMax = compressionMax * 1.3
			if est.Warning == "" {
				est.Warning = "Source has low bitrate. Transcoding may not save much space."
			}
		}
	}

	// Calculate estimated sizes
	est.EstimatedSize = int64(float64(probe.Size) * compressionRatio)
	est.EstimatedSizeMin = int64(float64(probe.Size) * compressionMin)
	est.EstimatedSizeMax = int64(float64(probe.Size) * compressionMax)

	// Ensure min <= estimated <= max
	if est.EstimatedSizeMin > est.EstimatedSize {
		est.EstimatedSizeMin = est.EstimatedSize
	}
	if est.EstimatedSizeMax < est.EstimatedSize {
		est.EstimatedSizeMax = est.EstimatedSize
	}

	// Calculate savings
	est.SpaceSaved = probe.Size - est.EstimatedSize
	if probe.Size > 0 {
		est.SavingsPercent = float64(est.SpaceSaved) / float64(probe.Size) * 100
	}

	// Check for low savings warning
	if est.SavingsPercent < 20 && est.Warning == "" {
		est.Warning = "Estimated savings are less than 20%. This content may already be well-compressed."
	}

	// Estimate time: assume 0.5x to 1x realtime for software encoding
	// Use 0.75x as middle ground (more conservative than optimistic)
	// Time = duration / speed
	encodeSpeed := 0.75 // Conservative estimate
	est.EstimatedTime = time.Duration(float64(probe.Duration) / encodeSpeed)

	return est
}

// EstimateMultiple estimates totals for multiple files
func EstimateMultiple(probes []*ProbeResult, preset *Preset) *Estimate {
	total := &Estimate{}

	for _, probe := range probes {
		est := EstimateTranscode(probe, preset)
		total.CurrentSize += est.CurrentSize
		total.EstimatedSize += est.EstimatedSize
		total.EstimatedSizeMin += est.EstimatedSizeMin
		total.EstimatedSizeMax += est.EstimatedSizeMax
		total.EstimatedTime += est.EstimatedTime
	}

	total.SpaceSaved = total.CurrentSize - total.EstimatedSize
	if total.CurrentSize > 0 {
		total.SavingsPercent = float64(total.SpaceSaved) / float64(total.CurrentSize) * 100
	}

	// Set warning if overall savings are low
	if total.SavingsPercent < 20 {
		total.Warning = "Estimated savings are less than 20%. This content may already be well-compressed."
	}

	return total
}

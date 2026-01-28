package vmaf

import (
	"context"
	"fmt"

	"github.com/gwlsn/shrinkray/internal/logger"
)

// QualityRange defines the search bounds for an encoder
type QualityRange struct {
	Min         int     // Minimum quality value (best quality)
	Max         int     // Maximum quality value (most compression)
	UsesBitrate bool    // True for VideoToolbox (bitrate modifier)
	MinMod      float64 // Min bitrate modifier (most compression)
	MaxMod      float64 // Max bitrate modifier (best quality)
}

// SearchResult holds the result of binary search
type SearchResult struct {
	Quality    int     // CRF/CQ/QP value
	Modifier   float64 // Bitrate modifier (for VideoToolbox)
	VMafScore  float64 // Achieved VMAF score
	Iterations int     // Number of search iterations
}

// EncodeSampleFunc is a function that encodes a sample at the given quality
// and returns the path to the encoded file
type EncodeSampleFunc func(ctx context.Context, samplePath string, quality int, modifier float64) (string, error)

// BinarySearch finds the optimal quality setting via binary search
func BinarySearch(ctx context.Context, ffmpegPath string, referenceSamples []*Sample,
	qRange QualityRange, threshold float64, height int, encodeSample EncodeSampleFunc) (*SearchResult, error) {

	if qRange.UsesBitrate {
		return binarySearchBitrate(ctx, ffmpegPath, referenceSamples, qRange, threshold, height, encodeSample)
	}
	return binarySearchCRF(ctx, ffmpegPath, referenceSamples, qRange, threshold, height, encodeSample)
}

func binarySearchCRF(ctx context.Context, ffmpegPath string, referenceSamples []*Sample,
	qRange QualityRange, threshold float64, height int, encodeSample EncodeSampleFunc) (*SearchResult, error) {

	low := qRange.Min  // Best quality
	high := qRange.Max // Most compression

	var bestQuality int
	var bestScore float64
	var found bool
	iterations := 0

	for low <= high {
		iterations++
		mid := (low + high) / 2

		// Encode all samples at this quality
		distortedSamples := make([]*Sample, 0, len(referenceSamples))
		for i, ref := range referenceSamples {
			distPath, err := encodeSample(ctx, ref.Path, mid, 0)
			if err != nil {
				return nil, fmt.Errorf("encoding sample %d at CRF %d: %w", i, mid, err)
			}
			distortedSamples = append(distortedSamples, &Sample{Path: distPath})
		}

		// Score samples
		minScore, err := ScoreSamples(ctx, ffmpegPath, referenceSamples, distortedSamples, height)

		// Cleanup encoded samples
		CleanupSamples(distortedSamples)

		if err != nil {
			return nil, fmt.Errorf("scoring at CRF %d: %w", mid, err)
		}

		logger.Info("VMAF search iteration",
			"crf", mid,
			"vmaf", fmt.Sprintf("%.2f", minScore),
			"threshold", threshold)

		if minScore >= threshold {
			// Quality is acceptable, try more compression
			bestQuality = mid
			bestScore = minScore
			found = true
			low = mid + 1
		} else {
			// Quality too low, try less compression
			high = mid - 1
		}
	}

	if !found {
		// No acceptable quality found
		return nil, nil
	}

	return &SearchResult{
		Quality:    bestQuality,
		VMafScore:  bestScore,
		Iterations: iterations,
	}, nil
}

func binarySearchBitrate(ctx context.Context, ffmpegPath string, referenceSamples []*Sample,
	qRange QualityRange, threshold float64, height int, encodeSample EncodeSampleFunc) (*SearchResult, error) {

	// For bitrate modifier: higher = better quality, lower = more compression
	low := qRange.MinMod  // Most compression (e.g., 0.05)
	high := qRange.MaxMod // Best quality (e.g., 0.80)

	var bestModifier float64
	var bestScore float64
	var found bool
	iterations := 0

	// Binary search with float precision (ensure at least one iteration)
	for high-low > 0.02 || !found {
		iterations++
		mid := (low + high) / 2

		// Encode all samples at this bitrate modifier
		distortedSamples := make([]*Sample, 0, len(referenceSamples))
		for i, ref := range referenceSamples {
			distPath, err := encodeSample(ctx, ref.Path, 0, mid)
			if err != nil {
				return nil, fmt.Errorf("encoding sample %d at modifier %.2f: %w", i, mid, err)
			}
			distortedSamples = append(distortedSamples, &Sample{Path: distPath})
		}

		// Score samples
		minScore, err := ScoreSamples(ctx, ffmpegPath, referenceSamples, distortedSamples, height)

		// Cleanup encoded samples
		CleanupSamples(distortedSamples)

		if err != nil {
			return nil, fmt.Errorf("scoring at modifier %.2f: %w", mid, err)
		}

		logger.Info("VMAF search iteration",
			"modifier", fmt.Sprintf("%.2f", mid),
			"vmaf", fmt.Sprintf("%.2f", minScore),
			"threshold", threshold)

		if minScore >= threshold {
			// Quality is acceptable, try more compression (lower modifier)
			bestModifier = mid
			bestScore = minScore
			found = true
			high = mid
		} else {
			// Quality too low, try less compression (higher modifier)
			low = mid
		}

		// Break after first iteration if range is already narrow
		if high-low <= 0.02 {
			break
		}
	}

	if !found {
		return nil, nil
	}

	return &SearchResult{
		Modifier:   bestModifier,
		VMafScore:  bestScore,
		Iterations: iterations,
	}, nil
}

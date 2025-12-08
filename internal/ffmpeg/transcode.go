package ffmpeg

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Progress represents the current transcoding progress
type Progress struct {
	Frame       int64         `json:"frame"`
	FPS         float64       `json:"fps"`
	Size        int64         `json:"size"`        // Current output size in bytes
	Time        time.Duration `json:"time"`        // Current position in video
	Bitrate     float64       `json:"bitrate"`     // Current bitrate in kbits/s
	Speed       float64       `json:"speed"`       // Encoding speed (1.0 = realtime)
	Percent     float64       `json:"percent"`     // Progress percentage (0-100)
	ETA         time.Duration `json:"eta"`         // Estimated time remaining
}

// TranscodeResult contains the result of a transcode operation
type TranscodeResult struct {
	InputPath   string `json:"input_path"`
	OutputPath  string `json:"output_path"`
	InputSize   int64  `json:"input_size"`
	OutputSize  int64  `json:"output_size"`
	SpaceSaved  int64  `json:"space_saved"`
	Duration    time.Duration `json:"duration"` // How long the transcode took
}

// Transcoder wraps ffmpeg transcoding functionality
type Transcoder struct {
	ffmpegPath string
}

// NewTranscoder creates a new Transcoder with the given ffmpeg path
func NewTranscoder(ffmpegPath string) *Transcoder {
	return &Transcoder{ffmpegPath: ffmpegPath}
}

// Transcode transcodes a video file using the given preset
// It sends progress updates to the progress channel and returns the result
// sourceBitrate is the source video bitrate in bits/second (for dynamic bitrate calculation)
func (t *Transcoder) Transcode(
	ctx context.Context,
	inputPath string,
	outputPath string,
	preset *Preset,
	duration time.Duration,
	sourceBitrate int64,
	progressCh chan<- Progress,
) (*TranscodeResult, error) {
	startTime := time.Now()

	// Get input file size
	inputInfo, err := os.Stat(inputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat input file: %w", err)
	}
	inputSize := inputInfo.Size()

	// Build preset args with source bitrate for dynamic calculation
	presetArgs := BuildPresetArgs(preset, sourceBitrate)

	// Build ffmpeg command
	args := []string{
		"-i", inputPath,
		"-y", // Overwrite output without asking
		"-progress", "pipe:1", // Output progress to stdout
		"-nostats", // Disable default stats output
	}
	args = append(args, presetArgs...)
	args = append(args, outputPath)

	cmd := exec.CommandContext(ctx, t.ffmpegPath, args...)

	// Capture stdout for progress
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	// Start the command
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start ffmpeg: %w", err)
	}

	// Parse progress from stdout
	go func() {
		defer close(progressCh)
		scanner := bufio.NewScanner(stdout)
		var currentProgress Progress

		for scanner.Scan() {
			line := scanner.Text()
			// Progress output format: key=value
			if idx := strings.Index(line, "="); idx > 0 {
				key := line[:idx]
				value := line[idx+1:]

				switch key {
				case "frame":
					currentProgress.Frame, _ = strconv.ParseInt(value, 10, 64)
				case "fps":
					currentProgress.FPS, _ = strconv.ParseFloat(value, 64)
				case "total_size":
					currentProgress.Size, _ = strconv.ParseInt(value, 10, 64)
				case "out_time_us":
					us, _ := strconv.ParseInt(value, 10, 64)
					currentProgress.Time = time.Duration(us) * time.Microsecond
				case "bitrate":
					// Format: "1234.5kbits/s" or "N/A"
					if value != "N/A" {
						value = strings.TrimSuffix(value, "kbits/s")
						currentProgress.Bitrate, _ = strconv.ParseFloat(value, 64)
					}
				case "speed":
					// Format: "1.5x" or "N/A"
					if value != "N/A" {
						value = strings.TrimSuffix(value, "x")
						currentProgress.Speed, _ = strconv.ParseFloat(value, 64)
					}
				case "progress":
					// "continue" or "end"
					if value == "continue" || value == "end" {
						// Calculate percent and ETA
						if duration > 0 {
							currentProgress.Percent = float64(currentProgress.Time) / float64(duration) * 100
							if currentProgress.Percent > 100 {
								currentProgress.Percent = 100
							}

							// Calculate ETA based on speed
							if currentProgress.Speed > 0 {
								remaining := duration - currentProgress.Time
								currentProgress.ETA = time.Duration(float64(remaining) / currentProgress.Speed)
							}
						}

						// Send progress update (non-blocking)
						select {
						case progressCh <- currentProgress:
						default:
						}
					}
				}
			}
		}
	}()

	// Wait for ffmpeg to complete
	if err := cmd.Wait(); err != nil {
		// Clean up partial output file
		os.Remove(outputPath)
		return nil, fmt.Errorf("ffmpeg failed: %w", err)
	}

	// Get output file size
	outputInfo, err := os.Stat(outputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat output file: %w", err)
	}
	outputSize := outputInfo.Size()

	return &TranscodeResult{
		InputPath:  inputPath,
		OutputPath: outputPath,
		InputSize:  inputSize,
		OutputSize: outputSize,
		SpaceSaved: inputSize - outputSize,
		Duration:   time.Since(startTime),
	}, nil
}

// BuildTempPath generates a temporary output path for transcoding
func BuildTempPath(inputPath, tempDir string) string {
	base := filepath.Base(inputPath)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	tempName := fmt.Sprintf("%s.shrinkray.tmp.mkv", name)
	return filepath.Join(tempDir, tempName)
}

// FinalizeTranscode handles the original file based on the configured behavior
// If replace=true, renames original to .old and moves temp to original location
// If replace=false, keeps original and renames temp to final output
func FinalizeTranscode(inputPath, tempPath string, replace bool) (finalPath string, err error) {
	if replace {
		// Rename original to .old
		oldPath := inputPath + ".old"
		if err := os.Rename(inputPath, oldPath); err != nil {
			return "", fmt.Errorf("failed to rename original to .old: %w", err)
		}

		// Move temp to original location (with .mkv extension)
		dir := filepath.Dir(inputPath)
		base := filepath.Base(inputPath)
		ext := filepath.Ext(base)
		name := strings.TrimSuffix(base, ext)
		finalPath = filepath.Join(dir, name+".mkv")

		if err := os.Rename(tempPath, finalPath); err != nil {
			// Try to restore original
			os.Rename(oldPath, inputPath)
			return "", fmt.Errorf("failed to move temp to final location: %w", err)
		}

		return finalPath, nil
	}

	// Keep both: temp file becomes the final output (already in place)
	// Just rename from .shrinkray.tmp.mkv to .mkv
	dir := filepath.Dir(tempPath)
	base := filepath.Base(inputPath)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	finalPath = filepath.Join(dir, name+".mkv")

	if finalPath == inputPath {
		// Input was already .mkv, need a different name
		finalPath = filepath.Join(dir, name+".shrinkray.mkv")
	}

	if err := os.Rename(tempPath, finalPath); err != nil {
		return "", fmt.Errorf("failed to rename temp to final: %w", err)
	}

	return finalPath, nil
}

// progressRegex matches FFmpeg's default progress output
var progressRegex = regexp.MustCompile(`frame=\s*(\d+)\s+fps=\s*([\d.]+)\s+.*size=\s*(\d+)kB\s+time=(\d+):(\d+):(\d+)\.(\d+)\s+bitrate=\s*([\d.]+)kbits/s\s+speed=\s*([\d.]+)x`)

// ParseProgressLine parses a line from FFmpeg's default stderr output
// This is a fallback for older FFmpeg versions that don't support -progress pipe
func ParseProgressLine(line string, duration time.Duration) *Progress {
	matches := progressRegex.FindStringSubmatch(line)
	if matches == nil {
		return nil
	}

	frame, _ := strconv.ParseInt(matches[1], 10, 64)
	fps, _ := strconv.ParseFloat(matches[2], 64)
	sizeKB, _ := strconv.ParseInt(matches[3], 10, 64)
	hours, _ := strconv.ParseInt(matches[4], 10, 64)
	mins, _ := strconv.ParseInt(matches[5], 10, 64)
	secs, _ := strconv.ParseInt(matches[6], 10, 64)
	ms, _ := strconv.ParseInt(matches[7], 10, 64)
	bitrate, _ := strconv.ParseFloat(matches[8], 64)
	speed, _ := strconv.ParseFloat(matches[9], 64)

	currentTime := time.Duration(hours)*time.Hour +
		time.Duration(mins)*time.Minute +
		time.Duration(secs)*time.Second +
		time.Duration(ms)*10*time.Millisecond

	progress := &Progress{
		Frame:   frame,
		FPS:     fps,
		Size:    sizeKB * 1024,
		Time:    currentTime,
		Bitrate: bitrate,
		Speed:   speed,
	}

	if duration > 0 {
		progress.Percent = float64(currentTime) / float64(duration) * 100
		if progress.Speed > 0 {
			remaining := duration - currentTime
			progress.ETA = time.Duration(float64(remaining) / progress.Speed)
		}
	}

	return progress
}

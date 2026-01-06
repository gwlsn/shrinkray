package ffmpeg

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gwlsn/shrinkray/internal/logger"
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
// qualityHEVC/qualityAV1 are CRF values to use (0 = use preset defaults)
func (t *Transcoder) Transcode(
	ctx context.Context,
	inputPath string,
	outputPath string,
	preset *Preset,
	duration time.Duration,
	sourceBitrate int64,
	qualityHEVC, qualityAV1 int,
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
	// inputArgs go before -i (hwaccel), outputArgs go after
	inputArgs, outputArgs := BuildPresetArgs(preset, sourceBitrate, qualityHEVC, qualityAV1)

	// Build ffmpeg command
	// Structure: ffmpeg [inputArgs] -i input [outputArgs] output
	args := []string{}
	args = append(args, inputArgs...)
	args = append(args,
		"-i", inputPath,
		"-y",                   // Overwrite output without asking
		"-progress", "pipe:1", // Output progress to stdout
		"-nostats",            // Disable default stats output
	)
	args = append(args, outputArgs...)
	args = append(args, outputPath)

	cmd := exec.CommandContext(ctx, t.ffmpegPath, args...)

	// Log the command at debug level
	logger.Debug("FFmpeg command", "args", strings.Join(args, " "))

	// Capture stdout for progress
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	// Capture stderr for error messages
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

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
		// Log stderr for debugging
		stderrOutput := stderr.String()
		if stderrOutput != "" {
			// Get last few lines of stderr for the error message
			lines := strings.Split(strings.TrimSpace(stderrOutput), "\n")
			lastLines := lines
			if len(lines) > 5 {
				lastLines = lines[len(lines)-5:]
			}
			logger.Error("FFmpeg failed", "error", err, "stderr", strings.Join(lastLines, " | "))
			return nil, fmt.Errorf("ffmpeg failed: %w - %s", err, strings.Join(lastLines, " | "))
		}
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

// copyFile copies a file from src to dst.
// Works across filesystems unlike os.Rename.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return err
	}

	return dstFile.Close()
}

// FinalizeTranscode handles the original file based on the configured behavior
// If replace=true, deletes original and copies temp to final location
// If replace=false (keep), renames original to .old and copies temp to final location
// Uses copy-then-delete instead of rename to support cross-filesystem moves.
func FinalizeTranscode(inputPath, tempPath string, replace bool) (finalPath string, err error) {
	dir := filepath.Dir(inputPath)
	base := filepath.Base(inputPath)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	finalPath = filepath.Join(dir, name+".mkv")

	if replace {
		// Replace mode: delete original, copy temp to final location
		if err := os.Remove(inputPath); err != nil {
			return "", fmt.Errorf("failed to remove original file: %w", err)
		}

		if err := copyFile(tempPath, finalPath); err != nil {
			return "", fmt.Errorf("failed to copy temp to final location: %w", err)
		}

		os.Remove(tempPath)
		return finalPath, nil
	}

	// Keep mode: rename original to .old, copy temp to final location
	oldPath := inputPath + ".old"
	if err := os.Rename(inputPath, oldPath); err != nil {
		return "", fmt.Errorf("failed to rename original to .old: %w", err)
	}

	if err := copyFile(tempPath, finalPath); err != nil {
		// Try to restore original (best effort)
		_ = os.Rename(oldPath, inputPath)
		return "", fmt.Errorf("failed to copy temp to final location: %w", err)
	}

	os.Remove(tempPath)
	return finalPath, nil
}

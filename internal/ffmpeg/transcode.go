package ffmpeg

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
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

// TranscodeError contains detailed error information from a failed transcode
type TranscodeError struct {
	Message   string   // The error message
	Stderr    string   // Bounded stderr output (last ~64KB)
	ExitCode  int      // FFmpeg exit code
	Args      []string // FFmpeg command arguments
}

func (e *TranscodeError) Error() string {
	return e.Message
}

// IsHardwareEncoderFailure checks if the error indicates a hardware encoder failure
// that might succeed with a software encoder retry. Only returns true for errors
// that are specifically related to hardware encoding initialization or execution,
// not for general errors like corrupt input or missing files.
func (e *TranscodeError) IsHardwareEncoderFailure() bool {
	stderr := strings.ToLower(e.Stderr)

	// Strong patterns: definitively indicate HW encoder failure (match immediately)
	strongPatterns := []string{
		// NVENC specific failures
		"openencodesessionex failed",
		"cannot load nvcuda",
		"cannot load cuda",
		"failed to open nvenc",
		"no capable devices found",
		// VAAPI specific failures
		"failed to initialise vaapi",
		"vainitialize failed",
		"cannot open drm render node",
		// QSV specific failures
		"mfxsession could not be created",
		"error initializing qsv",
		// VideoToolbox specific failures
		"vt compression session",
		"videotoolbox encode failed",
		"error in vt_encode",
		// Generic HW encoder failures
		"encoder initialization failed",
		"hardware encoder init failed",
		"device setup failed",
		"no encode device",
	}

	for _, pattern := range strongPatterns {
		if strings.Contains(stderr, pattern) {
			return true
		}
	}

	// Weak patterns: vendor/technology keywords that only indicate HW failure
	// when combined with failure indicators
	weakPatterns := []string{
		"nvenc", "cuda", "nvidia",
		"vaapi", "va-api",
		"qsv", "quick sync",
		"videotoolbox",
		"hwaccel", "hw accel",
		"hardware encoder", "hardware acceleration",
	}

	failureIndicators := []string{
		"failed", "error", "cannot", "unable", "no device", "not found",
		"could not", "initialization", "unavailable",
	}

	// Check if any weak pattern appears alongside a failure indicator
	for _, weak := range weakPatterns {
		if strings.Contains(stderr, weak) {
			for _, fail := range failureIndicators {
				if strings.Contains(stderr, fail) {
					return true
				}
			}
		}
	}

	return false
}

// maxStderrSize is the maximum amount of stderr to capture (64KB)
const maxStderrSize = 64 * 1024

// boundedBuffer is a ring buffer that keeps only the last N bytes
type boundedBuffer struct {
	buf   []byte
	size  int
	start int
}

func newBoundedBuffer(size int) *boundedBuffer {
	return &boundedBuffer{
		buf:  make([]byte, size),
		size: 0,
	}
}

func (b *boundedBuffer) Write(p []byte) (n int, err error) {
	n = len(p)
	if n >= len(b.buf) {
		// If input is larger than buffer, just keep the end
		copy(b.buf, p[n-len(b.buf):])
		b.size = len(b.buf)
		b.start = 0
	} else if b.size < len(b.buf) {
		// Buffer not yet full
		space := len(b.buf) - b.size
		if n <= space {
			copy(b.buf[b.size:], p)
			b.size += n
		} else {
			// Fill remaining space, then wrap
			copy(b.buf[b.size:], p[:space])
			copy(b.buf, p[space:])
			b.size = len(b.buf)
			b.start = n - space
		}
	} else {
		// Buffer is full, overwrite from start
		end := b.start + n
		if end <= len(b.buf) {
			copy(b.buf[b.start:], p)
		} else {
			firstPart := len(b.buf) - b.start
			copy(b.buf[b.start:], p[:firstPart])
			copy(b.buf, p[firstPart:])
		}
		b.start = end % len(b.buf)
	}
	return n, nil
}

func (b *boundedBuffer) String() string {
	if b.size < len(b.buf) {
		return string(b.buf[:b.size])
	}
	// Reorder circular buffer
	result := make([]byte, len(b.buf))
	copy(result, b.buf[b.start:])
	copy(result[len(b.buf)-b.start:], b.buf[:b.start])
	return string(result)
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
	// inputArgs go before -i (hwaccel), outputArgs go after
	inputArgs, outputArgs := BuildPresetArgs(preset, sourceBitrate)

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

	// Log the ffmpeg command for debugging
	log.Printf("[transcode] Running: ffmpeg %s", strings.Join(args, " "))

	cmd := exec.CommandContext(ctx, t.ffmpegPath, args...)

	// Capture stdout for progress
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	// Capture stderr for error diagnostics (bounded to prevent memory issues)
	stderrBuf := newBoundedBuffer(maxStderrSize)
	cmd.Stderr = stderrBuf

	// Start the command
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start ffmpeg: %w", err)
	}

	// Parse progress from stdout
	go func() {
		defer close(progressCh)
		scanner := bufio.NewScanner(stdout)
		var currentProgress Progress
		progressUpdateCount := 0

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
					// Use out_time_us exclusively - it's the most precise
					// Don't parse out_time_ms or out_time because they come AFTER out_time_us
					// and out_time can be "N/A" for complex files, which would overwrite
					// the valid microseconds value with 0
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
						progressUpdateCount++
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
						} else {
							// Log when duration is 0 - this would cause 0% progress
							if progressUpdateCount == 1 {
								log.Printf("[transcode] Warning: duration is 0, progress will always be 0%%")
							}
						}

						// Send progress update (non-blocking)
						select {
						case progressCh <- currentProgress:
						default:
							// Channel full, skip this update
						}
					}
				}
			}
		}
		if err := scanner.Err(); err != nil {
			log.Printf("[transcode] Scanner error: %v", err)
		}
	}()

	// Wait for ffmpeg to complete
	if err := cmd.Wait(); err != nil {
		// Clean up partial output file
		os.Remove(outputPath)

		// Extract exit code if available
		exitCode := 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}

		// Return detailed error for diagnostics
		return nil, &TranscodeError{
			Message:  fmt.Sprintf("ffmpeg failed: %v", err),
			Stderr:   stderrBuf.String(),
			ExitCode: exitCode,
			Args:     args,
		}
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
		// Try to restore original
		os.Rename(oldPath, inputPath)
		return "", fmt.Errorf("failed to copy temp to final location: %w", err)
	}

	os.Remove(tempPath)
	return finalPath, nil
}

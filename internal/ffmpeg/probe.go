package ffmpeg

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// SubtitleStream contains metadata about a subtitle stream.
// Index is the absolute stream index (used with -map 0:N), not subtitle-relative.
type SubtitleStream struct {
	Index     int    // Absolute stream index in the file (for -map 0:N)
	CodecName string // e.g., "mov_text", "subrip", "hdmv_pgs_subtitle"
}

// ProbeResult contains metadata about a video file
type ProbeResult struct {
	Path        string        `json:"path"`
	Size        int64         `json:"size"`
	Duration    time.Duration `json:"duration"`
	Format      string        `json:"format"`
	VideoCodec  string        `json:"video_codec"`
	AudioCodec  string        `json:"audio_codec"`
	Width       int           `json:"width"`
	Height      int           `json:"height"`
	Bitrate     int64         `json:"bitrate"` // bits per second
	FrameRate   float64       `json:"frame_rate"`
	IsHEVC      bool          `json:"is_hevc"` // true if already x265/HEVC
	IsAV1       bool          `json:"is_av1"`  // true if already AV1
	Profile     string        `json:"profile"`     // e.g., "High", "High 10", "Main 10"
	PixelFormat string        `json:"pix_fmt"`     // e.g., "yuv420p", "yuv420p10le"
	BitDepth    int           `json:"bit_depth"`   // 8, 10, 12
	// HDR metadata
	ColorTransfer  string `json:"color_transfer"`  // e.g., "smpte2084" (HDR10), "arib-std-b67" (HLG), "bt709"
	ColorPrimaries string `json:"color_primaries"` // e.g., "bt2020", "bt709"
	ColorSpace     string `json:"color_space"`     // e.g., "bt2020nc", "bt709"
	IsHDR          bool   `json:"is_hdr"`          // true if HDR content detected
}

// ffprobeOutput represents the JSON output from ffprobe
type ffprobeOutput struct {
	Format  ffprobeFormat   `json:"format"`
	Streams []ffprobeStream `json:"streams"`
}

type ffprobeFormat struct {
	Filename   string `json:"filename"`
	FormatName string `json:"format_name"`
	Duration   string `json:"duration"`
	Size       string `json:"size"`
	BitRate    string `json:"bit_rate"`
}

type ffprobeStream struct {
	Index            int    `json:"index"`
	CodecType        string `json:"codec_type"`
	CodecName        string `json:"codec_name"`
	Width            int    `json:"width"`
	Height           int    `json:"height"`
	RFrameRate       string `json:"r_frame_rate"`
	AvgFrameRate     string `json:"avg_frame_rate"`
	Profile          string `json:"profile"`
	PixelFormat      string `json:"pix_fmt"`
	BitsPerRawSample string `json:"bits_per_raw_sample"`
	// Color metadata for HDR detection
	ColorTransfer  string `json:"color_transfer"`
	ColorPrimaries string `json:"color_primaries"`
	ColorSpace     string `json:"color_space"`
}

// Prober wraps ffprobe functionality
type Prober struct {
	ffprobePath string
}

// NewProber creates a new Prober with the given ffprobe path
func NewProber(ffprobePath string) *Prober {
	return &Prober{ffprobePath: ffprobePath}
}

// Probe returns metadata about a video file
func (p *Prober) Probe(ctx context.Context, path string) (*ProbeResult, error) {
	cmd := exec.CommandContext(ctx, p.ffprobePath,
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path,
	)

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("ffprobe failed: %s", string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("ffprobe failed: %w", err)
	}

	var probeOutput ffprobeOutput
	if err := json.Unmarshal(output, &probeOutput); err != nil {
		return nil, fmt.Errorf("failed to parse ffprobe output: %w", err)
	}

	result := &ProbeResult{
		Path:   path,
		Format: probeOutput.Format.FormatName,
	}

	// Parse format-level metadata
	if probeOutput.Format.Size != "" {
		result.Size, _ = strconv.ParseInt(probeOutput.Format.Size, 10, 64)
	}
	if probeOutput.Format.BitRate != "" {
		result.Bitrate, _ = strconv.ParseInt(probeOutput.Format.BitRate, 10, 64)
	}
	if probeOutput.Format.Duration != "" {
		durationSec, _ := strconv.ParseFloat(probeOutput.Format.Duration, 64)
		result.Duration = time.Duration(durationSec * float64(time.Second))
	}

	// Parse stream-level metadata
	for i := range probeOutput.Streams {
		stream := &probeOutput.Streams[i]
		switch stream.CodecType {
		case "video":
			if result.VideoCodec == "" { // Take first video stream
				result.VideoCodec = stream.CodecName
				result.Width = stream.Width
				result.Height = stream.Height
				result.IsHEVC = isHEVCCodec(stream.CodecName)
				result.IsAV1 = isAV1Codec(stream.CodecName)
				result.FrameRate = parseFrameRate(stream.RFrameRate)
				if result.FrameRate == 0 {
					result.FrameRate = parseFrameRate(stream.AvgFrameRate)
				}
				result.Profile = stream.Profile
				result.PixelFormat = stream.PixelFormat
				if stream.BitsPerRawSample != "" {
					result.BitDepth, _ = strconv.Atoi(stream.BitsPerRawSample)
				}
				// Fallback: infer bit depth from pixel format if not provided
				if result.BitDepth == 0 {
					result.BitDepth = inferBitDepth(stream.PixelFormat)
				}
				// HDR metadata
				result.ColorTransfer = stream.ColorTransfer
				result.ColorPrimaries = stream.ColorPrimaries
				result.ColorSpace = stream.ColorSpace
				result.IsHDR = detectHDR(stream.ColorTransfer, stream.ColorPrimaries, result.BitDepth)
			}
		case "audio":
			if result.AudioCodec == "" { // Take first audio stream
				result.AudioCodec = stream.CodecName
			}
		}
	}

	return result, nil
}

// ProbeSubtitles returns subtitle stream info for a file.
// Returns nil slice if no subtitle streams exist.
func (p *Prober) ProbeSubtitles(ctx context.Context, path string) ([]SubtitleStream, error) {
	cmd := exec.CommandContext(ctx, p.ffprobePath,
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams",
		"-select_streams", "s", // Only subtitle streams
		path,
	)

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("ffprobe failed: %s", string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("ffprobe failed: %w", err)
	}

	var probeOutput ffprobeOutput
	if err := json.Unmarshal(output, &probeOutput); err != nil {
		return nil, fmt.Errorf("failed to parse ffprobe output: %w", err)
	}

	// Since we used -select_streams s, all returned streams are subtitles.
	// No need to filter by CodecType (which could be empty in some ffprobe versions).
	var subtitles []SubtitleStream
	for _, stream := range probeOutput.Streams {
		subtitles = append(subtitles, SubtitleStream{
			Index:     stream.Index,
			CodecName: stream.CodecName,
		})
	}

	return subtitles, nil
}

// detectHDR determines if video is HDR based on color metadata.
// Primary detection: smpte2084 (PQ) transfer = HDR10
// Fallback heuristic: 10-bit + bt2020 primaries = likely HDR
func detectHDR(colorTransfer, colorPrimaries string, bitDepth int) bool {
	transfer := strings.ToLower(colorTransfer)

	// Primary detection: PQ transfer function = HDR10
	if transfer == "smpte2084" {
		return true
	}

	// HLG detection (for future support)
	if transfer == "arib-std-b67" {
		return true
	}

	// Fallback heuristic: if color_transfer is missing but we have
	// 10-bit + bt2020 primaries, assume HDR (catches poorly-tagged content)
	if colorTransfer == "" && bitDepth >= 10 {
		primaries := strings.ToLower(colorPrimaries)
		if primaries == "bt2020" {
			return true
		}
	}

	return false
}

// isHEVCCodec returns true if the codec is HEVC/x265
func isHEVCCodec(codec string) bool {
	codec = strings.ToLower(codec)
	return codec == "hevc" || codec == "h265" || codec == "x265"
}

// isAV1Codec returns true if the codec is AV1
func isAV1Codec(codec string) bool {
	codec = strings.ToLower(codec)
	return codec == "av1" || codec == "libaom-av1" || codec == "libsvtav1"
}

// parseFrameRate parses a frame rate string like "30000/1001" or "30/1"
func parseFrameRate(s string) float64 {
	if s == "" || s == "0/0" {
		return 0
	}
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		f, _ := strconv.ParseFloat(s, 64)
		return f
	}
	num, _ := strconv.ParseFloat(parts[0], 64)
	den, _ := strconv.ParseFloat(parts[1], 64)
	if den == 0 {
		return 0
	}
	return num / den
}

// inferBitDepth attempts to determine bit depth from pixel format string
func inferBitDepth(pixFmt string) int {
	if pixFmt == "" {
		return 8 // Default to 8-bit if unknown
	}
	// Common 10-bit formats: yuv420p10le, yuv420p10be, p010le, etc.
	if strings.Contains(pixFmt, "10le") || strings.Contains(pixFmt, "10be") || strings.Contains(pixFmt, "p010") {
		return 10
	}
	// Common 12-bit formats: yuv420p12le, yuv420p12be, etc.
	if strings.Contains(pixFmt, "12le") || strings.Contains(pixFmt, "12be") {
		return 12
	}
	return 8
}

// IsVideoFile returns true if the file extension suggests a video file
func IsVideoFile(path string) bool {
	ext := strings.ToLower(path)
	videoExtensions := []string{
		".mkv", ".mp4", ".avi", ".mov", ".wmv", ".flv",
		".webm", ".m4v", ".mpeg", ".mpg", ".m2ts", ".ts",
	}
	for _, ve := range videoExtensions {
		if strings.HasSuffix(ext, ve) {
			return true
		}
	}
	return false
}

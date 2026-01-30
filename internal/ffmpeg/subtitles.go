package ffmpeg

import "strings"

// mkvCompatibleCodecs lists subtitle codecs that can be muxed to MKV.
// Based on FFmpeg's matroska.c ff_mkv_codec_tags mapping.
// See: https://github.com/FFmpeg/FFmpeg/blob/master/libavformat/matroska.c
var mkvCompatibleCodecs = map[string]bool{
	"subrip":             true, // S_TEXT/UTF8
	"srt":                true, // Alias for subrip
	"ass":                true, // S_TEXT/ASS
	"ssa":                true, // S_TEXT/SSA
	"text":               true, // S_TEXT/UTF8
	"dvd_subtitle":       true, // S_VOBSUB
	"dvb_subtitle":       true, // S_DVBSUB
	"hdmv_pgs_subtitle":  true, // S_HDMV/PGS (Blu-ray)
	"hdmv_text_subtitle": true, // S_HDMV/TEXTST
	"arib_caption":       true, // S_ARIBSUB (Japanese)
	"webvtt":             true, // D_WEBVTT/*
}

// IsMKVCompatible returns true if the subtitle codec can be muxed to MKV.
// Normalizes to lowercase for safety (matching isHEVCCodec/isAV1Codec pattern).
// Unknown codecs return false for safety (better to drop than fail transcode).
func IsMKVCompatible(codecName string) bool {
	return mkvCompatibleCodecs[strings.ToLower(codecName)]
}

// FilterMKVCompatible partitions subtitle streams into compatible and incompatible.
// Returns indices of compatible streams (for -map 0:N arguments) and codec names
// of dropped streams (for logging warnings to the user).
//
// IMPORTANT: Return value semantics for worker logic:
//   - nil input → nil output (no subtitle streams exist)
//   - non-nil input → non-nil output (possibly empty slice if all incompatible)
//
// The worker uses nil to mean "map all" and empty slice to mean "map none".
func FilterMKVCompatible(streams []SubtitleStream) (compatibleIndices []int, droppedCodecs []string) {
	if streams == nil {
		return nil, nil
	}

	// Pre-allocate to ensure we return empty slice, not nil, when all are incompatible
	compatibleIndices = make([]int, 0, len(streams))

	for _, s := range streams {
		if IsMKVCompatible(s.CodecName) {
			compatibleIndices = append(compatibleIndices, s.Index)
		} else {
			droppedCodecs = append(droppedCodecs, s.CodecName)
		}
	}
	return compatibleIndices, droppedCodecs
}

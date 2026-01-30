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

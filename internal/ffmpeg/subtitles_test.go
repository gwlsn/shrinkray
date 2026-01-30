package ffmpeg

import "testing"

func TestIsMKVCompatible(t *testing.T) {
	tests := []struct {
		codec    string
		expected bool
	}{
		// Compatible codecs (should be kept)
		{"subrip", true},
		{"srt", true},
		{"ass", true},
		{"ssa", true},
		{"text", true},
		{"dvd_subtitle", true},
		{"dvb_subtitle", true},
		{"hdmv_pgs_subtitle", true},
		{"hdmv_text_subtitle", true},
		{"arib_caption", true},
		{"webvtt", true},

		// Incompatible codecs (should be dropped)
		{"mov_text", false},
		{"tx3g", false},
		{"eia_608", false},
		{"c608", false},
		{"ttml", false},
		{"dvb_teletext", false},
		{"xsub", false},

		// Unknown codecs (treat as incompatible for safety)
		{"unknown_codec", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.codec, func(t *testing.T) {
			result := IsMKVCompatible(tt.codec)
			if result != tt.expected {
				t.Errorf("IsMKVCompatible(%q) = %v, want %v", tt.codec, result, tt.expected)
			}
		})
	}
}

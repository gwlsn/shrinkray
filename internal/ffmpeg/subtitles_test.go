package ffmpeg

import (
	"testing"
)

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

		// Case-insensitive matching
		{"SRT", true},
		{"SUBRIP", true},
		{"AsS", true},
		{"MOV_TEXT", false},

		// Whitespace handling
		{" subrip ", true},
		{" mov_text ", false},
		{"  ass  ", true},
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

func TestFilterMKVCompatible(t *testing.T) {
	tests := []struct {
		name             string
		streams          []SubtitleStream
		wantIndices      []int
		wantNilIndices   bool // true if expecting nil (no streams), false if expecting slice (possibly empty)
		wantDroppedCount int
	}{
		{
			name:             "nil input returns nil",
			streams:          nil,
			wantIndices:      nil,
			wantNilIndices:   true, // nil input → nil output
			wantDroppedCount: 0,
		},
		{
			name: "all compatible",
			streams: []SubtitleStream{
				{Index: 2, CodecName: "subrip"},
				{Index: 3, CodecName: "ass"},
			},
			wantIndices:      []int{2, 3},
			wantNilIndices:   false,
			wantDroppedCount: 0,
		},
		{
			name: "all incompatible returns empty slice not nil",
			streams: []SubtitleStream{
				{Index: 2, CodecName: "mov_text"},
				{Index: 3, CodecName: "eia_608"},
			},
			wantIndices:      []int{}, // CRITICAL: empty slice, NOT nil
			wantNilIndices:   false,   // Must be non-nil so worker knows filtering happened
			wantDroppedCount: 2,
		},
		{
			name: "mixed compatible and incompatible",
			streams: []SubtitleStream{
				{Index: 2, CodecName: "mov_text"},
				{Index: 3, CodecName: "subrip"},
				{Index: 4, CodecName: "eia_608"},
				{Index: 5, CodecName: "ass"},
			},
			wantIndices:      []int{3, 5},
			wantNilIndices:   false,
			wantDroppedCount: 2,
		},
		{
			name:             "empty input returns empty slice not nil",
			streams:          []SubtitleStream{},
			wantIndices:      []int{},
			wantNilIndices:   false, // non-nil input → non-nil output
			wantDroppedCount: 0,
		},
		{
			name: "duplicate incompatible codecs are deduplicated",
			streams: []SubtitleStream{
				{Index: 2, CodecName: "mov_text"},
				{Index: 3, CodecName: "mov_text"},
				{Index: 4, CodecName: "mov_text"},
			},
			wantIndices:      []int{},
			wantNilIndices:   false,
			wantDroppedCount: 1, // Only one "mov_text" in dropped list
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			indices, dropped := FilterMKVCompatible(tt.streams)

			// Check nil vs non-nil (critical for worker logic)
			if tt.wantNilIndices && indices != nil {
				t.Errorf("expected nil indices, got %v", indices)
			}
			if !tt.wantNilIndices && indices == nil {
				t.Errorf("expected non-nil indices (empty slice), got nil")
			}

			// Check indices content
			if len(indices) != len(tt.wantIndices) {
				t.Errorf("got %d indices, want %d", len(indices), len(tt.wantIndices))
			}
			for i, idx := range indices {
				if i < len(tt.wantIndices) && idx != tt.wantIndices[i] {
					t.Errorf("indices[%d] = %d, want %d", i, idx, tt.wantIndices[i])
				}
			}

			// Check dropped count
			if len(dropped) != tt.wantDroppedCount {
				t.Errorf("got %d dropped, want %d: %v", len(dropped), tt.wantDroppedCount, dropped)
			}
		})
	}
}

func TestFilterMKVCompatible_DroppedCodecContents(t *testing.T) {
	// Test that dropped codecs contain expected values in order
	streams := []SubtitleStream{
		{Index: 2, CodecName: "mov_text"},
		{Index: 3, CodecName: "subrip"},
		{Index: 4, CodecName: "eia_608"},
		{Index: 5, CodecName: "mov_text"}, // Duplicate - should be deduplicated
	}

	_, dropped := FilterMKVCompatible(streams)

	// Should have exactly 2 unique dropped codecs
	if len(dropped) != 2 {
		t.Fatalf("expected 2 dropped codecs, got %d: %v", len(dropped), dropped)
	}

	// Check order is preserved (first occurrence order)
	if dropped[0] != "mov_text" {
		t.Errorf("dropped[0] = %q, want %q", dropped[0], "mov_text")
	}
	if dropped[1] != "eia_608" {
		t.Errorf("dropped[1] = %q, want %q", dropped[1], "eia_608")
	}
}

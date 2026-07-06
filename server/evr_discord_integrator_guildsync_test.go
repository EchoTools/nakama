package server

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateRuneSafe(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		maxBytes int
		want     string
	}{
		{
			name:     "empty string",
			in:       "",
			maxBytes: 255,
			want:     "",
		},
		{
			name:     "shorter than limit is unchanged",
			in:       "Jett's Hangout!",
			maxBytes: 255,
			want:     "Jett's Hangout!",
		},
		{
			name:     "exactly at limit is unchanged",
			in:       strings.Repeat("a", 255),
			maxBytes: 255,
			want:     strings.Repeat("a", 255),
		},
		{
			name:     "one over limit is truncated to limit",
			in:       strings.Repeat("a", 256),
			maxBytes: 255,
			want:     strings.Repeat("a", 255),
		},
		{
			name:     "265 chars truncated to 255 (the Jett's Hangout incident shape)",
			in:       strings.Repeat("a", 265),
			maxBytes: 255,
			want:     strings.Repeat("a", 255),
		},
		{
			name:     "emoji straddling the boundary is dropped whole",
			in:       strings.Repeat("a", 253) + "\U0001F30A", // 253 bytes + 4-byte emoji = 257 bytes
			maxBytes: 255,
			want:     strings.Repeat("a", 253),
		},
		{
			name:     "emoji ending exactly at the boundary is kept",
			in:       strings.Repeat("a", 251) + "\U0001F30A", // 251 bytes + 4-byte emoji = 255 bytes
			maxBytes: 255,
			want:     strings.Repeat("a", 251) + "\U0001F30A",
		},
		{
			name:     "all-emoji string truncates on a rune boundary",
			in:       strings.Repeat("\U0001F30A", 100), // 400 bytes
			maxBytes: 255,
			want:     strings.Repeat("\U0001F30A", 63), // 63*4 = 252 bytes
		},
		{
			name:     "zero max yields empty",
			in:       "abc",
			maxBytes: 0,
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateRuneSafe(tt.in, tt.maxBytes)
			if got != tt.want {
				t.Errorf("truncateRuneSafe(%q, %d) = %q, want %q", tt.in, tt.maxBytes, got, tt.want)
			}
			if len(got) > tt.maxBytes {
				t.Errorf("result byte length %d exceeds max %d", len(got), tt.maxBytes)
			}
			if !utf8.ValidString(got) {
				t.Errorf("result is not valid UTF-8: %q", got)
			}
		})
	}
}

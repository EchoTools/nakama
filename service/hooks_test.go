package service

import (
	"strconv"
	"testing"
)

func TestIntent_MarshalText(t *testing.T) {
	tests := []struct {
		name   string
		intent Intent
		want   string
	}{
		{
			name:   "AllFalse",
			intent: Intent{},
			want:   strconv.QuoteToASCII(""),
		},
		{
			name:   "GuildMatchesTrue",
			intent: Intent{GuildMatches: true},
			want:   strconv.QuoteToASCII("guild_matches"),
		},
		{
			name:   "MatchesTrue",
			intent: Intent{Matches: true},
			want:   strconv.QuoteToASCII("matches"),
		},
		{
			name:   "StorageObjectsTrue",
			intent: Intent{StorageObjects: true},
			want:   strconv.QuoteToASCII("storage_objects"),
		},
		{
			name:   "MultipleTrue",
			intent: Intent{GuildMatches: true, Matches: true, StorageObjects: true},
			want:   strconv.QuoteToASCII("guild_matches,matches,storage_objects"),
		},
		{
			name:   "TwoTrue",
			intent: Intent{GuildMatches: true, Matches: true},
			want:   strconv.QuoteToASCII("guild_matches,matches"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.intent.MarshalText()
			if err != nil {
				t.Fatalf("MarshalText() error = %v, want nil", err)
			}
			if string(got) != tt.want {
				t.Errorf("MarshalText() = %q, want %q", got, tt.want)
			}
		})
	}
}

package server

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"
)

// discordMaxGuildNameChars / discordMaxGuildDescriptionChars are Discord's own
// limits, in characters. They bound what guildSync can ever be handed.
const (
	discordMaxGuildNameChars        = 100
	discordMaxGuildDescriptionChars = 300
)

func TestTruncateRuneSafe(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		maxChars int
		want     string
	}{
		{
			name:     "empty string",
			in:       "",
			maxChars: 255,
			want:     "",
		},
		{
			name:     "shorter than limit is unchanged",
			in:       "Jett's Hangout!",
			maxChars: 255,
			want:     "Jett's Hangout!",
		},
		{
			name:     "exactly at limit is unchanged",
			in:       strings.Repeat("a", 255),
			maxChars: 255,
			want:     strings.Repeat("a", 255),
		},
		{
			name:     "one over limit is truncated to limit",
			in:       strings.Repeat("a", 256),
			maxChars: 255,
			want:     strings.Repeat("a", 255),
		},
		{
			// Discord caps guild descriptions at 300 characters, so 300 is the
			// largest input guildSync can ever see. It must land at exactly 255
			// characters -- the VARCHAR(255) column counts characters.
			name:     "max-length Discord description truncated to 255 characters",
			in:       strings.Repeat("a", discordMaxGuildDescriptionChars),
			maxChars: 255,
			want:     strings.Repeat("a", 255),
		},
		{
			// The regression: a Discord guild NAME can never exceed 100
			// characters, so it can never violate VARCHAR(255) and must come
			// back untouched -- even when every character is a 4-byte emoji.
			name:     "max-length all-emoji Discord guild name is never truncated",
			in:       strings.Repeat("\U0001F30A", discordMaxGuildNameChars),
			maxChars: 255,
			want:     strings.Repeat("\U0001F30A", discordMaxGuildNameChars),
		},
		{
			name:     "emoji string at exactly the character limit is unchanged",
			in:       strings.Repeat("\U0001F30A", 255),
			maxChars: 255,
			want:     strings.Repeat("\U0001F30A", 255),
		},
		{
			name:     "emoji string over the character limit is cut on a rune boundary",
			in:       strings.Repeat("\U0001F30A", 256),
			maxChars: 255,
			want:     strings.Repeat("\U0001F30A", 255),
		},
		{
			name:     "mixed-width string counts characters not bytes",
			in:       strings.Repeat("\U0001F30A", 60) + strings.Repeat("a", 200), // 260 chars, 440 bytes
			maxChars: 255,
			want:     strings.Repeat("\U0001F30A", 60) + strings.Repeat("a", 195),
		},
		{
			name:     "zero max yields empty",
			in:       "abc",
			maxChars: 0,
			want:     "",
		},
		{
			name:     "negative max yields empty",
			in:       "abc",
			maxChars: -1,
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateRuneSafe(tt.in, tt.maxChars)
			if got != tt.want {
				t.Errorf("truncateRuneSafe(%q, %d) = %q, want %q", tt.in, tt.maxChars, got, tt.want)
			}
			if n := utf8.RuneCountInString(got); tt.maxChars >= 0 && n > tt.maxChars {
				t.Errorf("result character length %d exceeds max %d", n, tt.maxChars)
			}
			if !utf8.ValidString(got) {
				t.Errorf("result is not valid UTF-8: %q", got)
			}
		})
	}
}

// TestTruncateRuneSafeDistinguishesNamesSharingABytePrefix pins the uniqueness
// hazard: groups.name carries the UNIQUE constraint groups_name_key. Two guild
// names that share a 255-BYTE prefix but differ within the first 255
// CHARACTERS must not collapse to the same stored name, or the second guild's
// GroupCreate fails with runtime.ErrGroupNameInUse and it becomes a permanent
// orphan (and then a prune-leave candidate).
func TestTruncateRuneSafeDistinguishesNamesSharingABytePrefix(t *testing.T) {
	// 80 emoji = 320 bytes, so the first 255 bytes are identical in both.
	prefix := strings.Repeat("\U0001F30A", 80)
	a := truncateRuneSafe(prefix+" alpha", 255)
	b := truncateRuneSafe(prefix+" bravo", 255)

	if a == b {
		t.Fatalf("names sharing a 255-byte prefix collapsed to the same value %q; groups_name_key would reject the second guild", a)
	}
}

// TestGuildSyncRejectsUnnamedGuild pins the explicit guard: an unavailable
// guild stub carries an empty Name. groups.name is NOT NULL and UNIQUE, so
// syncing one would either fail or collide with every other unnamed guild.
// The refusal must be explicit rather than an incidental side effect of the
// owner lookup failing further down.
func TestGuildSyncRejectsUnnamedGuild(t *testing.T) {
	d := &DiscordIntegrator{}

	// Without the guard, execution reaches d.dg.State.User.ID and nil-derefs.
	// Recover so this reports as one failed test instead of taking the whole
	// test binary down and masking every other failure in the run.
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("guildSync panicked on a guild with an empty name instead of refusing it: %v", r)
			}
		}()
		err = d.guildSync(context.Background(), zap.NewNop(), &discordgo.Guild{ID: "1522261692355055849"}, false)
	}()

	if err == nil {
		t.Fatal("guildSync accepted a guild with an empty name; want an error")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Fatalf("guildSync error %q does not mention the missing name", err)
	}
}

package server

import (
	"encoding/json"
	"testing"
)

// TestMinimumDiscordAccountAge_LegacyKey is the reason the old key survives the
// rename.
//
// Guild metadata written before `minimum_account_age_days` became
// `minimum_discord_account_age_days` is still in storage. If the gate read only
// the new key it would see 0 for those guilds -- and 0 does not mean "no
// opinion", it means the gate is OFF. A rename that silently disables a
// security control on precisely the guilds that had configured it is a
// fail-open, so this pins that it does not happen.
func TestMinimumDiscordAccountAge_LegacyKey(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
	}{
		{
			// Metadata written before the rename. The whole point.
			name: "LegacyKeyOnly",
			raw:  `{"minimum_account_age_days": 30}`,
			want: 30,
		},
		{
			name: "NewKeyOnly",
			raw:  `{"minimum_discord_account_age_days": 14}`,
			want: 14,
		},
		{
			// Both present: the new key wins, so rewriting a guild's metadata
			// takes effect even before the stale key is cleaned out.
			name: "BothKeysNewWins",
			raw:  `{"minimum_discord_account_age_days": 14, "minimum_account_age_days": 30}`,
			want: 14,
		},
		{
			name: "NeitherKey",
			raw:  `{}`,
			want: 0,
		},
		{
			// An explicit zero on the new key falls back to the legacy value,
			// so a guild with a stored minimum_account_age_days keeps its gate
			// until that key is cleared too. This is deliberate and
			// fail-closed: zero is indistinguishable from unset on an int, so
			// preferring the legacy value cannot silently disable an
			// enforcement setting a guild had turned on.
			//
			// The consequence is real and is asserted, not glossed: writing 0
			// to the new key does NOT turn the gate off while the legacy key
			// is set. Do not "fix" the accessor to match a tidier reading --
			// that converts fail-closed precedence into fail-open.
			name: "NewKeyZeroWithLegacySet",
			raw:  `{"minimum_discord_account_age_days": 0, "minimum_account_age_days": 30}`,
			want: 30,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			md := &GroupMetadata{}
			if err := json.Unmarshal([]byte(tt.raw), md); err != nil {
				t.Fatalf("Unmarshal(%s) error = %v", tt.raw, err)
			}
			if got := md.MinimumDiscordAccountAge(); got != tt.want {
				t.Errorf("MinimumDiscordAccountAge() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestMinimumDiscordAccountAge_RoundTrip pins that the legacy key is not
// re-emitted once a guild has been migrated, so the stale key drains out of
// storage rather than lingering forever.
func TestMinimumDiscordAccountAge_RoundTrip(t *testing.T) {
	md := &GroupMetadata{MinimumDiscordAccountAgeDays: 30}

	encoded, err := json.Marshal(md)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if _, present := decoded["minimum_account_age_days"]; present {
		t.Error("marshalled metadata still carries the legacy key; it is omitempty and must drop when unset")
	}
	if _, present := decoded["minimum_discord_account_age_days"]; !present {
		t.Error("marshalled metadata is missing minimum_discord_account_age_days")
	}
}

// TestMinimumDiscordAccountAge_IsNotTheNakamaGate guards the confusion that
// made this rename worth doing: the two gates read different clocks and must
// stay independent.
func TestMinimumDiscordAccountAge_IsNotTheNakamaGate(t *testing.T) {
	md := &GroupMetadata{}
	if err := json.Unmarshal([]byte(`{"minimum_account_age_days": 30}`), md); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if got := md.MinimumDiscordAccountAge(); got != 30 {
		t.Errorf("MinimumDiscordAccountAge() = %d, want 30", got)
	}
	if md.MinimumNakamaAccountAgeDays != 0 {
		t.Errorf("MinimumNakamaAccountAgeDays = %d, want 0; the legacy key must not arm the EchoVR gate",
			md.MinimumNakamaAccountAgeDays)
	}
}

package server

import (
	"testing"

	"github.com/heroiclabs/nakama-common/runtime"
)

func TestAssignDivision(t *testing.T) {
	boundaries := []float64{15.0, 25.0, 35.0}
	names := []string{"Bronze", "Silver", "Gold", "Diamond"}
	newPlayerThreshold := 50

	tests := []struct {
		name        string
		mu          float64
		gamesPlayed int
		want        string
	}{
		// New player override: always lowest division regardless of mu
		{"new player low mu", 5.0, 10, "Bronze"},
		{"new player high mu", 40.0, 10, "Bronze"},
		{"new player zero games", 30.0, 0, "Bronze"},
		{"new player at threshold minus one", 25.0, 49, "Bronze"},

		// Veteran players: division based on mu
		{"veteran bronze low", 0.0, 100, "Bronze"},
		{"veteran bronze mid", 10.0, 100, "Bronze"},
		{"veteran bronze edge", 14.9, 100, "Bronze"},
		{"veteran silver at boundary", 15.0, 50, "Silver"},
		{"veteran silver mid", 20.0, 200, "Silver"},
		{"veteran silver edge", 24.9, 200, "Silver"},
		{"veteran gold at boundary", 25.0, 100, "Gold"},
		{"veteran gold mid", 30.0, 100, "Gold"},
		{"veteran gold edge", 34.9, 100, "Gold"},
		{"veteran diamond at boundary", 35.0, 100, "Diamond"},
		{"veteran diamond high", 50.0, 100, "Diamond"},

		// Edge: exactly at new player threshold
		{"at threshold exactly", 25.0, 50, "Gold"},

		// Negative mu
		{"negative mu veteran", -5.0, 100, "Bronze"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AssignDivision(tt.mu, tt.gamesPlayed, newPlayerThreshold, boundaries, names)
			if got != tt.want {
				t.Errorf("AssignDivision(mu=%v, gp=%d) = %q, want %q", tt.mu, tt.gamesPlayed, got, tt.want)
			}
		})
	}
}

func TestAssignDivision_CustomBoundaries(t *testing.T) {
	// Single boundary: two divisions
	boundaries := []float64{20.0}
	names := []string{"Low", "High"}

	tests := []struct {
		name string
		mu   float64
		want string
	}{
		{"below", 10.0, "Low"},
		{"at boundary", 20.0, "High"},
		{"above", 30.0, "High"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AssignDivision(tt.mu, 100, 50, boundaries, names)
			if got != tt.want {
				t.Errorf("AssignDivision(mu=%v) = %q, want %q", tt.mu, got, tt.want)
			}
		})
	}
}

func TestAssignDivision_EmptyBoundaries(t *testing.T) {
	// No boundaries: everyone goes to the single division
	got := AssignDivision(25.0, 100, 50, nil, []string{"Everyone"})
	if got != "Everyone" {
		t.Errorf("got %q, want %q", got, "Everyone")
	}
}

// divTestEntry implements runtime.MatchmakerEntry for division testing
type divTestEntry struct {
	ticket     string
	presence   runtime.Presence
	properties map[string]interface{}
}

func (m *divTestEntry) GetTicket() string                     { return m.ticket }
func (m *divTestEntry) GetPresence() runtime.Presence         { return m.presence }
func (m *divTestEntry) GetProperties() map[string]interface{} { return m.properties }
func (m *divTestEntry) GetPartyId() string                    { return "" }
func (m *divTestEntry) GetCreateTime() int64                  { return 0 }

// divTestPresence implements runtime.Presence for division testing
type divTestPresence struct {
	userID    string
	sessionID string
	username  string
}

func (m *divTestPresence) GetUserId() string                    { return m.userID }
func (m *divTestPresence) GetSessionId() string                 { return m.sessionID }
func (m *divTestPresence) GetNodeId() string                    { return "test" }
func (m *divTestPresence) GetUsername() string                   { return m.username }
func (m *divTestPresence) GetHidden() bool                      { return false }
func (m *divTestPresence) GetPersistence() bool                 { return false }
func (m *divTestPresence) GetStatus() string                    { return "" }
func (m *divTestPresence) GetReason() runtime.PresenceReason    { return 0 }

func newDivTestEntry(id string, division string, mu float64, gamesPlayed int) runtime.MatchmakerEntry {
	return &divTestEntry{
		ticket: "ticket-" + id,
		presence: &divTestPresence{
			userID:    "user-" + id,
			sessionID: "session-" + id,
			username:  "player-" + id,
		},
		properties: map[string]interface{}{
			"division":       division,
			"rating_mu":      mu,
			"games_played":   float64(gamesPlayed),
			"max_team_size":  float64(4),
			"count_multiple": float64(2),
		},
	}
}

func TestFilterEntriesByDivision(t *testing.T) {
	entries := []runtime.MatchmakerEntry{
		newDivTestEntry("1", "Bronze", 10.0, 100),
		newDivTestEntry("2", "Bronze", 12.0, 100),
		newDivTestEntry("3", "Silver", 20.0, 100),
		newDivTestEntry("4", "Gold", 30.0, 100),
		newDivTestEntry("5", "Gold", 32.0, 100),
		newDivTestEntry("6", "Gold", 33.0, 100),
	}

	result := FilterEntriesByDivision(entries)

	if len(result) != 3 {
		t.Fatalf("expected 3 divisions, got %d", len(result))
	}
	if len(result["Bronze"]) != 2 {
		t.Errorf("Bronze: expected 2 entries, got %d", len(result["Bronze"]))
	}
	if len(result["Silver"]) != 1 {
		t.Errorf("Silver: expected 1 entry, got %d", len(result["Silver"]))
	}
	if len(result["Gold"]) != 3 {
		t.Errorf("Gold: expected 3 entries, got %d", len(result["Gold"]))
	}
}

func TestFilterEntriesByDivision_Empty(t *testing.T) {
	result := FilterEntriesByDivision(nil)
	if len(result) != 0 {
		t.Errorf("expected empty map, got %d divisions", len(result))
	}
}

func TestFilterEntriesByDivision_NoDivisionProperty(t *testing.T) {
	entry := &divTestEntry{
		ticket:   "ticket-x",
		presence: &divTestPresence{userID: "u", sessionID: "s", username: "n"},
		properties: map[string]interface{}{
			"max_team_size": float64(4),
		},
	}
	result := FilterEntriesByDivision([]runtime.MatchmakerEntry{entry})
	if len(result) != 1 {
		t.Fatalf("expected 1 group, got %d", len(result))
	}
	if len(result[""]) != 1 {
		t.Errorf("expected 1 entry in empty-key group, got %d", len(result[""]))
	}
}

func TestCrossDivisionCandidatesNeverFormed(t *testing.T) {
	// Build entries from two divisions, enough for one match each
	var entries []runtime.MatchmakerEntry
	for i := 0; i < 8; i++ {
		id := string(rune('a' + i))
		entries = append(entries, newDivTestEntry(id, "Bronze", 10.0, 100))
	}
	for i := 0; i < 8; i++ {
		id := string(rune('A' + i))
		entries = append(entries, newDivTestEntry(id, "Gold", 30.0, 100))
	}

	grouped := FilterEntriesByDivision(entries)

	// Run groupEntriesSequentially on each division separately, as the feature does
	for division, divEntries := range grouped {
		candidates := groupEntriesSequentially(divEntries)
		for _, candidate := range candidates {
			for _, e := range candidate {
				d, _ := e.GetProperties()["division"].(string)
				if d != division {
					t.Errorf("candidate in division %q contains entry with division %q", division, d)
				}
			}
		}
	}
}

func TestCrossDivisionCandidatesNeverFormed_MixedInput(t *testing.T) {
	entries := []runtime.MatchmakerEntry{
		newDivTestEntry("b1", "Bronze", 10.0, 100),
		newDivTestEntry("g1", "Gold", 30.0, 100),
		newDivTestEntry("b2", "Bronze", 12.0, 100),
		newDivTestEntry("g2", "Gold", 32.0, 100),
	}

	grouped := FilterEntriesByDivision(entries)

	for division, divEntries := range grouped {
		candidates := groupEntriesSequentially(divEntries)
		for _, candidate := range candidates {
			for _, e := range candidate {
				d, _ := e.GetProperties()["division"].(string)
				if d != division {
					t.Errorf("cross-division leak: candidate division=%q, entry division=%q", division, d)
				}
			}
		}
	}
}

func TestHardDivisionsDisabledByDefault(t *testing.T) {
	settings := GlobalMatchmakingSettings{}
	if settings.HardDivisionsEnabled() {
		t.Error("HardDivisionsEnabled should return false by default (nil pointer)")
	}
}

func TestHardDivisionsEnabledExplicitly(t *testing.T) {
	enabled := true
	settings := GlobalMatchmakingSettings{
		EnableHardDivisions: &enabled,
	}
	if !settings.HardDivisionsEnabled() {
		t.Error("HardDivisionsEnabled should return true when explicitly enabled")
	}
}

func TestHardDivisionsDisabledExplicitly(t *testing.T) {
	disabled := false
	settings := GlobalMatchmakingSettings{
		EnableHardDivisions: &disabled,
	}
	if settings.HardDivisionsEnabled() {
		t.Error("HardDivisionsEnabled should return false when explicitly disabled")
	}
}

func TestAssignPartyDivision(t *testing.T) {
	boundaries := []float64{15.0, 25.0, 35.0}
	names := []string{"Bronze", "Silver", "Gold", "Diamond"}
	newPlayerThreshold := 50

	tests := []struct {
		name    string
		members []struct {
			mu float64
			gp int
		}
		want string
	}{
		{
			"solo player",
			[]struct {
				mu float64
				gp int
			}{{20.0, 100}},
			"Silver",
		},
		{
			"party with mixed divisions plays at highest",
			[]struct {
				mu float64
				gp int
			}{{10.0, 100}, {30.0, 100}},
			"Gold",
		},
		{
			"party with diamond player",
			[]struct {
				mu float64
				gp int
			}{{10.0, 100}, {40.0, 100}},
			"Diamond",
		},
		{
			"party where new player is bronze but party plays at veteran highest",
			[]struct {
				mu float64
				gp int
			}{{40.0, 10}, {20.0, 100}},
			"Silver",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			divisions := make([]string, len(tt.members))
			for i, m := range tt.members {
				divisions[i] = AssignDivision(m.mu, m.gp, newPlayerThreshold, boundaries, names)
			}
			got := HighestDivision(divisions, names)
			if got != tt.want {
				t.Errorf("HighestDivision(%v) = %q, want %q", divisions, got, tt.want)
			}
		})
	}
}

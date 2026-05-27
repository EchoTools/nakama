package server

import (
	"fmt"
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

func TestAssignDivision_MoreBoundariesThanNames(t *testing.T) {
	// Misconfigured: 3 boundaries but only 2 names. Should not panic.
	boundaries := []float64{10.0, 20.0, 30.0}
	names := []string{"Low", "High"}

	got := AssignDivision(5.0, 100, 50, boundaries, names)
	if got != "Low" {
		t.Errorf("got %q, want %q", got, "Low")
	}
	got = AssignDivision(15.0, 100, 50, boundaries, names)
	if got != "High" {
		t.Errorf("got %q, want %q for mu above clamped boundary", got, "High")
	}
}

func TestHighestDivision_UnknownDivisions(t *testing.T) {
	names := []string{"Bronze", "Silver", "Gold"}

	// All unknown divisions should return empty string.
	got := HighestDivision([]string{"Fake", "Invalid"}, names)
	if got != "" {
		t.Errorf("got %q, want empty string for all unknown divisions", got)
	}

	// Mix of unknown and known: should return the known one.
	got = HighestDivision([]string{"Fake", "Silver", "Invalid"}, names)
	if got != "Silver" {
		t.Errorf("got %q, want %q", got, "Silver")
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

func (m *divTestPresence) GetUserId() string                 { return m.userID }
func (m *divTestPresence) GetSessionId() string              { return m.sessionID }
func (m *divTestPresence) GetNodeId() string                 { return "test" }
func (m *divTestPresence) GetUsername() string               { return m.username }
func (m *divTestPresence) GetHidden() bool                   { return false }
func (m *divTestPresence) GetPersistence() bool              { return false }
func (m *divTestPresence) GetStatus() string                 { return "" }
func (m *divTestPresence) GetReason() runtime.PresenceReason { return 0 }

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

// ============================================================================
// Division Mismatch Party Matchmaking Bug: Behavioral Acceptance Tests
// ============================================================================
//
// Bug: When hard divisions are enabled, parties with mixed-skill members
// (e.g., Bronze + Diamond) can't find matches. The party is assigned to the
// highest member's division. The division-separated queues are too small to
// form matches, and wait-time skill range expansion doesn't apply when hard
// divisions are on.
//
// These tests demonstrate the bug by showing:
// 1. A mixed party gets assigned to the highest member's division
// 2. FilterEntriesByDivision separates mixed parties into only the highest division pool
// 3. Small division-separated queues can't form complete matches
// 4. Cross-division matching is impossible when hard divisions are enabled

func TestDivisionMismatchPartyBug_MixedPartyAssignedToHighest(t *testing.T) {
	// BUG TEST 1: A party with Bronze (mu=10) + Diamond (mu=40) members
	// gets assigned to Diamond division (highest member).
	// This is correct behavior for party assignment, but the bug manifests
	// downstream when hard divisions restrict queuing to only the Diamond pool.

	boundaries := []float64{15.0, 25.0, 35.0}
	names := []string{"Bronze", "Silver", "Gold", "Diamond"}
	newPlayerThreshold := 50

	// Create a mixed party: one Bronze, one Diamond
	bronzePlayer := struct {
		mu float64
		gp int
	}{10.0, 100} // mu=10 -> Bronze

	diamondPlayer := struct {
		mu float64
		gp int
	}{40.0, 100} // mu=40 -> Diamond

	// Assign individual divisions
	bronzeDivision := AssignDivision(bronzePlayer.mu, bronzePlayer.gp, newPlayerThreshold, boundaries, names)
	diamondDivision := AssignDivision(diamondPlayer.mu, diamondPlayer.gp, newPlayerThreshold, boundaries, names)

	if bronzeDivision != "Bronze" {
		t.Errorf("Bronze player: expected Bronze, got %q", bronzeDivision)
	}
	if diamondDivision != "Diamond" {
		t.Errorf("Diamond player: expected Diamond, got %q", diamondDivision)
	}

	// Party gets assigned to highest member's division
	partyDivision := HighestDivision([]string{bronzeDivision, diamondDivision}, names)
	if partyDivision != "Diamond" {
		t.Errorf("Mixed party assignment: expected Diamond, got %q", partyDivision)
	}
}

func TestDivisionMismatchPartyBug_FilterEntriesBySeparatesDivisionsCompletely(t *testing.T) {
	// BUG TEST 2: When hard divisions are enabled, FilterEntriesByDivision
	// separates entries by their division property. If a party (single ticket)
	// is assigned to Diamond, both members will have division="Diamond" in their
	// properties. A Bronze member whose skill is being hidden will appear as
	// Diamond in the properties, effectively erasing their true skill level.
	//
	// Demonstration: Create entries with mixed-division parties. When filtered,
	// they separate into pure division pools with no cross-division mixing.

	// Create a mixed party: simulate what would happen if we tried to put
	// mixed-skill players together with hard divisions enabled.
	// Party ticket is "party-123", but both members marked as Diamond (highest)
	entries := []runtime.MatchmakerEntry{
		// First, a pure Bronze pool (3 players)
		newDivTestEntry("b1", "Bronze", 10.0, 100),
		newDivTestEntry("b2", "Bronze", 12.0, 100),
		newDivTestEntry("b3", "Bronze", 14.0, 100),
		// Pure Diamond pool (2 players)
		newDivTestEntry("d1", "Diamond", 40.0, 100),
		newDivTestEntry("d2", "Diamond", 42.0, 100),
	}

	grouped := FilterEntriesByDivision(entries)

	// Verify strict separation: no cross-division leakage
	if len(grouped) != 2 {
		t.Fatalf("expected 2 divisions (Bronze, Diamond), got %d", len(grouped))
	}

	bronzeCount := len(grouped["Bronze"])
	diamondCount := len(grouped["Diamond"])

	if bronzeCount != 3 {
		t.Errorf("Bronze pool: expected 3 entries, got %d", bronzeCount)
	}
	if diamondCount != 2 {
		t.Errorf("Diamond pool: expected 2 entries, got %d", diamondCount)
	}

	// Verify no entry in Diamond pool came from Bronze
	for _, entry := range grouped["Diamond"] {
		div, _ := entry.GetProperties()["division"].(string)
		if div != "Diamond" {
			t.Errorf("Diamond pool leaked non-Diamond entry: division=%q", div)
		}
	}
}

func TestDivisionMismatchPartyBug_SmallDividedQueuesCannotFormMatches(t *testing.T) {
	// BUG TEST 3: A queue with 4 Bronze + 5 Diamond players produces
	// candidates in separate pools (4 and 5), neither big enough for a full
	// match (need 8 players total for a 4v4 match with maxTeamSize=4).

	// Simulate a divided queue
	var entries []runtime.MatchmakerEntry

	// Bronze pool: 4 players (not enough for 4v4)
	for i := 1; i <= 4; i++ {
		entries = append(entries, newDivTestEntry(
			fmt.Sprintf("b%d", i),
			"Bronze",
			10.0+float64(i),
			100,
		))
	}

	// Diamond pool: 5 players (not enough for 4v4)
	for i := 1; i <= 5; i++ {
		entries = append(entries, newDivTestEntry(
			fmt.Sprintf("d%d", i),
			"Diamond",
			40.0+float64(i),
			100,
		))
	}

	// Simulate hard divisions behavior: filter by division first
	grouped := FilterEntriesByDivision(entries)

	// Count total candidates across all divisions
	totalCandidates := 0
	for division, divEntries := range grouped {
		candidates := groupEntriesSequentially(divEntries)
		totalCandidates += len(candidates)

		// Each candidate needs maxCount (8) players minimum for a 4v4 match
		for _, candidate := range candidates {
			if len(candidate) < 8 {
				t.Logf("Division %q: candidate too small (%d players, need 8 minimum for 4v4)",
					division, len(candidate))
			}
		}
	}

	// Verify that neither Bronze nor Diamond pool can form a complete match
	if len(grouped["Bronze"]) != 4 {
		t.Fatalf("Bronze pool size: expected 4, got %d", len(grouped["Bronze"]))
	}
	if len(grouped["Diamond"]) != 5 {
		t.Fatalf("Diamond pool size: expected 5, got %d", len(grouped["Diamond"]))
	}

	// Both pools are undersized (< 8)
	if len(grouped["Bronze"]) >= 8 {
		t.Errorf("BUG: Bronze pool is large enough for a match; test expectations need revision")
	}
	if len(grouped["Diamond"]) >= 8 {
		t.Errorf("BUG: Diamond pool is large enough for a match; test expectations need revision")
	}

	t.Logf("Division mismatch demonstrated: 4 Bronze + 5 Diamond = 9 total players, "+
		"but separated into pools of %d and %d (both < 8 min match size)",
		len(grouped["Bronze"]), len(grouped["Diamond"]))
}

func TestDivisionMismatchPartyBug_NoMatchBetweenDivisions(t *testing.T) {
	// BUG TEST 4: A mixed-division party placed in Diamond queue can't match
	// against pure Bronze players (they're in a different pool). Cross-division
	// matching is impossible when hard divisions are on.

	// Setup: One party with 4 players (2 Bronze + 2 Diamond on same ticket)
	// Against: 4 pure Bronze solos in the queue
	// Expected with hard divisions: NO MATCH (separate pools)
	// Expected without hard divisions: could form a match (same pool)

	// Create party ticket with mixed members (both marked as highest division)
	partyTicket := "party-mixed-skill"
	partyBronzeMu := 10.0
	partyDiamondMu := 40.0

	// Party is assigned to Diamond (highest member)
	partyDivision := "Diamond"

	// Create party entries (would both be marked "Diamond" in hard divisions mode)
	partyEntries := []runtime.MatchmakerEntry{
		&divTestEntry{
			ticket: partyTicket,
			presence: &divTestPresence{
				userID:    "party-user-1",
				sessionID: "session-1",
				username:  "party-player-1",
			},
			properties: map[string]interface{}{
				"division":       partyDivision, // Both marked as highest division
				"rating_mu":      partyBronzeMu, // True skill ignored
				"max_team_size":  float64(4),
				"count_multiple": float64(2),
			},
		},
		&divTestEntry{
			ticket: partyTicket,
			presence: &divTestPresence{
				userID:    "party-user-2",
				sessionID: "session-2",
				username:  "party-player-2",
			},
			properties: map[string]interface{}{
				"division":       partyDivision, // Both marked as highest division
				"rating_mu":      partyDiamondMu,
				"max_team_size":  float64(4),
				"count_multiple": float64(2),
			},
		},
	}

	// Create pure Bronze solos (4 players, separate tickets)
	var bronzeEntries []runtime.MatchmakerEntry
	for i := 1; i <= 4; i++ {
		bronzeEntries = append(bronzeEntries, newDivTestEntry(
			fmt.Sprintf("bronze-solo-%d", i),
			"Bronze",
			10.0+float64(i),
			100,
		))
	}

	// Combine all entries and filter by division (hard divisions mode)
	allEntries := append(partyEntries, bronzeEntries...)
	grouped := FilterEntriesByDivision(allEntries)

	// Verify: party and bronze solos end up in different pools
	diamondPool := grouped["Diamond"]
	bronzePool := grouped["Bronze"]

	if len(diamondPool) == 0 {
		t.Errorf("Diamond pool is empty (expected to contain the mixed party)")
	}
	if len(bronzePool) != 4 {
		t.Errorf("Bronze pool: expected 4 solos, got %d", len(bronzePool))
	}

	// Check that Diamond pool contains the party ticket
	found := false
	for _, entry := range diamondPool {
		if entry.GetTicket() == partyTicket {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Party ticket %q not found in Diamond pool", partyTicket)
	}

	t.Logf("Cross-division match prevention demonstrated: "+
		"4-player mixed party in Diamond pool (size %d) cannot match "+
		"4 pure Bronze solos in Bronze pool (size %d)",
		len(diamondPool), len(bronzePool))
}

func TestDivisionMismatchPartyBug_WaitTimeExpansionBypassedByHardDivisions(t *testing.T) {
	// BUG TEST 5: Wait-time skill range expansion doesn't apply when hard
	// divisions are enabled. Even if a player waits a long time, they remain
	// restricted to their division's small pool.
	//
	// This test demonstrates the structural issue: hard divisions filter entries
	// at the top level, so wait-time expansion logic (which would normally relax
	// skill matching) never gets a chance to pull from other divisions.

	// Create a scenario: Gold division has 3 players waiting (too small for a match)
	// Silver division has many players
	// Normally, after wait time, a Gold player could match into Silver.
	// With hard divisions, they remain stuck in Gold pool.

	var entries []runtime.MatchmakerEntry

	// Gold pool: only 3 players (insufficient for 4v4 match of 8)
	for i := 1; i <= 3; i++ {
		entries = append(entries, newDivTestEntry(
			fmt.Sprintf("gold-%d", i),
			"Gold",
			30.0+float64(i),
			100,
		))
	}

	// Silver pool: 10 players (sufficient for matches)
	for i := 1; i <= 10; i++ {
		entries = append(entries, newDivTestEntry(
			fmt.Sprintf("silver-%d", i),
			"Silver",
			20.0+float64(i),
			100,
		))
	}

	// When hard divisions filter, Gold players are isolated from Silver pool
	grouped := FilterEntriesByDivision(entries)

	goldCount := len(grouped["Gold"])
	silverCount := len(grouped["Silver"])

	if goldCount != 3 {
		t.Fatalf("Gold pool: expected 3, got %d", goldCount)
	}
	if silverCount != 10 {
		t.Fatalf("Silver pool: expected 10, got %d", silverCount)
	}

	// Gold players cannot access Silver pool entries even after long wait
	// (no mechanism in hard divisions to relax the boundary)
	if goldCount < 8 {
		t.Logf("Hard divisions isolation: %d Gold players are stuck waiting "+
			"even though %d Silver players are available nearby in skill range",
			goldCount, silverCount)
	}
}

func TestDivisionMismatchPartyBug_MixedPartyWithNewPlayerEdgeCase(t *testing.T) {
	// BUG TEST 6: Edge case where a new player (who would normally be Bronze)
	// joins a Diamond veteran. The party gets assigned to Diamond. The new
	// player's true skill (very low) is masked by the party's division assignment.

	boundaries := []float64{15.0, 25.0, 35.0}
	names := []string{"Bronze", "Silver", "Gold", "Diamond"}
	newPlayerThreshold := 50

	// New player with very few games (would be Bronze if aged)
	newPlayer := struct {
		mu float64
		gp int
	}{5.0, 10} // mu=5 but only 10 games -> Bronze (new player rule)

	// Diamond veteran
	diamondVeteran := struct {
		mu float64
		gp int
	}{40.0, 200}

	// Assign divisions
	newPlayerDivision := AssignDivision(newPlayer.mu, newPlayer.gp, newPlayerThreshold, boundaries, names)
	veteranDivision := AssignDivision(diamondVeteran.mu, diamondVeteran.gp, newPlayerThreshold, boundaries, names)

	if newPlayerDivision != "Bronze" {
		t.Errorf("New player: expected Bronze (forced for < 50 games), got %q", newPlayerDivision)
	}
	if veteranDivision != "Diamond" {
		t.Errorf("Veteran: expected Diamond, got %q", veteranDivision)
	}

	// Party gets assigned to highest
	partyDivision := HighestDivision([]string{newPlayerDivision, veteranDivision}, names)
	if partyDivision != "Diamond" {
		t.Errorf("Mixed party: expected Diamond, got %q", partyDivision)
	}

	// Result: new player (5.0 mu) placed in Diamond queue, isolated from
	// Bronze players with similar skill
	t.Logf("New player edge case: player with %.1f mu forced into Diamond "+
		"due to party with Diamond veteran; isolated from Bronze players",
		newPlayer.mu)
}

package server

import (
	"fmt"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

// makeEntry creates a MatchmakerEntry with the given mu and games_played.
func makeEntry(id int, mu float64, gamesPlayed int) *MatchmakerEntry {
	sid := uuid.NewV5(uuid.Nil, fmt.Sprintf("bias-%d", id))
	return &MatchmakerEntry{
		Ticket: uuid.NewV5(uuid.Nil, fmt.Sprintf("bias-ticket-%d", id)).String(),
		Presence: &MatchmakerPresence{
			UserId:    sid.String(),
			SessionId: sid.String(),
			Username:  fmt.Sprintf("player_%d", id),
			SessionID: sid,
		},
		Properties: map[string]any{
			"rating_mu":    mu,
			"games_played": float64(gamesPlayed),
		},
	}
}

func isNewInSlice(entries []runtime.MatchmakerEntry, threshold int) bool {
	for _, e := range entries {
		if IsNewPlayer(e, threshold) {
			return true
		}
	}
	return false
}

func TestNewPlayerTeamBias_SwapsNewPlayerToStrongerTeam(t *testing.T) {
	threshold := 50

	// Blue team: veteran(20) + new player(19) = 39
	// Orange team: veteran(22) + veteran(21) = 43
	// Orange is stronger. New player is on weaker team (blue).
	// Swapping new player (mu=19) with closest veteran on orange (mu=21, diff=2):
	//   New blue = 20 + 21 = 41, New orange = 22 + 19 = 41. Imbalance = 0. Better than 4.
	match := []runtime.MatchmakerEntry{
		makeEntry(1, 20.0, 200), // veteran
		makeEntry(2, 19.0, 5),   // new player
		makeEntry(3, 22.0, 200), // veteran
		makeEntry(4, 21.0, 150), // veteran
	}
	teamSize := 2

	result := ApplyNewPlayerTeamBias(match, teamSize, threshold)

	// New player should be on the stronger team (orange, indices 2-3).
	if !isNewInSlice(result[teamSize:], threshold) {
		t.Errorf("expected new player to be swapped to stronger team (orange)")
	}
}

func TestNewPlayerTeamBias_NoSwapWhenAlreadyOnStronger(t *testing.T) {
	threshold := 50
	// Blue team is stronger and already has the new player.
	match := []runtime.MatchmakerEntry{
		makeEntry(1, 30.0, 200), // veteran
		makeEntry(2, 15.0, 5),   // new player
		makeEntry(3, 18.0, 200), // veteran
		makeEntry(4, 12.0, 150), // veteran
	}
	teamSize := 2

	// Blue = 45, Orange = 30. Blue is stronger. New player already on blue.
	result := ApplyNewPlayerTeamBias(match, teamSize, threshold)

	if !isNewInSlice(result[:teamSize], threshold) {
		t.Errorf("expected new player to remain on stronger team (blue)")
	}
}

func TestNewPlayerTeamBias_NoSwapWhenWouldWorsenBalance(t *testing.T) {
	threshold := 50
	// Blue = veteran(30) + new(10) = 40
	// Orange = veteran(25) + veteran(22) = 47
	// Swapping new(10) with closest orange vet(22): new blue=30+22=52, new orange=25+10=35
	// New imbalance = 17, worse than current 7. No swap.
	match := []runtime.MatchmakerEntry{
		makeEntry(1, 30.0, 200),
		makeEntry(2, 10.0, 5),   // new player with very different mu
		makeEntry(3, 25.0, 200),
		makeEntry(4, 22.0, 150),
	}
	teamSize := 2

	original := make([]string, len(match))
	for i, e := range match {
		original[i] = e.GetPresence().GetSessionId()
	}

	result := ApplyNewPlayerTeamBias(match, teamSize, threshold)

	for i, e := range result {
		if e.GetPresence().GetSessionId() != original[i] {
			t.Errorf("expected no swap (would worsen balance), but position %d changed", i)
		}
	}
}

func TestNewPlayerTeamBias_MultipleNewPlayers(t *testing.T) {
	threshold := 50
	// Blue team: vet(20) + new(18) + new(17) + new(16) = 71
	// Orange team: vet(22) + vet(21) + vet(19) + vet(18.5) = 80.5
	// Orange is stronger. Multiple new players on weaker team.
	match := []runtime.MatchmakerEntry{
		makeEntry(1, 20.0, 200),
		makeEntry(2, 18.0, 5),
		makeEntry(3, 17.0, 10),
		makeEntry(4, 16.0, 3),
		makeEntry(5, 22.0, 200),
		makeEntry(6, 21.0, 150),
		makeEntry(7, 19.0, 120),
		makeEntry(8, 18.5, 100),
	}
	teamSize := 4

	result := ApplyNewPlayerTeamBias(match, teamSize, threshold)

	// Count new players on orange team after bias.
	orangeNew := 0
	for _, e := range result[teamSize:] {
		if IsNewPlayer(e, threshold) {
			orangeNew++
		}
	}

	// At least one new player should have moved to orange (the originally stronger team).
	if orangeNew == 0 {
		t.Errorf("expected at least one new player to be moved to stronger team (orange), got 0")
	}
}

func TestNewPlayerTeamBias_DisabledByZeroThreshold(t *testing.T) {
	match := []runtime.MatchmakerEntry{
		makeEntry(1, 20.0, 200),
		makeEntry(2, 19.0, 5),
		makeEntry(3, 22.0, 200),
		makeEntry(4, 21.0, 150),
	}
	teamSize := 2

	original := make([]string, len(match))
	for i, e := range match {
		original[i] = e.GetPresence().GetSessionId()
	}

	// threshold=0 means no one is "new"
	result := ApplyNewPlayerTeamBias(match, teamSize, 0)

	for i, e := range result {
		if e.GetPresence().GetSessionId() != original[i] {
			t.Errorf("expected match unchanged when bias disabled, but position %d changed", i)
		}
	}
}

func TestNewPlayerTeamBias_NoNewPlayers(t *testing.T) {
	threshold := 50
	match := []runtime.MatchmakerEntry{
		makeEntry(1, 30.0, 200),
		makeEntry(2, 20.0, 100),
		makeEntry(3, 25.0, 200),
		makeEntry(4, 22.0, 150),
	}
	teamSize := 2

	original := make([]string, len(match))
	for i, e := range match {
		original[i] = e.GetPresence().GetSessionId()
	}

	result := ApplyNewPlayerTeamBias(match, teamSize, threshold)

	for i, e := range result {
		if e.GetPresence().GetSessionId() != original[i] {
			t.Errorf("expected match unchanged with no new players, but position %d changed", i)
		}
	}
}

func TestNewPlayerTeamBias_EqualTeams(t *testing.T) {
	threshold := 50
	// Both teams equal — no swap needed.
	match := []runtime.MatchmakerEntry{
		makeEntry(1, 25.0, 200),
		makeEntry(2, 20.0, 5), // new player
		makeEntry(3, 25.0, 200),
		makeEntry(4, 20.0, 150),
	}
	teamSize := 2

	original := make([]string, len(match))
	for i, e := range match {
		original[i] = e.GetPresence().GetSessionId()
	}

	result := ApplyNewPlayerTeamBias(match, teamSize, threshold)

	for i, e := range result {
		if e.GetPresence().GetSessionId() != original[i] {
			t.Errorf("expected no swap when teams are equal, but position %d changed", i)
		}
	}
}

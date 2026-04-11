package server

import (
	"github.com/heroiclabs/nakama-common/runtime"
)

// HasSuspensionHistory returns true if the matchmaker entry has the
// has_suspension_history string property set to "true".
func HasSuspensionHistory(entry runtime.MatchmakerEntry) bool {
	props := entry.GetProperties()
	v, ok := props["has_suspension_history"].(string)
	return ok && v == "true"
}

// CandidateContainsToxicNewPlayerMix returns true if the candidate contains
// BOTH a new player (games_played < threshold) AND a player with suspension
// history. Enforcers/global operators are exempt at ticket creation time
// (their has_suspension_history is always "false").
func CandidateContainsToxicNewPlayerMix(candidate []runtime.MatchmakerEntry, newPlayerThreshold int) bool {
	if newPlayerThreshold <= 0 {
		return false
	}

	hasNewPlayer := false
	hasToxicPlayer := false

	for _, entry := range candidate {
		if IsNewPlayer(entry, newPlayerThreshold) {
			hasNewPlayer = true
		}
		if HasSuspensionHistory(entry) {
			hasToxicPlayer = true
		}
		if hasNewPlayer && hasToxicPlayer {
			return true
		}
	}
	return false
}

// FilterToxicNewPlayerCandidates nils out candidates that contain both a new
// player and a player with suspension history. Returns the number of candidates
// filtered.
func FilterToxicNewPlayerCandidates(candidates [][]runtime.MatchmakerEntry, newPlayerThreshold int) int {
	filtered := 0
	for i, candidate := range candidates {
		if candidate == nil {
			continue
		}
		if CandidateContainsToxicNewPlayerMix(candidate, newPlayerThreshold) {
			candidates[i] = nil
			filtered++
		}
	}
	return filtered
}

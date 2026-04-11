package server

import (
	"github.com/heroiclabs/nakama-common/runtime"
)

// TeamSplit describes which entry indices belong to each team.
type TeamSplit struct {
	BlueIndices   []int
	OrangeIndices []int
}

// ScoreTeamComposition scores a pair of teams based on archetype diversity.
// Higher scores indicate better team compositions. The scoring rules:
//   - +2 per team that has at least one striker
//   - +1 per team that has at least one playmaker
//   - -2 per team that has 2+ rookies
//   - -1 if a team has a new player but no striker or playmaker teammate
func ScoreTeamComposition(team1Archetypes, team2Archetypes []string, team1HasNewPlayer, team2HasNewPlayer bool) int {
	return scoreTeam(team1Archetypes, team1HasNewPlayer) + scoreTeam(team2Archetypes, team2HasNewPlayer)
}

func scoreTeam(archetypes []string, hasNewPlayer bool) int {
	var score int
	var hasStriker, hasPlaymaker bool
	var rookieCount int

	for _, a := range archetypes {
		switch a {
		case ArchetypeStriker:
			hasStriker = true
		case ArchetypePlaymaker:
			hasPlaymaker = true
		case ArchetypeRookie:
			rookieCount++
		}
	}

	if hasStriker {
		score += 2
	}
	if hasPlaymaker {
		score++
	}
	if rookieCount >= 2 {
		score -= 2
	}
	if hasNewPlayer && !hasStriker && !hasPlaymaker {
		score--
	}

	return score
}

// SelectBestTeamSplit picks the team split with the highest composition score.
// If splits is empty, returns a zero-value TeamSplit.
func SelectBestTeamSplit(entries []runtime.MatchmakerEntry, splits []TeamSplit, newPlayerThreshold int) TeamSplit {
	if len(splits) == 0 {
		return TeamSplit{}
	}

	bestIdx := 0
	bestScore := scoreTeamSplitComposition(entries, splits[0], newPlayerThreshold)

	for i := 1; i < len(splits); i++ {
		s := scoreTeamSplitComposition(entries, splits[i], newPlayerThreshold)
		if s > bestScore {
			bestScore = s
			bestIdx = i
		}
	}

	return splits[bestIdx]
}

// scoreTeamSplitComposition extracts archetypes from entries by index and scores.
func scoreTeamSplitComposition(entries []runtime.MatchmakerEntry, split TeamSplit, newPlayerThreshold int) int {
	team1Archetypes := make([]string, len(split.BlueIndices))
	team2Archetypes := make([]string, len(split.OrangeIndices))
	var team1HasNew, team2HasNew bool

	for i, idx := range split.BlueIndices {
		team1Archetypes[i] = entryArchetype(entries[idx])
		if IsNewPlayer(entries[idx], newPlayerThreshold) {
			team1HasNew = true
		}
	}
	for i, idx := range split.OrangeIndices {
		team2Archetypes[i] = entryArchetype(entries[idx])
		if IsNewPlayer(entries[idx], newPlayerThreshold) {
			team2HasNew = true
		}
	}

	return ScoreTeamComposition(team1Archetypes, team2Archetypes, team1HasNew, team2HasNew)
}

// entryArchetype reads the "archetype" string property from a matchmaker entry.
// Returns ArchetypeRookie if not set.
func entryArchetype(entry runtime.MatchmakerEntry) string {
	props := entry.GetProperties()
	if a, ok := props["archetype"].(string); ok && a != "" {
		return a
	}
	return ArchetypeRookie
}

package server

import (
	"github.com/heroiclabs/nakama-common/runtime"
)

// teamSplit describes which entry indices belong to each team.
type teamSplit struct {
	blueIndices   []int
	orangeIndices []int
}

// scoreTeamComposition scores a pair of teams based on archetype diversity.
// Higher scores indicate better team compositions. The scoring rules:
//   - +2 per team that has at least one striker
//   - +1 per team that has at least one playmaker
//   - -2 per team that has 2+ rookies
//   - -1 if a team has a new player but no striker or playmaker teammate
func scoreTeamComposition(team1Archetypes, team2Archetypes []string, team1HasNewPlayer, team2HasNewPlayer bool) int {
	return scoreTeam(team1Archetypes, team1HasNewPlayer) + scoreTeam(team2Archetypes, team2HasNewPlayer)
}

// scoreTeamCompositionFromEntries computes composition score directly from
// entry slices, avoiding intermediate teamSplit allocations in hot loops.
func scoreTeamCompositionFromEntries(blueTeam, orangeTeam MatchmakerEntries, newPlayerThreshold int) int {
	blueArchetypes := make([]string, len(blueTeam))
	orangeArchetypes := make([]string, len(orangeTeam))
	var blueHasNew, orangeHasNew bool

	for i, e := range blueTeam {
		blueArchetypes[i] = entryArchetype(e)
		if IsNewPlayer(e, newPlayerThreshold) {
			blueHasNew = true
		}
	}
	for i, e := range orangeTeam {
		orangeArchetypes[i] = entryArchetype(e)
		if IsNewPlayer(e, newPlayerThreshold) {
			orangeHasNew = true
		}
	}

	return scoreTeamComposition(blueArchetypes, orangeArchetypes, blueHasNew, orangeHasNew)
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
	if hasNewPlayer && rookieCount == 0 && !hasStriker && !hasPlaymaker {
		score--
	}

	return score
}

// selectBestTeamSplit picks the team split with the highest composition score.
// If splits is empty, returns a zero-value teamSplit.
func selectBestTeamSplit(entries []runtime.MatchmakerEntry, splits []teamSplit, newPlayerThreshold int) teamSplit {
	if len(splits) == 0 {
		return teamSplit{}
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
func scoreTeamSplitComposition(entries []runtime.MatchmakerEntry, split teamSplit, newPlayerThreshold int) int {
	team1Archetypes := make([]string, len(split.blueIndices))
	team2Archetypes := make([]string, len(split.orangeIndices))
	var team1HasNew, team2HasNew bool

	for i, idx := range split.blueIndices {
		team1Archetypes[i] = entryArchetype(entries[idx])
		if IsNewPlayer(entries[idx], newPlayerThreshold) {
			team1HasNew = true
		}
	}
	for i, idx := range split.orangeIndices {
		team2Archetypes[i] = entryArchetype(entries[idx])
		if IsNewPlayer(entries[idx], newPlayerThreshold) {
			team2HasNew = true
		}
	}

	return scoreTeamComposition(team1Archetypes, team2Archetypes, team1HasNew, team2HasNew)
}

// entryArchetype reads the "archetype" string property from a matchmaker entry.
// Returns an empty string when the archetype is missing or unrecognized,
// so that missing data does not get misclassified as rookie.
func entryArchetype(entry runtime.MatchmakerEntry) string {
	props := entry.GetProperties()
	if a, ok := props["archetype"].(string); ok {
		switch a {
		case ArchetypeStriker, ArchetypePlaymaker, ArchetypeRookie,
			ArchetypeGoalie, ArchetypeInterceptor, ArchetypeLowActivity:
			return a
		}
	}
	return ""
}

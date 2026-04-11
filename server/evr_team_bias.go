package server

import (
	"math"

	"github.com/heroiclabs/nakama-common/runtime"
)

// ApplyNewPlayerTeamBias takes a match slice where the first teamSize entries
// are blue team and the remaining are orange team. It moves new players
// (games_played < threshold) to the predicted-stronger team by swapping them
// with non-new players on the stronger team who have the closest mu value.
//
// Only swaps that do not worsen overall team balance (measured by absolute
// difference in summed mu) are applied.
//
// Returns the (possibly modified) match slice.
func ApplyNewPlayerTeamBias(match []runtime.MatchmakerEntry, teamSize, threshold int) []runtime.MatchmakerEntry {
	if threshold <= 0 || len(match) < 2*teamSize {
		return match
	}

	blue := match[:teamSize]
	orange := match[teamSize : 2*teamSize]

	// Compute team mu sums.
	blueMu := teamMuSum(blue)
	orangeMu := teamMuSum(orange)

	// Identify stronger team. If equal, no bias needed.
	if blueMu == orangeMu {
		return match
	}

	var stronger, weaker []runtime.MatchmakerEntry
	var strongerStart int
	if blueMu >= orangeMu {
		stronger = blue
		weaker = orange
		strongerStart = 0
	} else {
		stronger = orange
		weaker = blue
		strongerStart = teamSize
	}

	// Collect new players on the weaker team. Process each once.
	// Use a set to track already-processed session IDs to avoid infinite loops
	// when a swap changes team composition.
	processed := make(map[string]bool)

	for {
		swapped := false

		for wi := 0; wi < len(weaker); wi++ {
			sid := weaker[wi].GetPresence().GetSessionId()
			if processed[sid] {
				continue
			}
			if !IsNewPlayer(weaker[wi], threshold) {
				continue
			}

			newMu := entryMu(weaker[wi])

			// Find the best swap candidate on the stronger team: a non-new player
			// with the closest mu to the new player.
			bestIdx := -1
			bestDiff := math.MaxFloat64
			for si := 0; si < len(stronger); si++ {
				if IsNewPlayer(stronger[si], threshold) {
					continue // don't swap new-for-new
				}
				diff := math.Abs(entryMu(stronger[si]) - newMu)
				if diff < bestDiff {
					bestDiff = diff
					bestIdx = si
				}
			}

			if bestIdx < 0 {
				processed[sid] = true
				continue // no eligible swap partner
			}

			// Check whether swapping improves or preserves balance.
			currentImbalance := math.Abs(blueMu - orangeMu)

			swapMu := entryMu(stronger[bestIdx])
			newBlueMu := blueMu
			newOrangeMu := orangeMu

			if strongerStart == 0 {
				// stronger=blue, weaker=orange
				newBlueMu = blueMu - swapMu + newMu
				newOrangeMu = orangeMu - newMu + swapMu
			} else {
				// stronger=orange, weaker=blue
				newBlueMu = blueMu - newMu + swapMu
				newOrangeMu = orangeMu - swapMu + newMu
			}

			newImbalance := math.Abs(newBlueMu - newOrangeMu)
			if newImbalance > currentImbalance {
				processed[sid] = true
				continue // swap would worsen balance
			}

			// Perform the swap in the match slice.
			if strongerStart == 0 {
				match[bestIdx], match[teamSize+wi] = match[teamSize+wi], match[bestIdx]
			} else {
				match[wi], match[teamSize+bestIdx] = match[teamSize+bestIdx], match[wi]
			}

			processed[sid] = true

			// Update mu sums.
			blueMu = newBlueMu
			orangeMu = newOrangeMu

			// Re-derive team slices.
			blue = match[:teamSize]
			orange = match[teamSize : 2*teamSize]

			// After a swap the stronger/weaker teams may have flipped.
			if blueMu >= orangeMu {
				stronger = blue
				weaker = orange
				strongerStart = 0
			} else {
				stronger = orange
				weaker = blue
				strongerStart = teamSize
			}

			swapped = true
			break // restart scan with new team composition
		}

		if !swapped {
			break
		}
	}

	return match
}

func teamMuSum(team []runtime.MatchmakerEntry) float64 {
	var sum float64
	for _, e := range team {
		sum += entryMu(e)
	}
	return sum
}

func entryMu(e runtime.MatchmakerEntry) float64 {
	if mu, ok := e.GetProperties()["rating_mu"].(float64); ok {
		return mu
	}
	return 0
}

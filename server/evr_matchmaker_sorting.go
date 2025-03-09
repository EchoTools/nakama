package server

import (
	"slices"
	"time"
)

type PredictMatchKeys struct {
	RankPercentile float64
}

type PredictedMatch struct {
	TeamA              RatedEntryTeam `json:"team1"`
	TeamB              RatedEntryTeam `json:"team2"`
	Draw               float64        `json:"draw"`
	Size               int            `json:"size"`
	AvgRankPercentileA float64        `json:"rank_percentile_a"`
	AvgRankPercentileB float64        `json:"rank_percentile_b"`
	RankDelta          float64        `json:"rank_percentile_delta"`
	DivisionCount      int            `json:"division_count"`
	PriorityExpiry     string         `json:"priority_expiry"`
}

func (p PredictedMatch) Entrants() RatedEntryTeam {
	return append(p.TeamA, p.TeamB...)
}

func (p PredictedMatch) Teams() []RatedEntryTeam {
	return []RatedEntryTeam{p.TeamA, p.TeamB}
}

func (m *SkillBasedMatchmaker) sortPredictions(predictions []PredictedMatch, maxDelta float64, ignorePriority bool) {
	// Sort the predictions by the draw probability

	m.sortByDraw(predictions)

	// Sort by size
	m.sortBySize(predictions)

	// Sort by rank delta
	m.sortLimitRankDelta(predictions, maxDelta)

	// Sort by division count
	m.sortedByDivison(predictions)

	// Sort by priority
	m.sortedPriority(predictions)
}

func (m *SkillBasedMatchmaker) sortByDraw(predictions []PredictedMatch) {
	slices.SortStableFunc(predictions, func(a, b PredictedMatch) int {
		return int((b.Draw - a.Draw) * 100)
	})
}

func (m *SkillBasedMatchmaker) sortBySize(predictions []PredictedMatch) {

	// Sort by size, then by prediction of a draw
	slices.SortStableFunc(predictions, func(a, b PredictedMatch) int {
		return b.Size - a.Size
	})
}

func (m *SkillBasedMatchmaker) sortLimitRankDelta(predictions []PredictedMatch, maxDelta float64) {

	slices.SortStableFunc(predictions, func(a, b PredictedMatch) int {

		if a.RankDelta > maxDelta && b.RankDelta > maxDelta {
			return 0
		}

		if a.RankDelta > maxDelta {
			return 1
		}

		if b.RankDelta > maxDelta {
			return -1
		}

		return 0
	})
}

func (m *SkillBasedMatchmaker) sortedByDivison(predictions []PredictedMatch) {

	slices.SortStableFunc(predictions, func(a, b PredictedMatch) int {
		// Sort by the number of divisions in common, excluding where candiate have no division
		if a.DivisionCount < b.DivisionCount {
			return -1
		}
		if a.DivisionCount > b.DivisionCount {
			return 1
		}
		return 0
	})
}

func (m *SkillBasedMatchmaker) sortedPriority(predictions []PredictedMatch) {

	now := time.Now().UTC().Format(time.RFC3339)

	slices.SortStableFunc(predictions, func(a, b PredictedMatch) int {
		// If a player has a priority_threshold set, and it's less than "now" it should be sorted to the top
		if a.PriorityExpiry < now && b.PriorityExpiry > now {
			return -1
		}
		if a.PriorityExpiry > now && b.PriorityExpiry < now {
			return 1
		}

		return 0
	})

}

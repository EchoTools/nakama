package server

import (
	"slices"
)

type PredictMatchKeys struct {
	RankPercentile float64
}

type PredictedMatch struct {
	TeamA              []*MatchmakingTicket `json:"team1"`
	TeamB              []*MatchmakingTicket `json:"team2"`
	Draw               float64              `json:"draw"`
	Size               int                  `json:"size"`
	AvgRankPercentileA float64              `json:"rank_percentile_a"`
	AvgRankPercentileB float64              `json:"rank_percentile_b"`
	RankDelta          float64              `json:"rank_percentile_delta"`
}

func (p PredictedMatch) Entrants() []*MatchmakerEntry {
	entrants := make([]*MatchmakerEntry, 0, len(p.TeamA)+len(p.TeamB))
	for _, team := range [...][]*MatchmakingTicket{p.TeamA, p.TeamB} {
		for _, party := range team {
			for _, member := range party.Members {
				entrants = append(entrants, member.Entry)
			}
		}
	}
	return entrants
}

func (m *SkillBasedMatchmaker) sortPredictions(predictions []PredictedMatch, maxDelta float64, ignorePriority bool) {

	// Sort the predictions by the draw probability
	m.sortByDraw(predictions)

	// Sort by rank delta
	m.sortLimitRankDelta(predictions, maxDelta)

	// Sort by size
	m.sortBySize(predictions)
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
			return 1
		}

		if a.RankDelta < maxDelta {
			return -1
		}

		if b.RankDelta < maxDelta {
			return 1
		}

		return 0
	})
}

package server

import (
	"math"
	"slices"
	"sort"

	"maps"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/intinig/go-openskill/rating"
	"github.com/intinig/go-openskill/types"
)

type PredictedMatch struct {
	Candidate           []runtime.MatchmakerEntry `json:"match"`
	Draw                float64                   `json:"draw"`
	Size                int                       `json:"size"`
	TeamRankPercentileA float64                   `json:"rank_percentile_a"`
	TeamRankPercentileB float64                   `json:"rank_percentile_b"`
	RankPercentileDelta float64                   `json:"rank_percentile_delta"`
	TeamOrdinalA        float64                   `json:"team_ordinal_a"`
	TeamOrdinalB        float64                   `json:"team_ordinal_b"`
	TeamStrengthA       float64                   `json:"team_strength_a"`
	TeamStrengthB       float64                   `json:"team_strength_b"`
	OrdinalDelta        float64                   `json:"ordinal_delta"`
	MuDelta             float64                   `json:"mu_delta"`
}

type CandidateList []runtime.MatchmakerEntry

func (g CandidateList) Len() int {
	return len(g)
}

func (g CandidateList) Ratings() []types.Rating {
	ratings := make([]types.Rating, len(g))
	for i, e := range g {
		ratings[i] = types.Rating{
			Mu:    e.GetProperties()["rating_mu"].(float64),
			Sigma: e.GetProperties()["rating_sigma"].(float64),
			Z:     3,
		}
	}
	return ratings
}

func (g CandidateList) TeamRating() types.TeamRating {
	return types.TeamRating{Team: g.Ratings()}
}

func (g CandidateList) TeamOrdinal() float64 {
	return rating.TeamOrdinal(g.TeamRating())
}

func (g CandidateList) Strength() float64 {
	var strength float64
	for _, e := range g {
		strength += e.GetProperties()["rating_mu"].(float64)
	}
	return strength
}

func HashMatchmakerEntries[E runtime.MatchmakerEntry](entries []E) uint64 {
	var hash uint64
	tickets := make([]string, len(entries))
	for i, entry := range entries {
		tickets[i] = entry.GetTicket()
	}
	slices.Sort(tickets) // Ensure the order of tickets doesn't matter
	for _, ticket := range tickets {
		for i := 0; i < len(ticket); i++ {
			hash = hash*31 + uint64(ticket[i])
		}
	}
	return hash
}

func predictCandidateOutcomes(candidates [][]runtime.MatchmakerEntry) []PredictedMatch {

	// count the number of valid candidates
	var validCandidates int
	for _, c := range candidates {
		if c != nil {
			validCandidates++
		}
	}

	var (
		predictions = make([]PredictedMatch, 0, validCandidates)
		tickets     = make(map[string]CandidateList, 0)
		hashSet     = make(map[uint64]struct{}, 0)
	)

	for _, c := range candidates {
		if c == nil {
			continue
		}
		// Skip over duplicate candidates
		if _, ok := hashSet[HashMatchmakerEntries(c)]; ok {
			continue
		}
		hashSet[HashMatchmakerEntries(c)] = struct{}{}

		ticketSet := make(map[string]CandidateList, 0)
		for _, e := range c {
			// skip over duplicate tickets
			if _, found := tickets[e.GetTicket()]; found {
				continue
			}
			ticketSet[e.GetTicket()] = append(ticketSet[e.GetTicket()], e)
		}

		maps.Copy(tickets, ticketSet)

		// Put the strongest players on team A
		groups := make([]CandidateList, 0, len(ticketSet))
		for _, entries := range ticketSet {
			groups = append(groups, entries)
		}
		sortByRanks(groups)

		teamA, teamB := organizeAsTeams(groups, len(c)/2)
		// If the teams are not balanced, continue to the next candidate
		if len(teamA) != len(teamB) {
			continue
		}
		// copy the teams into the candidate
		copy(c, append(teamA, teamB...))

		predictions = append(predictions, PredictedMatch{
			Candidate:     c,
			Draw:          rating.PredictDraw([]types.Team{teamA.Ratings(), teamB.Ratings()}, nil),
			Size:          len(c),
			TeamOrdinalA:  teamA.TeamOrdinal(),
			TeamOrdinalB:  teamB.TeamOrdinal(),
			TeamStrengthA: teamA.Strength(),
			TeamStrengthB: teamB.Strength(),
			OrdinalDelta:  math.Abs(teamA.TeamOrdinal() - teamB.TeamOrdinal()),
		})
	}

	return predictions
}

func organizeAsTeams(groups []CandidateList, teamSize int) (CandidateList, CandidateList) {
	teamA, teamB := make(CandidateList, 0, teamSize), make(CandidateList, 0, teamSize)
	for _, entries := range groups {
		if len(teamA) <= len(teamB) {
			teamA = append(teamA, entries...)
		} else {
			teamB = append(teamB, entries...)
		}
	}
	return teamA, teamB
}

func sortByRanks(groups []CandidateList) {

	ratings := make([]types.Team, 0, len(groups))
	for _, entries := range groups {
		ratings = append(ratings, entries.Ratings())
	}

	ranks, _ := rating.PredictRank(ratings, nil)

	sort.SliceStable(groups, func(i, j int) bool {
		return ranks[i] < ranks[j]
	})
}

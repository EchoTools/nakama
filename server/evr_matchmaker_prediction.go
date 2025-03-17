package server

import (
	"math"
	"slices"

	"maps"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/intinig/go-openskill/rating"
	"github.com/intinig/go-openskill/types"
)

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

func (g CandidateList) Ordinal() float64 {
	return rating.TeamOrdinal(g.TeamRating())
}

func (g CandidateList) Strength() float64 {
	var strength float64
	for _, e := range g {
		strength += e.GetProperties()["rating_mu"].(float64)
	}
	return strength
}

func HashMatchmakerEntries(entries []runtime.MatchmakerEntry) uint64 {
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

func predictMatchOutcomes(candidates [][]runtime.MatchmakerEntry) []PredictedMatch {

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

		// Check if the players are able to reach each other.

		// Sort the candidates by size, largest first, then by strength (descending)
		slices.SortStableFunc(c, func(a, b runtime.MatchmakerEntry) int {
			ticketA := a.GetTicket()
			ticketB := b.GetTicket()
			if ticketA == ticketB {
				return 0
			}
			candidatesA := tickets[ticketA]
			candidatesB := tickets[ticketB]
			if candidatesA.Len() != candidatesB.Len() {
				return candidatesB.Len() - candidatesA.Len()
			}
			return int(candidatesA.Strength() - candidatesB.Strength())
		})

		teamSize := len(c) / 2
		teamA, teamB := make(CandidateList, 0, teamSize), make(CandidateList, 0, teamSize)

		for _, entries := range ticketSet {
			if len(teamA) > len(teamB) || teamA.Strength() > teamB.Strength() {
				teamB = append(teamB, entries...)
			} else {
				teamA = append(teamA, entries...)
			}
		}

		strengthA := teamA.Strength()
		strengthB := teamB.Strength()
		// Sort so that team1 (blue) is the stronger team
		if strengthA < strengthB {
			teamA, teamB = teamB, teamA
		}

		// copy the teams into the candidate
		copy(c, append(teamA, teamB...))

		predictions = append(predictions, PredictedMatch{
			Candidate:    c,
			Draw:         rating.PredictDraw([]types.Team{teamA.Ratings(), teamB.Ratings()}, nil),
			Size:         len(c),
			TeamOrdinalA: strengthA,
			TeamOrdinalB: strengthB,
			OrdinalDelta: math.Abs(teamA.Ordinal() - teamB.Ordinal()),
		})
	}

	return predictions
}

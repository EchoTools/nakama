package server

import (
	"sort"
	"strings"
	"time"

	"maps"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/intinig/go-openskill/rating"
	"github.com/intinig/go-openskill/types"
)

type PredictedMatch struct {
	Candidate             []runtime.MatchmakerEntry `json:"match"`
	Draw                  float32                   `json:"draw"`
	Size                  int8                      `json:"size"`
	DivisionCount         int8                      `json:"division_count"`
	OldestTicketTimestamp int64                     `json:"oldest_ticket"`
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
func (g CandidateList) DivisionSet() map[string]struct{} {
	divisionSet := make(map[string]struct{}, len(g))
	for _, e := range g {
		divisions := strings.Split(e.GetProperties()["divisions"].(string), ",")
		for _, division := range divisions {
			divisionSet[division] = struct{}{}
		}
	}
	return divisionSet
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

	// Sort entries based on their ticket strings directly.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].GetTicket() < entries[j].GetTicket()
	})
	var hash uint64 = 5381 // Start with a non-zero initial hash value

	for _, entry := range entries {
		ticket := entry.GetTicket()

		// Use FNV-1a hash.
		for i := range 8 {
			hash = (hash * 33) ^ uint64(ticket[i])
		}
	}
	return hash
}

func predictCandidateOutcomes(candidates [][]runtime.MatchmakerEntry) <-chan PredictedMatch {
	predictCh := make(chan PredictedMatch)

	go func() {
		defer close(predictCh)
		// Count valid candidates
		validCandidates := 0
		for _, c := range candidates {
			if c != nil {
				validCandidates++
			}
		}

		var (
			allTickets          = make(map[string]CandidateList, validCandidates)
			candidateHashSet    = make(map[uint64]struct{}, validCandidates)
			ratingsByGroup      = make([]types.Team, 0, 10)
			candidateTickets    = make(map[string]CandidateList, 10)
			groups              = make([]CandidateList, 0, 10)
			teamA               = make(CandidateList, 0, 10)
			teamB               = make(CandidateList, 0, 10)
			teamRatingsA        = make([]types.Rating, 0, 5)
			teamRatingsB        = make([]types.Rating, 0, 5)
			ratingsByTicket     = make(map[string]types.Team, 10)
			divisionSetByTicket = make(map[string]map[string]struct{}, 10)
			ageByTicket         = make(map[string]float64, 10)
			divisionSet         = make(map[string]struct{}, 10)
		)

		for _, c := range candidates {
			if c == nil {
				continue
			}

			// Skip duplicate candidates
			hash := HashMatchmakerEntries(c)
			if _, found := candidateHashSet[hash]; found {
				continue
			}
			candidateHashSet[hash] = struct{}{}

			// Clear candidateTickets map
			for k := range candidateTickets {
				delete(candidateTickets, k)
			}

			// Collect tickets efficiently
			for _, e := range c {
				ticket := e.GetTicket()
				if existing, found := allTickets[ticket]; found {
					candidateTickets[ticket] = existing
				} else {
					continue
				}
				candidateTickets[ticket] = append(candidateTickets[ticket], e)
			}

			// Update allTickets selectively
			maps.Copy(allTickets, candidateTickets)

			for ticket, entries := range candidateTickets {
				ratingsByTicket[ticket] = entries.Ratings()
				divisionSetByTicket[ticket] = entries.DivisionSet()
				oldest := float64(time.Now().UTC().Unix())
				for _, entry := range entries {
					if entry.GetProperties()["submission_time"].(float64) < oldest {
						oldest = entry.GetProperties()["submission_time"].(float64)
					}
				}
				ageByTicket[ticket] = oldest
			}

			// Reuse groups slice
			groups = groups[:0]
			for _, entries := range candidateTickets {
				groups = append(groups, entries)
			}

			ratingsByGroup = ratingsByGroup[:0]
			for _, entries := range groups {
				ratingsByGroup = append(ratingsByGroup, ratingsByTicket[entries[0].GetTicket()])
			}

			ranks, _ := rating.PredictRank(ratingsByGroup, nil)

			// Sort groups by best rating first
			sort.SliceStable(groups, func(i, j int) bool {
				return ranks[i] > ranks[j]
			})

			// Create teams

			teamA, teamB = teamA[:0], teamB[:0]
			teamRatingsA, teamRatingsB = teamRatingsA[:0], teamRatingsB[:0]
			divisionSet = make(map[string]struct{}, 10)

			teamSize := len(c) / 2
			for _, entries := range groups {
				ticket := entries[0].GetTicket()
				maps.Copy(divisionSet, divisionSetByTicket[ticket])
				if len(teamA)+len(entries) <= teamSize {
					teamA = append(teamA, entries...)
					teamRatingsA = append(teamRatingsA, ratingsByTicket[ticket]...)
				} else {
					teamB = append(teamB, entries...)
					teamRatingsB = append(teamRatingsB, ratingsByTicket[ticket]...)
				}
			}

			if len(teamA) != len(teamB) {
				continue
			}

			// Copy teams into candidate slice
			copy(c[:len(teamA)], teamA)
			copy(c[len(teamA):], teamB)

			predictCh <- PredictedMatch{
				Candidate:             c,
				Draw:                  float32(rating.PredictDraw([]types.Team{teamRatingsA, teamRatingsB}, nil)),
				Size:                  int8(len(c)),
				DivisionCount:         int8(len(divisionSet)),
				OldestTicketTimestamp: int64(ageByTicket[c[0].GetTicket()]),
			}
		}

	}()
	return predictCh
}

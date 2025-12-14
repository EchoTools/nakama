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

// RosterVariant indicates the team formation strategy used
type RosterVariant int8

const (
	RosterVariantSequential RosterVariant = iota // Original sequential filling
	RosterVariantSnakeDraft                      // Snake draft for balanced teams
)

// PredictionConfig contains settings for match outcome prediction
type PredictionConfig struct {
	PartyBoostPercent      float64         // Boost party effective skill by this percentage
	EnableRosterVariants   bool            // Generate multiple roster variants for better match selection
	UseSnakeDraftFormation bool            // Use snake draft instead of sequential filling
	Variants               []RosterVariant // Pre-computed list of variants to generate (if set, overrides other variant settings)
}

type PredictedMatch struct {
	Candidate             []runtime.MatchmakerEntry `json:"match"`
	Draw                  float32                   `json:"draw"`
	Size                  int8                      `json:"size"`
	DivisionCount         int8                      `json:"division_count"`
	OldestTicketTimestamp int64                     `json:"oldest_ticket"`
	Variant               RosterVariant             `json:"variant"` // Which team formation strategy was used
}

type MatchmakerEntries []runtime.MatchmakerEntry

func (g MatchmakerEntries) Len() int {
	return len(g)
}

func (g MatchmakerEntries) Ratings() []types.Rating {
	ratings := make([]types.Rating, len(g))
	for i, e := range g {
		props := e.GetProperties()
		mu := props["rating_mu"].(float64)
		sigma := props["rating_sigma"].(float64)
		ratings[i] = NewRating(0, mu, sigma)
	}
	return ratings
}

// RatingsWithPartyBoost returns ratings with an optional boost for parties (groups with multiple members)
func (g MatchmakerEntries) RatingsWithPartyBoost(boostPercent float64) []types.Rating {
	ratings := make([]types.Rating, len(g))
	isParty := len(g) > 1
	for i, e := range g {
		props := e.GetProperties()
		mu := props["rating_mu"].(float64)
		sigma := props["rating_sigma"].(float64)
		// Apply party boost to Mu for rank prediction purposes
		if isParty && boostPercent > 0 {
			mu = mu * (1 + boostPercent)
		}
		ratings[i] = NewRating(0, mu, sigma)
	}
	return ratings
}
func (g MatchmakerEntries) DivisionSet() map[string]struct{} {
	divisionSet := make(map[string]struct{}, len(g))
	for _, e := range g {
		props := e.GetProperties()
		divisionsVal, ok := props["divisions"]
		if !ok || divisionsVal == nil {
			continue
		}
		divisionsStr, ok := divisionsVal.(string)
		if !ok || divisionsStr == "" {
			continue
		}
		divisions := strings.Split(divisionsStr, ",")
		for _, division := range divisions {
			divisionSet[division] = struct{}{}
		}
	}
	return divisionSet
}
func (g MatchmakerEntries) TeamRating() types.TeamRating {
	return types.TeamRating{Team: g.Ratings()}
}

func (g MatchmakerEntries) TeamOrdinal() float64 {
	return rating.TeamOrdinal(g.TeamRating())
}

func (g MatchmakerEntries) Strength() float64 {
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
	// Get settings for party boost and roster variants
	config := PredictionConfig{}
	if settings := ServiceSettings(); settings != nil {
		config.PartyBoostPercent = settings.Matchmaking.PartySkillBoostPercent
		config.EnableRosterVariants = settings.Matchmaking.EnableRosterVariants
		config.UseSnakeDraftFormation = settings.Matchmaking.UseSnakeDraftTeamFormation
	}

	return predictCandidateOutcomesWithConfig(candidates, config)
}

// predictCandidateOutcomesWithConfig allows testing with specific settings
func predictCandidateOutcomesWithConfig(candidates [][]runtime.MatchmakerEntry, config PredictionConfig) <-chan PredictedMatch {
	// Generate roster variants based on config if not already specified
	variants := config.Variants
	if len(variants) == 0 {
		if config.UseSnakeDraftFormation {
			variants = append(variants, RosterVariantSnakeDraft)
		} else {
			variants = append(variants, RosterVariantSequential)
		}
		// If roster variants are enabled, generate both types
		if config.EnableRosterVariants {
			if config.UseSnakeDraftFormation {
				variants = append(variants, RosterVariantSequential)
			} else {
				variants = append(variants, RosterVariantSnakeDraft)
			}
		}
	}

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
			candidateHashSet    = make(map[uint64]struct{}, validCandidates)
			ratingsByGroup      = make([]types.Team, 0, 10)
			candidateTickets    = make(map[string]MatchmakerEntries, 10)
			groups              = make([]MatchmakerEntries, 0, 10)
			teamA               = make(MatchmakerEntries, 0, 10)
			teamB               = make(MatchmakerEntries, 0, 10)
			teamRatingsA        = make([]types.Rating, 0, 5)
			teamRatingsB        = make([]types.Rating, 0, 5)
			actualTeamRatingsA  = make([]types.Rating, 0, 5)
			actualTeamRatingsB  = make([]types.Rating, 0, 5)
			ratingsByTicket     = make(map[string]types.Team, 40)
			divisionSetByTicket = make(map[string]map[string]struct{}, 40)
			ageByTicket         = make(map[string]float64, 40)
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

			// Collect tickets efficiently - group entries by ticket
			for _, e := range c {
				ticket := e.GetTicket()
				candidateTickets[ticket] = append(candidateTickets[ticket], e)
			}

			// Skip if no groups formed
			if len(candidateTickets) == 0 {
				continue
			}

			for ticket, entries := range candidateTickets {
				// Check cache to avoid recomputing identical tickets
				if _, ok := ratingsByTicket[ticket]; !ok {
					// Use boosted ratings for parties when calculating ranks
					ratingsByTicket[ticket] = entries.RatingsWithPartyBoost(config.PartyBoostPercent)
					divisionSetByTicket[ticket] = entries.DivisionSet()
				}
				oldest := float64(time.Now().UTC().Unix())
				for _, entry := range entries {
					props := entry.GetProperties()
					if st, ok := props["submission_time"].(float64); ok && st < oldest {
						oldest = st
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
				return ranks[i] < ranks[j]
			})

			// Collect division set - reuse map from cache
			for k := range divisionSet {
				delete(divisionSet, k)
			}
			for _, entries := range groups {
				ticket := entries[0].GetTicket()
				maps.Copy(divisionSet, divisionSetByTicket[ticket])
			}

			teamSize := len(c) / 2

			for _, variant := range variants {
				// Create teams based on variant
				teamA, teamB = teamA[:0], teamB[:0]
				teamRatingsA, teamRatingsB = teamRatingsA[:0], teamRatingsB[:0]

				switch variant {
				case RosterVariantSequential:
					// Original sequential filling (best groups fill Team A first)
					for _, entries := range groups {
						ticket := entries[0].GetTicket()
						if len(teamA)+len(entries) <= teamSize {
							teamA = append(teamA, entries...)
							teamRatingsA = append(teamRatingsA, ratingsByTicket[ticket]...)
						} else {
							teamB = append(teamB, entries...)
							teamRatingsB = append(teamRatingsB, ratingsByTicket[ticket]...)
						}
					}

				case RosterVariantSnakeDraft:
					// Snake draft: alternates team assignment to balance strength
					// Pattern for 8 picks: A, B, B, A, A, B, B, A (creates balance by giving
					// the weaker team consecutive picks)
					// For groups sorted by rank (best first):
					// - 1st (best) → A
					// - 2nd, 3rd → B
					// - 4th, 5th → A
					// - 6th, 7th → B
					// - 8th → A
					for groupIndex, entries := range groups {
						ticket := entries[0].GetTicket()

						// Determine which team gets this group using snake pattern
						// Group index 0: A, 1-2: B, 3-4: A, 5-6: B, 7: A
						// General snake draft assignment for any group size
						// For two teams: alternate direction every round of 2 picks
						roundSize := 2
						round := groupIndex / roundSize
						posInRound := groupIndex % roundSize
						var assignToA bool
						if round%2 == 0 {
							assignToA = (posInRound == 0)
						} else {
							assignToA = (posInRound == 1)
						}

						// Check if assignment would exceed team size, flip if needed
						if assignToA && len(teamA)+len(entries) > teamSize {
							assignToA = false
						} else if !assignToA && len(teamB)+len(entries) > teamSize {
							assignToA = true
						}

						if assignToA {
							teamA = append(teamA, entries...)
							teamRatingsA = append(teamRatingsA, ratingsByTicket[ticket]...)
						} else {
							teamB = append(teamB, entries...)
							teamRatingsB = append(teamRatingsB, ratingsByTicket[ticket]...)
						}
					}
				}

				if len(teamA) != len(teamB) {
					continue
				}

				// Create a copy of the candidate slice for this variant
				variantCandidate := make([]runtime.MatchmakerEntry, len(c))
				copy(variantCandidate[:len(teamA)], teamA)
				copy(variantCandidate[len(teamA):], teamB)

				// Get actual (non-boosted) ratings for draw probability calculation - reuse slices
				actualTeamRatingsA = actualTeamRatingsA[:0]
				actualTeamRatingsB = actualTeamRatingsB[:0]
				for _, e := range teamA {
					props := e.GetProperties()
					mu := props["rating_mu"].(float64)
					sigma := props["rating_sigma"].(float64)
					actualTeamRatingsA = append(actualTeamRatingsA, NewRating(0, mu, sigma))
				}
				for _, e := range teamB {
					props := e.GetProperties()
					mu := props["rating_mu"].(float64)
					sigma := props["rating_sigma"].(float64)
					actualTeamRatingsB = append(actualTeamRatingsB, NewRating(0, mu, sigma))
				}

				predictCh <- PredictedMatch{
					Candidate:             variantCandidate,
					Draw:                  float32(rating.PredictDraw([]types.Team{actualTeamRatingsA, actualTeamRatingsB}, nil)),
					Size:                  int8(len(variantCandidate)),
					DivisionCount:         int8(len(divisionSet)),
					OldestTicketTimestamp: int64(ageByTicket[variantCandidate[0].GetTicket()]),
					Variant:               variant,
				}
			}
		}
	}()
	return predictCh
}

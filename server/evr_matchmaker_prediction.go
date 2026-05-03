package server

import (
	"math"
	"sort"
	"strings"
	"time"

	"maps"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
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
	PartyBoostPercent        float64                 // Boost party effective skill by this percentage
	EnableRosterVariants     bool                    // Generate multiple roster variants for better match selection
	UseSnakeDraftFormation   bool                    // Use snake draft instead of sequential filling
	Variants                 []RosterVariant         // Pre-computed list of variants to generate (if set, overrides other variant settings)
	OpenSkillOptions         *types.OpenSkillOptions // Options for OpenSkill calculations
	EnableArchetypeBalancing bool                    // Score team compositions by archetype diversity
	NewPlayerThreshold       int                     // Games played threshold for new player detection (0 = disabled)
	EnableNewPlayerTeamBias  bool                    // Apply new player team bias after team formation
}

type PredictedMatch struct {
	Candidate             []runtime.MatchmakerEntry `json:"match"`
	DrawProb              float32                   `json:"draw"`
	Size                  int8                      `json:"size"`
	DivisionCount         int8                      `json:"division_count"`
	OldestTicketTimestamp int64                     `json:"oldest_ticket"`
	Variant               RosterVariant             `json:"variant"`           // Which team formation strategy was used
	CompositionScore      int8                      `json:"composition_score"` // Archetype balance score (higher = better composition)
}

type MatchmakerEntries []runtime.MatchmakerEntry

func (g MatchmakerEntries) Len() int {
	return len(g)
}

func (g MatchmakerEntries) Ratings(opts *types.OpenSkillOptions) []types.Rating {
	// Use default rating if opts is nil
	if opts == nil {
		defaultRating := NewDefaultRating()
		opts = &types.OpenSkillOptions{
			Mu:    &defaultRating.Mu,
			Sigma: &defaultRating.Sigma,
			Z:     &defaultRating.Z,
		}
	}
	ratings := make([]types.Rating, len(g))
	for i, e := range g {
		props := e.GetProperties()
		mu, ok := props["rating_mu"].(float64)
		if !ok {
			mu = *opts.Mu
		}
		sigma, ok := props["rating_sigma"].(float64)
		if !ok {
			sigma = *opts.Sigma
		}
		ratings[i] = rating.NewWithOptions(&types.OpenSkillOptions{
			Mu:    &mu,
			Sigma: &sigma,
			Z:     opts.Z,
		})
	}
	return ratings
}

// RatingsInto fills the provided ratings slice with no allocations
func (g MatchmakerEntries) RatingsInto(ratings []types.Rating, opts *types.OpenSkillOptions) {
	// Use default rating if opts is nil
	if opts == nil {
		defaultRating := NewDefaultRating()
		opts = &types.OpenSkillOptions{
			Mu:    &defaultRating.Mu,
			Sigma: &defaultRating.Sigma,
			Z:     &defaultRating.Z,
		}
	}
	for i, e := range g {
		props := e.GetProperties()
		mu, ok := props["rating_mu"].(float64)
		if !ok {
			mu = *opts.Mu
		}
		sigma, ok := props["rating_sigma"].(float64)
		if !ok {
			sigma = *opts.Sigma
		}
		ratings[i] = rating.NewWithOptions(&types.OpenSkillOptions{
			Mu:    &mu,
			Sigma: &sigma,
			Z:     opts.Z,
		})
	}
}

// RatingsWithPartyBoost returns ratings with an optional boost for parties (groups with multiple members)
func (g MatchmakerEntries) RatingsWithPartyBoost(boostPercent float64, opts *types.OpenSkillOptions) []types.Rating {
	// Use default rating if opts is nil
	if opts == nil {
		defaultRating := NewDefaultRating()
		opts = &types.OpenSkillOptions{
			Mu:    &defaultRating.Mu,
			Sigma: &defaultRating.Sigma,
			Z:     &defaultRating.Z,
		}
	}
	ratings := make([]types.Rating, len(g))
	isParty := len(g) > 1
	for i, e := range g {
		props := e.GetProperties()
		mu, ok := props["rating_mu"].(float64)
		if !ok {
			mu = *opts.Mu
		}
		sigma, ok := props["rating_sigma"].(float64)
		if !ok {
			sigma = *opts.Sigma
		}
		// Apply party boost to Mu for rank prediction purposes
		if isParty && boostPercent > 0 {
			mu = mu * (1 + boostPercent)
		}
		ratings[i] = rating.NewWithOptions(&types.OpenSkillOptions{
			Mu:    &mu,
			Sigma: &sigma,
			Z:     opts.Z,
		})
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
func (g MatchmakerEntries) TeamRating(opts *types.OpenSkillOptions) types.TeamRating {
	return types.TeamRating{Team: g.Ratings(opts)}
}

func (g MatchmakerEntries) TeamOrdinal(opts *types.OpenSkillOptions) float64 {
	return rating.TeamOrdinal(g.TeamRating(opts))
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
		n := min(len(ticket), 8)
		for i := range n {
			hash = (hash * 33) ^ uint64(ticket[i])
		}
	}
	return hash
}

// predictCandidateOutcomesWithConfig allows testing with specific settings
func predictCandidateOutcomesWithConfig(candidates [][]runtime.MatchmakerEntry, cfg PredictionConfig) <-chan PredictedMatch {
	// Generate roster variants based on config if not already specified
	variants := cfg.Variants
	if len(variants) == 0 {
		if cfg.UseSnakeDraftFormation {
			variants = append(variants, RosterVariantSnakeDraft)
		} else {
			variants = append(variants, RosterVariantSequential)
		}
		// If roster variants are enabled, generate both types
		if cfg.EnableRosterVariants {
			if cfg.UseSnakeDraftFormation {
				variants = append(variants, RosterVariantSequential)
			} else {
				variants = append(variants, RosterVariantSnakeDraft)
			}
		}
	}

	out := make(chan PredictedMatch)

	go func() {
		defer close(out)
		// Count valid candidates
		validCount := 0
		for _, candidate := range candidates {
			if candidate != nil {
				validCount++
			}
		}

		// Predefine constants for maximum sizes
		const (
			MaxTeamSize              = 5
			MaxPlayersPerMatch       = MaxTeamSize * 2
			MaxGroupsPerMatch        = MaxPlayersPerMatch // Worst case: all single-player groups
			InitialTicketCacheSize   = 40
			InitialDivisionCacheSize = 10
		)

		// Pre-allocate all reusable data structures
		var (
			seen          = make(map[uint64]struct{}, validCount)
			groupRatings  = make([]types.Team, 0, MaxGroupsPerMatch)
			ticketGroups  = make(map[string]MatchmakerEntries, MaxGroupsPerMatch)
			groups        = make([]MatchmakerEntries, 0, MaxGroupsPerMatch)
			blueTeam      = make(MatchmakerEntries, 0, MaxTeamSize)
			orangeTeam    = make(MatchmakerEntries, 0, MaxTeamSize)
			blueRatings   = make([]types.Rating, 0, MaxTeamSize)
			orangeRatings = make([]types.Rating, 0, MaxTeamSize)
			blueActual    = make([]types.Rating, 0, MaxTeamSize)
			orangeActual  = make([]types.Rating, 0, MaxTeamSize)
			ticketRatings = make(map[string]types.Team, InitialTicketCacheSize)
			ticketDivs    = make(map[string]map[string]struct{}, InitialTicketCacheSize)
			ticketAge     = make(map[string]float64, InitialTicketCacheSize)
			divs          = make(map[string]struct{}, InitialDivisionCacheSize)
			drawProb      float64
		)

		for _, candidate := range candidates {
			if candidate == nil {
				continue
			}

			// Skip duplicate candidates
			h := HashMatchmakerEntries(candidate)
			if _, ok := seen[h]; ok {
				continue
			}
			seen[h] = struct{}{}

			// Clear ticketGroups map
			for k := range ticketGroups {
				delete(ticketGroups, k)
			}

			// Collect tickets efficiently - group entries by ticket
			modestr, _ := candidate[0].GetProperties()["game_mode"].(string)
			isCombat := modestr == evr.ModeCombatPublic.String()
			isPublic := modestr == evr.ModeArenaPublic.String() || modestr == evr.ModeCombatPublic.String() || modestr == evr.ModeSocialPublic.String()

			for _, entry := range candidate {
				ticket := entry.GetTicket()
				if isCombat {
					// For combat, split tickets to allow fair team balancing
					ticket = entry.GetPresence().GetUserId()
				}
				ticketGroups[ticket] = append(ticketGroups[ticket], entry)
			}

			// Skip if no groups formed
			if len(ticketGroups) == 0 {
				continue
			}

			for ticket, entries := range ticketGroups {
				// Check cache to avoid recomputing identical tickets
				if _, ok := ticketRatings[ticket]; !ok {
					// Use boosted ratings for parties when calculating ranks
					ticketRatings[ticket] = entries.RatingsWithPartyBoost(cfg.PartyBoostPercent, cfg.OpenSkillOptions)
					ticketDivs[ticket] = entries.DivisionSet()
				}
				oldest := float64(time.Now().UTC().Unix())
				for _, entry := range entries {
					props := entry.GetProperties()
					if v, ok := props["timestamp"]; ok {
						var ts float64
						switch v := v.(type) {
						case float64:
							ts = v
						case int64:
							ts = float64(v)
						case int:
							ts = float64(v)
						default:
							continue
						}
						if ts < oldest {
							oldest = ts
						}
					}
				}
				ticketAge[ticket] = oldest
			}

			// Reuse groups slice
			groups = groups[:0]
			for _, entries := range ticketGroups {
				groups = append(groups, entries)
			}

			groupRatings = groupRatings[:0]
			for _, g := range groups {
				ticket := g[0].GetTicket()
				if isCombat {
					ticket = g[0].GetPresence().GetUserId()
				}
				groupRatings = append(groupRatings, ticketRatings[ticket])
			}

			ranks, _ := rating.PredictRank(groupRatings, nil)

			// Sort groups by best rating first
			sort.SliceStable(groups, func(i, j int) bool {
				return ranks[i] < ranks[j]
			})

			// Collect division set - reuse map from cache
			for k := range divs {
				delete(divs, k)
			}
			for _, g := range groups {
				ticket := g[0].GetTicket()
				if isCombat {
					ticket = g[0].GetPresence().GetUserId()
				}
				maps.Copy(divs, ticketDivs[ticket])
			}

			minTeamSize := 4
			if v, ok := candidate[0].GetProperties()["min_team_size"]; ok {
				switch v := v.(type) {
				case float64:
					minTeamSize = int(v)
				case int64:
					minTeamSize = int(v)
				case int:
					minTeamSize = v
				}
			}



			teamSize := len(candidate) / 2
			if teamSize < minTeamSize {
				continue
			}

			// For public, enforce a minimum queue time (60s) for the oldest ticket
			// unless a 4v4 match or larger is already possible.
			if isPublic && len(candidate) < 8 {
				oldestTimestamp := float64(time.Now().UTC().Unix())
				for _, g := range groups {
					ticket := g[0].GetPresence().GetUserId()
					if age := ticketAge[ticket]; age < oldestTimestamp {
						oldestTimestamp = age
					}
				}
				if time.Now().UTC().Unix()-int64(oldestTimestamp) < 60 {
					continue
				}
			}

			for _, variant := range variants {
				// Create teams based on variant
				blueTeam, orangeTeam = blueTeam[:0], orangeTeam[:0]
				blueRatings, orangeRatings = blueRatings[:0], orangeRatings[:0]

				switch variant {
				case RosterVariantSequential:
					// Original sequential filling (best groups fill blue team first)
					for _, g := range groups {
						ticket := g[0].GetTicket()
						if isCombat {
							ticket = g[0].GetPresence().GetUserId()
						}
						if len(blueTeam)+len(g) <= teamSize {
							blueTeam = append(blueTeam, g...)
							blueRatings = append(blueRatings, ticketRatings[ticket]...)
						} else {
							orangeTeam = append(orangeTeam, g...)
							orangeRatings = append(orangeRatings, ticketRatings[ticket]...)
						}
					}

				case RosterVariantSnakeDraft:
					// Snake draft: alternates team assignment to balance strength
					// Pattern for 8 picks: A, B, B, A, A, B, B, A (creates balance by giving
					// the weaker team consecutive picks)
					// For groups sorted by rank (best first):
					// - 1st (best) → Blue
					// - 2nd, 3rd → Orange
					// - 4th, 5th → Blue
					// - 6th, 7th → Orange
					// - 8th → Blue
					for idx, g := range groups {
						ticket := g[0].GetTicket()
						if isCombat {
							ticket = g[0].GetPresence().GetUserId()
						}

						// Determine which team gets this group using snake pattern
						// Group index 0: Blue, 1-2: Orange, 3-4: Blue, 5-6: Orange, 7: Blue
						// General snake draft assignment for any group size
						// For two teams: alternate direction every round of 2 picks
						roundSize := 2
						round := idx / roundSize
						pos := idx % roundSize
						var assignToBlue bool
						if round%2 == 0 {
							assignToBlue = (pos == 0)
						} else {
							assignToBlue = (pos == 1)
						}

						// Check if assignment would exceed team size, flip if needed
						if assignToBlue && len(blueTeam)+len(g) > teamSize {
							assignToBlue = false
						} else if !assignToBlue && len(orangeTeam)+len(g) > teamSize {
							assignToBlue = true
						}

						if assignToBlue {
							blueTeam = append(blueTeam, g...)
							blueRatings = append(blueRatings, ticketRatings[ticket]...)
						} else {
							orangeTeam = append(orangeTeam, g...)
							orangeRatings = append(orangeRatings, ticketRatings[ticket]...)
						}
					}
				}

				if len(blueTeam) != len(orangeTeam) {
					continue
				}

				// Create a copy of the candidate slice for this variant
				match := make([]runtime.MatchmakerEntry, len(candidate))
				copy(match[:len(blueTeam)], blueTeam)
				copy(match[len(blueTeam):], orangeTeam)

				// Apply new player team bias: move new players to the stronger team
				// when it does not worsen overall balance.
				if cfg.EnableNewPlayerTeamBias && cfg.NewPlayerThreshold > 0 {
					defaultMu := NewDefaultRating().Mu
					if cfg.OpenSkillOptions != nil && cfg.OpenSkillOptions.Mu != nil {
						defaultMu = *cfg.OpenSkillOptions.Mu
					}
					match = ApplyNewPlayerTeamBias(match, teamSize, cfg.NewPlayerThreshold, defaultMu)
				}

				// Get actual (non-boosted) ratings for draw probability calculation - reuse slices
				// Grow slices if team exceeds pre-allocated capacity
				if len(blueTeam) > cap(blueActual) {
					blueActual = make([]types.Rating, len(blueTeam))
				} else {
					blueActual = blueActual[:len(blueTeam)]
				}
				if len(orangeTeam) > cap(orangeActual) {
					orangeActual = make([]types.Rating, len(orangeTeam))
				} else {
					orangeActual = orangeActual[:len(orangeTeam)]
				}

				blueTeam.RatingsInto(blueActual, cfg.OpenSkillOptions)
				orangeTeam.RatingsInto(orangeActual, cfg.OpenSkillOptions)
				drawProb = rating.PredictDraw([]types.Team{blueActual, orangeActual}, cfg.OpenSkillOptions)

				var compScore int8
				if cfg.EnableArchetypeBalancing {
					compScore = int8(scoreTeamCompositionFromEntries(blueTeam, orangeTeam, cfg.NewPlayerThreshold))
				}

				// Find the oldest ticket across all tickets in this candidate
				oldestTs := int64(math.MaxInt64)
				for ticket := range ticketGroups {
					if a := int64(ticketAge[ticket]); a < oldestTs {
						oldestTs = a
					}
				}

				out <- PredictedMatch{
					Candidate:             match,
					DrawProb:              float32(drawProb),
					Size:                  int8(len(match)),
					DivisionCount:         int8(len(divs)),
					OldestTicketTimestamp: oldestTs,
					Variant:               variant,
					CompositionScore:      compScore,
				}
			}
		}
	}()
	return out
}

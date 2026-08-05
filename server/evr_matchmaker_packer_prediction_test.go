package server

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/intinig/go-openskill/types"
)

// These tests close the gap between the two halves of candidate assembly.
//
// groupEntriesSequentially admits an arena candidate only when its whole
// ticket groups can be partitioned into two equal teams. That is a NECESSARY
// condition downstream — predictCandidateOutcomesWithConfig rejects
// len(blueTeam) != len(orangeTeam) — but it was not SUFFICIENT, because both
// roster variants fill greedily over rating rank with no backtracking and can
// miss a split that exists. The result was rating-dependent total starvation:
// every player in the pool matched or none did, decided by who happened to
// rank first.
//
// Everything here drives the real packer into the real prediction pipeline, so
// the assertions are about matches actually formed, not candidate shapes.

// packerEntry builds one arena entry. Ticket IDs must be UUIDs because
// HashMatchmakerEntries reads the first 8 bytes of the ticket.
func packerEntry(ticket, session string, mu float64) *MatchmakerEntry {
	sid := uuid.NewV5(uuid.Nil, session)
	return &MatchmakerEntry{
		Ticket: uuid.NewV5(uuid.Nil, ticket).String(),
		Presence: &MatchmakerPresence{
			UserId:    uuid.NewV5(uuid.Nil, session).String(),
			SessionId: sid.String(),
			Username:  "player_" + session,
			SessionID: sid,
		},
		Properties: map[string]any{
			"rating_mu":       mu,
			"rating_sigma":    10.0 / 3.0,
			"game_mode":       "echo_arena",
			"group_id":        "guild-a",
			"max_team_size":   4.0,
			"count_multiple":  2.0,
			"submission_time": float64(time.Now().UTC().Unix()),
			"timestamp":       float64(time.Now().UTC().Unix() - 3600),
			"divisions":       "gold",
		},
	}
}

// productionDefaultPredictionConfig mirrors the shipped defaults. Both
// EnableRosterVariants and UseSnakeDraftTeamFormation default to false in
// evr_global_settings.go, so production runs sequential fill only — the
// single variant that cannot recover from a bad greedy assignment. The
// package's defaultPredictionConfig() enables both variants and therefore
// masks the defect; do not substitute it here.
func productionDefaultPredictionConfig() PredictionConfig {
	z := 3
	mu := 10.0
	sigma := 10.0 / 3.0
	tau := 0.3
	return PredictionConfig{
		PartyBoostPercent:      0.10,
		EnableRosterVariants:   false,
		UseSnakeDraftFormation: false,
		OpenSkillOptions:       &types.OpenSkillOptions{Z: &z, Mu: &mu, Sigma: &sigma, Tau: &tau},
	}
}

// buildArenaPool creates one ticket per entry of partySizes, assigning
// mus[i] to every member of party i. Distinct mus control rank order, which
// is the variable the greedy fill is sensitive to.
func buildArenaPool(partySizes []int, mus []float64) []runtime.MatchmakerEntry {
	var entries []runtime.MatchmakerEntry
	for i, size := range partySizes {
		ticket := fmt.Sprintf("party-%d", i)
		for p := 0; p < size; p++ {
			entries = append(entries, packerEntry(ticket, fmt.Sprintf("party-%d-p%d", i, p), mus[i]))
		}
	}
	return entries
}

// placeCandidates runs candidates through prediction and returns the number of
// distinct players placed into non-overlapping matches.
func placeCandidates(candidates [][]runtime.MatchmakerEntry) int {
	placed := 0
	used := make(map[string]bool)
	for p := range predictCandidateOutcomesWithConfig(candidates, productionDefaultPredictionConfig()) {
		overlap := false
		for _, e := range p.Candidate {
			if used[e.GetPresence().GetSessionId()] {
				overlap = true
				break
			}
		}
		if overlap {
			continue
		}
		for _, e := range p.Candidate {
			used[e.GetPresence().GetSessionId()] = true
		}
		placed += len(p.Candidate)
	}
	return placed
}

// allRankOrderings returns every permutation of 0..n-1, reusing the Heap's
// algorithm helper already in this package (evr_matchmaker_balance_integration_test.go).
func allRankOrderings(n int) [][]int {
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	var out [][]int
	permuteInts(idx, func(p []int) { out = append(out, p) })
	return out
}

// TestEmittedCandidatesSurvivePrediction is the regression driver. For every
// shape the feasibility guard admits, EVERY rating ordering must place every
// player the packer emitted. Before partitionGroupsEvenly, 3-2-2-1 placed zero
// players under 4 of its 24 orderings and 3-3-1-1 placed zero whenever the two
// solos outranked the two parties.
func TestEmittedCandidatesSurvivePrediction(t *testing.T) {
	shapes := [][]int{
		{3, 2, 2, 1},
		{3, 3, 1, 1},
		{2, 2, 1, 1},
		{3, 2, 1, 1, 1},
		{4, 4},
		{3, 3, 2},
		{4, 1, 1, 1, 1},
		{2, 2, 2, 2},
	}

	// Rank order is what the greedy fill is sensitive to, and rank is not a
	// simple function of mu: PredictRank scores whole groups, and
	// PartyBoostPercent inflates party mu, so with a narrow spread parties
	// almost always outrank solos and the defect stays hidden. The wide
	// (geometric) spread is what lets a solo outrank a party — it is the only
	// one that turns 3-3-1-1, 2-2-1-1 and 3-2-1-1-1 red. Keep both.
	spreads := []struct {
		name string
		mu   func(rank int) float64
	}{
		{"narrow", func(r int) float64 { return 5.0 + float64(r)*10.0 }},
		{"wide", func(r int) float64 { return 5.0 * math.Pow(4, float64(r)) }},
	}

	for _, shape := range shapes {
		t.Run(fmt.Sprint(shape), func(t *testing.T) {
			emitted := -1
			orderings := allRankOrderings(len(shape))

			for _, spread := range spreads {
				for _, perm := range orderings {
					mus := make([]float64, len(shape))
					for i, r := range perm {
						mus[i] = spread.mu(r)
					}
					candidates := groupEntriesSequentially(buildArenaPool(shape, mus))

					want := 0
					for _, c := range candidates {
						want += len(c)
					}
					if emitted < 0 {
						emitted = want
					}
					// The packer must not be rating-sensitive; it never reads mu.
					if want != emitted {
						t.Fatalf("packer output changed with rating order: %d vs %d", want, emitted)
					}

					if got := placeCandidates(candidates); got != want {
						t.Errorf("%s spread, rating order %v: packer emitted %d players but prediction placed %d",
							spread.name, mus, want, got)
					}
				}
			}

			if emitted == 0 {
				t.Fatalf("shape %v emitted no candidates; it no longer guards anything", shape)
			}
			t.Logf("shape %v: %d players emitted and placed under all %d rating orderings x %d spreads",
				shape, emitted, len(orderings), len(spreads))
		})
	}
}

// TestPartitionGroupsEvenly covers the solver directly, including the cases
// the greedy fills get wrong and the ones where no split exists at all.
func TestPartitionGroupsEvenly(t *testing.T) {
	mkGroups := func(sizes []int) []MatchmakerEntries {
		groups := make([]MatchmakerEntries, len(sizes))
		for i, sz := range sizes {
			g := make(MatchmakerEntries, sz)
			for p := 0; p < sz; p++ {
				g[p] = packerEntry(fmt.Sprintf("g%d", i), fmt.Sprintf("g%d-p%d", i, p), 10.0)
			}
			groups[i] = g
		}
		return groups
	}

	tests := []struct {
		name     string
		sizes    []int
		teamSize int
		wantOK   bool
		wantBlue []int // group sizes assigned to blue, in group order
	}{
		// Greedy first-fit puts {1,1} on blue then overflows both 3-parties
		// into orange. The solver takes the first 3-party instead.
		{"solos first, parties last", []int{1, 1, 3, 3}, 4, true, []int{1, 3}},
		{"3-2-2-1 worst order", []int{2, 3, 1, 2}, 4, true, []int{2, 2}},
		{"two 4-parties", []int{4, 4}, 4, true, []int{4}},
		{"all solos", []int{1, 1, 1, 1}, 2, true, []int{1, 1}},
		// Blue prefers the strongest groups it can take, matching sequential's
		// intent: the leading 3-party goes to blue, not the trailing one.
		{"prefers earlier groups", []int{3, 1, 3, 1}, 4, true, []int{3, 1}},
		{"no even split of 4+2", []int{4, 2}, 3, false, nil},
		{"no even split of 3+3+2", []int{3, 3, 2}, 4, false, nil},
		{"odd total", []int{3, 1, 1}, 2, false, nil},
		{"teamSize zero", []int{1}, 0, false, nil},
		{"no groups", []int{}, 2, false, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groups := mkGroups(tt.sizes)
			mask, ok := partitionGroupsEvenly(groups, tt.teamSize)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}

			var blue, orange []int
			blueTotal := 0
			for i, m := range mask {
				if m {
					blue = append(blue, tt.sizes[i])
					blueTotal += tt.sizes[i]
				} else {
					orange = append(orange, tt.sizes[i])
				}
			}
			if blueTotal != tt.teamSize {
				t.Errorf("blue sums to %d, want %d (blue=%v orange=%v)", blueTotal, tt.teamSize, blue, orange)
			}
			if fmt.Sprint(blue) != fmt.Sprint(tt.wantBlue) {
				t.Errorf("blue groups = %v, want %v", blue, tt.wantBlue)
			}
		})
	}
}

// TestPartitionGroupsEvenlyIsDeterministic pins that the solver is a pure
// function of group order, so equal-rated pools cannot flip between matched
// and unmatched from one cycle to the next.
func TestPartitionGroupsEvenlyIsDeterministic(t *testing.T) {
	sizes := []int{3, 1, 2, 2}
	groups := make([]MatchmakerEntries, len(sizes))
	for i, sz := range sizes {
		g := make(MatchmakerEntries, sz)
		for p := 0; p < sz; p++ {
			g[p] = packerEntry(fmt.Sprintf("d%d", i), fmt.Sprintf("d%d-p%d", i, p), 10.0)
		}
		groups[i] = g
	}

	first, ok := partitionGroupsEvenly(groups, 4)
	if !ok {
		t.Fatal("expected a partition of 3+1+2+2 into two teams of 4")
	}
	for i := 0; i < 50; i++ {
		got, ok := partitionGroupsEvenly(groups, 4)
		if !ok || fmt.Sprint(got) != fmt.Sprint(first) {
			t.Fatalf("run %d differed: %v vs %v (ok=%v)", i, got, first, ok)
		}
	}
}

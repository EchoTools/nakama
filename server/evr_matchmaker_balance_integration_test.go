package server

import (
	"math"
	"testing"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/types"
)

// =============================================================================
// In-process integration tests guarding the matchmaker team-balance fixes on
// branch test/mm-balance-integration (off the #478 changes).
//
// These tests run WITHOUT a database. They construct ticket sets in memory and
// drive them through the production team-assignment path:
//
//	predictCandidateOutcomesWithConfig -> PredictRank -> snake/sequential draft
//
// Fixes guarded:
//
//   - 8744d8a42 "permute ranks with groups when sorting predicted rosters":
//     predictCandidateOutcomesWithConfig sorted groups by rank but indexed an
//     unpermuted ranks slice, so once sort.SliceStable began swapping group
//     positions the rank used for a slot no longer described the group in that
//     slot. The corruption was Go-MAP-ORDER-DEPENDENT (groups are collected by
//     ranging a map), so it only manifested for some input orderings and piled
//     both top-skill players onto one team in the snake-draft variant.
//
//     The teeth here come from feeding the SAME roster in MANY input orderings
//     (all permutations of a small roster, plus many deterministic shuffles of
//     larger rosters) and asserting the teams are ALWAYS balanced: the top
//     players are split across teams, never piled onto one, regardless of input
//     order.
//
//   - 3248415a0 "full-roster rating weights": CalculateRatingWeights now seeds
//     every player at 0, so the returned map always represents the FULL roster
//     even for players who neither scored nor were on the winning team.
// =============================================================================

// snakeConfig is the prediction config used by the balance integration tests.
// It forces snake-draft formation (the variant the 8744d8a42 bug corrupted) and
// pins explicit OpenSkill options so results are deterministic across runs.
func snakeConfig() PredictionConfig {
	return PredictionConfig{
		Variants:         []RosterVariant{RosterVariantSnakeDraft},
		OpenSkillOptions: &types.OpenSkillOptions{},
	}
}

// teamMuStrengths drives an 8-player roster through the production prediction
// path for a single input ordering and returns the (Team A, Team B) summed-Mu
// strengths plus the resulting candidate slice for inspection.
func teamMuStrengths(t *testing.T, entries []*MatchmakerEntry, cfg PredictionConfig) (float64, float64, []runtime.MatchmakerEntry) {
	t.Helper()

	candidate := make([]runtime.MatchmakerEntry, len(entries))
	for i, e := range entries {
		candidate[i] = e
	}
	candidates := [][]runtime.MatchmakerEntry{candidate}

	var result PredictedMatch
	got := 0
	for p := range predictCandidateOutcomesWithConfig(candidates, cfg) {
		result = p
		got++
	}
	if got == 0 {
		t.Fatalf("prediction produced no result for roster of %d", len(entries))
	}
	if len(result.Candidate) != len(entries) {
		t.Fatalf("expected %d players in result, got %d", len(entries), len(result.Candidate))
	}

	half := len(result.Candidate) / 2
	teamA := result.Candidate[:half]
	teamB := result.Candidate[half:]
	return calculateTeamStrength(teamA), calculateTeamStrength(teamB), result.Candidate
}

// permuteInts yields every permutation of the input index slice via Heap's
// algorithm, invoking fn for each. Used to exhaust the input orderings of a
// small roster so the map-order-dependent bug cannot hide in an unlucky order.
func permuteInts(idx []int, fn func([]int)) {
	var recurse func(k int)
	recurse = func(k int) {
		if k == 1 {
			perm := make([]int, len(idx))
			copy(perm, idx)
			fn(perm)
			return
		}
		for i := 0; i < k; i++ {
			recurse(k - 1)
			if k%2 == 0 {
				idx[i], idx[k-1] = idx[k-1], idx[i]
			} else {
				idx[0], idx[k-1] = idx[k-1], idx[0]
			}
		}
	}
	recurse(len(idx))
}

// reorder returns a fresh slice of entries arranged by the given permutation.
func reorder(entries []*MatchmakerEntry, perm []int) []*MatchmakerEntry {
	out := make([]*MatchmakerEntry, len(perm))
	for i, p := range perm {
		out[i] = entries[p]
	}
	return out
}

// topMus returns the two highest Mu values present in the roster.
func topTwoMus(entries []*MatchmakerEntry) (float64, float64) {
	first, second := math.Inf(-1), math.Inf(-1)
	for _, e := range entries {
		mu := e.Properties["rating_mu"].(float64)
		if mu > first {
			second = first
			first = mu
		} else if mu > second {
			second = mu
		}
	}
	return first, second
}

// countTeamMu reports how many entries on a team have the given Mu value.
func countTeamMu(team []runtime.MatchmakerEntry, mu float64) int {
	n := 0
	for _, e := range team {
		if e.GetProperties()["rating_mu"].(float64) == mu {
			n++
		}
	}
	return n
}

// TestSnakeDraftBalance_AllOrderings is the CORE guard for 8744d8a42.
//
// It builds a roster with two clear top-skill players and six lower-skill
// players. The 8744d8a42 bug piled BOTH top players onto one team for the
// input orderings where the map-collected group slice happened to confuse the
// rank-to-group mapping during the sort. By exhausting EVERY permutation of the
// roster as input order, this test guarantees it exercises the orderings that
// previously corrupted the sort. For every ordering the snake draft MUST:
//
//   - split the two top-skill players across the two teams (never both on one),
//   - keep the summed-Mu imbalance within tolerance.
func TestSnakeDraftBalance_AllOrderings(t *testing.T) {
	// Two clear pros + six beginners. A balanced split puts one pro on each
	// team; piling both pros on one team is the exact failure the fix prevents.
	makeRoster := func() []*MatchmakerEntry {
		return []*MatchmakerEntry{
			createTestEntry("pro1", "pro1", 42.0, 2.0),
			createTestEntry("pro2", "pro2", 40.0, 2.0),
			createTestEntry("beg1", "beg1", 16.0, 5.0),
			createTestEntry("beg2", "beg2", 15.5, 5.0),
			createTestEntry("beg3", "beg3", 15.0, 5.0),
			createTestEntry("beg4", "beg4", 14.5, 5.0),
			createTestEntry("beg5", "beg5", 14.0, 5.0),
			createTestEntry("beg6", "beg6", 13.5, 5.0),
		}
	}

	base := makeRoster()
	pro1Mu, pro2Mu := topTwoMus(base)

	// Total Mu is fixed; tolerate at most this fraction of imbalance. A run
	// that piles both pros on one team blows past this by a wide margin.
	const maxImbalancePct = 25.0

	idx := make([]int, len(base))
	for i := range idx {
		idx[i] = i
	}

	orderings := 0
	worstImbalance := 0.0
	permuteInts(idx, func(perm []int) {
		orderings++
		roster := reorder(base, perm)

		strengthA, strengthB, candidate := teamMuStrengths(t, roster, snakeConfig())
		half := len(candidate) / 2
		teamA := candidate[:half]
		teamB := candidate[half:]

		// Core assertion: the two top players are NEVER on the same team.
		pro1A := countTeamMu(teamA, pro1Mu)
		pro1B := countTeamMu(teamB, pro1Mu)
		pro2A := countTeamMu(teamA, pro2Mu)
		pro2B := countTeamMu(teamB, pro2Mu)
		topOnA := pro1A + pro2A
		topOnB := pro1B + pro2B
		if topOnA == 2 || topOnB == 2 {
			t.Fatalf("ordering %v piled both top players onto one team (A=%d, B=%d): teamA=%v teamB=%v",
				perm, topOnA, topOnB, getPlayerMus(teamA), getPlayerMus(teamB))
		}

		imbalance := math.Abs(strengthA-strengthB) / (strengthA + strengthB) * 100
		if imbalance > worstImbalance {
			worstImbalance = imbalance
		}
		if imbalance > maxImbalancePct {
			t.Fatalf("ordering %v produced imbalance %.2f%% (> %.1f%%): teamA=%v (%.1f) teamB=%v (%.1f)",
				perm, imbalance, maxImbalancePct, getPlayerMus(teamA), strengthA, getPlayerMus(teamB), strengthB)
		}
	})

	if orderings != 40320 { // 8!
		t.Fatalf("expected to exercise all 8! = 40320 orderings, exercised %d", orderings)
	}
	t.Logf("snake draft kept top players split and imbalance <= %.2f%% across all %d input orderings",
		worstImbalance, orderings)
}

// TestSnakeDraftBalance_DeterministicShuffles complements the exhaustive test
// with larger, mixed rosters (parties + solos, varied mu/sigma) fed in many
// deterministic, index-seeded shuffles. This widens coverage of the
// map-order-dependent path without a 10!+ permutation blow-up.
func TestSnakeDraftBalance_DeterministicShuffles(t *testing.T) {
	type rosterCase struct {
		name         string
		entries      []*MatchmakerEntry
		maxImbalance float64
		topMustSplit bool // top-2 skilled players must end up on opposite teams
	}

	// Roster A: a high-skill party of 2 plus six solos of descending skill.
	// Parties stay together, so the two party members both land on one team;
	// the guard here is overall balance staying within tolerance regardless of
	// input order.
	partyRoster := func() []*MatchmakerEntry {
		out := []*MatchmakerEntry{}
		party := createTestParty("hi-party", []struct {
			id        string
			mu, sigma float64
		}{
			{"pp1", 30.0, 3.0},
			{"pp2", 29.0, 3.0},
		})
		out = append(out, party...)
		out = append(out,
			createTestEntry("ps1", "ps1", 28.0, 3.0),
			createTestEntry("ps2", "ps2", 24.0, 3.5),
			createTestEntry("ps3", "ps3", 22.0, 4.0),
			createTestEntry("ps4", "ps4", 20.0, 4.5),
			createTestEntry("ps5", "ps5", 18.0, 5.0),
			createTestEntry("ps6", "ps6", 16.0, 5.5),
		)
		return out
	}

	// Roster B: eight solos with two clear standouts that must be split.
	soloRoster := func() []*MatchmakerEntry {
		return []*MatchmakerEntry{
			createTestEntry("s1", "s1", 38.0, 2.5),
			createTestEntry("s2", "s2", 36.0, 2.5),
			createTestEntry("s3", "s3", 21.0, 3.5),
			createTestEntry("s4", "s4", 20.0, 3.5),
			createTestEntry("s5", "s5", 19.0, 4.0),
			createTestEntry("s6", "s6", 18.0, 4.0),
			createTestEntry("s7", "s7", 17.0, 4.5),
			createTestEntry("s8", "s8", 16.0, 4.5),
		}
	}

	cases := []rosterCase{
		{name: "high-party-plus-solos", entries: partyRoster(), maxImbalance: 35.0, topMustSplit: false},
		{name: "two-standout-solos", entries: soloRoster(), maxImbalance: 25.0, topMustSplit: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			base := tc.entries
			pro1Mu, pro2Mu := topTwoMus(base)

			idx := make([]int, len(base))
			for i := range idx {
				idx[i] = i
			}

			const shuffles = 500
			worst := 0.0
			for seed := 0; seed < shuffles; seed++ {
				perm := deterministicShuffle(idx, seed)
				roster := reorder(base, perm)

				strengthA, strengthB, candidate := teamMuStrengths(t, roster, snakeConfig())
				half := len(candidate) / 2
				teamA := candidate[:half]
				teamB := candidate[half:]

				if tc.topMustSplit {
					topOnA := countTeamMu(teamA, pro1Mu) + countTeamMu(teamA, pro2Mu)
					topOnB := countTeamMu(teamB, pro1Mu) + countTeamMu(teamB, pro2Mu)
					if topOnA == 2 || topOnB == 2 {
						t.Fatalf("seed %d (%v) piled both top players onto one team: teamA=%v teamB=%v",
							seed, perm, getPlayerMus(teamA), getPlayerMus(teamB))
					}
				}

				imbalance := math.Abs(strengthA-strengthB) / (strengthA + strengthB) * 100
				if imbalance > worst {
					worst = imbalance
				}
				if imbalance > tc.maxImbalance {
					t.Fatalf("seed %d (%v) produced imbalance %.2f%% (> %.1f%%): teamA=%v (%.1f) teamB=%v (%.1f)",
						seed, perm, imbalance, tc.maxImbalance, getPlayerMus(teamA), strengthA, getPlayerMus(teamB), strengthB)
				}
			}
			t.Logf("%s: imbalance <= %.2f%% across %d deterministic shuffles", tc.name, worst, shuffles)
		})
	}
}

// deterministicShuffle returns a permutation of idx derived purely from seed,
// so the test is fully reproducible (no time/rand source). It uses a simple
// LCG-driven Fisher-Yates so different seeds yield different, repeatable orders.
func deterministicShuffle(idx []int, seed int) []int {
	out := make([]int, len(idx))
	copy(out, idx)
	state := uint64(seed)*2862933555777941757 + 3037000493
	for i := len(out) - 1; i > 0; i-- {
		state = state*6364136223846793005 + 1442695040888963407
		j := int(state>>33) % (i + 1)
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// TestSnakeDraftBalance_OrderInvariant asserts the snake draft is fully input
// order-INVARIANT: the same roster must yield the SAME team strengths no matter
// how the entries are ordered on input. The 8744d8a42 bug made the outcome
// depend on input/map order; with the fix the (sorted) strengths are stable.
func TestSnakeDraftBalance_OrderInvariant(t *testing.T) {
	base := []*MatchmakerEntry{
		createTestEntry("o1", "o1", 35.0, 2.0),
		createTestEntry("o2", "o2", 33.0, 2.0),
		createTestEntry("o3", "o3", 20.0, 4.0),
		createTestEntry("o4", "o4", 19.0, 4.0),
		createTestEntry("o5", "o5", 18.0, 4.0),
		createTestEntry("o6", "o6", 17.0, 4.0),
		createTestEntry("o7", "o7", 16.0, 4.0),
		createTestEntry("o8", "o8", 15.0, 4.0),
	}

	idx := make([]int, len(base))
	for i := range idx {
		idx[i] = i
	}

	// Reference outcome from the identity ordering. We compare the SORTED pair
	// of strengths because the fix guarantees balance, not a fixed team label.
	refA, refB, _ := teamMuStrengths(t, base, snakeConfig())
	refLo, refHi := math.Min(refA, refB), math.Max(refA, refB)

	checked := 0
	permuteInts(idx, func(perm []int) {
		checked++
		roster := reorder(base, perm)
		a, b, _ := teamMuStrengths(t, roster, snakeConfig())
		lo, hi := math.Min(a, b), math.Max(a, b)
		if math.Abs(lo-refLo) > 1e-9 || math.Abs(hi-refHi) > 1e-9 {
			t.Fatalf("ordering %v changed team strengths: got {%.4f, %.4f}, want {%.4f, %.4f}",
				perm, lo, hi, refLo, refHi)
		}
	})
	if checked != 40320 {
		t.Fatalf("expected 8! = 40320 orderings, checked %d", checked)
	}
	t.Logf("snake-draft team strengths were invariant across all %d input orderings (sorted {%.1f, %.1f})",
		checked, refLo, refHi)
}

// TestSnakeDraftBeatsSequential_AcrossOrderings cross-checks that, for the
// worst-case 2-pro/6-beginner roster, snake draft is at least as balanced as
// sequential filling for EVERY input ordering, and strictly better on average.
// Pre-fix, the corrupted sort made snake collapse to sequential-or-worse for
// some orderings; this catches that regression from a different angle.
func TestSnakeDraftBeatsSequential_AcrossOrderings(t *testing.T) {
	makeRoster := func() []*MatchmakerEntry {
		return []*MatchmakerEntry{
			createTestEntry("x1", "x1", 41.0, 2.0),
			createTestEntry("x2", "x2", 39.0, 2.0),
			createTestEntry("x3", "x3", 15.0, 5.0),
			createTestEntry("x4", "x4", 15.0, 5.0),
			createTestEntry("x5", "x5", 14.0, 5.0),
			createTestEntry("x6", "x6", 14.0, 5.0),
			createTestEntry("x7", "x7", 13.0, 5.0),
			createTestEntry("x8", "x8", 13.0, 5.0),
		}
	}

	base := makeRoster()
	idx := make([]int, len(base))
	for i := range idx {
		idx[i] = i
	}

	seqCfg := PredictionConfig{
		Variants:         []RosterVariant{RosterVariantSequential},
		OpenSkillOptions: &types.OpenSkillOptions{},
	}

	const sampleEvery = 7 // sample orderings to keep runtime bounded but broad
	orderings, sampled, snakeWins := 0, 0, 0
	permuteInts(idx, func(perm []int) {
		orderings++
		if orderings%sampleEvery != 0 {
			return
		}
		sampled++
		roster := reorder(base, perm)

		sa, sb, _ := teamMuStrengths(t, roster, seqCfg)
		seqImb := math.Abs(sa-sb) / (sa + sb) * 100

		na, nb, _ := teamMuStrengths(t, roster, snakeConfig())
		snakeImb := math.Abs(na-nb) / (na + nb) * 100

		if snakeImb > seqImb+1e-9 {
			t.Fatalf("ordering %v: snake (%.2f%%) worse than sequential (%.2f%%)", perm, snakeImb, seqImb)
		}
		if snakeImb < seqImb-1e-9 {
			snakeWins++
		}
	})

	if snakeWins == 0 {
		t.Fatalf("snake draft never improved on sequential across %d sampled orderings — fix likely inactive", sampled)
	}
	t.Logf("snake draft was never worse than sequential and strictly better in %d/%d sampled orderings", snakeWins, sampled)
}

// TestCalculateRatingWeights_FullRoster guards 3248415a0: CalculateRatingWeights
// must compute weights across the FULL roster. A player who neither scored nor
// was on the winning team must still appear in the result (seeded at 0). Before
// the fix such players were absent from the map entirely.
func TestCalculateRatingWeights_FullRoster(t *testing.T) {
	// Eight-player roster (4v4). Blue wins on a single goal by one player.
	// The other seven players never score and are not all on the winning team,
	// yet every one of them must be present in the returned weight map.
	mkID := func(n uint64) evr.EvrId { return evr.EvrId{PlatformCode: evr.DMO, AccountId: n} }

	players := []PlayerInfo{
		{EvrID: mkID(1), Team: BlueTeam},
		{EvrID: mkID(2), Team: BlueTeam},
		{EvrID: mkID(3), Team: BlueTeam},
		{EvrID: mkID(4), Team: BlueTeam},
		{EvrID: mkID(5), Team: OrangeTeam},
		{EvrID: mkID(6), Team: OrangeTeam},
		{EvrID: mkID(7), Team: OrangeTeam},
		{EvrID: mkID(8), Team: OrangeTeam},
	}

	label := &MatchLabel{
		Players: players,
		goals: []*evr.MatchGoal{
			{TeamID: int64(BlueTeam), XPID: mkID(1), PointsValue: 2},
		},
	}

	weights := label.CalculateRatingWeights()

	// Full-roster invariant: every player present.
	if len(weights) != len(players) {
		t.Fatalf("expected weights for all %d players, got %d: %v", len(players), len(weights), weights)
	}
	for _, p := range players {
		if _, ok := weights[p.EvrID]; !ok {
			t.Fatalf("player %v missing from weights map (full-roster seeding regression): %v", p.EvrID, weights)
		}
	}

	// Spot-check the scoring math survives full-roster seeding.
	// Scorer (id 1): 2 points + 4 winning-team bonus = 6.
	if got := weights[mkID(1)]; got != 6 {
		t.Fatalf("scorer weight = %d, want 6", got)
	}
	// Non-scoring winners (ids 2-4): 0 + 4 bonus = 4.
	for _, n := range []uint64{2, 3, 4} {
		if got := weights[mkID(n)]; got != 4 {
			t.Fatalf("non-scoring winner id %d weight = %d, want 4", n, got)
		}
	}
	// Losing, non-scoring players (ids 5-8): seeded at 0, no bonus.
	for _, n := range []uint64{5, 6, 7, 8} {
		if got := weights[mkID(n)]; got != 0 {
			t.Fatalf("losing non-scorer id %d weight = %d, want 0 (must be present, not absent)", n, got)
		}
	}
}

// TestCalculateRatingWeights_ScorelessRosterFullyPresent is a tighter guard on
// 3248415a0: with ZERO goals (a scoreless/edge state) the map must STILL list
// every player. Pre-fix, with no goals the map only ever gained winning-team
// entries; losers were silently dropped.
func TestCalculateRatingWeights_ScorelessRosterFullyPresent(t *testing.T) {
	mkID := func(n uint64) evr.EvrId { return evr.EvrId{PlatformCode: evr.DMO, AccountId: n} }

	players := make([]PlayerInfo, 0, 6)
	for n := uint64(1); n <= 6; n++ {
		team := BlueTeam
		if n > 3 {
			team = OrangeTeam
		}
		players = append(players, PlayerInfo{EvrID: mkID(n), Team: team})
	}

	label := &MatchLabel{Players: players, goals: []*evr.MatchGoal{}}
	weights := label.CalculateRatingWeights()

	if len(weights) != len(players) {
		t.Fatalf("scoreless roster: expected %d entries, got %d: %v", len(players), len(weights), weights)
	}
	for _, p := range players {
		if _, ok := weights[p.EvrID]; !ok {
			t.Fatalf("scoreless roster: player %v absent from weights (full-roster regression)", p.EvrID)
		}
	}
}

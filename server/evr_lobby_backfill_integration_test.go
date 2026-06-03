package server

import (
	"net"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
)

// This file holds DB-free, in-process integration tests that guard the backfill
// production fixes on this branch:
//
//   - 872d50a9f: prepareMatches must DROP candidate matches whose GameServer is
//     nil (no nil holes in the prepared slice), so findBestBackfillMatchOptimized
//     never dereferences a nil hole and panics; groupByTicket must emit groups in
//     deterministic first-seen order.
//   - 4eedc038c: getPossibleTeams must offer a team for BALANCED/EMPTY combat
//     matches so combat backfill is not blocked.
//
// These tests construct candidate match sets directly in memory and run the real
// selection path (prepareMatches -> findBestBackfillMatchOptimized,
// getPossibleTeams, groupByTicket). No database, no match registry, no sessions.

// newPreparedCandidate builds a preparedBackfillCandidate without touching any
// session registry, so the scoring/selection path can be exercised in-process.
func newPreparedCandidate(ticket string, mode evr.Symbol, extIP string) *preparedBackfillCandidate {
	return &preparedBackfillCandidate{
		BackfillCandidate: &BackfillCandidate{
			Ticket:         ticket,
			Mode:           mode,
			TeamAlignment:  evr.TeamUnassigned,
			Rating:         15.0,
			MaxRTT:         180,
			RTTs:           map[string]int{extIP: 50},
			GroupID:        uuid.Must(uuid.NewV4()),
			SubmissionTime: time.Now().Add(-30 * time.Second),
		},
		partySize: 1,
	}
}

// validMatch builds a BackfillMatch with a non-nil GameServer.
func validMatch(mode evr.Symbol, extIP string, blueSlots, orangeSlots, blueCount, orangeCount int) *BackfillMatch {
	return &BackfillMatch{
		Label: &MatchLabel{
			Mode:      mode,
			RatingMu:  15.0,
			StartTime: time.Now().Add(-1 * time.Minute),
			GameServer: &GameServerPresence{
				Endpoint: evr.Endpoint{
					ExternalIP: net.ParseIP(extIP),
				},
			},
		},
		OpenSlots: map[int]int{
			evr.TeamBlue:   blueSlots,
			evr.TeamOrange: orangeSlots,
		},
		TeamCounts: map[int]int{
			evr.TeamBlue:   blueCount,
			evr.TeamOrange: orangeCount,
		},
		RTTs: map[string]int{extIP: 50},
	}
}

// nilGameServerMatch builds a BackfillMatch whose Label.GameServer is nil. Before
// the 872d50a9f fix, prepareMatches left a nil hole for such a match, and
// findBestBackfillMatchOptimized then dereferenced match.BackfillMatch -> panic.
func nilGameServerMatch(mode evr.Symbol, blueSlots, orangeSlots int) *BackfillMatch {
	return &BackfillMatch{
		Label: &MatchLabel{
			Mode:      mode,
			StartTime: time.Now().Add(-1 * time.Minute),
			// GameServer intentionally nil.
		},
		OpenSlots: map[int]int{
			evr.TeamBlue:   blueSlots,
			evr.TeamOrange: orangeSlots,
		},
		TeamCounts: map[int]int{
			evr.TeamBlue:   0,
			evr.TeamOrange: 0,
		},
	}
}

// TestBackfillSelection_NilGameServerMatchesDoNotPanic feeds a candidate set that
// interleaves nil-GameServer matches with valid ones through the real selection
// path (prepareMatches -> findBestBackfillMatchOptimized) and asserts:
//
//   - no panic occurs (the nil-hole teeth for 872d50a9f), and
//   - only matches with a non-nil GameServer are ever considered/selected.
//
// On pre-fix code this panics with a nil-pointer dereference inside
// findBestBackfillMatchOptimized because prepareMatches leaves nil holes for the
// skipped (nil-GameServer) matches and the optimized finder dereferences them.
func TestBackfillSelection_NilGameServerMatchesDoNotPanic(t *testing.T) {
	t.Parallel()

	const goodIP = "10.0.0.7"

	backfill := &PostMatchmakerBackfill{}
	bctx := &backfillContext{
		settings: GlobalMatchmakingSettings{},
		now:      time.Now(),
	}

	// Interleave nil-GameServer matches around the single valid match so that, on
	// the buggy pre-fix code, the nil hole lands at an index the finder visits.
	rawMatches := []*BackfillMatch{
		nilGameServerMatch(evr.ModeArenaPublic, 4, 4),
		validMatch(evr.ModeArenaPublic, goodIP, 4, 4, 0, 0),
		nilGameServerMatch(evr.ModeArenaPublic, 4, 4),
		nilGameServerMatch(evr.ModeArenaPublic, 2, 2),
	}

	// prepareMatches must drop every nil-GameServer match and leave no nil holes.
	prepared := backfill.prepareMatches(rawMatches, bctx)
	require.Len(t, prepared, 1, "only the single valid-GameServer match should survive prepareMatches")
	for i, p := range prepared {
		require.NotNil(t, p, "prepared[%d] must not be a nil hole", i)
		require.NotNil(t, p.BackfillMatch, "prepared[%d].BackfillMatch must not be nil", i)
		require.NotNil(t, p.Label.GameServer, "prepared[%d] must have a non-nil GameServer", i)
	}

	candidate := newPreparedCandidate("nil-hole-ticket", evr.ModeArenaPublic, goodIP)

	// This is the production selection path. On pre-fix code it dereferences a nil
	// hole and panics; require.NotPanics turns that into a clean test failure.
	var result *BackfillResult
	require.NotPanics(t, func() {
		result = backfill.findBestBackfillMatchOptimized(candidate, prepared, bctx)
	}, "selection over a candidate set containing nil-GameServer matches must not panic")

	require.NotNil(t, result, "the valid match should be selected")
	require.Equal(t, goodIP, result.Match.Label.GameServer.Endpoint.GetExternalIP(),
		"the selected match must be the one with a valid GameServer")
	require.True(t, result.Team == evr.TeamBlue || result.Team == evr.TeamOrange,
		"arena backfill must assign blue or orange, got %d", result.Team)
}

// TestBackfillSelection_AllNilGameServerYieldsNoResult is the degenerate case: a
// candidate set composed entirely of nil-GameServer matches. prepareMatches drops
// all of them, the finder is handed an empty slice, and selection returns nil
// without panicking (rather than dereferencing nil holes).
func TestBackfillSelection_AllNilGameServerYieldsNoResult(t *testing.T) {
	t.Parallel()

	backfill := &PostMatchmakerBackfill{}
	bctx := &backfillContext{now: time.Now()}

	rawMatches := []*BackfillMatch{
		nilGameServerMatch(evr.ModeArenaPublic, 4, 4),
		nilGameServerMatch(evr.ModeArenaPublic, 2, 2),
	}

	prepared := backfill.prepareMatches(rawMatches, bctx)
	require.Empty(t, prepared, "no match has a valid GameServer, so none should survive")

	candidate := newPreparedCandidate("all-nil-ticket", evr.ModeArenaPublic, "10.0.0.9")

	var result *BackfillResult
	require.NotPanics(t, func() {
		result = backfill.findBestBackfillMatchOptimized(candidate, prepared, bctx)
	}, "selection over an all-nil-GameServer candidate set must not panic")
	require.Nil(t, result, "no valid match exists, so no result should be returned")
}

// TestGroupByTicket_DeterministicFirstSeenOrder guards the deterministic-grouping
// half of 872d50a9f. groupByTicket must emit groups in first-seen ticket order,
// identically across repeated runs. Before the fix it ranged a map, so group order
// was random per run.
func TestGroupByTicket_DeterministicFirstSeenOrder(t *testing.T) {
	t.Parallel()

	lb := &LobbyBuilder{}

	// Many distinct tickets, with some repeats interleaved. Enough tickets that a
	// map-ranging implementation would almost certainly reorder across runs.
	entrants := []*MatchmakerEntry{
		{Ticket: "t-charlie"},
		{Ticket: "t-alpha"},
		{Ticket: "t-bravo"},
		{Ticket: "t-charlie"}, // repeat: must join existing first-seen group
		{Ticket: "t-delta"},
		{Ticket: "t-echo"},
		{Ticket: "t-alpha"}, // repeat
		{Ticket: "t-foxtrot"},
		{Ticket: "t-golf"},
		{Ticket: "t-hotel"},
		{Ticket: "t-bravo"}, // repeat
		{Ticket: "t-india"},
	}

	// Expected group order is the order tickets are first seen.
	wantOrder := []string{
		"t-charlie", "t-alpha", "t-bravo", "t-delta", "t-echo",
		"t-foxtrot", "t-golf", "t-hotel", "t-india",
	}
	wantSizes := map[string]int{
		"t-charlie": 2, "t-alpha": 2, "t-bravo": 2,
		"t-delta": 1, "t-echo": 1, "t-foxtrot": 1,
		"t-golf": 1, "t-hotel": 1, "t-india": 1,
	}

	groupOrder := func(groups [][]*MatchmakerEntry) []string {
		order := make([]string, 0, len(groups))
		for _, g := range groups {
			require.NotEmpty(t, g, "group must not be empty")
			order = append(order, g[0].Ticket)
		}
		return order
	}

	// First run establishes the expected first-seen order and group sizes.
	first := lb.groupByTicket(entrants)
	require.Equal(t, wantOrder, groupOrder(first), "groups must be in first-seen ticket order")
	for _, g := range first {
		require.Equal(t, wantSizes[g[0].Ticket], len(g),
			"group %q has unexpected size", g[0].Ticket)
		for _, e := range g {
			require.Equal(t, g[0].Ticket, e.Ticket, "every entrant in a group shares the ticket")
		}
	}

	// Repeated runs must produce byte-for-byte identical group ordering.
	for i := range 50 {
		got := groupOrder(lb.groupByTicket(entrants))
		require.Equal(t, wantOrder, got, "group order must be deterministic across runs (iteration %d)", i)
	}
}

// TestBackfillSelection_CombatBalancedEmptyGetsTeam guards 4eedc038c: a
// balanced/empty combat match must offer a team so combat backfill proceeds.
// It checks both getPossibleTeams (the unit-level contract) and the full
// findBestBackfillMatchOptimized selection path.
func TestBackfillSelection_CombatBalancedEmptyGetsTeam(t *testing.T) {
	t.Parallel()

	const combatIP = "10.0.1.5"

	backfill := &PostMatchmakerBackfill{}
	bctx := &backfillContext{
		settings: GlobalMatchmakingSettings{},
		now:      time.Now(),
	}

	scenarios := []struct {
		name        string
		blueCount   int
		orangeCount int
		blueSlots   int
		orangeSlots int
	}{
		{name: "empty combat match (0v0)", blueCount: 0, orangeCount: 0, blueSlots: 5, orangeSlots: 5},
		{name: "balanced combat match (1v1)", blueCount: 1, orangeCount: 1, blueSlots: 4, orangeSlots: 4},
		{name: "balanced combat match (2v2)", blueCount: 2, orangeCount: 2, blueSlots: 3, orangeSlots: 3},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()

			match := validMatch(evr.ModeCombatPublic, combatIP,
				sc.blueSlots, sc.orangeSlots, sc.blueCount, sc.orangeCount)

			candidate := newPreparedCandidate("combat-ticket", evr.ModeCombatPublic, combatIP)

			// getPossibleTeams must offer exactly one open team (prefer blue) for a
			// balanced/empty combat match. Pre-fix this returned no teams.
			teams := backfill.getPossibleTeams(candidate.BackfillCandidate, match, candidate.partySize)
			require.NotEmpty(t, teams, "balanced/empty combat match must offer a team for backfill")
			require.Equal(t, evr.TeamBlue, teams[0], "combat balanced case prefers blue")
			for _, team := range teams {
				require.True(t, team == evr.TeamBlue || team == evr.TeamOrange,
					"combat backfill must only offer blue/orange, got %d", team)
			}

			// Full selection path must produce a usable result for the combat player.
			prepared := backfill.prepareMatches([]*BackfillMatch{match}, bctx)
			require.Len(t, prepared, 1, "valid combat match should survive prepareMatches")

			result := backfill.findBestBackfillMatchOptimized(candidate, prepared, bctx)
			require.NotNil(t, result, "a combat player must be able to backfill a balanced/empty match")
			require.True(t, result.Team == evr.TeamBlue || result.Team == evr.TeamOrange,
				"combat backfill result team must be blue/orange, got %d", result.Team)
			require.Greater(t, result.Score, BackfillMinAcceptableScore,
				"combat backfill score must clear the acceptance threshold")
		})
	}
}

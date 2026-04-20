package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	goatomic "go.uber.org/atomic"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// ---------------------------------------------------------------------------
// groupEntriesSequentially — never-split-a-ticket invariant
// ---------------------------------------------------------------------------

// makePartyTicket returns partySize entries sharing a single ticket ID.
func makePartyTicket(ticketID string, partySize int, props map[string]any) []runtime.MatchmakerEntry {
	entries := make([]runtime.MatchmakerEntry, 0, partySize)
	for i := 0; i < partySize; i++ {
		p := map[string]any{
			"max_team_size":  4.0,
			"count_multiple": 2.0,
		}
		for k, v := range props {
			p[k] = v
		}
		entries = append(entries, &MatchmakerEntry{
			Ticket: ticketID,
			Presence: &MatchmakerPresence{
				UserId:    fmt.Sprintf("%s-user-%d", ticketID, i),
				SessionId: fmt.Sprintf("%s-session-%d", ticketID, i),
				Username:  fmt.Sprintf("%s-%d", ticketID, i),
			},
			Properties: p,
		})
	}
	return entries
}

// TestGroupEntriesNeverSplitsTicket verifies the invariant that
// groupEntriesSequentially never emits a partial ticket. Before the fix,
// the trim-to-countMultiple step would silently drop players from the last
// ticket added to a candidate when its size wasn't a multiple of 2 — so a
// 5-party followed by a 4-party and a 2-party (maxCount=8) produced
// candidates [4 of 5-party] and [4-party, 2-party], dropping one player
// from the 5-party permanently.
func TestGroupEntriesNeverSplitsTicket(t *testing.T) {
	cases := []struct {
		name    string
		parties []int // ticket sizes in input order (largest-first sort applies inside)
	}{
		{"5-4-2", []int{5, 4, 2}},
		{"5-5-2", []int{5, 5, 2}},
		{"3-3-3-3", []int{3, 3, 3, 3}},
		{"5-3-2", []int{5, 3, 2}},
		{"7-5", []int{7, 5}},
		{"1-1-1-1-1-1-1-1-1-1", []int{1, 1, 1, 1, 1, 1, 1, 1, 1, 1}},
		{"2-2-2-2-2", []int{2, 2, 2, 2, 2}},
		{"5-4-1-1-1", []int{5, 4, 1, 1, 1}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var entries []runtime.MatchmakerEntry
			ticketSizes := map[string]int{}
			for i, sz := range tc.parties {
				id := fmt.Sprintf("t%d", i)
				ticketSizes[id] = sz
				entries = append(entries, makePartyTicket(id, sz, nil)...)
			}

			candidates := groupEntriesSequentially(entries)

			// Invariant 1: every candidate size is a multiple of countMultiple (2).
			// Invariant 2: every candidate size is <= maxCount (8).
			// Invariant 3: tickets are never split — for any ticket appearing in
			// a candidate, ALL of its entries appear in that same candidate.
			ticketCandidateCount := map[string]int{}
			for i, c := range candidates {
				if len(c)%2 != 0 {
					t.Errorf("candidate[%d] has odd size %d (must be multiple of 2)", i, len(c))
				}
				if len(c) > 8 {
					t.Errorf("candidate[%d] exceeds maxCount=8: size=%d", i, len(c))
				}
				seen := map[string]int{}
				for _, e := range c {
					seen[e.GetTicket()]++
				}
				for ticket, count := range seen {
					ticketCandidateCount[ticket]++
					if count != ticketSizes[ticket] {
						t.Errorf("candidate[%d] has %d entries for ticket %s but the ticket has %d entries (partial = split)", i, count, ticket, ticketSizes[ticket])
					}
				}
			}

			// Invariant 4: a ticket is either fully matched into exactly one
			// candidate, or not matched at all — never split across candidates
			// AND never partially included.
			for ticket, appearances := range ticketCandidateCount {
				if appearances > 1 {
					t.Errorf("ticket %s appears in %d candidates (split)", ticket, appearances)
				}
			}
		})
	}
}

// TestGroupEntries_5_4_2_NoPlayerDropped nails down the concrete regression:
// with maxCount=8 countMultiple=2 and sorted-largest-first packing,
// [5, 4, 2] must yield candidates where the 4-party and 2-party are paired
// together (total=6) and the 5-party is deferred whole — NOT a [4-of-5]
// candidate plus a [4+2] candidate.
func TestGroupEntries_5_4_2_NoPlayerDropped(t *testing.T) {
	var entries []runtime.MatchmakerEntry
	entries = append(entries, makePartyTicket("a", 5, nil)...)
	entries = append(entries, makePartyTicket("b", 4, nil)...)
	entries = append(entries, makePartyTicket("c", 2, nil)...)

	candidates := groupEntriesSequentially(entries)

	// Expect exactly one candidate: [b(4) + c(2)] = 6 entries.
	require.Len(t, candidates, 1, "expected exactly one candidate (4+2), got %d", len(candidates))
	assert.Len(t, candidates[0], 6)

	// Verify it's b + c, not fragments of a.
	tickets := map[string]int{}
	for _, e := range candidates[0] {
		tickets[e.GetTicket()]++
	}
	assert.Equal(t, 4, tickets["b"], "expected full 4-party b")
	assert.Equal(t, 2, tickets["c"], "expected full 2-party c")
	assert.Equal(t, 0, tickets["a"], "5-party a should NOT appear in any candidate (deferred whole, not split)")
}

// ---------------------------------------------------------------------------
// EvrMatchmakerFn — malformed-entry filter
// ---------------------------------------------------------------------------

// TestEvrMatchmakerFn_FiltersNilPresence verifies that entries with nil
// presence or nil properties are dropped up front instead of panicking the
// matchmaker goroutine.
func TestEvrMatchmakerFn_FiltersNilPresence(t *testing.T) {
	m := NewSkillBasedMatchmaker()
	logger := NewRuntimeGoLogger(loggerForTest(t))

	now := float64(time.Now().UTC().Unix())
	goodProps := map[string]any{
		"group_id":        "g",
		"game_mode":       "echo_arena",
		"max_team_size":   4.0,
		"count_multiple":  2.0,
		"max_rtt":         250.0,
		"submission_time": now,
		"rating_mu":       25.0,
		"rating_sigma":    8.33,
		"rtt_test":        40.0,
	}

	entries := []runtime.MatchmakerEntry{
		nil, // nil entry
		&MatchmakerEntry{Ticket: "t1", Presence: nil, Properties: goodProps}, // nil presence
		&MatchmakerEntry{Ticket: "t2", Presence: &MatchmakerPresence{UserId: "u", SessionId: "s"}, Properties: nil}, // nil properties
		&MatchmakerEntry{Ticket: "t3", Presence: &MatchmakerPresence{UserId: "u3", SessionId: "s3"}, Properties: goodProps},
	}

	// Must not panic.
	assert.NotPanics(t, func() {
		m.EvrMatchmakerFn(context.Background(), logger, nil, nil, entries)
	})
}

// ---------------------------------------------------------------------------
// LobbySessionParameters.GetRating — TOCTOU race resistance
// ---------------------------------------------------------------------------

// TestGetRating_ConcurrentStoreNilDoesNotPanic runs GetRating in a tight loop
// while another goroutine swaps MatchmakingRating to nil and back. Before the
// fix, GetRating checked `.Load() != nil` and then re-called `.Load()` to
// dereference — a Store(nil) between those two calls panicked with nil
// pointer deref.
func TestGetRating_ConcurrentStoreNilDoesNotPanic(t *testing.T) {
	initial := NewDefaultRating()
	params := &LobbySessionParameters{}
	params.SetRating(initial)

	var (
		wg   sync.WaitGroup
		stop atomic.Bool
	)

	// Writer: flip between a real rating and nil.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			params.MatchmakingRating.Store(nil)
			r := types.Rating{Mu: 25, Sigma: 8.33, Z: 3}
			params.MatchmakingRating.Store(&r)
		}
	}()

	// Reader: hammer GetRating. Must never panic.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10000; i++ {
			_ = params.GetRating()
		}
		stop.Store(true)
	}()

	wg.Wait()
}

// ---------------------------------------------------------------------------
// FlushPendingLabelUpdates — synchronous flush before the ticker fires
// ---------------------------------------------------------------------------

// TestFlushPendingLabelUpdates_SynchronousIndex creates a parking-lot match,
// updates its label to a searchable social-lobby state, and asserts that
// after FlushPendingLabelUpdates the Bluge index reflects the new label
// BEFORE the ticker has had a chance to fire. Before the fix, production
// queries ran during this window and saw stale results.
func TestFlushPendingLabelUpdates_SynchronousIndex(t *testing.T) {
	logger := loggerForTest(t)
	groupID := uuid.Must(uuid.NewV4())

	// 100-hour ticker interval: effectively disables auto-flush so we can
	// prove the synchronous flush is what makes the index current.
	matchRegistry, createFn, _, err := createTestMatchRegistryWithInterval(t, logger, int(100*time.Hour/time.Millisecond))
	require.NoError(t, err)

	initialLabel := &MatchLabel{
		Open:      false,
		LobbyType: UnassignedLobby,
		Mode:      evr.ModeUnloaded,
		Level:     evr.LevelUnloaded,
		Players:   make([]PlayerInfo, 0),
	}
	initialBytes, err := json.Marshal(initialLabel)
	require.NoError(t, err)

	matchIDStr, err := matchRegistry.CreateMatch(context.Background(), createFn, "go", map[string]interface{}{
		"label": string(initialBytes),
	})
	require.NoError(t, err)

	// Flush so the initial parking-lot label is indexed (otherwise the match
	// is not findable at all and we can't distinguish "never indexed" from
	// "stale index").
	matchRegistry.FlushPendingLabelUpdates()

	// Now promote to a searchable social lobby.
	preparedLabel := &MatchLabel{
		Open:      true,
		LobbyType: PublicLobby,
		Mode:      evr.ModeSocialPublic,
		Level:     evr.LevelSocial,
		GroupID:   &groupID,
		MaxSize:   SocialLobbyMaxSize,
		TeamSize:  SocialLobbyMaxSize,
		StartTime: time.Now().UTC(),
		CreatedAt: time.Now().UTC(),
		Players:   make([]PlayerInfo, 0),
	}
	preparedBytes, err := json.Marshal(preparedLabel)
	require.NoError(t, err)

	matchID := MatchIDFromStringOrNil(matchIDStr)
	require.NoError(t, matchRegistry.UpdateMatchLabel(matchID.UUID, 10, "module", string(preparedBytes), time.Now().UnixNano()))

	query := fmt.Sprintf("+label.open:T +label.mode:%s +label.group_id:%s",
		evr.ModeSocialPublic.String(),
		Query.QuoteStringValue(groupID.String()),
	)

	// Before flush — the ticker won't fire for 100 hours, so the index is
	// still showing the old parking-lot label.
	matchesBefore, _, err := matchRegistry.ListMatches(context.Background(),
		100, nil, nil, nil, nil,
		&wrapperspb.StringValue{Value: query}, nil,
	)
	require.NoError(t, err)
	assert.Equal(t, 0, len(matchesBefore), "stale-index assumption: match must not be found before flush")

	// Synchronous flush → match visible immediately.
	matchRegistry.FlushPendingLabelUpdates()

	matchesAfter, _, err := matchRegistry.ListMatches(context.Background(),
		100, nil, nil, nil, nil,
		&wrapperspb.StringValue{Value: query}, nil,
	)
	require.NoError(t, err)
	require.Equal(t, 1, len(matchesAfter), "synchronous flush must make the new label searchable without waiting for the ticker")
	assert.Equal(t, matchIDStr, matchesAfter[0].MatchId)
}

// TestFlushPendingLabelUpdates_EmptyQueueNoOp verifies the flush is safe to
// call when pendingUpdates is empty (no panic, no index write).
func TestFlushPendingLabelUpdates_EmptyQueueNoOp(t *testing.T) {
	logger := loggerForTest(t)
	matchRegistry, _, _, err := createTestMatchRegistryWithInterval(t, logger, int(time.Hour/time.Millisecond))
	require.NoError(t, err)

	assert.NotPanics(t, func() {
		matchRegistry.FlushPendingLabelUpdates()
		matchRegistry.FlushPendingLabelUpdates()
	})
}

// ---------------------------------------------------------------------------
// ListMatchStates — minSize parameterization
// ---------------------------------------------------------------------------

// TestListMatchStatesMinSizeParameter is a structural check on the new
// signature — it ensures callers can ask for 0-size matches (social lobby
// discovery) and for 1-size matches (metrics) independently. The behavior
// difference is exercised in TestSocialLobbySearchAfterCreate.
func TestListMatchStatesMinSizeParameter(t *testing.T) {
	// Compile-time check: the signature includes minSize.
	// If someone reverts the parameterization, this test stops compiling.
	var _ = func(ctx context.Context, nk runtime.NakamaModule) {
		_, _ = ListMatchStates(ctx, nk, "*", 0)
		_, _ = ListMatchStates(ctx, nk, "*", 1)
	}
}

// Silence unused-import warnings if any future refactor drops usages.
var _ = goatomic.NewInt64

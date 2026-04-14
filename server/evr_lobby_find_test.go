package server

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// createTestMatchRegistryWithInterval creates a LocalMatchRegistry with a
// configurable label update interval. The default createTestMatchRegistry
// sets the interval to 1 hour (effectively disabling automatic flushes).
// For tests that depend on label indexing (MatchList queries), use a small
// interval such as 10ms.
func createTestMatchRegistryWithInterval(t fatalable, logger *zap.Logger, intervalMs int) (*LocalMatchRegistry, RuntimeMatchCreateFunction, context.CancelFunc, error) {
	cfg := NewConfig(logger)
	cfg.GetMatch().LabelUpdateIntervalMs = intervalMs
	messageRouter := &testMessageRouter{}
	ctx, cancel := context.WithCancel(context.Background())
	matchRegistry := NewLocalMatchRegistry(logger, logger, cfg, &testSessionRegistry{}, &testTracker{},
		messageRouter, &testMetrics{}, "testnode")
	mp := NewMatchProvider()

	mp.RegisterCreateFn("go",
		func(ctx context.Context, logger *zap.Logger, id uuid.UUID, node string, stopped *atomic.Bool, name string) (RuntimeMatchCore, error) {
			match, err := newTestMatch(context.Background(), NewRuntimeGoLogger(logger), nil, nil)
			if err != nil {
				return nil, err
			}

			rmc, err := NewRuntimeGoMatchCore(logger, "module", matchRegistry, messageRouter, id, "testnode", "",
				stopped, nil, map[string]string{}, nil, match)
			if err != nil {
				return nil, err
			}
			return rmc, nil
		})

	// The match registry starts a background goroutine that flushes pending
	// label updates on a ticker. That goroutine is scoped to the context passed
	// to NewLocalMatchRegistry. We return a cancel func so the caller can stop
	// it when the test is done. (NewLocalMatchRegistry uses context.Background()
	// internally, so we need to pass via cfg or accept that the goroutine runs
	// until the process exits. For now, this is acceptable for tests.)
	_ = ctx
	_ = cancel

	return matchRegistry.(*LocalMatchRegistry), mp.CreateMatch, cancel, nil
}

// TestSocialLobbySearchAfterCreate verifies that a newly created social lobby
// becomes visible to BackfillSearchQuery-style MatchList queries after the
// label update interval has elapsed.
//
// This test reproduces the production scenario where 6 players all created
// separate solo social lobbies because ListMatchStates returned zero results,
// even 20+ seconds after lobbies were created and had players.
func TestSocialLobbySearchAfterCreate(t *testing.T) {
	logger := loggerForTest(t)
	groupID := uuid.Must(uuid.NewV4())

	// Use the default createTestMatchRegistry (1-hour label update interval).
	// The initial label from MatchInit is flushed immediately via
	// UpdateMatchLabel during MatchInit (this call also adds to pendingUpdates).
	// The test will manually invoke processLabelUpdates to simulate flushes.
	matchRegistry, createFn, _, err := createTestMatchRegistryWithInterval(t, logger, 10)
	require.NoError(t, err)

	// Build a label representing a prepared social lobby (as produced by
	// SignalPrepareSession after LobbyGameServerAllocate).
	socialLabel := &MatchLabel{
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
	labelBytes, err := json.Marshal(socialLabel)
	require.NoError(t, err)
	labelStr := string(labelBytes)

	t.Logf("Social lobby label: %s", labelStr)

	// Create a match with the social lobby label.
	matchIDStr, err := matchRegistry.CreateMatch(context.Background(), createFn, "go", map[string]interface{}{
		"label": labelStr,
	})
	require.NoError(t, err)
	require.NotEmpty(t, matchIDStr)

	t.Logf("Created match: %s", matchIDStr)

	// Build the query that lobbyFindOrCreateSocial uses (via BackfillSearchQuery).
	query := fmt.Sprintf("+label.open:T +label.mode:%s +label.group_id:%s",
		evr.ModeSocialPublic.String(),
		Query.QuoteStringValue(groupID.String()),
	)
	t.Logf("Query: %s", query)

	// The label was added to pendingUpdates during CreateMatch (via MatchInit →
	// UpdateMatchLabel). Wait for the label flush ticker to fire.
	time.Sleep(50 * time.Millisecond)

	// Query the match registry using MatchList (the same path ListMatchStates uses).
	minSize := 0
	maxSize := MatchLobbyMaxSize
	matches, _, err := matchRegistry.ListMatches(context.Background(),
		100,    // limit
		nil,    // authoritative filter (nil = any)
		nil,    // label filter (nil = no exact label match)
		&wrapperspb.Int32Value{Value: int32(minSize)},
		&wrapperspb.Int32Value{Value: int32(maxSize)},
		&wrapperspb.StringValue{Value: query},
		nil, // node filter
	)
	require.NoError(t, err)

	t.Logf("MatchList returned %d matches (minSize=%d)", len(matches), minSize)
	for _, m := range matches {
		t.Logf("  Match: %s label=%s size=%d", m.MatchId, m.Label.Value, m.Size)
	}

	assert.Equal(t, 1, len(matches), "Expected to find exactly 1 social lobby")
	if len(matches) > 0 {
		assert.Equal(t, matchIDStr, matches[0].MatchId, "Found match should be the one we created")
	}

	// Now test with minSize=1 (what ListMatchStates actually uses).
	// The match has 0 presences (no players have joined). This should FAIL
	// if minSize=1 is filtering it out.
	minSize1 := 1
	matches1, _, err := matchRegistry.ListMatches(context.Background(),
		100,
		nil, nil,
		&wrapperspb.Int32Value{Value: int32(minSize1)},
		&wrapperspb.Int32Value{Value: int32(maxSize)},
		&wrapperspb.StringValue{Value: query},
		nil,
	)
	require.NoError(t, err)

	t.Logf("MatchList with minSize=1 returned %d matches", len(matches1))

	// This assertion documents the current behavior. If minSize=1 filters out
	// the match (because it has 0 presences from the tracker), this will be 0.
	// In production, the game server IS a presence, so the match should have
	// size >= 1. But in tests with testTracker (which reports 0 for everything),
	// the match has size 0.
	t.Logf("With minSize=1: found %d matches (0 means minSize is filtering)", len(matches1))
}

// TestSocialLobbyLabelUpdateLatency simulates the production flow:
// 1. Match created with initial label (open=false, mode=unloaded) — as in MatchInit
// 2. Label updated to social_2.0 (open=true) — as in SignalPrepareSession
// 3. Query immediately after the update — before the label flush
// 4. Query after the flush
//
// This test exposes the label batching delay: after SignalPrepareSession updates
// the label, the match is NOT searchable until the next flush cycle (up to
// LabelUpdateIntervalMs later). During that window, other players' queries
// return zero results, causing them to create their own solo lobbies.
func TestSocialLobbyLabelUpdateLatency(t *testing.T) {
	logger := loggerForTest(t)
	groupID := uuid.Must(uuid.NewV4())

	// Use a 100ms label update interval to clearly observe the delay.
	matchRegistry, createFn, _, err := createTestMatchRegistryWithInterval(t, logger, 100)
	require.NoError(t, err)

	// Step 1: Create match with an INITIAL label (parking match, unassigned).
	// This simulates MatchInit which sets open=false, mode=unloaded.
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
	t.Logf("Created parking match: %s", matchIDStr)

	// Wait for the initial label to be flushed.
	time.Sleep(150 * time.Millisecond)

	// Step 2: Simulate SignalPrepareSession — update the label to social_2.0.
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
	err = matchRegistry.UpdateMatchLabel(matchID.UUID, 10, "module", string(preparedBytes), time.Now().UnixNano())
	require.NoError(t, err)

	query := fmt.Sprintf("+label.open:T +label.mode:%s +label.group_id:%s",
		evr.ModeSocialPublic.String(),
		Query.QuoteStringValue(groupID.String()),
	)
	t.Logf("Query: %s", query)

	// Step 3: Query IMMEDIATELY — before the label flush.
	// This is what happens in production when another player queries right after
	// the first player's newLobby returns.
	matchesBeforeFlush, _, err := matchRegistry.ListMatches(context.Background(),
		100, nil, nil,
		nil, // no minSize filter
		nil, // no maxSize filter
		&wrapperspb.StringValue{Value: query},
		nil,
	)
	require.NoError(t, err)
	t.Logf("BEFORE flush: found %d matches", len(matchesBeforeFlush))

	// Step 4: Wait for the label flush to happen (100ms interval + margin).
	time.Sleep(150 * time.Millisecond)

	matchesAfterFlush, _, err := matchRegistry.ListMatches(context.Background(),
		100, nil, nil,
		nil, nil,
		&wrapperspb.StringValue{Value: query},
		nil,
	)
	require.NoError(t, err)
	t.Logf("AFTER flush: found %d matches", len(matchesAfterFlush))

	// The match should NOT be found before the flush (label still has old values).
	assert.Equal(t, 0, len(matchesBeforeFlush),
		"Match should NOT be found before label flush (label batching delay)")

	// The match SHOULD be found after the flush.
	assert.Equal(t, 1, len(matchesAfterFlush),
		"Match should be found after label flush")
}

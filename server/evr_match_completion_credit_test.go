package server

import (
	"context"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// matchOverState builds the minimum MatchLabel that drives the MatchLoop
// MatchOver branch: an arena public match that has started, still has the
// player on the server, and whose game state reports the match is over.
func matchOverState(player *EvrMatchPresence) *MatchLabel {
	serverSession := uuid.Must(uuid.NewV4())
	operatorID := uuid.Must(uuid.NewV4())
	groupID := uuid.Must(uuid.NewV4())

	state := &MatchLabel{
		ID:        MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "test-node"},
		CreatedAt: time.Now().UTC().Add(-time.Minute),
		StartTime: time.Now().UTC().Add(-time.Minute),
		Open:      true,
		LobbyType: PublicLobby,
		Mode:      evr.ModeArenaPublic,
		Level:     evr.LevelArena,
		MaxSize:   16,
		GroupID:   &groupID,
		GameServer: &GameServerPresence{
			SessionID:  serverSession,
			OperatorID: operatorID,
			GroupIDs:   []uuid.UUID{groupID},
		},
		server: &Presence{
			ID:     PresenceID{Node: "test-node", SessionID: serverSession},
			UserID: operatorID,
		},
		GameState:             &GameState{MatchOver: true},
		Players:               make([]PlayerInfo, 0, 16),
		presenceMap:           make(map[string]*EvrMatchPresence, 16),
		reservationMap:        make(map[string]*slotReservation),
		reconnectReservations: make(map[string]*reconnectReservation),
		presenceByEvrID:       make(map[evr.EvrId]*EvrMatchPresence, 16),
		TeamAlignments:        make(map[string]int, 16),
		joinTimestamps:        make(map[string]time.Time, 16),
		joinTimeMilliseconds:  make(map[string]int64, 16),
		participations:        make(map[string]*PlayerParticipation, 16),
		tickRate:              10,
	}
	state.levelLoaded = true
	state.presenceMap[player.GetSessionId()] = player
	state.joinTimestamps[player.GetSessionId()] = time.Now()
	state.rebuildCache()
	return state
}

// TestMatchLoopCompletion_CreditSurvivesARejectedCounterWrite is the MatchLoop
// half of the completion-credit ordering, the sibling of
// TestMatchCompletion_CreditSurvivesARejectedCounterWrite (which drives the
// post-match stats upload).
//
// Both reporters share one dedupe record — the player's completion history — so
// each of them must commit that record and the credited counter together. The
// MatchLoop MatchOver dispatch committed the record first and only Warn-logged a
// failed counter write, so a counter write that lost an optimistic-concurrency
// race left the match marked as counted but never counted. The stats upload
// then saw the record, returned first=false, and the credit was gone for good.
//
// Correct behaviour: a rejected write leaves no dedupe record behind, so the
// other reporter still finds an uncounted match and credits it exactly once.
func TestMatchLoopCompletion_CreditSurvivesARejectedCounterWrite(t *testing.T) {
	logger := reconnectTestLogger()
	nk := newCompletionTestNK()
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")

	player := reconnectTestPlayer("completion", evr.TeamBlue)
	userID := player.GetUserId()
	state := matchOverState(player)

	// One concurrent writer wins the race against the counter row, exactly once.
	rejected := false
	nk.beforeWrite = func(writes []*runtime.StorageWrite) error {
		if rejected {
			return nil
		}
		for _, w := range writes {
			if w.Collection == StorageCollectionEarlyQuit && w.Key == StorageKeyEarlyQuit && w.UserID == userID && w.Version != "" {
				rejected = true
				return runtime.ErrStorageRejectedVersion
			}
		}
		return nil
	}

	// Reporter 1: the MatchLoop MatchOver dispatch. tick must be a multiple of
	// 2*tickRate for the participation/summary block to run.
	m := &EvrMatch{}
	if out := m.MatchLoop(ctx, logger, nil, nk, &drainTestDispatcher{}, int64(2*state.tickRate), state, nil); out == nil {
		t.Fatal("MatchLoop terminated the match")
	}
	if !state.matchSummarySent {
		t.Fatal("precondition: the MatchOver branch never ran, so no completion was reported")
	}
	if !rejected {
		t.Fatal("precondition: the injected rejection never fired; the counter write was not attempted")
	}

	// Reporter 2: the post-match stats upload for the same match.
	s := &EventRemoteLogSet{}
	if err := s.incrementCompletedMatches(ctx, logger, nk, nil, &testSessionRegistry{}, userID, "", state.ID); err != nil {
		t.Fatalf("stats-upload report: %v", err)
	}

	stored, history := storedCompletionState(t, ctx, nk, userID)
	if stored.TotalCompletedMatches != 1 {
		t.Errorf("TotalCompletedMatches = %d, want 1: the completion was recorded but never credited",
			stored.TotalCompletedMatches)
	}
	if got := countCompletionsFor(history, state.ID); got != 1 {
		t.Errorf("completion records for the match = %d, want 1", got)
	}
}

// TestMatchLoopCompletion_CreditedOnceAcrossBothReporters is the ordinary
// no-fault path: the MatchLoop dispatch credits the match, and the post-match
// stats upload for the same match must find the dedupe record and credit
// nothing further.
func TestMatchLoopCompletion_CreditedOnceAcrossBothReporters(t *testing.T) {
	logger := reconnectTestLogger()
	nk := newCompletionTestNK()
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")

	player := reconnectTestPlayer("completion-clean", evr.TeamOrange)
	userID := player.GetUserId()
	state := matchOverState(player)

	m := &EvrMatch{}
	if out := m.MatchLoop(ctx, logger, nil, nk, &drainTestDispatcher{}, int64(2*state.tickRate), state, nil); out == nil {
		t.Fatal("MatchLoop terminated the match")
	}
	if !state.matchSummarySent {
		t.Fatal("precondition: the MatchOver branch never ran, so no completion was reported")
	}

	stored, history := storedCompletionState(t, ctx, nk, userID)
	if stored.TotalCompletedMatches != 1 {
		t.Fatalf("after the MatchLoop report, TotalCompletedMatches = %d, want 1", stored.TotalCompletedMatches)
	}
	if got := countCompletionsFor(history, state.ID); got != 1 {
		t.Fatalf("after the MatchLoop report, completion records = %d, want 1", got)
	}

	s := &EventRemoteLogSet{}
	if err := s.incrementCompletedMatches(ctx, logger, nk, nil, &testSessionRegistry{}, userID, "", state.ID); err != nil {
		t.Fatalf("stats-upload report: %v", err)
	}

	stored, history = storedCompletionState(t, ctx, nk, userID)
	if stored.TotalCompletedMatches != 1 {
		t.Errorf("TotalCompletedMatches = %d after the second reporter, want 1: the match was double-credited",
			stored.TotalCompletedMatches)
	}
	if got := countCompletionsFor(history, state.ID); got != 1 {
		t.Errorf("completion records for the match = %d, want 1: the dedupe record was duplicated", got)
	}
}

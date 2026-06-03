package server

// Regression tests for issue #439: /shutdown-match orphans the game server.
//
// Root cause: MatchShutdown set a fixed terminateTick (tick + grace*tickRate)
// and the match loop fired MatchTerminate unconditionally once that tick was
// reached, REGARDLESS of whether players had actually left the game server.
// MatchTerminate disconnects the game-server session, so the server was torn
// down while players were still on it ("orphaned").
//
// The fix performs an orderly drain:
//  1. Kick the players (entrant reject + CODE_ENDED) so the game server removes
//     them and MatchLeave fires for each, emptying presenceMap.
//  2. Wait — while players remain and the bounded deadline has not elapsed, the
//     loop must NOT terminate.
//  3. Terminate as soon as presenceMap is empty (drain complete), OR when the
//     bounded deadline elapses (timeout fallback for a non-cooperating server).
//
// These tests are written RED-first: they fail against the pre-fix behavior
// (fixed-deadline teardown) and pass once the drain gate is in place.

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap/zapcore"
)

// drainTestNK is a NakamaModule stub that records SessionDisconnect calls and
// satisfies the no-op surface MatchShutdown / MatchTerminate touch.
type drainTestNK struct {
	runtime.NakamaModule
	mu               sync.Mutex
	disconnectedSIDs []string
}

func (m *drainTestNK) MetricsCounterAdd(_ string, _ map[string]string, _ int64)          {}
func (m *drainTestNK) MetricsTimerRecord(_ string, _ map[string]string, _ time.Duration) {}
func (m *drainTestNK) MetricsGaugeSet(_ string, _ map[string]string, _ float64)          {}

func (m *drainTestNK) StorageWrite(_ context.Context, _ []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	return nil, nil
}

func (m *drainTestNK) StorageRead(_ context.Context, _ []*runtime.StorageRead) ([]*api.StorageObject, error) {
	return nil, nil
}

func (m *drainTestNK) SessionDisconnect(_ context.Context, sessionID string, _ ...runtime.PresenceReason) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disconnectedSIDs = append(m.disconnectedSIDs, sessionID)
	return nil
}

// drainTestDispatcher records every BroadcastMessageDeferred so tests can assert
// that the players were actually told to leave (entrant reject).
type drainTestDispatcher struct {
	mu         sync.Mutex
	broadcasts int
}

func (d *drainTestDispatcher) BroadcastMessage(_ int64, _ []byte, _ []runtime.Presence, _ runtime.Presence, _ bool) error {
	return nil
}

func (d *drainTestDispatcher) BroadcastMessageDeferred(_ int64, _ []byte, _ []runtime.Presence, _ runtime.Presence, _ bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.broadcasts++
	return nil
}

func (d *drainTestDispatcher) MatchKick(_ []runtime.Presence) error { return nil }
func (d *drainTestDispatcher) MatchLabelUpdate(_ string) error      { return nil }

func (d *drainTestDispatcher) broadcastCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.broadcasts
}

func drainTestLogger() runtime.Logger {
	return NewRuntimeGoLogger(NewJSONLogger(os.Stdout, zapcore.ErrorLevel, JSONFormat))
}

func drainTestState(playerCount int) (*MatchLabel, []*EvrMatchPresence) {
	serverSession := uuid.Must(uuid.NewV4())
	operatorID := uuid.Must(uuid.NewV4())
	groupID := uuid.Must(uuid.NewV4())

	state := &MatchLabel{
		ID:        MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"},
		CreatedAt: time.Now().UTC(),
		StartTime: time.Now().UTC().Add(-time.Minute),
		Open:      true,
		LobbyType: PublicLobby,
		Mode:      evr.ModeSocialNPE, // not in ValidLeaderboardModes -> no DB leaderboard write
		Level:     evr.LevelSocial,
		MaxSize:   16,
		GroupID:   &groupID,
		GameServer: &GameServerPresence{
			SessionID:  serverSession,
			OperatorID: operatorID,
			GroupIDs:   []uuid.UUID{groupID},
		},
		server: &Presence{
			ID:     PresenceID{Node: "testnode", SessionID: serverSession},
			UserID: operatorID,
		},
		GameState:             &GameState{},
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

	players := make([]*EvrMatchPresence, 0, playerCount)
	for i := 0; i < playerCount; i++ {
		p := reconnectTestPlayer(string(rune('a'+i)), evr.TeamBlue)
		state.presenceMap[p.GetSessionId()] = p
		state.joinTimestamps[p.GetSessionId()] = time.Now()
		players = append(players, p)
	}
	state.rebuildCache()
	return state, players
}

// TestMatchShutdown_KicksPlayers proves step (1): MatchShutdown actually tells
// the game server to remove the players (dispatches the entrant reject).
func TestMatchShutdown_KicksPlayers(t *testing.T) {
	t.Parallel()

	state, _ := drainTestState(2)
	nk := &drainTestNK{}
	dispatcher := &drainTestDispatcher{}
	logger := drainTestLogger()
	m := &EvrMatch{}
	ctx := context.Background()

	const tick int64 = 100
	const grace = 30

	out := m.MatchShutdown(ctx, logger, nil, nk, dispatcher, tick, state, grace)
	if out == nil {
		t.Fatal("MatchShutdown returned nil; expected the draining match state")
	}

	if dispatcher.broadcastCount() == 0 {
		t.Fatal("expected MatchShutdown to dispatch an entrant reject to kick the players, got none")
	}

	// Match must be closed to new joins and NOT yet terminated.
	if state.Open {
		t.Error("expected match to be closed (Open=false) after shutdown")
	}
}

// TestMatchShutdown_WaitsForPlayersToLeave proves steps (2)+(3): while players
// remain (and before the bounded deadline) the loop must NOT terminate, and it
// must terminate as soon as the presence map is empty — without waiting for the
// full grace deadline.
func TestMatchShutdown_WaitsForPlayersToLeave(t *testing.T) {
	t.Parallel()

	state, players := drainTestState(2)
	nk := &drainTestNK{}
	dispatcher := &drainTestDispatcher{}
	logger := drainTestLogger()
	m := &EvrMatch{}
	ctx := context.Background()

	const shutdownTick int64 = 100
	const grace = 30 // 30s * 10 ticks = 300 ticks fallback deadline

	m.MatchShutdown(ctx, logger, nil, nk, dispatcher, shutdownTick, state, grace)

	// Players are still on the server. The loop must NOT terminate yet, even
	// several ticks into the drain (well before the 300-tick deadline).
	for _, tick := range []int64{shutdownTick + 1, shutdownTick + 10, shutdownTick + 100} {
		out := m.MatchLoop(ctx, logger, nil, nk, dispatcher, tick, state, nil)
		if out == nil {
			t.Fatalf("tick %d: match terminated while players remained (orphans the game server)", tick)
		}
	}

	// Now the players leave (game server removed them; MatchLeave emptied the map).
	for _, p := range players {
		delete(state.presenceMap, p.GetSessionId())
	}
	state.rebuildCache()

	// The very next tick — still far before the deadline — must terminate.
	out := m.MatchLoop(ctx, logger, nil, nk, dispatcher, shutdownTick+101, state, nil)
	if out != nil {
		t.Fatal("expected match to terminate immediately once all players had left, but it did not")
	}
}

// TestMatchShutdown_TimeoutFallback proves the bounded fallback: a
// non-cooperating game server that never removes its players must still be torn
// down once the deadline elapses, so a match cannot hang forever.
func TestMatchShutdown_TimeoutFallback(t *testing.T) {
	t.Parallel()

	state, _ := drainTestState(2)
	nk := &drainTestNK{}
	dispatcher := &drainTestDispatcher{}
	logger := drainTestLogger()
	m := &EvrMatch{}
	ctx := context.Background()

	const shutdownTick int64 = 100
	const grace = 5 // 5s * 10 ticks = 50 ticks fallback deadline

	m.MatchShutdown(ctx, logger, nil, nk, dispatcher, shutdownTick, state, grace)

	// Players never leave. Before the deadline, no termination.
	if out := m.MatchLoop(ctx, logger, nil, nk, dispatcher, shutdownTick+10, state, nil); out == nil {
		t.Fatal("terminated before the fallback deadline with players still present")
	}

	// At/after the deadline, the match must terminate anyway despite the
	// players still being present.
	deadlineTick := shutdownTick + int64(grace)*state.tickRate
	if out := m.MatchLoop(ctx, logger, nil, nk, dispatcher, deadlineTick, state, nil); out != nil {
		t.Fatal("expected timeout fallback to terminate the match at the deadline, but it did not")
	}
}

package server

// Regression test for #763: spectators see "Server was shut down" at match end.
//
// The game-server half of #763 is not visible from this repo (see the issue).
// What this repo does decide is what happens to a spectator who is still in
// the presence map when the drain deadline passes: MatchTerminate disconnects
// its session along with everyone else's. The forced-termination log gave only
// a count, so a production log could not tell a stuck spectator from a stuck
// player. This test pins both halves.

import (
	"context"
	"slices"
	"testing"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestMatchShutdown_SpectatorPresentAtDrainDeadline(t *testing.T) {
	t.Parallel()

	state, players := drainTestState(1)
	spectator := reconnectTestPlayer("spec", evr.TeamSpectator)
	moderator := reconnectTestPlayer("moder", evr.TeamModerator)
	for _, p := range []*EvrMatchPresence{spectator, moderator} {
		state.presenceMap[p.GetSessionId()] = p
	}
	state.rebuildCache()

	core, logs := observer.New(zapcore.DebugLevel)
	logger := NewRuntimeGoLogger(zap.New(core))

	var tasks []matchTerminationTask
	m := &EvrMatch{enqueueTermination: func(_ runtime.Logger, task matchTerminationTask) {
		tasks = append(tasks, task)
	}}
	nk := &drainTestNK{}
	dispatcher := &drainTestDispatcher{}
	ctx := context.Background()

	// The grace MatchLeave passes when the game server reports the session ended.
	const shutdownTick int64 = 100
	const grace = 2
	m.MatchShutdown(ctx, logger, nil, nk, dispatcher, shutdownTick, state, grace)

	// Nobody leaves. One tick before the deadline the match is still draining.
	deadlineTick := shutdownTick + int64(grace)*state.tickRate
	if out := m.MatchLoop(ctx, logger, nil, nk, dispatcher, deadlineTick-1, state, nil); out == nil {
		t.Fatal("terminated before the drain deadline")
	}
	if out := m.MatchLoop(ctx, logger, nil, nk, dispatcher, deadlineTick, state, nil); out != nil {
		t.Fatal("expected forced termination at the drain deadline")
	}

	// The spectator's session is handed to the termination task with the rest:
	// a spectator still present at the deadline is disconnected, not moved.
	if len(tasks) != 1 {
		t.Fatalf("expected exactly one termination task, got %d", len(tasks))
	}
	for _, p := range []*EvrMatchPresence{players[0], spectator, moderator} {
		if !slices.Contains(tasks[0].playerSessionIDs, p.GetSessionId()) {
			t.Fatalf("role %d session %s missing from the sessions disconnected on terminate: %v", p.RoleAlignment, p.GetSessionId(), tasks[0].playerSessionIDs)
		}
	}

	// The termination log must say who was left, by role.
	want := map[string]int64{"remaining_sessions": 3, "players": 1, "spectators": 1, "moderators": 1}
	var found []observer.LoggedEntry
	for _, e := range logs.All() {
		if _, ok := e.ContextMap()["spectators"]; ok {
			found = append(found, e)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly one log record with a spectators field, got %d; all records: %v", len(found), logs.All())
	}
	if found[0].Level != zapcore.InfoLevel {
		t.Fatalf("expected the role breakdown at info, got %s", found[0].Level)
	}
	fields := found[0].ContextMap()
	for k, v := range want {
		got, ok := fields[k].(int64)
		if !ok || got != v {
			t.Fatalf("field %q = %v (%T), want %d; record: %v", k, fields[k], fields[k], v, fields)
		}
	}
}

// TestMatchTerminate_PlannedShutdownWithSpectatorLogsInfo drives MatchTerminate
// the way the runtime does for a planned node shutdown (match_handler.go
// QueueTerminate): terminateTick is 0, because MatchShutdown never armed a
// drain, and a spectator is still present. That is expected, so the role
// breakdown is Info, never Warn. The forced-drain path already warns in
// MatchLoop; this record must not repeat that warning.
func TestMatchTerminate_PlannedShutdownWithSpectatorLogsInfo(t *testing.T) {
	t.Parallel()

	state, _ := drainTestState(1)
	spectator := reconnectTestPlayer("spec", evr.TeamSpectator)
	state.presenceMap[spectator.GetSessionId()] = spectator
	state.rebuildCache()
	if state.terminateTick != 0 {
		t.Fatalf("precondition: terminateTick = %d, want 0 (no drain armed)", state.terminateTick)
	}

	core, logs := observer.New(zapcore.DebugLevel)
	logger := NewRuntimeGoLogger(zap.New(core))
	m := &EvrMatch{enqueueTermination: func(runtime.Logger, matchTerminationTask) {}}

	// The returned state is not the subject here.
	m.MatchTerminate(context.Background(), logger, nil, &drainTestNK{}, &drainTestDispatcher{}, 10, state, 10)

	var found []observer.LoggedEntry
	for _, e := range logs.All() {
		if _, ok := e.ContextMap()["spectators"]; ok {
			found = append(found, e)
		}
		if e.Level >= zapcore.WarnLevel {
			t.Fatalf("planned shutdown logged at %s: %q", e.Level, e.Message)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly one role-breakdown record, got %d; all records: %v", len(found), logs.All())
	}
	if found[0].Level != zapcore.InfoLevel {
		t.Fatalf("role breakdown level = %s, want info", found[0].Level)
	}
	if got, _ := found[0].ContextMap()["spectators"].(int64); got != 1 {
		t.Fatalf("spectators = %v, want 1; record: %v", found[0].ContextMap()["spectators"], found[0].ContextMap())
	}
}

// TestMatchLeave_GameServerEndsSessionWithSpectatorSendsNoReject pins the
// premise the #763 PR rests on, and is not a red-first test: it describes code
// that predates the PR. On a natural end the game server reports CODE_ENDED,
// SignalEndedSession removes its presence, and MatchLeave clears state.server
// before it calls MatchShutdown. So MatchShutdown skips the reason-6
// (PlayerRejectionReasonLobbyEnding) reject and the CODE_ENDED echo: this repo
// dispatches nothing, and a spectator still in the match waits out the drain.
// Whatever the spectator's client shows at that moment comes from the game
// server, not from a reject sent here. If this ever starts broadcasting, the
// #763 analysis has to be redone.
func TestMatchLeave_GameServerEndsSessionWithSpectatorSendsNoReject(t *testing.T) {
	t.Parallel()

	state, _ := drainTestState(0)
	spectator := reconnectTestPlayer("spec", evr.TeamSpectator)
	state.presenceMap[spectator.GetSessionId()] = spectator
	state.rebuildCache()

	gameServer := reconnectTestPresence{
		EvrMatchPresence: &EvrMatchPresence{
			Node:      "testnode",
			SessionID: state.GameServer.SessionID,
			UserID:    state.GameServer.OperatorID,
			Username:  "broadcaster:gameserver",
		},
		reason: runtime.PresenceReasonLeave,
	}

	nk := &drainTestNK{}
	dispatcher := &drainTestDispatcher{}
	// MatchLeave reads the node from the context; build it the way the runtime does.
	ctx := NewRuntimeGoContext(context.Background(), "testnode", "", nil, RuntimeExecutionModeMatch, nil, nil, 0, "", "", nil, "", "", "", "")
	m := &EvrMatch{}

	const leaveTick int64 = 50
	got := m.MatchLeave(ctx, drainTestLogger(), nil, nk, dispatcher, leaveTick, state, []runtime.Presence{gameServer})
	after, ok := got.(*MatchLabel)
	if !ok || after == nil {
		t.Fatalf("MatchLeave returned %T, want a draining *MatchLabel", got)
	}
	if after.terminateTick != leaveTick+2*after.tickRate {
		t.Fatalf("terminateTick = %d, want the 2s drain armed at tick %d", after.terminateTick, leaveTick+2*after.tickRate)
	}
	if n := dispatcher.broadcastCount(); n != 0 {
		t.Fatalf("game-server-ended path dispatched %d message(s); expected none (no reason-6 reject reaches anyone from this repo on this path)", n)
	}
	if _, ok := after.presenceMap[spectator.GetSessionId()]; !ok {
		t.Fatal("spectator left the presence map; expected it to stay until it leaves or the drain deadline passes")
	}
}

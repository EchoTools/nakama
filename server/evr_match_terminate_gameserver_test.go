package server

// Regression tests for issue #588: ending a match disconnected the game
// server's registration websocket.
//
// A game server registers once and hosts many matches on that one session. The
// monitor goroutine gameserverRegistrationRequest starts re-parks the server
// after each match, and its only exit is the session closing. So the session
// belongs to the fleet, not to any match.
//
// processMatchTerminationTask used to SessionDisconnect it on every
// MatchTerminate that still had state.server set, which is every termination
// except the one where the game server left first. An operator close with
// DisconnectGameServer=false therefore removed the server from the fleet until
// a human restarted it. Observed in production on 2026-09-02 and 2026-09-09,
// both times with the confirm payload ending ":0:0"; the logs are on #588.
//
// Dropping the server is decided in exactly one place: SignalShutdown, when the
// operator sets DisconnectGameServer. Termination ends a match, never a server.

import (
	"context"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

// terminateHarness runs a match to termination and processes the task
// MatchTerminate enqueues synchronously, so every SessionDisconnect the task
// makes has been recorded by the time the test asserts.
type terminateHarness struct {
	t          *testing.T
	ctx        context.Context
	logger     runtime.Logger
	m          *EvrMatch
	nk         *drainTestNK
	dispatcher *drainTestDispatcher
	tasks      []matchTerminationTask
}

func newTerminateHarness(t *testing.T) *terminateHarness {
	h := &terminateHarness{
		t: t,
		// Built the way RuntimeGoMatchCore builds every match callback's
		// context; MatchLeave refuses to run without the node set.
		ctx:        NewRuntimeGoContext(context.Background(), "testnode", "", nil, RuntimeExecutionModeMatch, nil, nil, 0, "", "", nil, "", "", "", ""),
		logger:     drainTestLogger(),
		nk:         &drainTestNK{},
		dispatcher: &drainTestDispatcher{},
	}
	h.m = &EvrMatch{enqueueTermination: func(logger runtime.Logger, task matchTerminationTask) {
		// enqueueMatchTerminationTask does the same; a task without a logger
		// returns before its first side effect.
		task.logger = logger
		h.tasks = append(h.tasks, task)
	}}
	return h
}

// signalShutdown delivers SignalShutdown the way every operator path does:
// through MatchSignal, so SignalShutdown's own DisconnectGameServer handling
// runs.
func (h *terminateHarness) signalShutdown(state *MatchLabel, tick int64, payload SignalShutdownPayload) {
	h.t.Helper()
	envelope := NewSignalEnvelope(uuid.Must(uuid.NewV4()).String(), SignalShutdown, payload).String()
	got, response := h.m.MatchSignal(h.ctx, h.logger, nil, h.nk, h.dispatcher, tick, state, envelope)
	if r := SignalResponseFromString(response); !r.Success {
		h.t.Fatalf("SignalShutdown was rejected: %q", r.Message)
	}
	if got == nil {
		h.t.Fatal("MatchSignal returned nil state for SignalShutdown; the drain never runs")
	}
}

// loopUntilTerminated ticks MatchLoop from tick until the match terminates, and
// fails if it has not within limit ticks.
func (h *terminateHarness) loopUntilTerminated(state *MatchLabel, tick, limit int64) {
	h.t.Helper()
	for end := tick + limit; tick <= end; tick++ {
		if h.m.MatchLoop(h.ctx, h.logger, nil, h.nk, h.dispatcher, tick, state, nil) == nil {
			return
		}
	}
	h.t.Fatalf("match did not terminate within %d ticks", limit)
}

// terminate runs the one task MatchTerminate enqueued, as the worker would.
func (h *terminateHarness) terminate() {
	h.t.Helper()
	if len(h.tasks) != 1 {
		h.t.Fatalf("MatchTerminate enqueued %d termination tasks, want exactly 1", len(h.tasks))
	}
	task := h.tasks[0]
	// processMatchTerminationTask returns early, silently, without any of
	// these. A test asserting that nothing was disconnected would then pass
	// without the disconnect code having run at all.
	if task.logger == nil || task.nk == nil || task.stateSnapshot == nil {
		h.t.Fatal("termination task would return before its disconnects; the assertions below would be vacuous")
	}
	processMatchTerminationTask(task)
}

// disconnects returns how many times sid was disconnected, and how many
// disconnects went to any other session.
func (h *terminateHarness) disconnects(sid string) (ofSID, ofOthers int) {
	h.nk.mu.Lock()
	defer h.nk.mu.Unlock()
	for _, got := range h.nk.disconnectedSIDs {
		if got == sid {
			ofSID++
		} else {
			ofOthers++
		}
	}
	return ofSID, ofOthers
}

// TestMatchTerminate_DoesNotDisconnectGameServer covers every path that reaches
// MatchTerminate with the game server's session known to the match. None of
// them asked for the server to be dropped, so none may close its session. The
// players still on the match at termination are disconnected as before.
func TestMatchTerminate_DoesNotDisconnectGameServer(t *testing.T) {
	t.Parallel()

	const tick int64 = 100

	tests := []struct {
		name    string
		players int
		// drive runs the match from its initial state until MatchTerminate.
		drive func(h *terminateHarness, state *MatchLabel, players []*EvrMatchPresence)
		// wantPlayerDisconnects is how many player sessions the task must close.
		// Non-zero proves the task reached its disconnect section.
		wantPlayerDisconnects int
	}{
		{
			// The production case, verbatim: a populated lobby, the operator
			// declines to drop the server, grace 0. The drain deadline is the
			// current tick, so the next MatchLoop forces termination with the
			// players and the game server still attached.
			name:    "operator close, DisconnectGameServer=false, grace 0, players present",
			players: 4,
			drive: func(h *terminateHarness, state *MatchLabel, _ []*EvrMatchPresence) {
				h.signalShutdown(state, tick, SignalShutdownPayload{DisconnectGameServer: false, GraceSeconds: 0})
				h.loopUntilTerminated(state, tick+1, 1)
			},
			wantPlayerDisconnects: 4,
		},
		{
			// Preemption (30s), reservation vacate (60s/20s), the shutdown RPC
			// (10s) and an operator close with a grace all send this payload.
			// The game server removes the players and the drain completes early.
			name:    "shutdown signal with a grace, players drained before the deadline",
			players: 2,
			drive: func(h *terminateHarness, state *MatchLabel, players []*EvrMatchPresence) {
				h.signalShutdown(state, tick, SignalShutdownPayload{DisconnectGameServer: false, GraceSeconds: 30})
				for _, p := range players {
					delete(state.presenceMap, p.GetSessionId())
				}
				state.rebuildCache()
				h.loopUntilTerminated(state, tick+1, 1)
			},
			wantPlayerDisconnects: 0,
		},
		{
			// The Discord command skips the confirmation step when the lobby is
			// empty and signals directly.
			name:    "operator close of an empty lobby",
			players: 0,
			drive: func(h *terminateHarness, state *MatchLabel, _ []*EvrMatchPresence) {
				h.signalShutdown(state, tick, SignalShutdownPayload{DisconnectGameServer: false, GraceSeconds: 0})
				h.loopUntilTerminated(state, tick+1, 1)
			},
			wantPlayerDisconnects: 0,
		},
		{
			// One of the match's own idle reclaims: MatchLoop calls MatchShutdown
			// itself, with no signal and no operator.
			name:    "started match empty too long",
			players: 0,
			drive: func(h *terminateHarness, state *MatchLabel, _ []*EvrMatchPresence) {
				state.emptyTicks = 60 * state.tickRate
				h.loopUntilTerminated(state, tick, 2)
			},
			wantPlayerDisconnects: 0,
		},
		{
			// Nakama's own graceful node shutdown: LocalMatchRegistry.Stop ->
			// QueueTerminate -> MatchTerminate, with no MatchShutdown and no
			// drain. The process exit that follows closes every socket; the
			// match has no business closing the game server's first.
			name:    "node shutdown, MatchTerminate called directly",
			players: 2,
			drive: func(h *terminateHarness, state *MatchLabel, _ []*EvrMatchPresence) {
				if got := h.m.MatchTerminate(h.ctx, h.logger, nil, h.nk, h.dispatcher, tick, state, 30); got != nil {
					h.t.Fatalf("MatchTerminate returned %T, want nil", got)
				}
			},
			wantPlayerDisconnects: 2,
		},
		{
			// The natural end: the game server leaves first, MatchLeave clears
			// state.server, and the termination has no server session to close.
			// This path was already correct; it is here so it stays that way.
			name:    "natural end, game server left first",
			players: 0,
			drive: func(h *terminateHarness, state *MatchLabel, _ []*EvrMatchPresence) {
				gameServer := reconnectTestPresence{
					EvrMatchPresence: &EvrMatchPresence{
						Node:      "testnode",
						SessionID: state.GameServer.SessionID,
						UserID:    state.GameServer.OperatorID,
						Username:  "broadcaster:gameserver",
					},
					reason: runtime.PresenceReasonLeave,
				}
				if got := h.m.MatchLeave(h.ctx, h.logger, nil, h.nk, h.dispatcher, tick, state, []runtime.Presence{gameServer}); got == nil {
					h.t.Fatal("MatchLeave returned nil when the game server left")
				}
				if state.server != nil {
					h.t.Fatal("precondition: MatchLeave did not clear state.server for the departed game server")
				}
				h.loopUntilTerminated(state, tick+1, 1)
			},
			wantPlayerDisconnects: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newTerminateHarness(t)
			state, players := drainTestState(tt.players)
			serverSID := state.GameServer.SessionID.String()

			tt.drive(h, state, players)
			h.terminate()

			server, others := h.disconnects(serverSID)
			if server != 0 {
				t.Errorf("game server session %s was disconnected %d time(s); its registration hosts every future match on this server, and the monitor that re-parks it exits with the session (#588)", serverSID, server)
			}
			if others != tt.wantPlayerDisconnects {
				t.Errorf("termination disconnected %d player session(s), want %d", others, tt.wantPlayerDisconnects)
			}
		})
	}
}

// TestSignalShutdown_DisconnectGameServerDropsItExactlyOnce: an operator who
// sets DisconnectGameServer gets the server dropped, once. SignalShutdown
// closes the session immediately; the termination that follows must not close
// it again. With grace 0 the termination runs on the next tick, before the game
// server's MatchLeave for that disconnect can clear state.server, which is the
// window in which it used to fire a second time.
func TestSignalShutdown_DisconnectGameServerDropsItExactlyOnce(t *testing.T) {
	t.Parallel()

	const tick int64 = 100

	h := newTerminateHarness(t)
	state, _ := drainTestState(4)
	serverSID := state.GameServer.SessionID.String()

	h.signalShutdown(state, tick, SignalShutdownPayload{DisconnectGameServer: true, GraceSeconds: 0})
	if got, _ := h.disconnects(serverSID); got != 1 {
		t.Fatalf("SignalShutdown with DisconnectGameServer=true disconnected the game server %d time(s), want 1", got)
	}

	h.loopUntilTerminated(state, tick+1, 1)
	h.terminate()

	server, players := h.disconnects(serverSID)
	if server != 1 {
		t.Errorf("game server session was disconnected %d time(s) across shutdown and termination, want exactly 1", server)
	}
	if players != 4 {
		t.Errorf("termination disconnected %d player session(s), want 4", players)
	}
}

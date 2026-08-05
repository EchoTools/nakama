package server

// Regression tests for the MatchLoop idle/empty timers.
//
// MatchLoop runs THREE independent shutdown deadlines:
//
//	(a) "no game server"        -> 10s   (state.server == nil)
//	(b) "unallocated parking"   -> 120s  (LobbyType == UnassignedLobby && server != nil)
//	(c) "started but empty"     -> 60s   (Started() && len(presenceMap) == 0)
//
// They used to share a single counter (state.emptyTicks) that was incremented at
// three sites and reset at two, with a reset running BEFORE one of the
// increments. The result was that (b) could never fire — the counter oscillated
// 0 -> 1 forever — and (a) either never fired (not-started matches) or fired at
// half its nominal deadline (started matches, double increment per tick).
//
// Each timer must own its counter. These tests pin the exact deadline of each
// one across started/not-started x server/no-server, and pin the cases where NO
// timer may fire.

import (
	"context"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// emptyTickState builds a MatchLoop-safe label with no players.
func emptyTickState(lobbyType LobbyType, hasServer bool, started bool, players int) *MatchLabel {
	serverSession := uuid.Must(uuid.NewV4())
	operatorID := uuid.Must(uuid.NewV4())
	groupID := uuid.Must(uuid.NewV4())

	state := &MatchLabel{
		ID:        MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"},
		CreatedAt: time.Now().UTC(),
		Open:      true,
		LobbyType: lobbyType,
		Mode:      evr.ModeSocialNPE, // not in ValidLeaderboardModes -> no DB write
		Level:     evr.LevelSocial,
		MaxSize:   16,
		GroupID:   &groupID,
		GameServer: &GameServerPresence{
			SessionID:  serverSession,
			OperatorID: operatorID,
			GroupIDs:   []uuid.UUID{groupID},
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

	if hasServer {
		state.server = &Presence{
			ID:     PresenceID{Node: "testnode", SessionID: serverSession},
			UserID: operatorID,
		}
	}

	if started {
		state.StartTime = time.Now().UTC().Add(-time.Minute)
		// A started match has already had its level loaded; without this the
		// loop would divert into MatchStart instead of reaching the timers.
		state.levelLoaded = true
	}

	for i := 0; i < players; i++ {
		p := reconnectTestPlayer(string(rune('a'+i)), evr.TeamBlue)
		state.presenceMap[p.GetSessionId()] = p
		state.joinTimestamps[p.GetSessionId()] = time.Now()
	}

	state.rebuildCache()
	return state
}

// runUntilShutdown drives MatchLoop up to maxIterations times and returns the
// 1-based iteration on which the match began shutting down (terminateTick set,
// or the loop returned nil). 0 means it never shut down.
func runUntilShutdown(t *testing.T, state *MatchLabel, maxIterations int) int {
	t.Helper()

	m := &EvrMatch{}
	nk := &drainTestNK{}
	dispatcher := &drainTestDispatcher{}
	logger := drainTestLogger()
	ctx := context.Background()

	for i := 1; i <= maxIterations; i++ {
		out := m.MatchLoop(ctx, logger, nil, nk, dispatcher, int64(i), state, nil)
		if out == nil {
			return i
		}
		if state.terminateTick != 0 {
			return i
		}
	}
	return 0
}

func TestMatchLoop_EmptyTickTimersAreIndependent(t *testing.T) {
	t.Parallel()

	const tickRate = 10

	cases := []struct {
		name          string
		lobbyType     LobbyType
		hasServer     bool
		started       bool
		players       int
		maxIterations int
		// wantShutdownAt is the 1-based MatchLoop iteration on which the
		// shutdown must begin. 0 means it must never begin.
		wantShutdownAt int
		why            string
	}{
		{
			name:           "no_server/not_started/empty fires the 10s no-server timer",
			lobbyType:      PublicLobby,
			hasServer:      false,
			started:        false,
			maxIterations:  400,
			wantShutdownAt: 10*tickRate + 1,
			why:            "the no-server deadline must not be reset by the started-and-empty branch",
		},
		{
			name:           "no_server/started/empty fires the 10s no-server timer once, not twice per tick",
			lobbyType:      PublicLobby,
			hasServer:      false,
			started:        true,
			maxIterations:  400,
			wantShutdownAt: 10*tickRate + 1,
			why:            "two increments per tick would halve the deadline to ~5s",
		},
		{
			name:           "no_server/unassigned parking fires the 10s no-server timer",
			lobbyType:      UnassignedLobby,
			hasServer:      false,
			started:        false,
			maxIterations:  400,
			wantShutdownAt: 10*tickRate + 1,
			why:            "a parking match whose game server never arrives must not leak",
		},
		{
			name:           "server/unassigned parking fires the 120s unallocated timer",
			lobbyType:      UnassignedLobby,
			hasServer:      true,
			started:        false,
			maxIterations:  1400,
			wantShutdownAt: 120*tickRate + 1,
			why:            "a parking match with a server that is never allocated must time out",
		},
		{
			name:           "server/started/empty fires the 60s empty timer",
			lobbyType:      PublicLobby,
			hasServer:      true,
			started:        true,
			maxIterations:  800,
			wantShutdownAt: 60*tickRate + 1,
			why:            "an abandoned started match must be reclaimed after 60s",
		},
		{
			name:           "server/started/occupied never shuts down",
			lobbyType:      PublicLobby,
			hasServer:      true,
			started:        true,
			players:        2,
			maxIterations:  1400,
			wantShutdownAt: 0,
			why:            "a live match with players must never be reclaimed by an idle timer",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			state := emptyTickState(tc.lobbyType, tc.hasServer, tc.started, tc.players)
			got := runUntilShutdown(t, state, tc.maxIterations)

			if got != tc.wantShutdownAt {
				if tc.wantShutdownAt == 0 {
					t.Fatalf("match shut down on iteration %d but must never shut down (%s)", got, tc.why)
				}
				if got == 0 {
					t.Fatalf("match never shut down within %d iterations; expected shutdown on iteration %d (%s)",
						tc.maxIterations, tc.wantShutdownAt, tc.why)
				}
				t.Fatalf("match shut down on iteration %d; expected iteration %d (%s)", got, tc.wantShutdownAt, tc.why)
			}
		})
	}
}

// TestMatchLoop_OccupancyDoesNotResetTheNoServerTimer pins the interaction that
// the shared counter got wrong: a match that lost its game server but still has
// players must still hit the 10s no-server deadline. Under the shared counter
// the "reset when not (started and empty)" branch wiped the no-server count on
// every tick.
func TestMatchLoop_OccupancyDoesNotResetTheNoServerTimer(t *testing.T) {
	t.Parallel()

	state := emptyTickState(PublicLobby, false, false, 2)
	got := runUntilShutdown(t, state, 400)

	const want = 10*10 + 1
	if got != want {
		t.Fatalf("no-server shutdown fired on iteration %d, want %d (players present must not reset the no-server timer)", got, want)
	}
}

// TestMatchLoop_ParkingMatchCounterDoesNotOscillate proves the concrete failure
// mode directly: for an unassigned parking match that HAS a game server, the
// unallocated counter must advance monotonically, one per tick.
func TestMatchLoop_ParkingMatchCounterDoesNotOscillate(t *testing.T) {
	t.Parallel()

	state := emptyTickState(UnassignedLobby, true, false, 0)

	m := &EvrMatch{}
	nk := &drainTestNK{}
	dispatcher := &drainTestDispatcher{}
	logger := drainTestLogger()
	ctx := context.Background()

	for i := 1; i <= 25; i++ {
		if out := m.MatchLoop(ctx, logger, nil, nk, dispatcher, int64(i), state, nil); out == nil {
			t.Fatalf("tick %d: parking match terminated far too early", i)
		}
		if got := state.unallocatedTicks; got != int64(i) {
			t.Fatalf("tick %d: unallocatedTicks = %d, want %d (counter is not advancing monotonically)", i, got, i)
		}
	}
}

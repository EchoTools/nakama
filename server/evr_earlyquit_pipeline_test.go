package server

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// TestEarlyQuitDetectionPipeline validates that early quits are properly
// detected and the total_early_quits counter is incremented.
//
// The previous version of this file never called production code: it
// re-implemented a hand-rolled shouldDetectEarlyQuit predicate (including a
// 60-second grace period that production does not implement — see
// evr_match.go:918, which has no duration threshold) and asserted on that
// re-implementation. Every gate case here instead drives the REAL MatchLeave
// charge gate:
//
//	!hasReconnectReservation && Mode == ArenaPublic && !GameState.IsMatchOver() && IsPlayer()
//
// and asserts on the stored EarlyQuit|statistics counter.
//
// Runs against the in-memory evrTestNakamaModule: no database, no services.
func TestEarlyQuitDetectionPipeline(t *testing.T) {
	nk, db := newChargeModule(t)
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")

	t.Run("Player leaving mid-game increments early quit counter", func(t *testing.T) {
		player := reconnectTestPlayer("b6-pipe-midgame", evr.TeamBlue)
		preseedEarlyQuitConfig(t, ctx, nk, player.GetUserId())

		state := chargeState(evr.ModeArenaPublic, player)
		driveVoluntaryLeave(ctx, t, db, nk, state, player)

		if err := assertStoredEarlyQuitCount(t, ctx, nk, player.GetUserId(), 1); err != nil {
			t.Error(err)
		}
	})

	t.Run("Player leaving before 60 second mark increments early quit counter", func(t *testing.T) {
		// Production has NO 60-second grace period: the charge gate at
		// evr_match.go:918 never consults the match start time. A leave 30
		// seconds in charges exactly like one 2 minutes in. This test replaces
		// the old "60 second grace" case, which asserted a rule production
		// does not implement.
		player := reconnectTestPlayer("b6-pipe-early", evr.TeamBlue)
		preseedEarlyQuitConfig(t, ctx, nk, player.GetUserId())

		state := chargeState(evr.ModeArenaPublic, player)
		state.joinTimestamps[player.GetSessionId()] = time.Now().Add(-30 * time.Second) // 30 seconds into the match
		driveVoluntaryLeave(ctx, t, db, nk, state, player)

		if err := assertStoredEarlyQuitCount(t, ctx, nk, player.GetUserId(), 1); err != nil {
			t.Error(err)
		}
	})

	t.Run("Player leaving after match ends does not increment early quit", func(t *testing.T) {
		player := reconnectTestPlayer("b6-pipe-after", evr.TeamBlue)
		preseedEarlyQuitConfig(t, ctx, nk, player.GetUserId())

		state := chargeState(evr.ModeArenaPublic, player)
		state.GameState = &GameState{MatchOver: true}
		driveVoluntaryLeave(ctx, t, db, nk, state, player)

		if err := assertStoredEarlyQuitCount(t, ctx, nk, player.GetUserId(), 0); err != nil {
			t.Error(err)
		}
	})

	t.Run("Spectator leaving does not increment early quit", func(t *testing.T) {
		player := reconnectTestPlayer("b6-pipe-spec", evr.TeamSpectator)
		preseedEarlyQuitConfig(t, ctx, nk, player.GetUserId())

		state := chargeState(evr.ModeArenaPublic, player)
		driveVoluntaryLeave(ctx, t, db, nk, state, player)

		if err := assertStoredEarlyQuitCount(t, ctx, nk, player.GetUserId(), 0); err != nil {
			t.Error(err)
		}
	})

	t.Run("Moderator leaving does not increment early quit", func(t *testing.T) {
		player := reconnectTestPlayer("b6-pipe-mod", evr.TeamModerator)
		preseedEarlyQuitConfig(t, ctx, nk, player.GetUserId())

		state := chargeState(evr.ModeArenaPublic, player)
		driveVoluntaryLeave(ctx, t, db, nk, state, player)

		if err := assertStoredEarlyQuitCount(t, ctx, nk, player.GetUserId(), 0); err != nil {
			t.Error(err)
		}
	})

	t.Run("Social lobby leaving does not increment early quit", func(t *testing.T) {
		player := reconnectTestPlayer("b6-pipe-social", evr.TeamBlue)
		preseedEarlyQuitConfig(t, ctx, nk, player.GetUserId())

		state := chargeState(evr.ModeSocialPublic, player)
		driveVoluntaryLeave(ctx, t, db, nk, state, player)

		if err := assertStoredEarlyQuitCount(t, ctx, nk, player.GetUserId(), 0); err != nil {
			t.Error(err)
		}
	})

	t.Run("Multiple early quits accumulate correctly", func(t *testing.T) {
		config := NewEarlyQuitPlayerState()

		// First early quit
		config.IncrementEarlyQuit()
		if config.NumEarlyQuits != 1 {
			t.Errorf("Expected 1 early quit, got %d", config.NumEarlyQuits)
		}

		// Second early quit
		config.IncrementEarlyQuit()
		if config.NumEarlyQuits != 2 {
			t.Errorf("Expected 2 early quits, got %d", config.NumEarlyQuits)
		}

		// Third early quit
		config.IncrementEarlyQuit()
		if config.NumEarlyQuits != 3 {
			t.Errorf("Expected 3 early quits, got %d", config.NumEarlyQuits)
		}

		// NumSteadyEarlyQuits should also track
		if config.NumSteadyEarlyQuits != 3 {
			t.Errorf("Expected NumSteadyEarlyQuits 3, got %d", config.NumSteadyEarlyQuits)
		}
	})
}

// TestEarlyQuitConditions exercises the conditions that must hold for the
// early-quit charge gate to fire, driving the REAL MatchLeave for each row and
// asserting on the stored counter.
//
// The previous version re-implemented the gate's conditions in the test (with
// a 60-second grace clause production does not have) and asserted on that
// re-implementation — it could never fail. There is deliberately no
// match-started-ago field: production has no duration threshold.
func TestEarlyQuitConditions(t *testing.T) {
	nk, db := newChargeModule(t)
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")

	tests := []struct {
		name          string
		seed          string
		mode          evr.Symbol
		gameState     *GameState // nil means "no game state"
		roleAlignment int
		shouldTrigger bool
		description   string
	}{
		{
			name:          "Normal early quit - arena public, mid-game player leave",
			seed:          "b6-cond-arena",
			mode:          evr.ModeArenaPublic,
			gameState:     &GameState{MatchOver: false},
			roleAlignment: evr.TeamBlue,
			shouldTrigger: true,
			description:   "Player leaving arena match should charge an early quit",
		},
		{
			name:          "Player leaving before 60 second mark",
			seed:          "b6-cond-early",
			mode:          evr.ModeArenaPublic,
			gameState:     &GameState{MatchOver: false},
			roleAlignment: evr.TeamBlue,
			shouldTrigger: true,
			description:   "Production has no grace period: a leave 30s into the match charges like any other",
		},
		{
			name:          "After match ends",
			seed:          "b6-cond-over",
			mode:          evr.ModeArenaPublic,
			gameState:     &GameState{MatchOver: true},
			roleAlignment: evr.TeamBlue,
			shouldTrigger: false,
			description:   "Player leaving after match ends should not charge an early quit",
		},
		{
			name:          "Private arena match",
			seed:          "b6-cond-private",
			mode:          evr.ModeArenaPrivate,
			gameState:     &GameState{MatchOver: false},
			roleAlignment: evr.TeamBlue,
			shouldTrigger: false,
			description:   "Private matches should not charge early quits",
		},
		{
			name:          "Combat mode",
			seed:          "b6-cond-combat",
			mode:          evr.ModeCombatPublic,
			gameState:     &GameState{MatchOver: false},
			roleAlignment: evr.TeamBlue,
			shouldTrigger: false,
			description:   "Combat matches should not charge early quits (only arena)",
		},
		{
			name:          "Social lobby",
			seed:          "b6-cond-social",
			mode:          evr.ModeSocialPublic,
			gameState:     &GameState{MatchOver: false},
			roleAlignment: evr.TeamBlue,
			shouldTrigger: false,
			description:   "Social lobbies should not charge early quits",
		},
		{
			name:          "Spectator leaving",
			seed:          "b6-cond-spectator",
			mode:          evr.ModeArenaPublic,
			gameState:     &GameState{MatchOver: false},
			roleAlignment: evr.TeamSpectator,
			shouldTrigger: false,
			description:   "Spectators leaving should not charge early quits",
		},
		{
			name:          "Moderator leaving",
			seed:          "b6-cond-moderator",
			mode:          evr.ModeArenaPublic,
			gameState:     &GameState{MatchOver: false},
			roleAlignment: evr.TeamModerator,
			shouldTrigger: false,
			description:   "Moderators leaving should not charge early quits",
		},
		{
			name:          "No game state",
			seed:          "b6-cond-nostate",
			mode:          evr.ModeArenaPublic,
			gameState:     nil,
			roleAlignment: evr.TeamBlue,
			shouldTrigger: true,
			description:   "GameState.IsMatchOver() is nil-safe: no game state is treated as not over",
		},
		{
			name:          "Orange team player leaving",
			seed:          "b6-cond-orange",
			mode:          evr.ModeArenaPublic,
			gameState:     &GameState{MatchOver: false},
			roleAlignment: evr.TeamOrange,
			shouldTrigger: true,
			description:   "Orange team player leaving should charge an early quit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			player := reconnectTestPlayer(tt.seed, tt.roleAlignment)
			preseedEarlyQuitConfig(t, ctx, nk, player.GetUserId())

			state := chargeState(tt.mode, player)
			if tt.gameState == nil {
				state.GameState = nil
			} else {
				state.GameState = tt.gameState
			}
			driveVoluntaryLeave(ctx, t, db, nk, state, player)

			want := int32(0)
			if tt.shouldTrigger {
				want = 1
			}
			if err := assertStoredEarlyQuitCount(t, ctx, nk, player.GetUserId(), want); err != nil {
				t.Errorf("%s: %v", tt.description, err)
			}
		})
	}
}

// newChargeModule returns the in-memory NakamaModule the charge-gate tests drive
// MatchLeave with, plus the nil *sql.DB that MatchLeave is handed alongside it.
//
// No database and no services. The module carries a real (in-memory)
// LocalSessionRegistry so the charge gate's session-refresh branch and its
// deferred-penalty goroutines run against a genuine registry rather than being
// skipped. The registry is empty, so the refresh finds no live session — the
// same shape as a player whose session has already gone.
//
// The *sql.DB is nil on purpose: MatchLeave only touches db on the Discord
// tier-change DM path, which is gated behind
// ServiceSettings().Matchmaking.EnableEarlyQuitPenalty. These tests leave that
// false, so a nil db is never dereferenced. A regression that reaches it would
// panic loudly rather than pass quietly.
func newChargeModule(t *testing.T) (*evrTestNakamaModule, *sql.DB) {
	t.Helper()
	nk := newEvrTestNakamaModule()
	nk.sessions = NewLocalSessionRegistry(&testMetrics{})
	return nk, nil
}

// chargeState builds a match label in the given mode with one player present
// and ready for MatchLeave.
func chargeState(mode evr.Symbol, player *EvrMatchPresence) *MatchLabel {
	state := reconnectTestState(mode)
	state.ID.Node = "test-node" // MatchID only round-trips through storage with a non-empty node
	state.presenceMap[player.GetSessionId()] = player
	state.joinTimestamps[player.GetSessionId()] = time.Now().Add(-2 * time.Minute)
	state.participations[player.GetUserId()] = &PlayerParticipation{
		UserID:      player.GetUserId(),
		Username:    player.Username,
		DisplayName: player.DisplayName,
		Team:        BlueTeam,
		JoinTime:    time.Now().Add(-2 * time.Minute),
	}
	state.rebuildCache()
	return state
}

// preseedEarlyQuitConfig writes a clean early-quit config so repeated runs are
// deterministic (storage rows persist across runs in the test DB).
func preseedEarlyQuitConfig(t *testing.T, ctx context.Context, nk runtime.NakamaModule, userID string) {
	t.Helper()
	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      StorageCollectionEarlyQuit,
		Key:             StorageKeyEarlyQuit,
		UserID:          userID,
		Value:           `{"num_early_quits":0,"num_steady_early_quits":0,"matchmaking_tier":1}`,
		Version:         "",
		PermissionRead:  int(runtime.STORAGE_PERMISSION_NO_READ),
		PermissionWrite: int(runtime.STORAGE_PERMISSION_NO_WRITE),
	}}); err != nil {
		t.Fatalf("preseed early quit state failed: %v", err)
	}
}

// driveVoluntaryLeave runs the real MatchLeave charge gate for the given state
// and player.
func driveVoluntaryLeave(ctx context.Context, t *testing.T, db *sql.DB, nk runtime.NakamaModule, state *MatchLabel, player *EvrMatchPresence) {
	t.Helper()
	dispatcher := &reconnectTestDispatcher{}
	leavePresence := reconnectTestPresence{EvrMatchPresence: player, reason: runtime.PresenceReasonLeave}

	m := &EvrMatch{}
	got := m.MatchLeave(ctx, reconnectTestLogger(), db, nk, dispatcher, 1, state, []runtime.Presence{leavePresence})
	if _, ok := got.(*MatchLabel); !ok {
		t.Fatalf("MatchLeave returned non-*MatchLabel state: %T", got)
	}
}

// assertStoredEarlyQuitCount reads the stored early-quit counter and compares
// it to want.
func assertStoredEarlyQuitCount(t *testing.T, ctx context.Context, nk runtime.NakamaModule, userID string, want int32) error {
	t.Helper()
	eqconfig := NewEarlyQuitPlayerState()
	if err := StorableRead(ctx, nk, userID, eqconfig, false); err != nil {
		return fmt.Errorf("expected early quit config to exist (preseeded): %v", err)
	}
	if eqconfig.NumEarlyQuits != want {
		return fmt.Errorf("expected early quit counter %d, got %d", want, eqconfig.NumEarlyQuits)
	}
	return nil
}

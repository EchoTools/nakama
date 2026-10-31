package server

// Round-end lock for public combat.
//
// The game server sends SignalLockSession at round end and SignalUnlockSession
// about 15 s later, between rounds. Arena has always turned the lock into
// GameStatusRoundClosing, which the backfill queries exclude. Combat ignored
// the lock, so post-matchmaker backfill kept joining players into combat
// matches that were already ending: on 2026-10-31, 13 of 31 public combat
// matches took 69 backfill joins after the lock.
//
// These tests drive the real MatchSignal handler, marshal the resulting state
// as the match label (what the registry indexes), and run the real backfill
// queries against a real LocalMatchRegistry.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func newRoundLockTestState(mode evr.Symbol, groupID uuid.UUID, status GameStatus) *MatchLabel {
	state := newSocialTestMatchLabel()
	state.Mode = mode
	state.LobbyType = PublicLobby
	state.GroupID = &groupID
	state.GameStatus = status
	state.StartTime = time.Now().UTC()
	return state
}

func signalRoundLock(t *testing.T, state *MatchLabel, op SignalOpCode) *MatchLabel {
	t.Helper()
	env := NewSignalEnvelope("", op, nil)
	m := &EvrMatch{}
	newState, resp := m.MatchSignal(context.Background(), reconnectTestLogger(), nil, &reconnectTestNakamaModule{}, nil, 1, state, env.String())
	ns, ok := newState.(*MatchLabel)
	require.True(t, ok, "MatchSignal returned non-*MatchLabel state; response=%q", resp)
	return ns
}

func TestSignalLockSession_CombatPublicClosesRound(t *testing.T) {
	state := newRoundLockTestState(evr.ModeCombatPublic, uuid.Must(uuid.NewV4()), GameStatusPlaying)

	state = signalRoundLock(t, state, SignalLockSession)

	assert.Equal(t, GameStatusRoundClosing, state.GameStatus)
	require.NotNil(t, state.LockedAt, "lock must record when the closing phase began")
	assert.True(t, state.Open, "RoundClosing keeps the match Open; the backfill query filter excludes it")
}

func TestSignalLockSession_UnchangedForOtherModes(t *testing.T) {
	for _, mode := range []evr.Symbol{evr.ModeSocialPublic, evr.ModeCombatPrivate, evr.ModeArenaPrivate} {
		state := newRoundLockTestState(mode, uuid.Must(uuid.NewV4()), GameStatusPlaying)

		state = signalRoundLock(t, state, SignalLockSession)

		assert.Equal(t, GameStatusPlaying, state.GameStatus, "mode %s", mode)
		assert.Nil(t, state.LockedAt, "mode %s", mode)
	}
}

func TestSignalLockSession_ArenaPublicStillClosesRound(t *testing.T) {
	state := newRoundLockTestState(evr.ModeArenaPublic, uuid.Must(uuid.NewV4()), GameStatusPlaying)

	state = signalRoundLock(t, state, SignalLockSession)

	assert.Equal(t, GameStatusRoundClosing, state.GameStatus)
	assert.NotNil(t, state.LockedAt)
}

func TestSignalUnlockSession_ArenaPublicStillIgnored(t *testing.T) {
	state := newRoundLockTestState(evr.ModeArenaPublic, uuid.Must(uuid.NewV4()), GameStatusPlaying)
	state = signalRoundLock(t, state, SignalLockSession)

	state = signalRoundLock(t, state, SignalUnlockSession)

	assert.Equal(t, GameStatusRoundClosing, state.GameStatus)
	assert.NotNil(t, state.LockedAt)
}

func TestSignalUnlockSession_CombatPublicReopensToPreLockStatus(t *testing.T) {
	// GameState is nil here: LockedAt must still be cleared, or the MatchLoop
	// hard-close (LockedAt set, status no longer RoundClosing) would shut the
	// match the unlock just reopened.
	for _, prior := range []GameStatus{GameStatusPlaying, GameStatusSuddenDeath, GameStatusRoundStart} {
		state := newRoundLockTestState(evr.ModeCombatPublic, uuid.Must(uuid.NewV4()), prior)
		state = signalRoundLock(t, state, SignalLockSession)
		require.Equal(t, GameStatusRoundClosing, state.GameStatus)

		state = signalRoundLock(t, state, SignalUnlockSession)

		assert.Equal(t, prior, state.GameStatus, "status must return to what it was before the lock")
		assert.True(t, state.Open)
		assert.Nil(t, state.LockedAt)
	}
}

func TestSignalUnlockSession_CombatPublicDoubleLockKeepsFirstPriorStatus(t *testing.T) {
	state := newRoundLockTestState(evr.ModeCombatPublic, uuid.Must(uuid.NewV4()), GameStatusPlaying)
	state = signalRoundLock(t, state, SignalLockSession)
	state = signalRoundLock(t, state, SignalLockSession)

	state = signalRoundLock(t, state, SignalUnlockSession)

	assert.Equal(t, GameStatusPlaying, state.GameStatus, "a repeated lock must not record round_closing as the prior status")
}

func TestSignalUnlockSession_CombatPublicIgnoredWhenMatchOver(t *testing.T) {
	t.Run("post_match status", func(t *testing.T) {
		state := newRoundLockTestState(evr.ModeCombatPublic, uuid.Must(uuid.NewV4()), GameStatusPlaying)
		state = signalRoundLock(t, state, SignalLockSession)
		// What MatchLoop does on a post-match game state update (evr_match.go).
		state.GameStatus = GameStatusPostMatch
		state.Open = false

		state = signalRoundLock(t, state, SignalUnlockSession)

		assert.False(t, state.Open, "unlock after post-match must stay closed")
		assert.Equal(t, GameStatusPostMatch, state.GameStatus)
		assert.NotNil(t, state.LockedAt)
	})
	t.Run("match_over flag", func(t *testing.T) {
		state := newRoundLockTestState(evr.ModeCombatPublic, uuid.Must(uuid.NewV4()), GameStatusPlaying)
		state.GameState = &GameState{}
		state = signalRoundLock(t, state, SignalLockSession)
		state.GameState.MatchOver = true

		state = signalRoundLock(t, state, SignalUnlockSession)

		assert.Equal(t, GameStatusRoundClosing, state.GameStatus, "must stay out of the backfill pool")
		assert.NotNil(t, state.LockedAt)
	})
}

func TestSignalUnlockSession_SocialUnchanged(t *testing.T) {
	state := newRoundLockTestState(evr.ModeSocialPublic, uuid.Must(uuid.NewV4()), GameStatusUnspecified)
	state.Open = false
	lockedAt := time.Now().UTC()
	state.LockedAt = &lockedAt
	state.GameState = &GameState{}

	state = signalRoundLock(t, state, SignalUnlockSession)

	assert.True(t, state.Open)
	assert.Nil(t, state.LockedAt)
	assert.Equal(t, GameStatusUnspecified, state.GameStatus)
}

// TestCombatRoundLock_BackfillCandidates is the end-to-end claim: which matches
// do the two backfill queries return, given the state each lock/unlock
// sequence leaves behind.
func TestCombatRoundLock_BackfillCandidates(t *testing.T) {
	logger := loggerForTest(t)
	groupID := uuid.Must(uuid.NewV4())

	matchRegistry, createFn, cancel, err := createTestMatchRegistryWithInterval(t, logger, 10)
	require.NoError(t, err)
	defer cancel()

	combat := func(steps ...SignalOpCode) *MatchLabel {
		s := newRoundLockTestState(evr.ModeCombatPublic, groupID, GameStatusPlaying)
		for _, op := range steps {
			s = signalRoundLock(t, s, op)
		}
		return s
	}

	playing := combat()
	locked := combat(SignalLockSession)
	reopened := combat(SignalLockSession, SignalUnlockSession)

	over := combat(SignalLockSession)
	over.GameStatus = GameStatusPostMatch
	over.Open = false
	over = signalRoundLock(t, over, SignalUnlockSession)

	arenaLocked := newRoundLockTestState(evr.ModeArenaPublic, groupID, GameStatusPlaying)
	arenaLocked = signalRoundLock(t, arenaLocked, SignalLockSession)

	create := func(s *MatchLabel) string {
		b, err := json.Marshal(s)
		require.NoError(t, err)
		id, err := matchRegistry.CreateMatch(context.Background(), createFn, "go", map[string]interface{}{"label": string(b)})
		require.NoError(t, err)
		return id
	}
	ids := map[string]string{
		"playing":     create(playing),
		"locked":      create(locked),
		"reopened":    create(reopened),
		"over":        create(over),
		"arenaLocked": create(arenaLocked),
	}
	time.Sleep(100 * time.Millisecond) // label flush interval is 10ms

	list := func(query string) map[string]bool {
		matches, _, err := matchRegistry.ListMatches(context.Background(), 100, nil, nil,
			&wrapperspb.Int32Value{Value: 0}, &wrapperspb.Int32Value{Value: int32(MatchLobbyMaxSize)},
			&wrapperspb.StringValue{Value: query}, nil)
		require.NoError(t, err)
		got := map[string]bool{}
		for _, m := range matches {
			for name, id := range ids {
				if m.MatchId == id {
					got[name] = true
				}
			}
		}
		return got
	}

	params := func(mode evr.Symbol) *LobbySessionParameters {
		return &LobbySessionParameters{Mode: mode, GroupID: groupID, MatchmakingTimestamp: time.Now().UTC(), PartySize: atomic.NewInt64(1)}
	}

	for name, query := range map[string]string{
		"GetBackfillMatches/combat":  backfillMatchesQuery(groupID, evr.ModeCombatPublic),
		"BackfillSearchQuery/combat": params(evr.ModeCombatPublic).BackfillSearchQuery(false, false),
	} {
		t.Run(name, func(t *testing.T) {
			got := list(query)
			assert.True(t, got["playing"], "an in-round combat match is a candidate")
			assert.False(t, got["locked"], "a combat match after the round-end lock must be excluded")
			assert.True(t, got["reopened"], "a combat match reopened by the between-rounds unlock is a candidate again")
			assert.False(t, got["over"], "an unlock after post-match must not put the match back in the pool")
			assert.False(t, got["arenaLocked"], "other modes never match a combat query")
		})
	}

	t.Run("arena unchanged", func(t *testing.T) {
		for name, query := range map[string]string{
			"GetBackfillMatches":  backfillMatchesQuery(groupID, evr.ModeArenaPublic),
			"BackfillSearchQuery": params(evr.ModeArenaPublic).BackfillSearchQuery(false, false),
		} {
			assert.False(t, list(query)["arenaLocked"], name)
		}
	})
}

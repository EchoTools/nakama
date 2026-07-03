package server

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// newTestLifecycle returns a lifecycle wired to an observable logger so tests
// can assert on log output.
func newTestLifecycle() (*PlayerMatchLifecycle, *observer.ObservedLogs) {
	core, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)
	return NewPlayerMatchLifecycle(logger), logs
}

func TestPlayerMatchLifecycle_NewStartsIdle(t *testing.T) {
	t.Parallel()
	lc, _ := newTestLifecycle()
	require.Equal(t, StateIdle, lc.State())
}

// TestPlayerMatchLifecycle_LegalTransitions exercises every edge in the legal
// transition graph.
func TestPlayerMatchLifecycle_LegalTransitions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		from   MatchLifecycleState
		to     MatchLifecycleState
		reason string
	}{
		{"Idle_to_SocialConverging", StateIdle, StateSocialConverging, "JoinPartyGroup, reservation created"},
		{"SocialConverging_to_SocialReady", StateSocialConverging, StateSocialReady, "Joined leader's social lobby"},
		{"SocialReady_to_Holding", StateSocialReady, StateHolding, "Sent LobbyFindSessionRequest (non-leader)"},
		{"SocialReady_to_Matchmaking", StateSocialReady, StateMatchmaking, "Leader submits ticket"},
		{"SocialReady_to_Idle", StateSocialReady, StateIdle, "Left party"},
		{"Holding_to_Matchmaking", StateHolding, StateMatchmaking, "Leader's ticket includes this member"},
		{"Holding_to_SocialReady", StateHolding, StateSocialReady, "Matchmaking cancelled"},
		{"Holding_to_InMatch", StateHolding, StateInMatch, "Followed leader directly (rare)"},
		{"Matchmaking_to_Joining", StateMatchmaking, StateJoining, "Match found, join started"},
		{"Matchmaking_to_SocialReady", StateMatchmaking, StateSocialReady, "Ticket cancelled (late arrival rebuild)"},
		{"Joining_to_InMatch", StateJoining, StateInMatch, "Connected to game server"},
		{"Joining_to_SocialReady", StateJoining, StateSocialReady, "Join failed, return to social"},
		{"InMatch_to_Returning", StateInMatch, StateReturning, "Match ended naturally"},
		{"InMatch_to_Crashed", StateInMatch, StateCrashed, "Client disconnected"},
		{"InMatch_to_Matchmaking", StateInMatch, StateMatchmaking, "Ticket submitted while stale InMatch"},
		{"Returning_to_SocialReady", StateReturning, StateSocialReady, "Back in social lobby"},
		{"Returning_to_Crashed", StateReturning, StateCrashed, "Disconnect during return"},
		{"Crashed_to_InMatch", StateCrashed, StateInMatch, "Reconnected within 27s"},
		{"Crashed_to_Idle", StateCrashed, StateIdle, "Reconnect failed, reservation expired"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lc, logs := newTestLifecycle()

			// Force into the source state directly so each case is independent.
			lc.mu.Lock()
			lc.state = tc.from
			lc.mu.Unlock()

			err := lc.Transition(tc.to, tc.reason)
			require.NoError(t, err)
			require.Equal(t, tc.to, lc.State())

			// The transition must be recorded as legal.
			history := lc.History()
			require.Len(t, history, 1)
			require.True(t, history[0].Legal)
			require.Equal(t, tc.from, history[0].From)
			require.Equal(t, tc.to, history[0].To)
			require.Equal(t, tc.reason, history[0].Reason)

			// Debug log, no warnings.
			require.Equal(t, 1, logs.Len(), "expected exactly one log entry")
			entry := logs.All()[0]
			require.Equal(t, zapcore.DebugLevel, entry.Level)
			require.Equal(t, "Player match lifecycle transition", entry.Message)
		})
	}
}

// TestPlayerMatchLifecycle_IllegalTransitions verifies that illegal transitions
// are logged at warn but still applied (observer mode).
func TestPlayerMatchLifecycle_IllegalTransitions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		from   MatchLifecycleState
		to     MatchLifecycleState
		reason string
	}{
		{"SocialConverging_to_Matchmaking", StateSocialConverging, StateMatchmaking, "premature matchmake"},
		{"Matchmaking_to_Idle", StateMatchmaking, StateIdle, "silent drop"},
		{"Joining_to_Idle", StateJoining, StateIdle, "silent abandon"},
		{"Idle_to_InMatch", StateIdle, StateInMatch, "direct jump"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lc, logs := newTestLifecycle()

			lc.mu.Lock()
			lc.state = tc.from
			lc.mu.Unlock()

			err := lc.Transition(tc.to, tc.reason)
			require.NoError(t, err, "observer mode: illegal transitions still return nil")

			// State must be applied despite being illegal.
			require.Equal(t, tc.to, lc.State())

			// Recorded as illegal.
			history := lc.History()
			require.Len(t, history, 1)
			require.False(t, history[0].Legal)

			// Warn log with anomaly field.
			require.Equal(t, 1, logs.Len())
			entry := logs.All()[0]
			require.Equal(t, zapcore.WarnLevel, entry.Level)
			require.Equal(t, "Illegal player match lifecycle transition", entry.Message)

			// Verify the anomaly field is present.
			found := false
			for _, f := range entry.ContextMap() {
				if f == "transition not in legal set" {
					found = true
					break
				}
			}
			require.True(t, found, "expected anomaly field in warn log")
		})
	}
}

// TestPlayerMatchLifecycle_TransitionOpts verifies functional options set
// fields during transitions.
func TestPlayerMatchLifecycle_TransitionOpts(t *testing.T) {
	t.Parallel()
	lc, _ := newTestLifecycle()

	err := lc.TransitionTo(StateSocialConverging, "join party",
		WithPartyID("party-123"),
		WithLeaderSID("leader-abc"),
		WithIsLeader(false),
	)
	require.NoError(t, err)

	lc.mu.RLock()
	require.Equal(t, "party-123", lc.partyID)
	require.Equal(t, "leader-abc", lc.leaderSID)
	require.False(t, lc.isLeader)
	lc.mu.RUnlock()

	// Transition to a match state with match ID.
	lc.mu.Lock()
	lc.state = StateJoining
	lc.mu.Unlock()

	err = lc.TransitionTo(StateInMatch, "connected",
		WithMatchID("match-xyz"),
	)
	require.NoError(t, err)

	lc.mu.RLock()
	require.Equal(t, "match-xyz", lc.matchID)
	require.Equal(t, "party-123", lc.partyID, "unchanged fields must persist")
	lc.mu.RUnlock()
}

// TestPlayerMatchLifecycle_Reset verifies that Reset clears all fields and
// returns to StateIdle.
func TestPlayerMatchLifecycle_Reset(t *testing.T) {
	t.Parallel()
	lc, _ := newTestLifecycle()

	// Set up some state.
	_ = lc.TransitionTo(StateSocialConverging, "join",
		WithPartyID("p1"),
		WithMatchID("m1"),
		WithLeaderSID("l1"),
		WithIsLeader(true),
	)
	require.Equal(t, StateSocialConverging, lc.State())
	require.NotEmpty(t, lc.History())

	lc.Reset()

	require.Equal(t, StateIdle, lc.State())

	lc.mu.RLock()
	require.Empty(t, lc.partyID)
	require.Empty(t, lc.matchID)
	require.Empty(t, lc.leaderSID)
	require.False(t, lc.isLeader)
	lc.mu.RUnlock()

	require.Empty(t, lc.History(), "history must be cleared on reset")
}

// TestPlayerMatchLifecycle_History verifies the transition log is returned
// as a copy and in order.
func TestPlayerMatchLifecycle_History(t *testing.T) {
	t.Parallel()
	lc, _ := newTestLifecycle()

	_ = lc.Transition(StateSocialConverging, "step1")
	_ = lc.Transition(StateSocialReady, "step2")

	history := lc.History()
	require.Len(t, history, 2)
	require.Equal(t, StateIdle, history[0].From)
	require.Equal(t, StateSocialConverging, history[0].To)
	require.Equal(t, StateSocialConverging, history[1].From)
	require.Equal(t, StateSocialReady, history[1].To)

	// Mutating the returned slice must not affect the internal log.
	history[0].Reason = "tampered"
	fresh := lc.History()
	require.Equal(t, "step1", fresh[0].Reason)
}

// TestPlayerMatchLifecycle_ConcurrentTransitions runs concurrent transitions
// to verify thread safety under the race detector.
func TestPlayerMatchLifecycle_ConcurrentTransitions(t *testing.T) {
	t.Parallel()
	lc, _ := newTestLifecycle()

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := range goroutines {
		go func(n int) {
			defer wg.Done()
			// Alternate between two transitions to exercise contention.
			if n%2 == 0 {
				_ = lc.TransitionTo(StateSocialConverging, "concurrent-even",
					WithPartyID("p"),
				)
			} else {
				_ = lc.Transition(StateSocialReady, "concurrent-odd")
			}
			// Read state concurrently with writes.
			_ = lc.State()
			_ = lc.History()
		}(i)
	}

	wg.Wait()

	// All transitions must have been recorded.
	history := lc.History()
	require.Len(t, history, goroutines)
}

// TestPlayerMatchLifecycle_HappyPathCycle walks the full happy-path loop:
// IDLE → SOCIAL_CONVERGING → SOCIAL_READY → HOLDING → MATCHMAKING →
// JOINING → IN_MATCH → RETURNING → SOCIAL_READY (loop back).
func TestPlayerMatchLifecycle_HappyPathCycle(t *testing.T) {
	t.Parallel()
	lc, logs := newTestLifecycle()

	steps := []struct {
		to     MatchLifecycleState
		reason string
	}{
		{StateSocialConverging, "JoinPartyGroup"},
		{StateSocialReady, "Joined leader's lobby"},
		{StateHolding, "Sent LobbyFindSessionRequest"},
		{StateMatchmaking, "Leader's ticket includes member"},
		{StateJoining, "Match found"},
		{StateInMatch, "Connected to game server"},
		{StateReturning, "Match ended"},
		{StateSocialReady, "Back in social lobby"},
	}

	for _, s := range steps {
		err := lc.Transition(s.to, s.reason)
		require.NoError(t, err)
		require.Equal(t, s.to, lc.State())
	}

	// Every transition must be legal.
	history := lc.History()
	require.Len(t, history, len(steps))
	for i, h := range history {
		require.True(t, h.Legal, "step %d (%s → %s) must be legal", i, h.From, h.To)
	}

	// No warn-level logs.
	for _, entry := range logs.All() {
		require.Equal(t, zapcore.DebugLevel, entry.Level,
			"happy path must not produce warn logs: %s", entry.Message)
	}
}

// TestPlayerMatchLifecycle_CrashRecovery tests IN_MATCH → CRASHED → IN_MATCH
// (successful reconnect within 27s).
func TestPlayerMatchLifecycle_CrashRecovery(t *testing.T) {
	t.Parallel()
	lc, logs := newTestLifecycle()

	// Fast-forward to InMatch.
	lc.mu.Lock()
	lc.state = StateInMatch
	lc.mu.Unlock()

	err := lc.Transition(StateCrashed, "client disconnected")
	require.NoError(t, err)
	require.Equal(t, StateCrashed, lc.State())

	err = lc.Transition(StateInMatch, "reconnected within 27s")
	require.NoError(t, err)
	require.Equal(t, StateInMatch, lc.State())

	history := lc.History()
	require.Len(t, history, 2)
	require.True(t, history[0].Legal)
	require.True(t, history[1].Legal)

	for _, entry := range logs.All() {
		require.Equal(t, zapcore.DebugLevel, entry.Level)
	}
}

// TestPlayerMatchLifecycle_CrashExpire tests IN_MATCH → CRASHED → IDLE
// (reconnect failed, reservation expired).
func TestPlayerMatchLifecycle_CrashExpire(t *testing.T) {
	t.Parallel()
	lc, logs := newTestLifecycle()

	lc.mu.Lock()
	lc.state = StateInMatch
	lc.mu.Unlock()

	err := lc.Transition(StateCrashed, "client disconnected")
	require.NoError(t, err)

	err = lc.Transition(StateIdle, "reconnect failed, reservation expired")
	require.NoError(t, err)
	require.Equal(t, StateIdle, lc.State())

	history := lc.History()
	require.Len(t, history, 2)
	require.True(t, history[0].Legal)
	require.True(t, history[1].Legal)

	for _, entry := range logs.All() {
		require.Equal(t, zapcore.DebugLevel, entry.Level)
	}
}

// TestPlayerMatchLifecycle_TicketCancellation tests MATCHMAKING → SOCIAL_READY
// (ticket cancelled for late arrival rebuild).
func TestPlayerMatchLifecycle_TicketCancellation(t *testing.T) {
	t.Parallel()
	lc, logs := newTestLifecycle()

	lc.mu.Lock()
	lc.state = StateMatchmaking
	lc.mu.Unlock()

	err := lc.Transition(StateSocialReady, "ticket cancelled (late arrival rebuild)")
	require.NoError(t, err)
	require.Equal(t, StateSocialReady, lc.State())

	history := lc.History()
	require.Len(t, history, 1)
	require.True(t, history[0].Legal)

	entry := logs.All()[0]
	require.Equal(t, zapcore.DebugLevel, entry.Level)
}

// TestPlayerMatchLifecycle_StateString verifies String() for all states and
// the unknown boundary.
func TestPlayerMatchLifecycle_StateString(t *testing.T) {
	t.Parallel()

	cases := []struct {
		state MatchLifecycleState
		want  string
	}{
		{StateIdle, "Idle"},
		{StateSocialConverging, "SocialConverging"},
		{StateSocialReady, "SocialReady"},
		{StateHolding, "Holding"},
		{StateMatchmaking, "Matchmaking"},
		{StateJoining, "Joining"},
		{StateInMatch, "InMatch"},
		{StateReturning, "Returning"},
		{StateCrashed, "Crashed"},
		{matchLifecycleStateCount, "Unknown(9)"},
		{MatchLifecycleState(255), "Unknown(255)"},
	}

	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, tc.state.String())
		})
	}
}

// TestPlayerMatchLifecycle_IsTerminal verifies no state is terminal.
func TestPlayerMatchLifecycle_IsTerminal(t *testing.T) {
	t.Parallel()
	for s := MatchLifecycleState(0); s < matchLifecycleStateCount; s++ {
		require.False(t, s.IsTerminal(), "%s must not be terminal", s)
	}
}

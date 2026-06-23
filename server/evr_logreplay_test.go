package server

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// replayFixture is the on-disk format for a lifecycle replay test case.
type replayFixture struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	SourceLog   string              `json:"source_log"`
	TimeWindow  string              `json:"time_window"`
	Transitions []fixtureTransition `json:"transitions"`
}

// fixtureTransition is one state change inside a replay fixture.
type fixtureTransition struct {
	Ts     string `json:"ts"`
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
	Legal  bool   `json:"legal"`
}

// statesByName maps human-readable state names to MatchLifecycleState values.
// Built once; used by parseMatchLifecycleState.
var statesByName = func() map[string]MatchLifecycleState {
	m := make(map[string]MatchLifecycleState, int(matchLifecycleStateCount))
	for i := MatchLifecycleState(0); i < matchLifecycleStateCount; i++ {
		m[stateNames[i]] = i
	}
	return m
}()

// parseMatchLifecycleState converts a state name string to its typed value.
func parseMatchLifecycleState(s string) (MatchLifecycleState, error) {
	st, ok := statesByName[s]
	if !ok {
		return 0, fmt.Errorf("unknown MatchLifecycleState %q", s)
	}
	return st, nil
}

// loadReplayFixture reads and unmarshals a replay fixture from testdata.
func loadReplayFixture(t *testing.T, path string) replayFixture {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err, "reading fixture %s", path)
	var fix replayFixture
	require.NoError(t, json.Unmarshal(data, &fix), "unmarshaling fixture %s", path)
	require.NotEmpty(t, fix.Transitions, "fixture %s has no transitions", path)
	return fix
}

func TestReplay_ReconnectSocialLobbyJoin(t *testing.T) {
	fix := loadReplayFixture(t, "testdata/replay/reconnect-social-lobby-join.json")
	logger := loggerForTest(t)
	lc := NewPlayerMatchLifecycle(logger)

	// Replay every transition from the fixture.
	for i, tr := range fix.Transitions {
		toState, err := parseMatchLifecycleState(tr.To)
		require.NoError(t, err, "transition %d: bad To state", i)
		require.NoError(t, lc.TransitionTo(toState, tr.Reason), "transition %d failed", i)
	}

	// Compare recorded history against fixture.
	history := lc.History()
	require.Len(t, history, len(fix.Transitions), "history length mismatch")

	for i, want := range fix.Transitions {
		got := history[i]

		wantFrom, err := parseMatchLifecycleState(want.From)
		require.NoError(t, err, "transition %d: bad From state in fixture", i)
		wantTo, err := parseMatchLifecycleState(want.To)
		require.NoError(t, err, "transition %d: bad To state in fixture", i)

		require.Equal(t, wantFrom, got.From,
			"transition %d: From: want %s, got %s", i, wantFrom, got.From)
		require.Equal(t, wantTo, got.To,
			"transition %d: To: want %s, got %s", i, wantTo, got.To)
		require.Equal(t, want.Reason, got.Reason,
			"transition %d: Reason mismatch", i)
		require.Equal(t, want.Legal, got.Legal,
			"transition %d: Legal: want %v, got %v (from %s to %s)",
			i, want.Legal, got.Legal, want.From, want.To)
	}
}

// replayFixtureTest is a shared helper that loads a fixture, replays all
// transitions through a fresh PlayerMatchLifecycle, and asserts that the
// recorded history matches the fixture exactly (From, To, Reason, Legal).
func replayFixtureTest(t *testing.T, path string) {
	t.Helper()
	fix := loadReplayFixture(t, path)
	logger := loggerForTest(t)
	lc := NewPlayerMatchLifecycle(logger)

	for i, tr := range fix.Transitions {
		toState, err := parseMatchLifecycleState(tr.To)
		require.NoError(t, err, "transition %d: bad To state", i)
		require.NoError(t, lc.TransitionTo(toState, tr.Reason), "transition %d failed", i)
	}

	history := lc.History()
	require.Len(t, history, len(fix.Transitions), "history length mismatch")

	for i, want := range fix.Transitions {
		got := history[i]

		wantFrom, err := parseMatchLifecycleState(want.From)
		require.NoError(t, err, "transition %d: bad From state in fixture", i)
		wantTo, err := parseMatchLifecycleState(want.To)
		require.NoError(t, err, "transition %d: bad To state in fixture", i)

		require.Equal(t, wantFrom, got.From,
			"transition %d: From: want %s, got %s", i, wantFrom, got.From)
		require.Equal(t, wantTo, got.To,
			"transition %d: To: want %s, got %s", i, wantTo, got.To)
		require.Equal(t, want.Reason, got.Reason,
			"transition %d: Reason mismatch", i)
		require.Equal(t, want.Legal, got.Legal,
			"transition %d: Legal: want %v, got %v (from %s to %s)",
			i, want.Legal, got.Legal, want.From, want.To)
	}
}

func TestReplay_StaleStreamInfiniteMatchmaking(t *testing.T) {
	replayFixtureTest(t, "testdata/replay/stale-stream-infinite-matchmaking.json")
}

func TestReplay_CrashRecoveryRejoin(t *testing.T) {
	replayFixtureTest(t, "testdata/replay/crash-recovery-rejoin.json")
}

func TestReplay_FullArenaLifecycle(t *testing.T) {
	replayFixtureTest(t, "testdata/replay/full-arena-lifecycle.json")
}

func TestReplay_PartyChurnRepeatedCycles(t *testing.T) {
	replayFixtureTest(t, "testdata/replay/party-churn-repeated-cycles.json")
}

func TestReplay_StaleSkipMatchmakingLoop(t *testing.T) {
	replayFixtureTest(t, "testdata/replay/stale-skip-matchmaking-loop.json")
}

func TestReplay_HealthyPartyFollow(t *testing.T) {
	replayFixtureTest(t, "testdata/replay/healthy-party-follow.json")
}

func TestLifecycleTransition_LegalityCheck(t *testing.T) {
	fix := loadReplayFixture(t, "testdata/replay/reconnect-social-lobby-join.json")

	for i, tr := range fix.Transitions {
		from, err := parseMatchLifecycleState(tr.From)
		require.NoError(t, err, "transition %d: bad From state", i)
		to, err := parseMatchLifecycleState(tr.To)
		require.NoError(t, err, "transition %d: bad To state", i)

		t.Run(fmt.Sprintf("%s_to_%s", tr.From, tr.To), func(t *testing.T) {
			t.Parallel()
			got := isLegalTransition(from, to)
			require.Equal(t, tr.Legal, got,
				"isLegalTransition(%s, %s): want %v, got %v", tr.From, tr.To, tr.Legal, got)
		})
	}
}

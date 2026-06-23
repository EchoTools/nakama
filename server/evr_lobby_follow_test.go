package server

import (
	"context"
	"encoding/json"
	"go.uber.org/atomic"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// ---------------------------------------------------------------------------
// Test helpers: reusable setup for TryFollowPartyLeader / pollFollowPartyLeader
// ---------------------------------------------------------------------------

type followTestEnv struct {
	leaderSID   uuid.UUID
	leaderUID   uuid.UUID
	followerSID uuid.UUID
	followerUID uuid.UUID
	groupID     uuid.UUID
	tracker     *mockMatchmakingTracker
	pipeline    *EvrPipeline
	session     *sessionWS
	lobbyGroup  *LobbyGroup
	ph          *PartyHandler
	params      *LobbySessionParameters
}

// newFollowTestEnv creates a standard duo party test environment.
// The leader and follower are in a party. No streams are tracked by default;
// callers set up the specific tracker state for their scenario.
func newFollowTestEnv(t *testing.T) *followTestEnv {
	t.Helper()

	leaderSID := uuid.Must(uuid.NewV4())
	leaderUID := uuid.Must(uuid.NewV4())
	followerSID := uuid.Must(uuid.NewV4())
	followerUID := uuid.Must(uuid.NewV4())
	groupID := uuid.Must(uuid.NewV4())

	tracker := newMockMatchmakingTracker()

	leaderUP := &rtapi.UserPresence{
		UserId:    leaderUID.String(),
		SessionId: leaderSID.String(),
		Username:  "leader",
	}

	ph := &PartyHandler{
		members: NewPartyPresenceList(8),
	}
	ph.leader = &PartyLeader{
		UserPresence: leaderUP,
		PresenceID:   &PresenceID{SessionID: leaderSID, Node: "testnode"},
	}

	lobbyGroup := &LobbyGroup{ph: ph}

	params := &LobbySessionParameters{GroupID: groupID}

	pipeline := &EvrPipeline{}

	session := &sessionWS{}
	session.id = followerSID
	session.userID = followerUID
	session.pipeline = &Pipeline{node: "testnode"}
	session.pipeline.tracker = tracker

	return &followTestEnv{
		leaderSID:   leaderSID,
		leaderUID:   leaderUID,
		followerSID: followerSID,
		followerUID: followerUID,
		groupID:     groupID,
		tracker:     tracker,
		pipeline:    pipeline,
		session:     session,
		lobbyGroup:  lobbyGroup,
		ph:          ph,
		params:      params,
	}
}

// setLeaderMatch tracks the leader's service stream pointing to the given match.
func (e *followTestEnv) setLeaderMatch(matchID MatchID) {
	e.tracker.Track(context.Background(), e.leaderSID,
		PresenceStream{Mode: StreamModeService, Subject: e.leaderSID, Label: StreamLabelMatchService},
		e.leaderUID,
		PresenceMeta{Status: matchID.String()})
}

// setFollowerMatch tracks the follower's service stream pointing to the given match.
func (e *followTestEnv) setFollowerMatch(matchID MatchID) {
	e.tracker.Track(context.Background(), e.followerSID,
		PresenceStream{Mode: StreamModeService, Subject: e.followerSID, Label: StreamLabelMatchService},
		e.followerUID,
		PresenceMeta{Status: matchID.String()})
}

// setLeaderMatchmaking puts the leader on the matchmaking stream.
func (e *followTestEnv) setLeaderMatchmaking() {
	e.tracker.Track(context.Background(), e.leaderSID,
		PresenceStream{Mode: StreamModeMatchmaking, Subject: e.groupID},
		e.leaderUID,
		PresenceMeta{Status: "matchmaking"})
}

// removeLeaderMatchmaking removes the leader from the matchmaking stream.
func (e *followTestEnv) removeLeaderMatchmaking() {
	e.tracker.UntrackLocalByModes(e.leaderSID, map[uint8]struct{}{StreamModeMatchmaking: {}}, PresenceStream{})
}

// removeLeaderMatch removes the leader's service stream.
func (e *followTestEnv) removeLeaderMatch() {
	e.tracker.UntrackLocalByModes(e.leaderSID, map[uint8]struct{}{StreamModeService: {}}, PresenceStream{})
}

// setLeader changes the party leader to the given user/session.
func (e *followTestEnv) setLeader(sessionID, userID uuid.UUID, username string) {
	e.ph.Lock()
	e.ph.leader = &PartyLeader{
		UserPresence: &rtapi.UserPresence{
			UserId:    userID.String(),
			SessionId: sessionID.String(),
			Username:  username,
		},
		PresenceID: &PresenceID{SessionID: sessionID, Node: "testnode"},
	}
	e.ph.Unlock()
}

// clearLeader sets the party leader to nil.
func (e *followTestEnv) clearLeader() {
	e.ph.Lock()
	e.ph.leader = nil
	e.ph.Unlock()
}

// runPollWithTimeout runs pollFollowPartyLeader in a goroutine and returns
// the result or times out. Returns (result, timedOut).
func (e *followTestEnv) runPollWithTimeout(ctx context.Context, logger interface{ Fatal(...interface{}) }, timeout time.Duration) (bool, bool) {
	done := make(chan bool, 1)
	go func() {
		done <- e.pipeline.pollFollowPartyLeader(ctx, loggerForTest(logger.(*testing.T)), e.session, e.params, e.lobbyGroup)
	}()

	select {
	case result := <-done:
		return result, false
	case <-time.After(timeout):
		return false, true
	}
}

// ---------------------------------------------------------------------------
// mockFollowMatchRegistry is a minimal MatchRegistry for follow tests.
// Only GetMatch is implemented; other methods panic if called.
// ---------------------------------------------------------------------------

type mockFollowMatchRegistry struct {
	mu            sync.RWMutex
	matches       map[string]*MatchLabel // matchID string → label
	getMatchCalls atomic.Int32           // count of GetMatch invocations
}

func newMockFollowMatchRegistry() *mockFollowMatchRegistry {
	return &mockFollowMatchRegistry{matches: make(map[string]*MatchLabel)}
}

func (r *mockFollowMatchRegistry) SetMatch(id MatchID, label *MatchLabel) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.matches[id.String()] = label
}

func (r *mockFollowMatchRegistry) GetMatch(_ context.Context, id string) (*api.Match, string, error) {
	r.getMatchCalls.Add(1)
	r.mu.RLock()
	defer r.mu.RUnlock()
	label, ok := r.matches[id]
	if !ok {
		return nil, "", ErrMatchNotFound
	}
	labelJSON, _ := json.Marshal(label)
	return &api.Match{
		MatchId: id,
		Label:   &wrapperspb.StringValue{Value: string(labelJSON)},
	}, "", nil
}

// Unimplemented methods — panic if called (tests should not reach these).
func (r *mockFollowMatchRegistry) CreateMatch(context.Context, RuntimeMatchCreateFunction, string, map[string]interface{}) (string, error) {
	panic("unimplemented")
}
func (r *mockFollowMatchRegistry) NewMatch(*zap.Logger, uuid.UUID, RuntimeMatchCore, *atomic.Bool, map[string]interface{}) (*MatchHandler, error) {
	panic("unimplemented")
}
func (r *mockFollowMatchRegistry) RemoveMatch(uuid.UUID, PresenceStream) { panic("unimplemented") }
func (r *mockFollowMatchRegistry) UpdateMatchLabel(uuid.UUID, int, string, string, int64) error {
	panic("unimplemented")
}
func (r *mockFollowMatchRegistry) ListMatches(context.Context, int, *wrapperspb.BoolValue, *wrapperspb.StringValue, *wrapperspb.Int32Value, *wrapperspb.Int32Value, *wrapperspb.StringValue, *wrapperspb.StringValue) ([]*api.Match, []string, error) {
	panic("unimplemented")
}
func (r *mockFollowMatchRegistry) Stop(int) chan struct{} { panic("unimplemented") }
func (r *mockFollowMatchRegistry) Count() int             { panic("unimplemented") }
func (r *mockFollowMatchRegistry) JoinAttempt(context.Context, uuid.UUID, string, uuid.UUID, uuid.UUID, string, int64, map[string]string, string, string, string, map[string]string) (bool, bool, bool, string, string, []*MatchPresence) {
	panic("unimplemented")
}
func (r *mockFollowMatchRegistry) Join(uuid.UUID, []*MatchPresence)      { panic("unimplemented") }
func (r *mockFollowMatchRegistry) Leave(uuid.UUID, []*MatchPresence)     { panic("unimplemented") }
func (r *mockFollowMatchRegistry) Kick(PresenceStream, []*MatchPresence) { panic("unimplemented") }
func (r *mockFollowMatchRegistry) SendData(uuid.UUID, string, uuid.UUID, uuid.UUID, string, string, int64, []byte, bool, int64) {
	panic("unimplemented")
}
func (r *mockFollowMatchRegistry) Signal(context.Context, string, string) (string, error) {
	panic("unimplemented")
}
func (r *mockFollowMatchRegistry) GetState(context.Context, uuid.UUID, string) ([]*rtapi.UserPresence, int64, string, error) {
	panic("unimplemented")
}

// withMockNK configures the test environment with a RuntimeGoNakamaModule
// backed by the given mock match registry. This allows tests to exercise
// the MatchLabelByID code path in pollFollowPartyLeader.
func (e *followTestEnv) withMockNK(registry *mockFollowMatchRegistry) *followTestEnv {
	e.pipeline.nk = &RuntimeGoNakamaModule{matchRegistry: registry}
	return e
}

// ---------------------------------------------------------------------------
// Bug 1: TryFollowPartyLeader should NOT follow the leader's stale match
// when the leader is currently matchmaking.
// ---------------------------------------------------------------------------

func TestTryFollowPartyLeader_LeaderMatchmaking_ReturnsFalse(t *testing.T) {
	// Setup: leader and follower are both in Social Lobby (Match A).
	// The leader is also on the matchmaking stream (actively matchmaking).
	// TryFollowPartyLeader should return false so the follower waits for
	// the leader to finish matchmaking, rather than "following" to the
	// stale social lobby.

	logger := loggerForTest(t)

	leaderSID := uuid.Must(uuid.NewV4())
	leaderUID := uuid.Must(uuid.NewV4())
	followerSID := uuid.Must(uuid.NewV4())
	followerUID := uuid.Must(uuid.NewV4())
	matchA := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	groupID := uuid.Must(uuid.NewV4())

	tracker := newMockMatchmakingTracker()

	// Leader has a match service stream pointing to Match A (social lobby).
	tracker.Track(context.Background(), leaderSID,
		PresenceStream{Mode: StreamModeService, Subject: leaderSID, Label: StreamLabelMatchService},
		leaderUID,
		PresenceMeta{Status: matchA.String()})

	// Leader is on the matchmaking stream (actively matchmaking for arena).
	tracker.Track(context.Background(), leaderSID,
		PresenceStream{Mode: StreamModeMatchmaking, Subject: groupID},
		leaderUID,
		PresenceMeta{Status: "matchmaking"})

	// Follower has a match service stream pointing to Match A (same lobby).
	tracker.Track(context.Background(), followerSID,
		PresenceStream{Mode: StreamModeService, Subject: followerSID, Label: StreamLabelMatchService},
		followerUID,
		PresenceMeta{Status: matchA.String()})

	// Create a minimal LobbyGroup with the leader set.
	leaderUP := &rtapi.UserPresence{
		UserId:    leaderUID.String(),
		SessionId: leaderSID.String(),
		Username:  "leader",
	}

	ph := &PartyHandler{}
	ph.members = NewPartyPresenceList(8)
	ph.leader = &PartyLeader{
		UserPresence: leaderUP,
		PresenceID:   &PresenceID{SessionID: leaderSID, Node: "testnode"},
	}

	lobbyGroup := &LobbyGroup{ph: ph}

	params := &LobbySessionParameters{
		GroupID: groupID,
	}

	// Create a minimal pipeline with our mock tracker.
	pipeline := &EvrPipeline{}

	// Create a minimal sessionWS.
	session := &sessionWS{}
	session.id = followerSID
	session.userID = followerUID
	session.pipeline = &Pipeline{node: "testnode"}
	session.pipeline.tracker = tracker

	ctx := context.Background()

	result := pipeline.TryFollowPartyLeader(ctx, logger, session, params, lobbyGroup)

	if result {
		t.Error("TryFollowPartyLeader returned true when leader is matchmaking; expected false")
	}
}

func TestTryFollowPartyLeader_LeaderNotMatchmaking_ProceedsNormally(t *testing.T) {
	// When the leader is NOT matchmaking and is in a match, the follower
	// should proceed to try following. If the follower is already in the
	// leader's match, it should return true.

	logger := loggerForTest(t)

	leaderSID := uuid.Must(uuid.NewV4())
	leaderUID := uuid.Must(uuid.NewV4())
	followerSID := uuid.Must(uuid.NewV4())
	followerUID := uuid.Must(uuid.NewV4())
	matchA := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	tracker := newMockMatchmakingTracker()

	// Leader has a match service stream pointing to Match A.
	tracker.Track(context.Background(), leaderSID,
		PresenceStream{Mode: StreamModeService, Subject: leaderSID, Label: StreamLabelMatchService},
		leaderUID,
		PresenceMeta{Status: matchA.String()})

	// Leader is NOT on the matchmaking stream (no presence set).

	// Follower is in the same match.
	tracker.Track(context.Background(), followerSID,
		PresenceStream{Mode: StreamModeService, Subject: followerSID, Label: StreamLabelMatchService},
		followerUID,
		PresenceMeta{Status: matchA.String()})

	leaderUP := &rtapi.UserPresence{
		UserId:    leaderUID.String(),
		SessionId: leaderSID.String(),
		Username:  "leader",
	}
	ph := &PartyHandler{}
	ph.members = NewPartyPresenceList(8)
	ph.leader = &PartyLeader{
		UserPresence: leaderUP,
		PresenceID:   &PresenceID{SessionID: leaderSID, Node: "testnode"},
	}
	lobbyGroup := &LobbyGroup{ph: ph}

	params := &LobbySessionParameters{
		GroupID: uuid.Must(uuid.NewV4()),
	}

	pipeline := &EvrPipeline{}
	session := &sessionWS{}
	session.id = followerSID
	session.userID = followerUID
	session.pipeline = &Pipeline{node: "testnode"}
	session.pipeline.tracker = tracker

	ctx := context.Background()

	result := pipeline.TryFollowPartyLeader(ctx, logger, session, params, lobbyGroup)

	if !result {
		t.Error("TryFollowPartyLeader returned false when follower is already in leader's match (leader not matchmaking); expected true")
	}
}

// ===========================================================================
// TryFollowPartyLeader — regression tests for every early-return path
// ===========================================================================

func TestTryFollow_NilLeader_ReturnsFalse(t *testing.T) {
	// GetLeader() returns nil when the party handler has no leader.
	// TryFollowPartyLeader should return false gracefully.
	env := newFollowTestEnv(t)
	env.clearLeader()

	logger := loggerForTest(t)
	result := env.pipeline.TryFollowPartyLeader(context.Background(), logger, env.session, env.params, env.lobbyGroup)

	if result {
		t.Error("Expected false when leader is nil")
	}
}

func TestTryFollow_SelfIsLeader_ReturnsFalse(t *testing.T) {
	// If this player IS the leader (e.g., leadership transferred mid-flow),
	// TryFollowPartyLeader should return false so the caller falls through
	// to the leader matchmaking path.
	env := newFollowTestEnv(t)
	env.setLeader(env.followerSID, env.followerUID, "follower-now-leader")

	logger := loggerForTest(t)
	result := env.pipeline.TryFollowPartyLeader(context.Background(), logger, env.session, env.params, env.lobbyGroup)

	if result {
		t.Error("Expected false when follower is the leader")
	}
}

func TestTryFollow_LeaderNoMatchPresence_ReturnsFalse(t *testing.T) {
	// Leader exists but has no service stream (not in any match).
	// TryFollowPartyLeader should return false.
	env := newFollowTestEnv(t)
	// No service stream set for leader.

	logger := loggerForTest(t)
	result := env.pipeline.TryFollowPartyLeader(context.Background(), logger, env.session, env.params, env.lobbyGroup)

	if result {
		t.Error("Expected false when leader has no match presence")
	}
}

func TestTryFollow_LeaderEmptyMatchID_ReturnsFalse(t *testing.T) {
	// Leader has a service stream but with an empty/invalid status (no match ID).
	env := newFollowTestEnv(t)
	env.tracker.Track(context.Background(), env.leaderSID,
		PresenceStream{Mode: StreamModeService, Subject: env.leaderSID, Label: StreamLabelMatchService},
		env.leaderUID,
		PresenceMeta{Status: ""}) // Empty → MatchIDFromStringOrNil returns NilMatchID

	logger := loggerForTest(t)
	result := env.pipeline.TryFollowPartyLeader(context.Background(), logger, env.session, env.params, env.lobbyGroup)

	if result {
		t.Error("Expected false when leader's match ID is empty/nil")
	}
}

// TestTryFollow_FollowerInDifferentMatch_AttemptsJoin verifies that when the
// follower is in a different match from the leader and the leader's match is
// joinable (mocked registry returns an open label), TryFollowPartyLeader
// proceeds through match validation. The critical assertion is that the mock
// registry's GetMatch was called — confirming all tracker-based early returns
// were bypassed and match-label validation was reached.
//
// Note: lobbyJoin panics on nil metrics (no full pipeline); we recover from it
// and check that GetMatch was called before the panic, which is sufficient
// proof that the match-validation code path was exercised.
func TestTryFollow_FollowerInDifferentMatch_AttemptsJoin(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	matchA := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchB)
	env.setFollowerMatch(matchA)

	// Provide a mock registry that tracks how many times GetMatch is called.
	registry := newMockFollowMatchRegistry()
	registry.SetMatch(matchB, &MatchLabel{
		ID:          matchB,
		Mode:        evr.ModeArenaPublic,
		Open:        true,
		PlayerLimit: 8,
	})
	env.withMockNK(registry)

	logger := loggerForTest(t)

	// Recover from the panic that lobbyJoin will trigger (nil metrics).
	// We only care that GetMatch was called before the panic.
	func() {
		defer func() { recover() }() //nolint:errcheck
		env.pipeline.TryFollowPartyLeader(context.Background(), logger, env.session, env.params, env.lobbyGroup)
	}()

	if registry.getMatchCalls.Load() == 0 {
		t.Error("TryFollowPartyLeader: expected MatchLabelByID (GetMatch) to be called " +
			"when follower is in a different match, but registry was never queried — " +
			"an early-return guard incorrectly blocked the match-validation path")
	}
}

// TestTryFollow_FollowerNoMatchPresence_AttemptsJoin verifies that when the
// follower is at the main menu (no service stream) and the leader is in a
// joinable match, TryFollowPartyLeader reaches match validation. The critical
// assertion: mock registry's GetMatch was called, confirming the code path
// reached label validation before attempting the join.
func TestTryFollow_FollowerNoMatchPresence_AttemptsJoin(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchB)
	// No follower service stream set — follower is at main menu.

	registry := newMockFollowMatchRegistry()
	registry.SetMatch(matchB, &MatchLabel{
		ID:          matchB,
		Mode:        evr.ModeArenaPublic,
		Open:        true,
		PlayerLimit: 8,
	})
	env.withMockNK(registry)

	logger := loggerForTest(t)

	// Recover from any panic from lobbyJoin / lobbyAuthorize (nil metrics).
	func() {
		defer func() { recover() }() //nolint:errcheck
		env.pipeline.TryFollowPartyLeader(context.Background(), logger, env.session, env.params, env.lobbyGroup)
	}()

	if registry.getMatchCalls.Load() == 0 {
		t.Error("TryFollowPartyLeader: expected MatchLabelByID (GetMatch) to be called " +
			"when follower is at main menu and leader is in a joinable match, " +
			"but registry was never queried — match-validation path was skipped")
	}
}

// ===========================================================================
// pollFollowPartyLeader — regression tests for every branch/return path
//
// isFollowerInLeaderMatch decision matrix (closure inside pollFollowPartyLeader):
//
//   ┌──────────────────┬──────────┬──────────┬──────────┬──────────┐
//   │     Branch       │ Behavior │   Test   │   NK?   │ Witness  │
//   ├──────────────────┼──────────┼──────────┼──────────┼──────────┤
//   │ leader == nil    │ false    │ Disappears│ any    │ leader   │
//   │ self-is-leader   │ false    │ Becomes  │ any     │ leader   │
//   │ no leader match  │ false    │ LeftMatch│ any     │ tracker  │
//   │ leader nil match │ false    │ NilMatch │ any     │ tracker  │
//   │ same CurrentMatch│ false    │StaleFalse│ any     │ params   │
//   │ follower no match│ false    │ Disappears│ any    │ tracker  │
//   │ different matchID│ false    │ See below│ any     │ tracker  │
//   │ NK+label has user│ true     │ SameMatch│ set     │ MatchGet │
//   │ NK+label no user │ false    │FullMatch │ set     │ MatchGet │
//   │ NK error→tracker │ true     │RegErrFall│ set     │ fallback │
//   │ nil NK→tracker   │ true     │ SameMatch│ nil     │ fallback │
//   └──────────────────┴──────────┴──────────┴──────────┴──────────┘
//
// "different matchID" does not immediately return false — the function
// proceeds to the join-attempt path (label lookup → lobbyJoin). That
// path is exercised by TestTryFollow_FollowerInDifferentMatch_AttemptsJoin,
// TestPoll_FollowerNotInLeaderMatch_LooksUpLabel, and the leader-switch
// tests which reach convergence via the tracker fallback once matches align.
// ===========================================================================

func TestPoll_LeaderDisappears_ReturnsFalse(t *testing.T) {
	// The party leader vanishes during the poll (e.g., disconnects).
	// pollFollowPartyLeader should return false, not hang.
	env := newFollowTestEnv(t)

	// Leader is in a match and not matchmaking.
	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchB)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// After 1 second, leader disappears.
	go func() {
		time.Sleep(1 * time.Second)
		env.clearLeader()
	}()

	result, timedOut := env.runPollWithTimeout(ctx, t, 10*time.Second)
	if timedOut {
		t.Fatal("pollFollowPartyLeader hung after leader disappeared")
	}
	if result {
		t.Error("Expected false when leader disappears during poll")
	}
}

func TestPoll_FollowerBecomesLeader_ReturnsFalse(t *testing.T) {
	// Leadership transfers to the follower during the poll (original leader
	// left). pollFollowPartyLeader should return false so the caller
	// detects the new leadership and enters the leader matchmaking path.
	env := newFollowTestEnv(t)

	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchB)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// After 1 second, follower becomes the leader.
	go func() {
		time.Sleep(1 * time.Second)
		env.setLeader(env.followerSID, env.followerUID, "follower-now-leader")
	}()

	result, timedOut := env.runPollWithTimeout(ctx, t, 10*time.Second)
	if timedOut {
		t.Fatal("pollFollowPartyLeader hung after leadership transfer")
	}
	if result {
		t.Error("Expected false when follower becomes the leader during poll")
	}
}

func TestPoll_LeaderStillMatchmaking_KeepsPolling(t *testing.T) {
	// Leader is still on the matchmaking stream (not settled yet).
	// pollFollowPartyLeader should continue polling, not return immediately.
	// We verify this by checking the poll doesn't return within one poll cycle.
	env := newFollowTestEnv(t)

	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchB)
	env.setLeaderMatchmaking()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, timedOut := env.runPollWithTimeout(ctx, t, 4*time.Second)
	if !timedOut {
		t.Error("Expected poll to keep waiting while leader is matchmaking, but it returned early")
	}
}

func TestPoll_LeaderStillMatchmaking_ThenSettles_FollowerInMatch_ReturnsTrue(t *testing.T) {
	// Leader starts matchmaking, then finishes and settles into a match.
	// Follower is also placed in the same match. Poll should eventually return true.
	env := newFollowTestEnv(t).withMockNK(newMockFollowMatchRegistry())

	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchB)
	env.setLeaderMatchmaking()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// After 1 second, leader finishes matchmaking and enters Match B.
	// Follower is also placed in Match B.
	go func() {
		time.Sleep(1 * time.Second)
		env.removeLeaderMatchmaking()
		env.setFollowerMatch(matchB)
	}()

	result, timedOut := env.runPollWithTimeout(ctx, t, 12*time.Second)
	if timedOut {
		t.Fatal("pollFollowPartyLeader timed out waiting for leader to settle")
	}
	if !result {
		t.Error("Expected true when leader settles and follower is in same match")
	}
}

func TestPoll_LeaderLeftMatch_ReturnsFalse(t *testing.T) {
	// Leader's service stream is removed during the poll (leader disconnected
	// from match or left). pollFollowPartyLeader should return false.
	env := newFollowTestEnv(t)

	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchB)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// After 1 second, leader leaves their match.
	go func() {
		time.Sleep(1 * time.Second)
		env.removeLeaderMatch()
	}()

	result, timedOut := env.runPollWithTimeout(ctx, t, 10*time.Second)
	if timedOut {
		t.Fatal("pollFollowPartyLeader hung after leader left match")
	}
	if result {
		t.Error("Expected false when leader leaves their match during poll")
	}
}

func TestPoll_LeaderNilMatchID_KeepsPolling(t *testing.T) {
	// Leader has a service stream but with an empty/invalid status.
	// MatchIDFromStringOrNil returns NilMatchID → poll continues.
	env := newFollowTestEnv(t)

	// Set leader's match service stream with empty status.
	env.tracker.Track(context.Background(), env.leaderSID,
		PresenceStream{Mode: StreamModeService, Subject: env.leaderSID, Label: StreamLabelMatchService},
		env.leaderUID,
		PresenceMeta{Status: ""}) // Will produce NilMatchID

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, timedOut := env.runPollWithTimeout(ctx, t, 4*time.Second)
	if !timedOut {
		t.Error("Expected poll to keep waiting when leader has nil match ID, but it returned early")
	}
}

func TestPoll_LeaderSwitchesMatches_FollowerInNewMatch_ReturnsTrue(t *testing.T) {
	// Leader is in Match B, then switches to Match C during the poll.
	// Follower ends up in Match C. Poll should detect Match C, not Match B.
	env := newFollowTestEnv(t).withMockNK(newMockFollowMatchRegistry())

	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	matchC := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchB)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// After the first poll interval, switch leader to Match C and place follower there.
	go func() {
		time.Sleep(2 * time.Second)
		env.setLeaderMatch(matchC)
		env.setFollowerMatch(matchC)
	}()

	result, timedOut := env.runPollWithTimeout(ctx, t, 15*time.Second)
	if timedOut {
		t.Fatal("pollFollowPartyLeader timed out")
	}
	if !result {
		t.Error("Expected true when follower is in leader's new match after leader switched")
	}
}

// TestPoll_FollowerNotInLeaderMatch_LooksUpLabel verifies that when the leader
// is settled in a match and the follower is in a different match,
// pollFollowPartyLeader calls GetMatch on the registry (via MatchLabelByID).
//
// The mock registry records GetMatch calls. After the poll completes (or panics
// from lobbyJoin's nil metrics), we assert that GetMatch was invoked at least
// once — confirming the poll reached the label-lookup step.
func TestPoll_FollowerNotInLeaderMatch_LooksUpLabel(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	logger := loggerForTest(t)

	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	matchA := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchB)
	env.setFollowerMatch(matchA) // Different match — follower must try to join

	// Wire a mock registry that records GetMatch invocations.
	registry := newMockFollowMatchRegistry()
	registry.SetMatch(matchB, &MatchLabel{
		ID:          matchB,
		Mode:        evr.ModeArenaPublic,
		Open:        true,
		PlayerLimit: 8,
	})
	env.withMockNK(registry)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Run the poll in a goroutine with panic recovery so that a nil-metrics panic
	// from lobbyJoin does not crash the test process.
	done := make(chan struct{})
	go func() {
		defer func() {
			recover() //nolint:errcheck
			close(done)
		}()
		env.pipeline.pollFollowPartyLeader(ctx, logger, env.session, env.params, env.lobbyGroup)
	}()

	select {
	case <-done:
		// Poll returned or panicked — check the registry.
	case <-time.After(12 * time.Second):
		cancel()
		t.Fatal("pollFollowPartyLeader neither returned nor panicked within 12 seconds")
	}

	if registry.getMatchCalls.Load() == 0 {
		t.Error("pollFollowPartyLeader: expected MatchLabelByID (GetMatch) to be called " +
			"when follower is not in leader's match, but the registry was never queried — " +
			"the label-lookup step was not reached")
	}
}

func TestPoll_LeaderChangesPartway_NewLeaderInMatch_ReturnsTrue(t *testing.T) {
	// Original leader is matchmaking. A third player becomes leader during
	// the poll and is already in a match. The follower is also in that match.
	// Poll should detect the new leader's match.
	env := newFollowTestEnv(t).withMockNK(newMockFollowMatchRegistry())

	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	newLeaderSID := uuid.Must(uuid.NewV4())
	newLeaderUID := uuid.Must(uuid.NewV4())

	env.setLeaderMatchmaking()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// After 1 second, new leader takes over and is in Match B.
	// Follower is also in Match B.
	go func() {
		time.Sleep(1 * time.Second)
		env.setLeader(newLeaderSID, newLeaderUID, "new-leader")
		env.tracker.Track(context.Background(), newLeaderSID,
			PresenceStream{Mode: StreamModeService, Subject: newLeaderSID, Label: StreamLabelMatchService},
			newLeaderUID,
			PresenceMeta{Status: matchB.String()})
		env.setFollowerMatch(matchB)
	}()

	result, timedOut := env.runPollWithTimeout(ctx, t, 12*time.Second)
	if timedOut {
		t.Fatal("pollFollowPartyLeader timed out after leader change")
	}
	if !result {
		t.Error("Expected true when new leader is in match and follower followed")
	}
}

// TestPoll_ContextTimeout_ReturnsFalse verifies that pollFollowPartyLeader
// respects context deadline (e.g., the matchmaking timeout from lobbyFind).
func TestPoll_ContextTimeout_ReturnsFalse(t *testing.T) {
	env := newFollowTestEnv(t)

	// Leader is matchmaking forever (never settles).
	env.setLeaderMatchmaking()
	env.setLeaderMatch(MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, timedOut := env.runPollWithTimeout(ctx, t, 5*time.Second)
	if timedOut {
		t.Fatal("poll should have returned when context expired, not timed out at test level")
	}
	if result {
		t.Error("Expected false when context times out during poll")
	}
}

// TestPoll_ConcurrentLeaderAndFollowerUpdates verifies that the poll handles
// concurrent tracker updates safely (no data races). Run with -race.
func TestPoll_ConcurrentLeaderAndFollowerUpdates(t *testing.T) {
	env := newFollowTestEnv(t)

	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	// Initial: leader matchmaking.
	env.setLeaderMatchmaking()
	env.setLeaderMatch(matchB)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Rapidly toggle leader state from multiple goroutines.
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			time.Sleep(200 * time.Millisecond)
			env.removeLeaderMatchmaking()
			time.Sleep(200 * time.Millisecond)
			env.setLeaderMatchmaking()
		}
		// Finally settle: leader in match, follower in same match.
		env.removeLeaderMatchmaking()
		env.setFollowerMatch(matchB)
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			time.Sleep(150 * time.Millisecond)
			newMatch := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
			env.setLeaderMatch(newMatch)
		}
		// Settle on the final match.
		env.setLeaderMatch(matchB)
	}()

	result, timedOut := env.runPollWithTimeout(ctx, t, 8*time.Second)
	wg.Wait()

	if timedOut {
		t.Fatal("pollFollowPartyLeader hung during concurrent updates")
	}
	if !result {
		t.Error("Expected true after concurrent updates settle with both players in same match")
	}
}

// ---------------------------------------------------------------------------
// Bug 2: pollFollowPartyLeader should return true when the follower is
// already in the leader's match, not loop forever.
// ---------------------------------------------------------------------------

func TestPollFollowPartyLeader_SameMatch_ReturnsTrue(t *testing.T) {
	// Setup: leader and follower are both in Match B (the matchmaker placed
	// them there). The leader is NOT matchmaking. pollFollowPartyLeader
	// should detect this and return true immediately.

	logger := loggerForTest(t)

	leaderSID := uuid.Must(uuid.NewV4())
	leaderUID := uuid.Must(uuid.NewV4())
	followerSID := uuid.Must(uuid.NewV4())
	followerUID := uuid.Must(uuid.NewV4())
	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	groupID := uuid.Must(uuid.NewV4())

	tracker := newMockMatchmakingTracker()

	// Leader has match service stream → Match B.
	tracker.Track(context.Background(), leaderSID,
		PresenceStream{Mode: StreamModeService, Subject: leaderSID, Label: StreamLabelMatchService},
		leaderUID,
		PresenceMeta{Status: matchB.String()})

	// Leader is NOT on matchmaking stream.

	// Follower has match service stream → Match B (same match, placed by matchmaker).
	tracker.Track(context.Background(), followerSID,
		PresenceStream{Mode: StreamModeService, Subject: followerSID, Label: StreamLabelMatchService},
		followerUID,
		PresenceMeta{Status: matchB.String()})

	leaderUP := &rtapi.UserPresence{
		UserId:    leaderUID.String(),
		SessionId: leaderSID.String(),
		Username:  "leader",
	}
	ph := &PartyHandler{}
	ph.members = NewPartyPresenceList(8)
	ph.leader = &PartyLeader{
		UserPresence: leaderUP,
		PresenceID:   &PresenceID{SessionID: leaderSID, Node: "testnode"},
	}
	lobbyGroup := &LobbyGroup{ph: ph}

	params := &LobbySessionParameters{
		GroupID: groupID,
	}

	pipeline := &EvrPipeline{}
	session := &sessionWS{}
	session.id = followerSID
	session.userID = followerUID
	session.pipeline = &Pipeline{node: "testnode"}
	session.pipeline.tracker = tracker

	// Use a short timeout to detect the infinite loop bug. Without the fix,
	// the function would loop forever and hit the timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan bool, 1)
	go func() {
		done <- pipeline.pollFollowPartyLeader(ctx, logger, session, params, lobbyGroup)
	}()

	select {
	case result := <-done:
		if !result {
			t.Error("pollFollowPartyLeader returned false when follower is in leader's match; expected true")
		}
	case <-time.After(8 * time.Second):
		cancel()
		t.Fatal("pollFollowPartyLeader did not return within 8 seconds — stuck in infinite loop (bug #2)")
	}
}

// ===========================================================================
// DUO MATCHMAKING DESYNC / LEADER LOOP
//
// Bug: In a duo party (2 players), the leader enters a match successfully but
// the follower gets stuck in a persistent matchmaking loop. The follower
// cycles in the matchmaking UI, unable to sync with the leader.
//
// Root cause: When the matchmaker places both players into a match (or just
// the leader), the follower's StreamModeMatchmaking presence is removed by
// LobbyJoinEntrants (via UntrackLocalByModes). The monitorMatchmakingStream
// goroutine detects this removal, waits a 1-second grace period, then cancels
// the follower's lobbyFind context. This causes pollFollowPartyLeader to
// return false (via ctx.Done()), even if the follower IS in the leader's match.
// The follower gets "unable to follow party leader" → client retries → loop.
// ===========================================================================

// TestDuoDesync_MonitorCancelsFollowerContext_DuringPoll reproduces the core
// desync: the matchmaking monitor cancels the follower's context while
// pollFollowPartyLeader is waiting for the leader to settle.
//
// Timeline:
//
//	T=0s: Follower enters pollFollowPartyLeader (3s poll interval)
//	T=1s: Matchmaker places both players → removes matchmaking streams,
//	       updates service streams to Match B
//	T=2s: Monitor detects matchmaking presence gone, starts 1s grace period
//	T=3s: Monitor grace expires → cancels context
//	T=3s: pollFollowPartyLeader wakes up, sees ctx.Done() → returns false
//
// Expected: pollFollowPartyLeader should return true (both are in Match B).
// Actual (bug): returns false because context was canceled by the monitor.
func TestDuoDesync_MonitorCancelsFollowerContext_DuringPoll(t *testing.T) {
	logger := loggerForTest(t)

	leaderSID := uuid.Must(uuid.NewV4())
	leaderUID := uuid.Must(uuid.NewV4())
	followerSID := uuid.Must(uuid.NewV4())
	followerUID := uuid.Must(uuid.NewV4())
	matchA := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"} // old social lobby
	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"} // new arena match
	groupID := uuid.Must(uuid.NewV4())

	tracker := newMockMatchmakingTracker()

	// Initial state: Both players are in Social Lobby (Match A).
	// Leader is actively matchmaking for arena.
	tracker.Track(context.Background(), leaderSID,
		PresenceStream{Mode: StreamModeService, Subject: leaderSID, Label: StreamLabelMatchService},
		leaderUID,
		PresenceMeta{Status: matchA.String()})

	tracker.Track(context.Background(), leaderSID,
		PresenceStream{Mode: StreamModeMatchmaking, Subject: groupID},
		leaderUID,
		PresenceMeta{Status: "matchmaking"})

	tracker.Track(context.Background(), followerSID,
		PresenceStream{Mode: StreamModeService, Subject: followerSID, Label: StreamLabelMatchService},
		followerUID,
		PresenceMeta{Status: matchA.String()})

	// Follower also has a matchmaking stream presence (set by JoinMatchmakingStream
	// in lobbyFind before entering pollFollowPartyLeader).
	tracker.Track(context.Background(), followerSID,
		PresenceStream{Mode: StreamModeMatchmaking, Subject: groupID},
		followerUID,
		PresenceMeta{Status: "matchmaking"})

	leaderUP := &rtapi.UserPresence{
		UserId:    leaderUID.String(),
		SessionId: leaderSID.String(),
		Username:  "leader",
	}
	ph := &PartyHandler{}
	ph.members = NewPartyPresenceList(8)
	ph.leader = &PartyLeader{
		UserPresence: leaderUP,
		PresenceID:   &PresenceID{SessionID: leaderSID, Node: "testnode"},
	}
	lobbyGroup := &LobbyGroup{ph: ph}

	params := &LobbySessionParameters{
		GroupID: groupID,
	}

	pipeline := &EvrPipeline{}
	session := &sessionWS{}
	session.id = followerSID
	session.userID = followerUID
	session.pipeline = &Pipeline{node: "testnode"}
	session.pipeline.tracker = tracker

	// Create a cancellable context that simulates what lobbyFind creates.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Start the matchmaking monitor (just like lobbyFind does).
	// This goroutine will cancel the context when the matchmaking stream
	// presence is removed.
	mmStream := PresenceStream{Mode: StreamModeMatchmaking, Subject: groupID}
	mmSession := &MatchmakingSession{
		SessionID: followerSID,
		UserID:    followerUID,
		Stream:    mmStream,
		Tracker:   tracker,
	}
	monitorDone := make(chan struct{})
	go func() {
		MonitorMatchmakingStreamV2(ctx, logger, mmSession, 500*time.Millisecond, 500*time.Millisecond, cancel)
		close(monitorDone)
	}()

	// Start pollFollowPartyLeader concurrently.
	pollResult := make(chan bool, 1)
	go func() {
		pollResult <- pipeline.pollFollowPartyLeader(ctx, logger, session, params, lobbyGroup)
	}()

	// Simulate the matchmaker placing both players into Match B after a brief delay.
	// This is what happens inside LobbyJoinEntrants:
	//   1. Update service streams to point to the new match
	//   2. UntrackLocalByModes removes StreamModeMatchmaking presences
	time.Sleep(1 * time.Second)

	// Leader enters Match B (matchmaker places leader first).
	tracker.Track(context.Background(), leaderSID,
		PresenceStream{Mode: StreamModeService, Subject: leaderSID, Label: StreamLabelMatchService},
		leaderUID,
		PresenceMeta{Status: matchB.String()})
	// Leader's matchmaking stream removed.
	tracker.UntrackLocalByModes(leaderSID, map[uint8]struct{}{StreamModeMatchmaking: {}}, PresenceStream{})

	// Follower also placed into Match B (matchmaker includes all party members).
	tracker.Track(context.Background(), followerSID,
		PresenceStream{Mode: StreamModeService, Subject: followerSID, Label: StreamLabelMatchService},
		followerUID,
		PresenceMeta{Status: matchB.String()})
	// Follower's matchmaking stream removed — THIS triggers the monitor.
	tracker.UntrackLocalByModes(followerSID, map[uint8]struct{}{StreamModeMatchmaking: {}}, PresenceStream{})

	// Wait for the result. The poll should return true (follower is in leader's match).
	select {
	case result := <-pollResult:
		if !result {
			t.Error("DUO DESYNC BUG: pollFollowPartyLeader returned false even though " +
				"the follower IS in the leader's match. The matchmaking monitor canceled " +
				"the follower's context before the poll could detect the successful placement. " +
				"This causes the follower to get a 'unable to follow party leader' error and " +
				"the client retries, creating the persistent matchmaking loop.")
		}
	case <-time.After(12 * time.Second):
		cancel()
		t.Fatal("pollFollowPartyLeader did not return within 12 seconds")
	}

	// Cleanup
	cancel()
	<-monitorDone
}

// TestDuoDesync_FollowerRetryLoop_ContextAlwaysCanceled demonstrates the
// persistent retry loop: each time the follower's lobbyFind retries, the same
// desync occurs because the monitor always races with the poll.
//
// This test simulates 3 consecutive lobbyFind attempts for the follower,
// showing that each one fails because the matchmaking monitor cancels the
// context before pollFollowPartyLeader can detect the follower is in the
// leader's match.
func TestDuoDesync_FollowerRetryLoop_ContextAlwaysCanceled(t *testing.T) {
	logger := loggerForTest(t)

	leaderSID := uuid.Must(uuid.NewV4())
	leaderUID := uuid.Must(uuid.NewV4())
	followerSID := uuid.Must(uuid.NewV4())
	followerUID := uuid.Must(uuid.NewV4())
	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	groupID := uuid.Must(uuid.NewV4())

	consecutiveFailures := 0
	maxRetries := 3

	for attempt := 0; attempt < maxRetries; attempt++ {
		tracker := newMockMatchmakingTracker()

		// Leader is already in Match B (entered on a previous cycle or first try).
		tracker.Track(context.Background(), leaderSID,
			PresenceStream{Mode: StreamModeService, Subject: leaderSID, Label: StreamLabelMatchService},
			leaderUID,
			PresenceMeta{Status: matchB.String()})
		// Leader is NOT matchmaking (already in match).

		// Follower joins matchmaking stream (JoinMatchmakingStream in lobbyFind).
		tracker.Track(context.Background(), followerSID,
			PresenceStream{Mode: StreamModeMatchmaking, Subject: groupID},
			followerUID,
			PresenceMeta{Status: "matchmaking"})

		// Follower's old service stream from previous lobby.
		tracker.Track(context.Background(), followerSID,
			PresenceStream{Mode: StreamModeService, Subject: followerSID, Label: StreamLabelMatchService},
			followerUID,
			PresenceMeta{Status: ""}) // no match yet

		leaderUP := &rtapi.UserPresence{
			UserId:    leaderUID.String(),
			SessionId: leaderSID.String(),
			Username:  "leader",
		}
		ph := &PartyHandler{}
		ph.members = NewPartyPresenceList(8)
		ph.leader = &PartyLeader{
			UserPresence: leaderUP,
			PresenceID:   &PresenceID{SessionID: leaderSID, Node: "testnode"},
		}
		lobbyGroup := &LobbyGroup{ph: ph}
		params := &LobbySessionParameters{GroupID: groupID}

		pipeline := &EvrPipeline{}
		session := &sessionWS{}
		session.id = followerSID
		session.userID = followerUID
		session.pipeline = &Pipeline{node: "testnode"}
		session.pipeline.tracker = tracker

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

		// Start the matchmaking monitor.
		mmSession := &MatchmakingSession{
			SessionID: followerSID,
			UserID:    followerUID,
			Stream:    PresenceStream{Mode: StreamModeMatchmaking, Subject: groupID},
			Tracker:   tracker,
		}
		go MonitorMatchmakingStreamV2(ctx, logger, mmSession, 300*time.Millisecond, 300*time.Millisecond, cancel)

		// Simulate: TryFollowPartyLeader runs first. Leader is NOT matchmaking,
		// so it proceeds to check leader's match. Follower is NOT in leader's
		// match yet, so TryFollowPartyLeader tries lobbyJoin which fails (no nk).
		// In the real code, follower falls through to pollFollowPartyLeader.

		// Start pollFollowPartyLeader.
		pollResult := make(chan bool, 1)
		go func() {
			pollResult <- pipeline.pollFollowPartyLeader(ctx, logger, session, params, lobbyGroup)
		}()

		// After a delay, the follower's matchmaking stream is removed.
		// (In reality, this happens when LobbyJoinEntrants runs for the follower
		// or when the matchmaker processes the result.)
		go func() {
			time.Sleep(800 * time.Millisecond)
			// Update follower's service stream to Match B (matchmaker placed them).
			tracker.Track(context.Background(), followerSID,
				PresenceStream{Mode: StreamModeService, Subject: followerSID, Label: StreamLabelMatchService},
				followerUID,
				PresenceMeta{Status: matchB.String()})
			// Remove matchmaking stream (LobbyJoinEntrants does this).
			tracker.UntrackLocalByModes(followerSID, map[uint8]struct{}{StreamModeMatchmaking: {}}, PresenceStream{})
		}()

		select {
		case result := <-pollResult:
			if !result {
				consecutiveFailures++
			}
		case <-time.After(8 * time.Second):
			cancel()
			consecutiveFailures++
		}

		cancel()
	}

	// consecutiveFailures == 0 means every attempt succeeded (poll returned true).
	// Any non-zero count reveals the desync bug: the monitor canceled the context
	// before pollFollowPartyLeader could detect the follower in the leader's match.
	if consecutiveFailures != 0 {
		t.Errorf("DUO DESYNC RETRY LOOP: %d/%d lobbyFind attempts failed. "+
			"The follower is stuck in a persistent matchmaking loop because the "+
			"matchmaking monitor cancels the context on every attempt before "+
			"pollFollowPartyLeader can detect the successful match placement.",
			consecutiveFailures, maxRetries)
	}
}

// TestDuoDesync_TryFollow_LeaderStillMatchmaking_FallsToDesyncPoll demonstrates
// the entry point of the desync: the follower calls TryFollowPartyLeader while
// the leader is still matchmaking. TryFollowPartyLeader correctly returns false,
// and the follower falls through to pollFollowPartyLeader — where the monitor
// cancellation race causes the persistent loop.
//
// This test shows that the follow path is a dead end for the follower: the leader
// is matchmaking (can't follow), so the follower enters pollFollowPartyLeader
// which is vulnerable to the context cancellation bug.
func TestDuoDesync_TryFollow_LeaderStillMatchmaking_FallsToDesyncPoll(t *testing.T) {
	logger := loggerForTest(t)

	leaderSID := uuid.Must(uuid.NewV4())
	leaderUID := uuid.Must(uuid.NewV4())
	followerSID := uuid.Must(uuid.NewV4())
	followerUID := uuid.Must(uuid.NewV4())
	matchA := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	groupID := uuid.Must(uuid.NewV4())

	tracker := newMockMatchmakingTracker()

	// Both players in Social Lobby (Match A). Leader is matchmaking for arena.
	tracker.Track(context.Background(), leaderSID,
		PresenceStream{Mode: StreamModeService, Subject: leaderSID, Label: StreamLabelMatchService},
		leaderUID,
		PresenceMeta{Status: matchA.String()})
	tracker.Track(context.Background(), leaderSID,
		PresenceStream{Mode: StreamModeMatchmaking, Subject: groupID},
		leaderUID,
		PresenceMeta{Status: "matchmaking"})
	tracker.Track(context.Background(), followerSID,
		PresenceStream{Mode: StreamModeService, Subject: followerSID, Label: StreamLabelMatchService},
		followerUID,
		PresenceMeta{Status: matchA.String()})
	tracker.Track(context.Background(), followerSID,
		PresenceStream{Mode: StreamModeMatchmaking, Subject: groupID},
		followerUID,
		PresenceMeta{Status: "matchmaking"})

	leaderUP := &rtapi.UserPresence{
		UserId:    leaderUID.String(),
		SessionId: leaderSID.String(),
		Username:  "leader",
	}
	ph := &PartyHandler{}
	ph.members = NewPartyPresenceList(8)
	ph.leader = &PartyLeader{
		UserPresence: leaderUP,
		PresenceID:   &PresenceID{SessionID: leaderSID, Node: "testnode"},
	}
	lobbyGroup := &LobbyGroup{ph: ph}
	params := &LobbySessionParameters{GroupID: groupID}

	pipeline := &EvrPipeline{}
	session := &sessionWS{}
	session.id = followerSID
	session.userID = followerUID
	session.pipeline = &Pipeline{node: "testnode"}
	session.pipeline.tracker = tracker

	// Step 1: TryFollowPartyLeader returns false because leader is matchmaking.
	ctx := context.Background()
	result := pipeline.TryFollowPartyLeader(ctx, logger, session, params, lobbyGroup)
	if result {
		t.Fatal("Expected TryFollowPartyLeader to return false while leader is matchmaking")
	}

	// Step 2: Follower falls through to pollFollowPartyLeader.
	// The matchmaker finds a match and places both players.
	// But the monitor cancels the context before the poll detects it.
	ctx2, cancel2 := context.WithCancel(context.Background())

	// Start the monitor (like lobbyFind does).
	mmSession := &MatchmakingSession{
		SessionID: followerSID,
		UserID:    followerUID,
		Stream:    PresenceStream{Mode: StreamModeMatchmaking, Subject: groupID},
		Tracker:   tracker,
	}
	go MonitorMatchmakingStreamV2(ctx2, logger, mmSession, 300*time.Millisecond, 300*time.Millisecond, cancel2)

	pollResult := make(chan bool, 1)
	go func() {
		pollResult <- pipeline.pollFollowPartyLeader(ctx2, logger, session, params, lobbyGroup)
	}()

	// Simulate matchmaker placing both after 1 second.
	go func() {
		time.Sleep(1 * time.Second)
		// Leader enters Match B.
		tracker.Track(context.Background(), leaderSID,
			PresenceStream{Mode: StreamModeService, Subject: leaderSID, Label: StreamLabelMatchService},
			leaderUID,
			PresenceMeta{Status: matchB.String()})
		tracker.UntrackLocalByModes(leaderSID, map[uint8]struct{}{StreamModeMatchmaking: {}}, PresenceStream{})
		// Follower enters Match B.
		tracker.Track(context.Background(), followerSID,
			PresenceStream{Mode: StreamModeService, Subject: followerSID, Label: StreamLabelMatchService},
			followerUID,
			PresenceMeta{Status: matchB.String()})
		tracker.UntrackLocalByModes(followerSID, map[uint8]struct{}{StreamModeMatchmaking: {}}, PresenceStream{})
	}()

	select {
	case r := <-pollResult:
		if !r {
			t.Error("DUO DESYNC BUG: Full desync sequence reproduced. " +
				"(1) TryFollowPartyLeader returned false (leader matchmaking). " +
				"(2) pollFollowPartyLeader returned false (context canceled by monitor " +
				"before detecting both players in Match B). The follower gets " +
				"'unable to follow party leader' and the client retries endlessly.")
		}
	case <-time.After(10 * time.Second):
		cancel2()
		t.Fatal("pollFollowPartyLeader did not return within 10 seconds")
	}

	cancel2()
}

// TestPoll_StaleMatchFalsePositive_ReturnsFalse reproduces the party-stuck-in-
// transition bug: when the matchmaker times out without finding a match, the
// leader and all followers still have service streams pointing to the OLD social
// lobby they were in together. isFollowerInLeaderMatch() returns a false positive
// because it only checks that leader and follower are in the "same" match — it
// doesn't verify it's a NEW match vs the stale original match.
//
// Timeline from production logs:
//
//	T=0:    All 4 party members in social lobby (matchA)
//	T=0:    Leader starts LobbyFind → vibinatorsGravity redirects to combat
//	T=0:    Followers enter pollFollowPartyLeader (leader is matchmaking)
//	T=0-10m: Leader is in matchmaker. Followers poll every 3s, see "leader is
//	         matchmaking" → continue. No poll iteration reaches the join path.
//	T=10m:  Context expires (MatchmakingTimeout). isFollowerInLeaderMatch()
//	         returns TRUE because both leader and followers still point to matchA.
//	T=10m:  pollFollowPartyLeader returns true (false success).
//	T=10m:  lobbyFind returns nil. No LobbySessionSuccess sent. Players stuck.
func TestPoll_StaleMatchFalsePositive_ReturnsFalse(t *testing.T) {
	env := newFollowTestEnv(t)

	// All party members were in the same social lobby before lobby find.
	matchA := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchA)
	env.setFollowerMatch(matchA)
	env.setLeaderMatchmaking()

	// Set CurrentMatchID so the code knows this is the original match.
	env.params.CurrentMatchID = matchA

	// Context expires after 2 seconds (simulates 10-minute matchmaking timeout).
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, timedOut := env.runPollWithTimeout(ctx, t, 5*time.Second)
	if timedOut {
		t.Fatal("pollFollowPartyLeader hung — should have returned when context expired")
	}
	if result {
		t.Error("STALE MATCH BUG: pollFollowPartyLeader returned true because " +
			"isFollowerInLeaderMatch() saw both players still pointing to the OLD " +
			"social lobby (matchA). No new match was found — the matchmaker timed " +
			"out. Players are now stuck in transition with no LobbySessionSuccess.")
	}
}

// TestPoll_StaleMatchFalsePositive_ContextCancelWithNewMatch_ReturnsTrue verifies
// that when the matchmaker DOES place the follower into a genuinely new match,
// the stale-match guard doesn't block it. isFollowerInLeaderMatch should return
// true when the leader's match is different from CurrentMatchID.
func TestPoll_StaleMatchFalsePositive_ContextCancelWithNewMatch_ReturnsTrue(t *testing.T) {
	env := newFollowTestEnv(t)

	matchA := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchA) // starts in old lobby
	env.setFollowerMatch(matchA)
	env.setLeaderMatchmaking()
	env.params.CurrentMatchID = matchA

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// After 1 second, matchmaker places both into matchB.
	go func() {
		time.Sleep(1 * time.Second)
		env.removeLeaderMatchmaking()
		env.setLeaderMatch(matchB)
		env.setFollowerMatch(matchB)
	}()

	result, timedOut := env.runPollWithTimeout(ctx, t, 12*time.Second)
	if timedOut {
		t.Fatal("pollFollowPartyLeader timed out waiting for new match detection")
	}
	if !result {
		t.Error("Expected true when matchmaker placed both players into a genuinely " +
			"new match (matchB), different from the stale original (matchA)")
	}
}

// TestDuoDesync_PollFollow_ContextCanceledBeforeSettleCheck demonstrates the
// specific timing issue within pollFollowPartyLeader: the function has TWO
// 3-second waits per iteration. The context can be canceled during either wait.
//
// Even if the first wait completes and the poll detects the leader's match,
// the SECOND wait ("wait for leader to settle") will catch the context
// cancellation, causing the poll to return false.
func TestDuoDesync_PollFollow_ContextCanceledBeforeSettleCheck(t *testing.T) {
	logger := loggerForTest(t)

	leaderSID := uuid.Must(uuid.NewV4())
	leaderUID := uuid.Must(uuid.NewV4())
	followerSID := uuid.Must(uuid.NewV4())
	followerUID := uuid.Must(uuid.NewV4())
	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	groupID := uuid.Must(uuid.NewV4())

	tracker := newMockMatchmakingTracker()

	// Leader is in Match B, not matchmaking.
	tracker.Track(context.Background(), leaderSID,
		PresenceStream{Mode: StreamModeService, Subject: leaderSID, Label: StreamLabelMatchService},
		leaderUID,
		PresenceMeta{Status: matchB.String()})

	// Follower is in Match B too (placed by matchmaker), but also still on
	// the matchmaking stream (the stream removal hasn't been processed yet).
	tracker.Track(context.Background(), followerSID,
		PresenceStream{Mode: StreamModeService, Subject: followerSID, Label: StreamLabelMatchService},
		followerUID,
		PresenceMeta{Status: matchB.String()})
	tracker.Track(context.Background(), followerSID,
		PresenceStream{Mode: StreamModeMatchmaking, Subject: groupID},
		followerUID,
		PresenceMeta{Status: "matchmaking"})

	leaderUP := &rtapi.UserPresence{
		UserId:    leaderUID.String(),
		SessionId: leaderSID.String(),
		Username:  "leader",
	}
	ph := &PartyHandler{}
	ph.members = NewPartyPresenceList(8)
	ph.leader = &PartyLeader{
		UserPresence: leaderUP,
		PresenceID:   &PresenceID{SessionID: leaderSID, Node: "testnode"},
	}
	lobbyGroup := &LobbyGroup{ph: ph}
	params := &LobbySessionParameters{GroupID: groupID}

	pipeline := &EvrPipeline{}
	session := &sessionWS{}
	session.id = followerSID
	session.userID = followerUID
	session.pipeline = &Pipeline{node: "testnode"}
	session.pipeline.tracker = tracker

	// Create context that will be canceled to simulate the monitor.
	ctx, cancel := context.WithCancel(context.Background())

	pollResult := make(chan bool, 1)
	go func() {
		pollResult <- pipeline.pollFollowPartyLeader(ctx, logger, session, params, lobbyGroup)
	}()

	// Wait for the first poll interval (3s) + a bit, then cancel the context
	// during the second wait ("settle" period). This simulates the monitor
	// detecting the matchmaking stream removal and canceling.
	//
	// Timeline inside pollFollowPartyLeader:
	//   T=0: enters first 3-second wait
	//   T=3: wakes up, checks leader → leader in Match B
	//        enters second 3-second wait ("settle")
	//   T=4: context canceled (monitor)
	//   T=4: select picks ctx.Done() → returns false
	//   (never reaches the memberMatchID == leaderMatchID check)
	go func() {
		time.Sleep(4 * time.Second) // After first wait, during settle wait
		cancel()
	}()

	select {
	case result := <-pollResult:
		if !result {
			t.Error("DUO DESYNC BUG: pollFollowPartyLeader returned false because the " +
				"context was canceled during the 'settle' wait, BEFORE it could check " +
				"whether the follower is in the leader's match. Both players are in " +
				"Match B but the poll never gets to verify this.")
		}
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("pollFollowPartyLeader did not return within 10 seconds")
	}
}

// ===========================================================================
// Party Reservation Placeholder Tests
// ===========================================================================

func TestPartyReservation_SoloPlayerCreatesNoReservations(t *testing.T) {
	// A solo player (no party) joining a social lobby should NOT have any
	// additional reservation placeholders appended.
	logger := loggerForTest(t)

	leaderSID := uuid.Must(uuid.NewV4())
	leaderUID := uuid.Must(uuid.NewV4())

	// Single entrant — the leader themselves.
	entrants := []*EvrMatchPresence{
		{
			SessionID: leaderSID,
			UserID:    leaderUID,
			Username:  "solo-player",
		},
	}

	params := &LobbySessionParameters{
		Mode: evr.ModeSocialPublic,
	}

	// No lobby group (solo player).
	result := appendPartyReservationPlaceholders(logger, entrants, nil, params, "testnode")

	if len(result) != 1 {
		t.Errorf("Expected 1 entrant for solo player, got %d", len(result))
	}
}

func TestPartyReservation_MissingEntrantGetsPlaceholder(t *testing.T) {
	// Party of 3: leader + followerA (online) + followerB (disconnected).
	// PrepareEntrantPresences would have created presences for leader and
	// followerA but NOT followerB (session not found). The party handler
	// still lists all 3 members (followerB hasn't been removed from the
	// party yet). appendPartyReservationPlaceholders should add a
	// placeholder for followerB since they're still in the party list.
	//
	// However, if the party handler has already removed followerB from its
	// member list (because the session disconnected), no placeholder is
	// created. This test validates that only members still in
	// lobbyGroup.List() get placeholders — and members already in the
	// entrants slice are skipped.

	logger := loggerForTest(t)

	leaderSID := uuid.Must(uuid.NewV4())
	leaderUID := uuid.Must(uuid.NewV4())
	followerASID := uuid.Must(uuid.NewV4())
	followerAUID := uuid.Must(uuid.NewV4())
	followerBSID := uuid.Must(uuid.NewV4())
	followerBUID := uuid.Must(uuid.NewV4())
	partyID := uuid.Must(uuid.NewV4())

	// entrants only has leader and followerA (followerB was not in sessionRegistry).
	entrants := []*EvrMatchPresence{
		{
			SessionID: leaderSID,
			UserID:    leaderUID,
			Username:  "leader",
		},
		{
			SessionID: followerASID,
			UserID:    followerAUID,
			Username:  "followerA",
		},
	}

	params := &LobbySessionParameters{
		Mode:    evr.ModeSocialPublic,
		PartyID: partyID,
	}

	// Build a lobby group with all 3 members (including disconnected followerB).
	leaderUP := &rtapi.UserPresence{
		UserId:    leaderUID.String(),
		SessionId: leaderSID.String(),
		Username:  "leader",
	}
	ph := &PartyHandler{}
	ph.leader = &PartyLeader{
		UserPresence: leaderUP,
		PresenceID:   &PresenceID{SessionID: leaderSID, Node: "testnode"},
	}
	ph.members = NewPartyPresenceList(5)
	// Add all three members to the party.
	ph.members.Join([]*Presence{
		newPresenceWithIDs("testnode", leaderSID, leaderUID),
		newPresenceWithIDs("testnode", followerASID, followerAUID),
		newPresenceWithIDs("testnode", followerBSID, followerBUID),
	})
	lobbyGroup := &LobbyGroup{ph: ph}

	result := appendPartyReservationPlaceholders(logger, entrants, lobbyGroup, params, "testnode")

	// Should have added 1 placeholder for followerB.
	if len(result) != 3 {
		t.Errorf("Expected 3 entrants (leader + followerA + followerB placeholder), got %d", len(result))
	}

	// Verify the placeholder is for followerB.
	placeholder := result[2]
	if placeholder.SessionID != followerBSID {
		t.Errorf("Expected placeholder SessionID %s, got %s", followerBSID, placeholder.SessionID)
	}
	if placeholder.UserID != followerBUID {
		t.Errorf("Expected placeholder UserID %s, got %s", followerBUID, placeholder.UserID)
	}
	if placeholder.RoleAlignment != evr.TeamSocial {
		t.Errorf("Expected placeholder RoleAlignment %d (TeamSocial), got %d", evr.TeamSocial, placeholder.RoleAlignment)
	}
	if placeholder.PartyID != partyID {
		t.Errorf("Expected placeholder PartyID %s, got %s", partyID, placeholder.PartyID)
	}
}

// ===========================================================================
// Bug: Infinite matchmaking when leader is in a closed/full non-social match
// ===========================================================================
//
// Scenario: Party leader is in an active arena game (closed, full).
// The follower initiates lobby find. TryFollowPartyLeader returns false
// (match is full, follower is at main menu). pollFollowPartyLeader is called.
//
// In pollFollowPartyLeader, the leader's match is looked up via MatchLabelByID.
// The match is closed/full, so line 963 hits `continue` — looping forever.
// The poll only exits when the context times out (matchmaking timeout),
// then returns false. lobbyFind returns ServerIsLocked, the client retries,
// and the cycle repeats: infinite matchmaking.
//
// The follower should be sent to a social lobby, not left polling forever
// for a match that will never open.

func TestPoll_LeaderInClosedArenaMatch_ShouldNotLoopForever(t *testing.T) {
	env := newFollowTestEnv(t)

	arenaMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	// Leader is in an active arena game, not matchmaking.
	env.setLeaderMatch(arenaMatchID)

	// Follower is in a different match (social lobby) or no match.
	socialLobbyID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setFollowerMatch(socialLobbyID)
	env.params.CurrentMatchID = socialLobbyID

	// Mock the match registry to return a closed, full arena match.
	registry := newMockFollowMatchRegistry()
	groupID := env.groupID
	// Populate Players so GetPlayerCount() returns 8 (OpenPlayerSlots uses
	// GetPlayerCount, not the PlayerCount field).
	fullPlayers := make([]PlayerInfo, 8)
	for i := range fullPlayers {
		fullPlayers[i] = PlayerInfo{UserID: uuid.Must(uuid.NewV4()).String(), Team: 0}
	}
	registry.SetMatch(arenaMatchID, &MatchLabel{
		ID:          arenaMatchID,
		Open:        false, // Match is closed (game in progress)
		Mode:        evr.ModeArenaPublic,
		PlayerLimit: 8,
		Players:     fullPlayers,
		GroupID:     &groupID,
	})
	env.withMockNK(registry)

	// One poll cycle is ~6s (3s poll + 3s settle). After 1 cycle of seeing
	// a non-joinable non-social match, pollFollowPartyLeader should give up.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	start := time.Now()
	result, timedOut := env.runPollWithTimeout(ctx, t, 30*time.Second)
	elapsed := time.Since(start)

	if timedOut {
		t.Fatalf("BUG: pollFollowPartyLeader hung for the entire timeout (%v). "+
			"When the leader is in a closed/full arena match, the follower should "+
			"be released to find a social lobby, not poll forever.", elapsed)
	}

	if result {
		t.Error("Expected false (follower cannot join leader's closed match)")
	}

	if elapsed > 22*time.Second {
		t.Errorf("pollFollowPartyLeader took %v — should return within ~18s (three poll cycles).", elapsed)
	}
}

func TestPoll_LeaderInFullCombatMatch_ShouldNotLoopForever(t *testing.T) {
	env := newFollowTestEnv(t)

	combatMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	env.setLeaderMatch(combatMatchID)
	// Follower at main menu (no current match).
	env.params.CurrentMatchID = MatchID{} // nil/zero

	registry := newMockFollowMatchRegistry()
	groupID := env.groupID
	fullPlayers := make([]PlayerInfo, 8)
	for i := range fullPlayers {
		fullPlayers[i] = PlayerInfo{UserID: uuid.Must(uuid.NewV4()).String(), Team: 0}
	}
	registry.SetMatch(combatMatchID, &MatchLabel{
		ID:          combatMatchID,
		Open:        true, // Match is "open" but...
		Mode:        evr.ModeCombatPublic,
		PlayerLimit: 8,
		Players:     fullPlayers, // ...no slots available
		GroupID:     &groupID,
	})
	env.withMockNK(registry)

	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()

	start := time.Now()
	result, timedOut := env.runPollWithTimeout(ctx, t, 30*time.Second)
	elapsed := time.Since(start)

	if timedOut {
		t.Fatalf("BUG: pollFollowPartyLeader hung for the entire timeout (%v). "+
			"When the leader is in a full combat match, the follower should "+
			"be released promptly, not poll forever.", elapsed)
	}

	if result {
		t.Error("Expected false (follower cannot join leader's full match)")
	}

	if elapsed > 22*time.Second {
		t.Errorf("pollFollowPartyLeader took %v — should return within ~18s (three poll cycles).", elapsed)
	}
}

// ---------------------------------------------------------------------------
// KC-1: ModeSocialPrivate bypass via party follow
//
// A leader in a ModeSocialPrivate lobby must NOT be joinable via the party
// follow path (TryFollowPartyLeader / pollFollowPartyLeader). Private lobbies
// require explicit invitation; the follow mechanism must not override that gate.
// ---------------------------------------------------------------------------

func TestKC1_TryFollowPartyLeader_RefusesPrivateSocialLobby(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	logger := loggerForTest(t)

	privateMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	groupID := env.groupID

	registry := newMockFollowMatchRegistry()
	registry.SetMatch(privateMatchID, &MatchLabel{
		ID:          privateMatchID,
		Open:        true,
		Mode:        evr.ModeSocialPrivate,
		PlayerLimit: 12,
		GroupID:     &groupID,
	})
	env.withMockNK(registry)
	env.setLeaderMatch(privateMatchID)

	result := env.pipeline.TryFollowPartyLeader(
		context.Background(), logger, env.session, env.params, env.lobbyGroup)

	if result {
		t.Errorf("KC-1 SECURITY: TryFollowPartyLeader returned true for ModeSocialPrivate lobby. " +
			"Party follow must not bypass the private-lobby invitation gate.")
	}
}

func TestKC1_PollFollowPartyLeader_RefusesPrivateSocialLobby(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)

	privateMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	groupID := env.groupID

	registry := newMockFollowMatchRegistry()
	registry.SetMatch(privateMatchID, &MatchLabel{
		ID:          privateMatchID,
		Open:        true,
		Mode:        evr.ModeSocialPrivate,
		PlayerLimit: 12,
		GroupID:     &groupID,
	})
	env.withMockNK(registry)
	env.setLeaderMatch(privateMatchID)

	// Poll loop has two 3s waits per cycle (tick + settle): give 10s for one cycle.
	result, timedOut := env.runPollWithTimeout(context.Background(), t, 10*time.Second)

	if timedOut {
		t.Fatal("KC-1: pollFollowPartyLeader hung — should return false within one poll cycle (~6s)")
	}
	if result {
		t.Errorf("KC-1 SECURITY: pollFollowPartyLeader returned true for ModeSocialPrivate lobby. " +
			"Party follow must not bypass the private-lobby invitation gate.")
	}
}

// ---------------------------------------------------------------------------
// Registry-error fallback: when MatchLabelByID returns an error but both
// tracker presences point to the same match, isFollowerInLeaderMatch must
// accept tracker-based evidence rather than returning false (which would
// trigger a desync). See decision matrix: NK error→tracker → true.
// ---------------------------------------------------------------------------

func TestPoll_RegistryError_FallsBackToTracker(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	// Wire a mock NK but do NOT register matchB in it.
	// GetMatch will return ErrMatchNotFound — simulating a transient registry miss.
	registry := newMockFollowMatchRegistry()
	env.withMockNK(registry)
	env.setLeaderMatch(matchB)
	env.setFollowerMatch(matchB)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, timedOut := env.runPollWithTimeout(ctx, t, 8*time.Second)
	if timedOut {
		t.Fatal("pollFollowPartyLeader timed out — expected tracker fallback to return true immediately")
	}
	if !result {
		t.Error("pollFollowPartyLeader returned false when tracker shows convergence and " +
			"MatchLabelByID returned an error. The tracker-based fallback must accept " +
			"matching service stream presences as sufficient evidence of convergence.")
	}

	// Verify that GetMatch was called (confirming the fallback was triggered,
	// not that the early-convergence path returned before any registry lookup).
	if registry.getMatchCalls.Load() == 0 {
		t.Error("Expected MatchLabelByID (GetMatch) to be called before the tracker fallback " +
			"accepted convergence, but the registry was never queried.")
	}
}

// ===========================================================================
// Arena/Combat follow-path gate: the follow path (TryFollowPartyLeader /
// pollFollowPartyLeader) must be restricted to social lobby convergence.
// When the leader is in an Arena or Combat match, the follow path must not
// be entered. Instead, followers should go to a social lobby and wait.
// ===========================================================================

// TestIsLeaderInArenaCombatMatch_ArenaPublic verifies that
// isLeaderInArenaCombatMatch returns true when the leader is in ModeArenaPublic.
func TestIsLeaderInArenaCombatMatch_ArenaPublic(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	arenaMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	registry := newMockFollowMatchRegistry()
	groupID := env.groupID
	registry.SetMatch(arenaMatchID, &MatchLabel{
		ID:          arenaMatchID,
		Mode:        evr.ModeArenaPublic,
		Open:        true,
		PlayerLimit: 8,
		GroupID:     &groupID,
	})
	env.withMockNK(registry)
	env.setLeaderMatch(arenaMatchID)

	logger := loggerForTest(t)
	result := env.pipeline.isLeaderInArenaCombatMatch(
		context.Background(), logger, env.session, env.params, env.lobbyGroup)

	if !result {
		t.Error("Expected true when leader is in ModeArenaPublic match")
	}
}

// TestIsLeaderInArenaCombatMatch_CombatPublic verifies that
// isLeaderInArenaCombatMatch returns true when the leader is in ModeCombatPublic.
func TestIsLeaderInArenaCombatMatch_CombatPublic(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	combatMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	registry := newMockFollowMatchRegistry()
	groupID := env.groupID
	registry.SetMatch(combatMatchID, &MatchLabel{
		ID:          combatMatchID,
		Mode:        evr.ModeCombatPublic,
		Open:        true,
		PlayerLimit: 8,
		GroupID:     &groupID,
	})
	env.withMockNK(registry)
	env.setLeaderMatch(combatMatchID)

	logger := loggerForTest(t)
	result := env.pipeline.isLeaderInArenaCombatMatch(
		context.Background(), logger, env.session, env.params, env.lobbyGroup)

	if !result {
		t.Error("Expected true when leader is in ModeCombatPublic match")
	}
}

// TestIsLeaderInArenaCombatMatch_SocialPublic verifies that
// isLeaderInArenaCombatMatch returns false when the leader is in ModeSocialPublic.
// Social mode should still use the follow path.
func TestIsLeaderInArenaCombatMatch_SocialPublic(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	socialMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	registry := newMockFollowMatchRegistry()
	groupID := env.groupID
	registry.SetMatch(socialMatchID, &MatchLabel{
		ID:          socialMatchID,
		Mode:        evr.ModeSocialPublic,
		Open:        true,
		PlayerLimit: 12,
		GroupID:     &groupID,
	})
	env.withMockNK(registry)
	env.setLeaderMatch(socialMatchID)

	logger := loggerForTest(t)
	result := env.pipeline.isLeaderInArenaCombatMatch(
		context.Background(), logger, env.session, env.params, env.lobbyGroup)

	if result {
		t.Error("Expected false when leader is in ModeSocialPublic — social follow path should be allowed")
	}
}

// TestIsLeaderInArenaCombatMatch_PrivateModes verifies that
// isLeaderInArenaCombatMatch returns true for private arena/combat modes.
func TestIsLeaderInArenaCombatMatch_PrivateModes(t *testing.T) {
	t.Parallel()

	for _, mode := range []evr.Symbol{evr.ModeArenaPrivate, evr.ModeCombatPrivate} {
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()

			env := newFollowTestEnv(t)
			matchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

			registry := newMockFollowMatchRegistry()
			groupID := env.groupID
			registry.SetMatch(matchID, &MatchLabel{
				ID:          matchID,
				Mode:        mode,
				Open:        true,
				PlayerLimit: 8,
				GroupID:     &groupID,
			})
			env.withMockNK(registry)
			env.setLeaderMatch(matchID)

			logger := loggerForTest(t)
			result := env.pipeline.isLeaderInArenaCombatMatch(
				context.Background(), logger, env.session, env.params, env.lobbyGroup)

			if !result {
				t.Errorf("Expected true when leader is in %s match", mode.String())
			}
		})
	}
}

// TestIsLeaderInArenaCombatMatch_LeaderMatchmaking verifies that
// isLeaderInArenaCombatMatch returns false when the leader is actively
// matchmaking, regardless of their current match. When the leader is
// matchmaking, the normal ticket-inclusion flow applies.
func TestIsLeaderInArenaCombatMatch_LeaderMatchmaking(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	arenaMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	registry := newMockFollowMatchRegistry()
	groupID := env.groupID
	registry.SetMatch(arenaMatchID, &MatchLabel{
		ID:          arenaMatchID,
		Mode:        evr.ModeArenaPublic,
		Open:        true,
		PlayerLimit: 8,
		GroupID:     &groupID,
	})
	env.withMockNK(registry)
	env.setLeaderMatch(arenaMatchID)
	env.setLeaderMatchmaking() // Leader is actively matchmaking

	logger := loggerForTest(t)
	result := env.pipeline.isLeaderInArenaCombatMatch(
		context.Background(), logger, env.session, env.params, env.lobbyGroup)

	if result {
		t.Error("Expected false when leader is actively matchmaking — ticket-inclusion path applies")
	}
}

// TestIsLeaderInArenaCombatMatch_NoMatch verifies that
// isLeaderInArenaCombatMatch returns false when the leader is not in any match.
func TestIsLeaderInArenaCombatMatch_NoMatch(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	// No leader match set — leader is not in any match.

	logger := loggerForTest(t)
	result := env.pipeline.isLeaderInArenaCombatMatch(
		context.Background(), logger, env.session, env.params, env.lobbyGroup)

	if result {
		t.Error("Expected false when leader is not in any match")
	}
}

// TestTryFollow_LeaderInArenaMatch_ReturnsFalse verifies the defense-in-depth
// guard in TryFollowPartyLeader: even if the lobbyFind gate fails to prevent
// entry, TryFollowPartyLeader must return false when the leader's match is
// Arena mode.
func TestTryFollow_LeaderInArenaMatch_ReturnsFalse(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	arenaMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	registry := newMockFollowMatchRegistry()
	groupID := env.groupID
	registry.SetMatch(arenaMatchID, &MatchLabel{
		ID:          arenaMatchID,
		Mode:        evr.ModeArenaPublic,
		Open:        true,
		PlayerLimit: 8,
		GroupID:     &groupID,
	})
	env.withMockNK(registry)
	env.setLeaderMatch(arenaMatchID)

	logger := loggerForTest(t)
	result := env.pipeline.TryFollowPartyLeader(
		context.Background(), logger, env.session, env.params, env.lobbyGroup)

	if result {
		t.Error("TryFollowPartyLeader should return false when leader is in ModeArenaPublic — " +
			"follow path is only for social lobby convergence")
	}
}

// TestTryFollow_LeaderInCombatMatch_ReturnsFalse verifies the defense-in-depth
// guard for Combat mode.
func TestTryFollow_LeaderInCombatMatch_ReturnsFalse(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	combatMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	registry := newMockFollowMatchRegistry()
	groupID := env.groupID
	registry.SetMatch(combatMatchID, &MatchLabel{
		ID:          combatMatchID,
		Mode:        evr.ModeCombatPublic,
		Open:        true,
		PlayerLimit: 8,
		GroupID:     &groupID,
	})
	env.withMockNK(registry)
	env.setLeaderMatch(combatMatchID)

	logger := loggerForTest(t)
	result := env.pipeline.TryFollowPartyLeader(
		context.Background(), logger, env.session, env.params, env.lobbyGroup)

	if result {
		t.Error("TryFollowPartyLeader should return false when leader is in ModeCombatPublic — " +
			"follow path is only for social lobby convergence")
	}
}

// TestTryFollow_LeaderInSocialMatch_FollowerAlreadyInMatch_ReturnsTrue
// verifies that the social follow path is preserved: when the leader is in a
// ModeSocialPublic lobby and the follower is already in it, TryFollowPartyLeader
// returns true.
func TestTryFollow_LeaderInSocialMatch_FollowerAlreadyInMatch_ReturnsTrue(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	socialMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	registry := newMockFollowMatchRegistry()
	groupID := env.groupID
	registry.SetMatch(socialMatchID, &MatchLabel{
		ID:          socialMatchID,
		Mode:        evr.ModeSocialPublic,
		Open:        true,
		PlayerLimit: 12,
		GroupID:     &groupID,
	})
	env.withMockNK(registry)
	env.setLeaderMatch(socialMatchID)
	env.setFollowerMatch(socialMatchID) // Already in the same lobby

	logger := loggerForTest(t)
	result := env.pipeline.TryFollowPartyLeader(
		context.Background(), logger, env.session, env.params, env.lobbyGroup)

	if !result {
		t.Error("TryFollowPartyLeader should return true when follower is already in leader's social lobby")
	}
}

// TestPoll_LeaderInArenaMatch_ReturnsFalseImmediately verifies the
// defense-in-depth guard in pollFollowPartyLeader: the poll must return false
// when the leader's match is Arena mode, not loop until context expiry.
func TestPoll_LeaderInArenaMatch_ReturnsFalseImmediately(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	arenaMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	registry := newMockFollowMatchRegistry()
	groupID := env.groupID
	registry.SetMatch(arenaMatchID, &MatchLabel{
		ID:          arenaMatchID,
		Mode:        evr.ModeArenaPublic,
		Open:        true,
		PlayerLimit: 8,
		GroupID:     &groupID,
	})
	env.withMockNK(registry)
	env.setLeaderMatch(arenaMatchID)
	// Follower in a different match (social lobby).
	socialMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setFollowerMatch(socialMatchID)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	start := time.Now()
	result, timedOut := env.runPollWithTimeout(ctx, t, 12*time.Second)
	elapsed := time.Since(start)

	if timedOut {
		t.Fatalf("pollFollowPartyLeader hung — should return false within one poll cycle for arena leader")
	}
	if result {
		t.Error("pollFollowPartyLeader should return false when leader is in ModeArenaPublic")
	}
	// Should exit within one poll cycle (3s poll + 3s settle = ~6s), not the full timeout.
	if elapsed > 10*time.Second {
		t.Errorf("pollFollowPartyLeader took %v — should exit promptly for arena leader", elapsed)
	}
}

// TestPoll_LeaderInCombatMatch_ReturnsFalseImmediately verifies the
// defense-in-depth guard for Combat mode in pollFollowPartyLeader.
func TestPoll_LeaderInCombatMatch_ReturnsFalseImmediately(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	combatMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	registry := newMockFollowMatchRegistry()
	groupID := env.groupID
	registry.SetMatch(combatMatchID, &MatchLabel{
		ID:          combatMatchID,
		Mode:        evr.ModeCombatPublic,
		Open:        true,
		PlayerLimit: 8,
		GroupID:     &groupID,
	})
	env.withMockNK(registry)
	env.setLeaderMatch(combatMatchID)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	start := time.Now()
	result, timedOut := env.runPollWithTimeout(ctx, t, 12*time.Second)
	elapsed := time.Since(start)

	if timedOut {
		t.Fatalf("pollFollowPartyLeader hung — should return false within one poll cycle for combat leader")
	}
	if result {
		t.Error("pollFollowPartyLeader should return false when leader is in ModeCombatPublic")
	}
	if elapsed > 10*time.Second {
		t.Errorf("pollFollowPartyLeader took %v — should exit promptly for combat leader", elapsed)
	}
}

// ---------------------------------------------------------------------------
// Nil NK + mismatch: when NK is unavailable and the follower is NOT in the
// leader's match, the poll loop must continue polling until context expiration
// rather than fast-failing. This documents that the nil NK path is not a
// short-circuit — it defers to context cancellation for loop termination.
// ---------------------------------------------------------------------------

func TestPoll_NilNK_FollowerNotInLeaderMatch_LoopsUntilContextExpiry(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	matchA := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchB)
	env.setFollowerMatch(matchA)

	// Short context timeout — if the function fast-fails, it returns before expiry.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	start := time.Now()
	result, timedOut := env.runPollWithTimeout(ctx, t, 6*time.Second)
	elapsed := time.Since(start)

	if timedOut {
		t.Fatal("pollFollowPartyLeader timed out at test level — expected context expiry to return false")
	}
	if result {
		t.Error("pollFollowPartyLeader returned true when follower and leader are in different matches")
	}
	// If the function fast-failed (nil NK guard returned false immediately),
	// elapsed would be < 100ms. It should have polled until ctx expired.
	if elapsed < 1*time.Second {
		t.Errorf("pollFollowPartyLeader returned in %v — expected it to loop until context expiry (~3s). "+
			"This suggests a fast-fail path was triggered instead of polling.", elapsed)
	}
}

// ---------------------------------------------------------------------------
// isFollowerAlreadyInLeaderMatch — issue #461 regression tests
// ---------------------------------------------------------------------------

// TestIsFollowerAlreadyInLeaderMatch_SameMatch verifies that when the follower
// and leader are in the same match, isFollowerAlreadyInLeaderMatch returns true.
// This is the fast path that prevents repeated "Joined party group" /
// "Already in leader's match" churn when the client re-sends
// LobbyFindSessionRequest on its normal message cycle.
func TestIsFollowerAlreadyInLeaderMatch_SameMatch(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	matchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	logger := loggerForTest(t)

	env.setLeaderMatch(matchID)
	env.setFollowerMatch(matchID)

	result := env.pipeline.isFollowerAlreadyInLeaderMatch(logger, env.session, env.lobbyGroup, MatchID{})
	if !result {
		t.Error("isFollowerAlreadyInLeaderMatch should return true when both are in the same match")
	}
}

// TestIsFollowerAlreadyInLeaderMatch_DifferentMatches verifies that when the
// follower and leader are in different matches, the function returns false.
func TestIsFollowerAlreadyInLeaderMatch_DifferentMatches(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	matchA := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	logger := loggerForTest(t)

	env.setLeaderMatch(matchA)
	env.setFollowerMatch(matchB)

	result := env.pipeline.isFollowerAlreadyInLeaderMatch(logger, env.session, env.lobbyGroup, MatchID{})
	if result {
		t.Error("isFollowerAlreadyInLeaderMatch should return false when in different matches")
	}
}

// TestIsFollowerAlreadyInLeaderMatch_LeaderNotInMatch verifies that when the
// leader is not in any match, the function returns false.
func TestIsFollowerAlreadyInLeaderMatch_LeaderNotInMatch(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	matchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	logger := loggerForTest(t)

	// Only follower is in a match; leader is not.
	env.setFollowerMatch(matchID)

	result := env.pipeline.isFollowerAlreadyInLeaderMatch(logger, env.session, env.lobbyGroup, MatchID{})
	if result {
		t.Error("isFollowerAlreadyInLeaderMatch should return false when leader has no match")
	}
}

// TestIsFollowerAlreadyInLeaderMatch_FollowerNotInMatch verifies that when the
// follower is not in any match, the function returns false.
func TestIsFollowerAlreadyInLeaderMatch_FollowerNotInMatch(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	matchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	logger := loggerForTest(t)

	// Only leader is in a match; follower is not.
	env.setLeaderMatch(matchID)

	result := env.pipeline.isFollowerAlreadyInLeaderMatch(logger, env.session, env.lobbyGroup, MatchID{})
	if result {
		t.Error("isFollowerAlreadyInLeaderMatch should return false when follower has no match")
	}
}

// TestIsFollowerAlreadyInLeaderMatch_NoLeader verifies that when the party has
// no leader, the function returns false gracefully.
func TestIsFollowerAlreadyInLeaderMatch_NoLeader(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	logger := loggerForTest(t)

	env.clearLeader()

	result := env.pipeline.isFollowerAlreadyInLeaderMatch(logger, env.session, env.lobbyGroup, MatchID{})
	if result {
		t.Error("isFollowerAlreadyInLeaderMatch should return false when there is no leader")
	}
}

// TestIsFollowerAlreadyInLeaderMatch_FollowerIsLeader verifies that when the
// follower is the leader (same session), the function returns false.
func TestIsFollowerAlreadyInLeaderMatch_FollowerIsLeader(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	matchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	logger := loggerForTest(t)

	// Promote the follower to leader.
	env.setLeader(env.followerSID, env.followerUID, "follower")
	env.setFollowerMatch(matchID)

	result := env.pipeline.isFollowerAlreadyInLeaderMatch(logger, env.session, env.lobbyGroup, MatchID{})
	if result {
		t.Error("isFollowerAlreadyInLeaderMatch should return false when follower is the leader")
	}
}

// TestIsFollowerAlreadyInLeaderMatch_HeartbeatDoesNotTriggerFollow verifies
// that non-LobbyFindSessionRequest messages (heartbeats, profile updates)
// do not reach the party follow path. This is a structural test that confirms
// the ProcessRequestEVR dispatch table only routes LobbyFindSessionRequest,
// LobbyCreateSessionRequest, and LobbyJoinSessionRequest to lobbySessionRequest.
func TestIsFollowerAlreadyInLeaderMatch_HeartbeatDoesNotTriggerFollow(t *testing.T) {
	t.Parallel()

	// Verify that non-lobby message types are NOT routed to lobbySessionRequest.
	// The dispatch table in ProcessRequestEVR maps message types to handler
	// functions. Only LobbyFindSessionRequest, LobbyCreateSessionRequest, and
	// LobbyJoinSessionRequest should reach lobbySessionRequest (and from there,
	// lobbyFind). Other message types like GenericMessage, UpdateClientProfile,
	// RemoteLogSet, etc. are routed to their own handlers that do not call
	// lobbyFind or any party follow logic.

	// This test validates the contract by checking that the message types that
	// could be confused as "heartbeat" or "routine client traffic" do not
	// implement the LobbySessionRequest interface, which is the gate in
	// lobbySessionRequest (line 38 of evr_pipeline_matchmaker.go).
	nonLobbyMessages := []evr.Message{
		&evr.GenericMessage{},
		&evr.UpdateClientProfile{},
		&evr.RemoteLogSet{},
		&evr.ConfigRequest{},
		&evr.DocumentRequest{},
		&evr.LoggedInUserProfileRequest{},
		&evr.ChannelInfoRequest{},
		&evr.OtherUserProfileRequest{},
		&evr.LobbyMatchmakerStatusRequest{},
		&evr.LobbyPingResponse{},
		&evr.LobbyPendingSessionCancel{},
	}

	for _, msg := range nonLobbyMessages {
		if _, ok := msg.(evr.LobbySessionRequest); ok {
			t.Errorf("%T implements LobbySessionRequest — it would reach "+
				"lobbySessionRequest and potentially trigger the follow path", msg)
		}
	}
}

// ---------------------------------------------------------------------------
// Tests for currentSocialLobbyForSession (#462)
// ---------------------------------------------------------------------------

// TestCurrentSocialLobby_AlreadyInMatchingSocialLobby verifies that when a
// player is in a social lobby with the same group ID, the function returns
// the current match ID (no-op fast path).
func TestCurrentSocialLobby_AlreadyInMatchingSocialLobby(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	socialMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	registry := newMockFollowMatchRegistry()
	groupID := env.groupID
	registry.SetMatch(socialMatchID, &MatchLabel{
		ID:          socialMatchID,
		Mode:        evr.ModeSocialPublic,
		Open:        true,
		PlayerLimit: 12,
		GroupID:     &groupID,
	})
	env.withMockNK(registry)
	env.setFollowerMatch(socialMatchID)
	env.params.GroupID = groupID
	// No relocation requested and no party-follow context: the intended target
	// is the player's CurrentMatchID, which equals the lobby they are in. This
	// is the originally-reported rejoin-same-lobby case (#462) — must stay a no-op.
	env.params.CurrentMatchID = socialMatchID

	logger := loggerForTest(t)
	result := env.pipeline.currentSocialLobbyForSession(
		context.Background(), logger, env.session, env.params, env.lobbyGroup)

	if result.IsNil() {
		t.Error("currentSocialLobbyForSession should return the match ID when player is already in a matching social lobby")
	}
	if result != socialMatchID {
		t.Errorf("expected match ID %s, got %s", socialMatchID, result)
	}
}

// TestCurrentSocialLobby_DifferentLobby_ReturnsNil verifies that when a
// player needs to join a different lobby (different group), the function
// returns nil so the normal find-or-create proceeds.
func TestCurrentSocialLobby_DifferentLobby_ReturnsNil(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	socialMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	registry := newMockFollowMatchRegistry()
	differentGroupID := uuid.Must(uuid.NewV4())
	registry.SetMatch(socialMatchID, &MatchLabel{
		ID:          socialMatchID,
		Mode:        evr.ModeSocialPublic,
		Open:        true,
		PlayerLimit: 12,
		GroupID:     &differentGroupID,
	})
	env.withMockNK(registry)
	env.setFollowerMatch(socialMatchID)
	// Searching for a different group.
	env.params.GroupID = uuid.Must(uuid.NewV4())

	logger := loggerForTest(t)
	result := env.pipeline.currentSocialLobbyForSession(
		context.Background(), logger, env.session, env.params, env.lobbyGroup)

	if !result.IsNil() {
		t.Error("currentSocialLobbyForSession should return nil when player is in a lobby with a different group ID")
	}
}

// TestCurrentSocialLobby_InArenaMatch_ReturnsNil verifies that a player
// in an arena match is not treated as already in a social lobby.
func TestCurrentSocialLobby_InArenaMatch_ReturnsNil(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	arenaMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	registry := newMockFollowMatchRegistry()
	groupID := env.groupID
	registry.SetMatch(arenaMatchID, &MatchLabel{
		ID:          arenaMatchID,
		Mode:        evr.ModeArenaPublic,
		Open:        true,
		PlayerLimit: 8,
		GroupID:     &groupID,
	})
	env.withMockNK(registry)
	env.setFollowerMatch(arenaMatchID)
	env.params.GroupID = groupID

	logger := loggerForTest(t)
	result := env.pipeline.currentSocialLobbyForSession(
		context.Background(), logger, env.session, env.params, env.lobbyGroup)

	if !result.IsNil() {
		t.Error("currentSocialLobbyForSession should return nil when player is in an arena match")
	}
}

// TestCurrentSocialLobby_NotInAnyMatch_ReturnsNil verifies that a player
// who is not in any match gets nil (proceed with find-or-create).
func TestCurrentSocialLobby_NotInAnyMatch_ReturnsNil(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	registry := newMockFollowMatchRegistry()
	env.withMockNK(registry)
	// Do NOT call setFollowerMatch — player has no match presence.
	env.params.GroupID = env.groupID

	logger := loggerForTest(t)
	result := env.pipeline.currentSocialLobbyForSession(
		context.Background(), logger, env.session, env.params, env.lobbyGroup)

	if !result.IsNil() {
		t.Error("currentSocialLobbyForSession should return nil when player is not in any match")
	}
}

// TestCurrentSocialLobby_MoveToDifferentLobbySameGuild_ReturnsNil covers GAP 1
// (#462): a player already in social lobby X of guild G who is being directed
// (via the party-follow path) to a DIFFERENT social lobby Y of the SAME guild G
// must NOT be treated as a no-op. The guard must be target-aware: it may only
// short-circuit when the player's current social lobby equals the intended
// target (here, the leader's lobby Y), not merely when the group IDs match.
func TestCurrentSocialLobby_MoveToDifferentLobbySameGuild_ReturnsNil(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	groupID := env.groupID

	// Player (follower) is currently in social lobby X of guild G.
	lobbyX := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	// Party leader is in a DIFFERENT social lobby Y of the SAME guild G —
	// this is the intended target.
	lobbyY := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	registry := newMockFollowMatchRegistry()
	registry.SetMatch(lobbyX, &MatchLabel{
		ID:          lobbyX,
		Mode:        evr.ModeSocialPublic,
		Open:        true,
		PlayerLimit: 12,
		GroupID:     &groupID,
	})
	registry.SetMatch(lobbyY, &MatchLabel{
		ID:          lobbyY,
		Mode:        evr.ModeSocialPublic,
		Open:        true,
		PlayerLimit: 12,
		GroupID:     &groupID,
	})
	env.withMockNK(registry)

	env.setFollowerMatch(lobbyX)
	env.setLeaderMatch(lobbyY)
	env.params.GroupID = groupID
	env.params.PartyGroupName = "squad" // party-follow context
	// Follower's CurrentMatchID is their current lobby X; the intended target
	// is the leader's lobby Y. These differ, so this is a real move, not a no-op.
	env.params.CurrentMatchID = lobbyX

	logger := loggerForTest(t)
	result := env.pipeline.currentSocialLobbyForSession(
		context.Background(), logger, env.session, env.params, env.lobbyGroup)

	if !result.IsNil() {
		t.Errorf("GAP 1: moving from social lobby X to a DIFFERENT social lobby Y of the same guild must NOT be a no-op; got match %s", result)
	}
}

// TestCurrentSocialLobby_ForcedRelocation_ReturnsNil covers GAP 2 (#462): the
// relocate path clears lobbyParams.CurrentMatchID to force a move to a larger
// social lobby. The guard must respect that relocation intent and NOT no-op the
// requested move, even though the player is still tracked in their old social
// lobby of the same guild.
func TestCurrentSocialLobby_ForcedRelocation_ReturnsNil(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	groupID := env.groupID

	// Player is still in their (too-small) social lobby of guild G.
	oldLobby := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	registry := newMockFollowMatchRegistry()
	registry.SetMatch(oldLobby, &MatchLabel{
		ID:          oldLobby,
		Mode:        evr.ModeSocialPublic,
		Open:        true,
		PlayerLimit: 12,
		GroupID:     &groupID,
	})
	env.withMockNK(registry)

	env.setFollowerMatch(oldLobby)
	env.params.GroupID = groupID
	// Relocate path (evr_lobby_find.go:371) cleared CurrentMatchID to signal
	// "find/create a different, larger lobby". No party-follow leader target.
	env.params.CurrentMatchID = MatchID{}

	logger := loggerForTest(t)
	result := env.pipeline.currentSocialLobbyForSession(
		context.Background(), logger, env.session, env.params, env.lobbyGroup)

	if !result.IsNil() {
		t.Errorf("GAP 2: a forced relocation (cleared CurrentMatchID) must NOT be short-circuited as a no-op; got match %s", result)
	}
}

// TestCurrentSocialLobby_FollowToLeaderSameLobby_IsNoop verifies the
// originally-reported case in the party-follow context: the follower is
// directed to the leader's social lobby, which is the SAME lobby the follower
// is already in. This must stay a no-op (return the current match ID).
func TestCurrentSocialLobby_FollowToLeaderSameLobby_IsNoop(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	groupID := env.groupID

	sharedLobby := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	registry := newMockFollowMatchRegistry()
	registry.SetMatch(sharedLobby, &MatchLabel{
		ID:          sharedLobby,
		Mode:        evr.ModeSocialPublic,
		Open:        true,
		PlayerLimit: 12,
		GroupID:     &groupID,
	})
	env.withMockNK(registry)

	env.setFollowerMatch(sharedLobby)
	env.setLeaderMatch(sharedLobby)
	env.params.GroupID = groupID
	env.params.PartyGroupName = "squad"
	env.params.CurrentMatchID = sharedLobby

	logger := loggerForTest(t)
	result := env.pipeline.currentSocialLobbyForSession(
		context.Background(), logger, env.session, env.params, env.lobbyGroup)

	if result != sharedLobby {
		t.Errorf("rejoin of the leader's (same) social lobby must remain a no-op; expected %s, got %s", sharedLobby, result)
	}
}

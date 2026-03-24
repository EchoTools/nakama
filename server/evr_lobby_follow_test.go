package server

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
)

// ---------------------------------------------------------------------------
// Test helpers: reusable setup for TryFollowPartyLeader / pollFollowPartyLeader
// ---------------------------------------------------------------------------

type followTestEnv struct {
	leaderSID  uuid.UUID
	leaderUID  uuid.UUID
	followerSID uuid.UUID
	followerUID uuid.UUID
	groupID    uuid.UUID
	tracker    *mockMatchmakingTracker
	pipeline   *EvrPipeline
	session    *sessionWS
	lobbyGroup *LobbyGroup
	ph         *PartyHandler
	params     *LobbySessionParameters
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

	ph := &PartyHandler{}
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
		leaderSID:  leaderSID,
		leaderUID:  leaderUID,
		followerSID: followerSID,
		followerUID: followerUID,
		groupID:    groupID,
		tracker:    tracker,
		pipeline:   pipeline,
		session:    session,
		lobbyGroup: lobbyGroup,
		ph:         ph,
		params:     params,
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

func TestTryFollow_FollowerInDifferentMatch_ReachesMatchValidation(t *testing.T) {
	// Leader is in Match B, follower is in Match A (different match).
	// TryFollowPartyLeader should NOT return true (follower != leader match)
	// and should proceed to match validation (MatchLabelByID). Since we don't
	// mock nk, this will panic — which confirms the path is reached and
	// validates that all earlier checks pass correctly.
	env := newFollowTestEnv(t)
	matchA := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchB)
	env.setFollowerMatch(matchA)

	logger := loggerForTest(t)

	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic from nil nk (MatchLabelByID), indicating the " +
				"match validation path was reached — all tracker-based checks passed")
		}
	}()

	env.pipeline.TryFollowPartyLeader(context.Background(), logger, env.session, env.params, env.lobbyGroup)
	t.Error("Should not reach here — expected panic")
}

func TestTryFollow_FollowerNoMatchPresence_ReachesMatchValidation(t *testing.T) {
	// Leader is in Match B, follower has no service stream at all (at main menu).
	// The memberPresence check (line 632) gets nil, so the "already in match"
	// branch is skipped. Proceeds to MatchLabelByID validation.
	env := newFollowTestEnv(t)
	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchB)
	// No follower service stream set.

	logger := loggerForTest(t)

	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic from nil nk (MatchLabelByID), indicating the " +
				"'follower not in any match' path reached match validation")
		}
	}()

	env.pipeline.TryFollowPartyLeader(context.Background(), logger, env.session, env.params, env.lobbyGroup)
	t.Error("Should not reach here — expected panic")
}

// ===========================================================================
// pollFollowPartyLeader — regression tests for every branch/return path
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
	env := newFollowTestEnv(t)

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
	env := newFollowTestEnv(t)

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

func TestPoll_FollowerNotInLeaderMatch_ReachesLabelLookup(t *testing.T) {
	// After the settle period, the follower is NOT in the leader's match.
	// The poll should reach the MatchLabelByID call (to check if joinable).
	// Since we don't mock nk, this will panic — confirming the path is reached.
	env := newFollowTestEnv(t)
	logger := loggerForTest(t)

	matchB := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	matchA := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(matchB)
	env.setFollowerMatch(matchA) // Different match

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	panicked := make(chan bool, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				panicked <- true
			} else {
				panicked <- false
			}
		}()
		env.pipeline.pollFollowPartyLeader(ctx, logger, env.session, env.params, env.lobbyGroup)
	}()

	select {
	case didPanic := <-panicked:
		if !didPanic {
			t.Error("Expected panic from nil nk when reaching MatchLabelByID — " +
				"follower not in leader's match should trigger join attempt")
		}
	case <-time.After(12 * time.Second):
		cancel()
		t.Fatal("pollFollowPartyLeader neither returned nor panicked within timeout")
	}
}

func TestPoll_LeaderChangesPartway_NewLeaderInMatch_ReturnsTrue(t *testing.T) {
	// Original leader is matchmaking. A third player becomes leader during
	// the poll and is already in a match. The follower is also in that match.
	// Poll should detect the new leader's match.
	env := newFollowTestEnv(t)

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
//   T=0s: Follower enters pollFollowPartyLeader (3s poll interval)
//   T=1s: Matchmaker places both players → removes matchmaking streams,
//          updates service streams to Match B
//   T=2s: Monitor detects matchmaking presence gone, starts 1s grace period
//   T=3s: Monitor grace expires → cancels context
//   T=3s: pollFollowPartyLeader wakes up, sees ctx.Done() → returns false
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

	if consecutiveFailures == maxRetries {
		t.Errorf("DUO DESYNC RETRY LOOP: All %d consecutive lobbyFind attempts failed. "+
			"The follower is stuck in a persistent matchmaking loop because the "+
			"matchmaking monitor cancels the context on every attempt before "+
			"pollFollowPartyLeader can detect the successful match placement.",
			maxRetries)
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

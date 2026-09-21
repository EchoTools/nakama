package server

import (
	"context"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// Regression tests for two follow-skip bugs that ended a party member's lobby
// find without a join when their leader queued (EchoTools/nakama#620).

func followSkipObservedLogger() (*zap.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(zapcore.DebugLevel)
	return zap.New(core), logs
}

// Bug B: the follow-skip ignored the requested mode.
//
// Member and leader are both in the leader's social lobby; the client reports
// that lobby as CurrentMatchID and requests echo_arena. lobbyFind's fast path
// (lobbyFind) consulted isFollowerAlreadyInLeaderMatch, which only
// looked at the SHARED match's mode (social -> "already converged") and never
// at the REQUESTED mode. lobbyFind logged "Follower already in leader's match,
// skipping follow path" and returned nil before the matchmaking timeout was
// armed, so the find ended without a join.
//
// Correct behaviour: a member requesting arena from a shared social lobby is
// NOT already where it asked to go; lobbyFind must not take the skip path.
func TestLobbyFind_FollowerInLeaderSocial_RequestingArena_DoesNotSkip(t *testing.T) {
	logger, logs := followSkipObservedLogger()

	tracker := newMockMatchmakingTracker()
	mm, mmCleanup := createLightMatchmaker(t, loggerForTest(t))
	defer mmCleanup()
	pr := NewLocalPartyRegistry(loggerForTest(t), cfg, mm, tracker, testStreamManager{}, &DummyMessageRouter{}, "testnode")

	groupName := "follow-skip-b"
	groupID := uuid.Must(uuid.NewV4())
	socialMatch := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	// Leader creates (and leads) the party.
	leaderSession := newTestSessionForParty(t, "leader", tracker, pr)
	leaderGroup, _, err := JoinPartyGroup(leaderSession, groupName, socialMatch)
	if err != nil {
		t.Fatalf("leader JoinPartyGroup: %v", err)
	}
	if got := leaderGroup.GetLeader().GetSessionId(); got != leaderSession.id.String() {
		t.Fatalf("fixture: expected leader session to lead party, leader=%s", got)
	}

	followerSession := newTestSessionForParty(t, "follower", tracker, pr)

	// Both are in the SAME social lobby per the tracker (service stream).
	tracker.Track(context.Background(), leaderSession.id,
		PresenceStream{Mode: StreamModeService, Subject: leaderSession.id, Label: StreamLabelMatchService},
		leaderSession.userID, PresenceMeta{Status: socialMatch.String()})
	tracker.Track(context.Background(), followerSession.id,
		PresenceStream{Mode: StreamModeService, Subject: followerSession.id, Label: StreamLabelMatchService},
		followerSession.userID, PresenceMeta{Status: socialMatch.String()})

	registry := newMockFollowMatchRegistry()
	registry.SetMatch(socialMatch, &MatchLabel{
		ID:          socialMatch,
		Mode:        evr.ModeSocialPublic,
		Open:        true,
		PlayerLimit: 12,
	})

	pipeline := newFollowPollPipeline()
	pipeline.node = "testnode"
	pipeline.nk = &RuntimeGoNakamaModule{
		logger:        loggerForTest(t),
		matchRegistry: registry,
		partyRegistry: pr,
		tracker:       tracker,
		metrics:       &testMetrics{},
		node:          "testnode",
	}

	params := makeMatchmakeTestLobbyParams(followerSession.userID, groupID, evr.ModeArenaPublic, 2)
	params.PartyGroupName = groupName
	params.CurrentMatchID = socialMatch // client reports the social lobby it is in

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Past the fast path, lobbyAuthorize fails on this test pipeline (no
	// session parameters) before any goroutine is started. Only whether the
	// skip path was taken matters here.
	findErr := pipeline.lobbyFind(ctx, logger, followerSession, params)

	skipped := logs.FilterMessage("Follower already in leader's match, skipping follow path").Len()
	if skipped > 0 {
		t.Errorf("lobbyFind took the follower skip path for a member REQUESTING %s while sharing the leader's SOCIAL match %s "+
			"(returned err=%v before the matchmaking timeout was armed); the skip must depend on the requested mode, "+
			"not only on the shared match's mode", params.Mode.String(), socialMatch.String(), findErr)
	}
	if logs.FilterMessage("Follower and leader share a social lobby, but follower requested another mode, not skipping").Len() != 1 {
		t.Errorf("expected isFollowerAlreadyInLeaderMatch to decline the skip because of the requested mode")
	}
	// The leader here is not queueing, so lobbyFind goes on to the existing
	// heading-to-social rule. What follows is covered by
	// TestFollowerInLeaderSocial_RequestingArena_LeaderIdle_StaysInLobby.
	if logs.FilterMessage("Leader is heading to a social lobby, forcing social mode for follower").Len() != 1 {
		t.Errorf("expected lobbyFind to reach the heading-to-social rule past the fast path")
	}
}

// followSkipJoinAttempts counts every log line that precedes a lobbyJoin on
// the follow paths. A join on the social lobby the member is already in is the
// da785b895 snap-back (a duplicate-join BAD REQUEST).
func followSkipJoinAttempts(logs *observer.ObservedLogs) int {
	return logs.FilterMessage("Joining leader's lobby").Len() +
		logs.FilterMessage("Joining leader's social lobby during poll").Len()
}

// followSkipSharedSocialEnv puts the leader and the member in one social lobby,
// with the member reporting it as current and requesting mode.
func followSkipSharedSocialEnv(t *testing.T, mode evr.Symbol) (*followTestEnv, MatchID) {
	env := newFollowTestEnv(t)
	socialLobby := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(socialLobby)
	env.setFollowerMatch(socialLobby)

	registry := newMockFollowMatchRegistry()
	registry.SetMatch(socialLobby, &MatchLabel{
		ID:          socialLobby,
		Mode:        evr.ModeSocialPublic,
		Open:        true,
		PlayerLimit: 12,
		Players: []PlayerInfo{
			{UserID: env.followerUID.String(), Team: 0},
			{UserID: env.leaderUID.String(), Team: 0},
		},
	})
	env.withMockNK(registry)

	env.params.Mode = mode
	env.params.CurrentMatchID = socialLobby
	return env, socialLobby
}

// Bug B, where the member goes after the fix when the leader IS queueing (the
// #620 production case). The member's lobbyFind now arms the matchmaking
// timeout, joins the matchmaking stream and runs the late-arrival check, so
// the leader's ticket is rebuilt to include it. Then TryFollowPartyLeader stops
// at "Leader is currently matchmaking" (it does not follow the leader's old
// lobby), and pollFollowPartyLeader finds the member already standing in the
// leader's lobby. The member waits there for the leader's ticket and is never
// re-joined to the lobby it is in.
func TestFollowerInLeaderSocial_RequestingArena_LeaderQueueing_WaitsOnTicket(t *testing.T) {
	logger, logs := followSkipObservedLogger()
	env, _ := followSkipSharedSocialEnv(t, evr.ModeArenaPublic)

	leaderParams := LobbySessionParameters{GroupID: env.groupID, Mode: evr.ModeArenaPublic}
	env.tracker.Track(context.Background(), env.leaderSID,
		PresenceStream{Mode: StreamModeMatchmaking, Subject: env.groupID},
		env.leaderUID, PresenceMeta{Status: leaderParams.String()})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if env.pipeline.isFollowerAlreadyInLeaderMatch(ctx, logger, env.session, env.lobbyGroup, env.params.CurrentMatchID, env.params.Mode) {
		t.Fatal("isFollowerAlreadyInLeaderMatch skipped a member requesting arena from the leader's social lobby")
	}
	if env.pipeline.isLeaderHeadingToSocial(ctx, logger, env.session, env.params, env.lobbyGroup) {
		t.Fatal("isLeaderHeadingToSocial forced social mode although the leader is queueing for arena")
	}
	if env.pipeline.TryFollowPartyLeader(ctx, logger, env.session, env.params, env.lobbyGroup) {
		t.Fatal("TryFollowPartyLeader returned true while the leader is queueing")
	}
	if logs.FilterMessage("Leader is currently matchmaking, falling through").Len() != 1 {
		t.Error("expected TryFollowPartyLeader to stop at the leader-matchmaking check")
	}
	if logs.FilterMessage("Already in leader's match").Len() != 0 {
		t.Error("TryFollowPartyLeader reported \"Already in leader's match\" for a member queueing for arena")
	}

	if !env.pipeline.pollFollowPartyLeader(ctx, logger, env.session, env.params, env.lobbyGroup) {
		t.Fatal("pollFollowPartyLeader did not find the member in the leader's lobby")
	}
	if logs.FilterMessage("Follower already in leader's match, poll returning success").Len() != 1 {
		t.Error("expected the poll to end on its convergence check")
	}
	if n := followSkipJoinAttempts(logs); n != 0 {
		t.Errorf("snap-back: %d lobbyJoin attempt(s) on the social lobby the member is already in", n)
	}
}

// Bug B, where the member goes after the fix when the leader is NOT yet
// queueing (the member's find lands first). The existing heading-to-social
// rule rewrites the request to social, and the member is already in the
// leader's social lobby, so TryFollowPartyLeader ends at "Already in leader's
// match" without a join. The leader's own find, when it arrives, builds the
// ticket from the party, which includes this member.
func TestFollowerInLeaderSocial_RequestingArena_LeaderIdle_StaysInLobby(t *testing.T) {
	logger, logs := followSkipObservedLogger()
	env, _ := followSkipSharedSocialEnv(t, evr.ModeArenaPublic)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if !env.pipeline.isLeaderHeadingToSocial(ctx, logger, env.session, env.params, env.lobbyGroup) {
		t.Fatal("expected isLeaderHeadingToSocial to be true for a leader idle in a social lobby")
	}
	env.params.Mode = evr.ModeSocialPublic // what lobbyFind does when heading to social

	if !env.pipeline.TryFollowPartyLeader(ctx, logger, env.session, env.params, env.lobbyGroup) {
		t.Fatal("TryFollowPartyLeader returned false for a member already in the leader's social lobby")
	}
	if logs.FilterMessage("Already in leader's match").Len() != 1 {
		t.Error("expected TryFollowPartyLeader to end at \"Already in leader's match\"")
	}
	if n := followSkipJoinAttempts(logs); n != 0 {
		t.Errorf("snap-back: %d lobbyJoin attempt(s) on the social lobby the member is already in", n)
	}
}

// Snap-back guard for a member requesting SOCIAL from the leader's social
// lobby: the fast path treats it as converged, and TryFollowPartyLeader and the
// poll agree without joining.
func TestFollowerInLeaderSocial_RequestingSocial_NoSnapBack(t *testing.T) {
	logger, logs := followSkipObservedLogger()
	env, _ := followSkipSharedSocialEnv(t, evr.ModeSocialPublic)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if !env.pipeline.isFollowerAlreadyInLeaderMatch(ctx, logger, env.session, env.lobbyGroup, env.params.CurrentMatchID, env.params.Mode) {
		t.Fatal("isFollowerAlreadyInLeaderMatch did not treat a shared social lobby as converged for a social request")
	}
	if !env.pipeline.TryFollowPartyLeader(ctx, logger, env.session, env.params, env.lobbyGroup) {
		t.Fatal("TryFollowPartyLeader returned false for a member already in the leader's social lobby")
	}
	if !env.pipeline.pollFollowPartyLeader(ctx, logger, env.session, env.params, env.lobbyGroup) {
		t.Fatal("pollFollowPartyLeader did not find the member in the leader's lobby")
	}
	if n := followSkipJoinAttempts(logs); n != 0 {
		t.Errorf("snap-back: %d lobbyJoin attempt(s) on the social lobby the member is already in", n)
	}
}

// Bug A: TryFollowPartyLeader trusted a stale tracker entry.
//
// Member and leader were in arena match M. The member left M 12ms before
// requesting social, but the tracker still showed the member in M. The client
// reports M as CurrentMatchID (the match being left) — confirmed by the fact
// that isFollowerAlreadyInLeaderMatch returned false via its "share the match
// being left" branch, which requires currentMatchID == follower's
// tracked match. TryFollowPartyLeader then compared only the stale
// tracker entry to the leader's match, logged "Already in leader's match" and
// returned true, so the find ended without a join.
//
// Correct behaviour: when the client reports it is leaving the very match the
// tracker says it shares with the leader, the tracker entry is stale and
// TryFollowPartyLeader must NOT report "already in leader's match".
func TestTryFollow_StaleTrackerSharedMatchBeingLeft_NotAlreadyInLeaderMatch(t *testing.T) {
	logger, logs := followSkipObservedLogger()

	env := newFollowTestEnv(t)
	arenaMatch := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(arenaMatch)
	env.setFollowerMatch(arenaMatch) // stale: member already left

	env.params.Mode = evr.ModeSocialPublic
	env.params.CurrentMatchID = arenaMatch // client: "I am leaving this match"

	registry := newMockFollowMatchRegistry()
	registry.SetMatch(arenaMatch, &MatchLabel{
		ID:          arenaMatch,
		Mode:        evr.ModeArenaPublic,
		Open:        true,
		PlayerLimit: 8,
	})
	env.withMockNK(registry)

	// Sanity: the lobbyFind fast-path check agrees the member is leaving
	// (production logged exactly this false).
	if env.pipeline.isFollowerAlreadyInLeaderMatch(context.Background(), logger, env.session, env.lobbyGroup, env.params.CurrentMatchID, env.params.Mode) {
		t.Fatalf("fixture: isFollowerAlreadyInLeaderMatch returned true; expected false (shared match being left)")
	}

	result := env.pipeline.TryFollowPartyLeader(context.Background(), logger, env.session, env.params, env.lobbyGroup)

	already := logs.FilterMessage("Already in leader's match").Len()
	if already > 0 || result {
		t.Errorf("TryFollowPartyLeader reported the member already in the leader's match %s (result=%v, "+
			"\"Already in leader's match\" logged %d time(s)) although the client reports it is LEAVING that match; "+
			"the tracker entry is stale and the find ends without a join", arenaMatch.String(), result, already)
	}
	// The leader's match is arena, so the follow path does not apply and the
	// member falls through to its own social find.
	if logs.FilterMessage("Leader is in a non-social match, follow path not applicable").Len() != 1 {
		t.Errorf("expected TryFollowPartyLeader to fall through to leader-match validation")
	}
}

// #625: the follow-skip ignored both modes when the client did not report the
// shared match as current.
//
// isFollowerAlreadyInLeaderMatch applied the leaving/social distinction and the
// requested-mode check (#623) only when CurrentMatchID equalled the match the
// tracker shows the member sharing with the leader. With a nil CurrentMatchID
// (the client is at the menu) or a different one, it returned true for any
// shared match and any requested mode, so lobbyFind returned at the skip before
// the matchmaking timeout was armed.
//
// Correct behaviour, the same outcome as the CurrentMatchID branch: only a
// shared social lobby, for a member asking for a social lobby, is already
// converged. Everything else takes the follow path.
func TestIsFollowerAlreadyInLeaderMatch_CurrentMatchNotShared(t *testing.T) {
	otherMatch := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	cases := []struct {
		name       string
		sharedMode evr.Symbol
		requested  evr.Symbol
		current    MatchID
		want       bool
	}{
		{"nil current, shared social, requesting social", evr.ModeSocialPublic, evr.ModeSocialPublic, MatchID{}, true},
		{"nil current, shared social, requesting arena", evr.ModeSocialPublic, evr.ModeArenaPublic, MatchID{}, false},
		{"nil current, shared arena, requesting social", evr.ModeArenaPublic, evr.ModeSocialPublic, MatchID{}, false},
		{"nil current, shared arena, requesting arena", evr.ModeArenaPublic, evr.ModeArenaPublic, MatchID{}, false},
		{"other current, shared social, requesting social", evr.ModeSocialPublic, evr.ModeSocialPublic, otherMatch, true},
		{"other current, shared social, requesting arena", evr.ModeSocialPublic, evr.ModeArenaPublic, otherMatch, false},
		{"other current, shared arena, requesting social", evr.ModeArenaPublic, evr.ModeSocialPublic, otherMatch, false},
		{"other current, shared arena, requesting arena", evr.ModeArenaPublic, evr.ModeArenaPublic, otherMatch, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newFollowTestEnv(t)
			shared := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
			env.setLeaderMatch(shared)
			env.setFollowerMatch(shared)

			registry := newMockFollowMatchRegistry()
			registry.SetMatch(shared, &MatchLabel{ID: shared, Mode: tc.sharedMode, Open: true, PlayerLimit: 12})
			env.withMockNK(registry)

			got := env.pipeline.isFollowerAlreadyInLeaderMatch(context.Background(), loggerForTest(t), env.session, env.lobbyGroup, tc.current, tc.requested)
			if got != tc.want {
				t.Errorf("isFollowerAlreadyInLeaderMatch(shared %s, requested %s, current %s) = %v, want %v",
					tc.sharedMode.String(), tc.requested.String(), tc.current.String(), got, tc.want)
			}
		})
	}
}

// #625 end to end: a member at the menu (nil CurrentMatchID) whom the tracker
// still shows in the leader's social lobby, requesting echo_arena, must not
// take lobbyFind's skip path.
func TestLobbyFind_FollowerNilCurrentMatch_RequestingArena_DoesNotSkip(t *testing.T) {
	logger, logs := followSkipObservedLogger()

	tracker := newMockMatchmakingTracker()
	mm, mmCleanup := createLightMatchmaker(t, loggerForTest(t))
	defer mmCleanup()
	pr := NewLocalPartyRegistry(loggerForTest(t), cfg, mm, tracker, testStreamManager{}, &DummyMessageRouter{}, "testnode")

	groupName := "follow-skip-625"
	groupID := uuid.Must(uuid.NewV4())
	socialMatch := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	leaderSession := newTestSessionForParty(t, "leader", tracker, pr)
	if _, _, err := JoinPartyGroup(leaderSession, groupName, socialMatch); err != nil {
		t.Fatalf("leader JoinPartyGroup: %v", err)
	}
	followerSession := newTestSessionForParty(t, "follower", tracker, pr)

	tracker.Track(context.Background(), leaderSession.id,
		PresenceStream{Mode: StreamModeService, Subject: leaderSession.id, Label: StreamLabelMatchService},
		leaderSession.userID, PresenceMeta{Status: socialMatch.String()})
	tracker.Track(context.Background(), followerSession.id,
		PresenceStream{Mode: StreamModeService, Subject: followerSession.id, Label: StreamLabelMatchService},
		followerSession.userID, PresenceMeta{Status: socialMatch.String()})

	registry := newMockFollowMatchRegistry()
	registry.SetMatch(socialMatch, &MatchLabel{ID: socialMatch, Mode: evr.ModeSocialPublic, Open: true, PlayerLimit: 12})

	pipeline := newFollowPollPipeline()
	pipeline.node = "testnode"
	pipeline.nk = &RuntimeGoNakamaModule{
		logger:        loggerForTest(t),
		matchRegistry: registry,
		partyRegistry: pr,
		tracker:       tracker,
		metrics:       &testMetrics{},
		node:          "testnode",
	}

	params := makeMatchmakeTestLobbyParams(followerSession.userID, groupID, evr.ModeArenaPublic, 2)
	params.PartyGroupName = groupName
	params.CurrentMatchID = MatchID{} // client reports no current match

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Past the skip, lobbyAuthorize fails on this test pipeline before any
	// goroutine is started. Only whether the skip was taken matters here.
	findErr := pipeline.lobbyFind(ctx, logger, followerSession, params)

	if n := logs.FilterMessage("Follower already in leader's match, skipping follow path").Len(); n > 0 {
		t.Errorf("lobbyFind took the follower skip path for a member with a nil CurrentMatchID REQUESTING %s "+
			"(returned err=%v before the matchmaking timeout was armed); the requested mode must be checked on this path too",
			params.Mode.String(), findErr)
	}
	if logs.FilterMessage("Follower and leader share a match the client does not report as current, not skipping").Len() != 1 {
		t.Errorf("expected isFollowerAlreadyInLeaderMatch to decline the skip on the nil-CurrentMatchID path")
	}
	// The leader here is not queueing, so lobbyFind goes on to the existing
	// heading-to-social rule, as in the CurrentMatchID case.
	if logs.FilterMessage("Leader is heading to a social lobby, forcing social mode for follower").Len() != 1 {
		t.Errorf("expected lobbyFind to reach the heading-to-social rule past the skip")
	}
}

// #624: the poll's convergence check trusted only the match label. With a
// stale label that does not yet list the member, while the member's own
// tracker presence already points at the leader's social lobby, the poll
// joined the lobby the member is standing in (the da785b895 snap-back).
//
// Correct behaviour: the member's tracker presence in the leader's lobby is
// convergence; zero join attempts.
func TestPoll_StaleLabelOmitsMember_TrackerInLeaderLobby_NoSnapBack(t *testing.T) {
	logger, logs := followSkipObservedLogger()

	env := newFollowTestEnv(t)
	socialLobby := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(socialLobby)
	env.setFollowerMatch(socialLobby)

	// Stale label: lists the leader only.
	registry := newMockFollowMatchRegistry()
	registry.SetMatch(socialLobby, &MatchLabel{
		ID:          socialLobby,
		Mode:        evr.ModeSocialPublic,
		Open:        true,
		PlayerLimit: 12,
		Players:     []PlayerInfo{{UserID: env.leaderUID.String(), Team: 0}},
	})
	env.withMockNK(registry)

	env.params.Mode = evr.ModeArenaPublic
	env.params.CurrentMatchID = socialLobby
	env.pipeline.pollFollowInterval = 10 * time.Millisecond
	env.pipeline.pollFollowMaxDuration = 500 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var panicked any
	func() {
		defer func() { panicked = recover() }()
		env.pipeline.pollFollowPartyLeader(ctx, logger, env.session, env.params, env.lobbyGroup)
	}()

	if n := followSkipJoinAttempts(logs); n != 0 {
		t.Errorf("snap-back: %d lobbyJoin attempt(s) on the social lobby %s the member's tracker presence already names "+
			"(stale label omitted the member; lobbyJoin panic=%v)", n, socialLobby.String(), panicked)
	}
	if panicked != nil {
		t.Errorf("pollFollowPartyLeader panicked: %v", panicked)
	}
}

// #624 guard must not fire for a member who left the leader's social lobby.
//
// The member's service-stream match entry is written only on match accept
// (evr_pipeline_lobby.go) and is not cleared when the member leaves a match, so
// after the member leaves the leader's social lobby for the menu it still names
// that lobby. The label, updated on leave, correctly omits the member, and the
// client reports no current match. The poll must go on to join the leader's
// lobby as it did before #624, not treat the stale tracker entry as convergence
// (which ends the find with no join and no reply).
func TestPoll_LeftLeaderLobbyToMenu_StaleTracker_StillJoins(t *testing.T) {
	logger, logs := followSkipObservedLogger()

	env := newFollowTestEnv(t)
	socialLobby := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(socialLobby)
	env.setFollowerMatch(socialLobby) // stale: the member left for the menu

	registry := newMockFollowMatchRegistry()
	registry.SetMatch(socialLobby, &MatchLabel{
		ID:          socialLobby,
		Mode:        evr.ModeSocialPublic,
		Open:        true,
		PlayerLimit: 12,
		Players:     []PlayerInfo{{UserID: env.leaderUID.String(), Team: 0}},
	})
	env.withMockNK(registry)

	env.params.Mode = evr.ModeArenaPublic
	env.params.CurrentMatchID = MatchID{} // client: at the menu
	env.pipeline.pollFollowInterval = 10 * time.Millisecond
	env.pipeline.pollFollowMaxDuration = 500 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var result bool
	func() {
		// lobbyJoin panics on this test pipeline; the join attempt is what
		// is asserted, via its log line.
		defer func() { _ = recover() }()
		result = env.pipeline.pollFollowPartyLeader(ctx, logger, env.session, env.params, env.lobbyGroup)
	}()

	if n := logs.FilterMessage("Follower's tracker presence is already in leader's social lobby, label is stale, not rejoining").Len(); n != 0 || result {
		t.Errorf("poll treated a stale tracker entry as convergence for a member at the menu (result=%v, logged %d time(s)); "+
			"the find ends with no join and no reply", result, n)
	}
	if n := logs.FilterMessage("Joining leader's social lobby during poll").Len(); n != 1 {
		t.Errorf("expected the poll to join the leader's social lobby once, got %d", n)
	}
}

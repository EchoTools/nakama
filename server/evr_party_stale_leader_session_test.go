package server

import (
	"context"
	"testing"

	"github.com/gofrs/uuid/v5"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// ---------------------------------------------------------------------------
// Regression (#621): party leader slot is not moved when the leader's USER
// replaces their session (production, 2026-09-21 15:16-15:20 UTC).
//
//   15:16:43  leader's client opens session L2 while L1 (the party leader) is
//             still open; L1 closes 15:16:57.
//   15:17:07  L2 lobby find -> TryFollowPartyLeader logs "Party leader is this
//             player's own (different) session" (leader_sid=L1) -> released to
//             independent matchmaking, matched solo without the member.
//   ...       member polls the leader match against L1 until "Poll budget
//             exhausted" at 15:20:55.
//
// Mechanism in the code (read, not assumed):
//   - JoinPartyGroup for L2 finds the USER already a member and skips
//     JoinRequest; the party-stream Track then fires a tracker Join event.
//   - PartyPresenceList.Join treats same-user/different-session as a session
//     replacement and DROPS L1 from the roster -- but PartyHandler.Join never
//     touches p.leader, so the leader slot still names L1.
//   - When L1 closes, its Leave finds no roster entry (already dropped), so
//     PartyHandler.Leave returns early and never re-elects: L1 leads forever.
//
// Both tests were RED against a0d6af155; fixed in PartyHandler.Join/Leave.
// ---------------------------------------------------------------------------

func staleLeaderObservedLogger() (*zap.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(zapcore.DebugLevel)
	return zap.New(core), logs
}

// partyForwardingTracker is mockMatchmakingTracker plus the one behaviour of
// the real LocalTracker these tests depend on: party-stream joins and leaves
// are delivered to the party registry (tracker.go processEvent, the
// partyJoins/partyLeaves branches).
type partyForwardingTracker struct {
	*mockMatchmakingTracker
	pr *LocalPartyRegistry
}

func (t *partyForwardingTracker) Track(ctx context.Context, sessionID uuid.UUID, stream PresenceStream, userID uuid.UUID, meta PresenceMeta) (bool, bool) {
	ok, isNew := t.mockMatchmakingTracker.Track(ctx, sessionID, stream, userID, meta)
	if ok && isNew && stream.Mode == StreamModeParty && t.pr != nil {
		t.pr.Join(stream.Subject, []*Presence{{
			ID:     PresenceID{Node: "testnode", SessionID: sessionID},
			Stream: stream,
			UserID: userID,
			Meta:   meta,
		}})
	}
	return ok, isNew
}

// closeSession simulates the session closing: its party-stream presence
// leaves, delivered to the registry as the real tracker does.
func (t *partyForwardingTracker) closeSession(sessionID, userID uuid.UUID, partyStream PresenceStream, username string) {
	t.mockMatchmakingTracker.UntrackLocalByModes(sessionID, map[uint8]struct{}{StreamModeParty: {}}, PresenceStream{})
	t.pr.Leave(partyStream.Subject, []*Presence{{
		ID:     PresenceID{Node: "testnode", SessionID: sessionID},
		Stream: partyStream,
		UserID: userID,
		Meta:   PresenceMeta{Username: username},
	}})
}

type staleLeaderFixture struct {
	tracker   *partyForwardingTracker
	pr        *LocalPartyRegistry
	l1        *sessionWS
	member    *sessionWS
	l2        *sessionWS
	l2Group   *LobbyGroup
	l2Leader  bool
	groupName string
}

func newStaleLeaderFixture(t *testing.T) *staleLeaderFixture {
	t.Helper()
	tracker := &partyForwardingTracker{mockMatchmakingTracker: newMockMatchmakingTracker()}
	mm, mmCleanup := createLightMatchmaker(t, loggerForTest(t))
	t.Cleanup(mmCleanup)
	pr := NewLocalPartyRegistry(loggerForTest(t), cfg, mm, tracker, testStreamManager{}, &DummyMessageRouter{}, "testnode").(*LocalPartyRegistry)
	tracker.pr = pr

	groupName := "repro-stale-leader"

	// L1 creates and leads the party.
	l1 := newTestSessionForParty(t, "leader", tracker, pr)
	l1Group, l1Leader, err := JoinPartyGroup(l1, groupName, MatchID{})
	if err != nil {
		t.Fatalf("L1 JoinPartyGroup: %v", err)
	}
	if !l1Leader || l1Group.GetLeader().GetSessionId() != l1.id.String() {
		t.Fatalf("fixture: L1 should lead, isLeader=%v leader_sid=%s", l1Leader, l1Group.GetLeader().GetSessionId())
	}

	// The member joins.
	member := newTestSessionForParty(t, "member", tracker, pr)
	if _, memberLeader, err := JoinPartyGroup(member, groupName, MatchID{}); err != nil || memberLeader {
		t.Fatalf("fixture: member JoinPartyGroup err=%v isLeader=%v", err, memberLeader)
	}
	if n := l1Group.Size(); n != 2 {
		t.Fatalf("fixture: expected party of 2, got %d", n)
	}

	// Same USER opens a new session L2 while L1 is still open.
	l2 := newTestSessionForParty(t, "leader", tracker, pr)
	l2.userID = l1.userID
	l2Group, l2Leader, err := JoinPartyGroup(l2, groupName, MatchID{})
	if err != nil {
		t.Fatalf("L2 JoinPartyGroup: %v", err)
	}

	return &staleLeaderFixture{tracker, pr, l1, member, l2, l2Group, l2Leader, groupName}
}

// L2 is the only live session of the leader's user in the roster; it must be
// the leader, and its own find must not treat it as a follower of L1.
func TestStaleLeaderSession_ReplacementSessionLeadsAndDoesNotSelfFollow(t *testing.T) {
	f := newStaleLeaderFixture(t)

	// Sanity: the roster already replaced L1 with L2 (session replacement).
	rosterHasL1, rosterHasL2 := false, false
	for _, m := range f.l2Group.List() {
		switch m.Presence.GetSessionId() {
		case f.l1.id.String():
			rosterHasL1 = true
		case f.l2.id.String():
			rosterHasL2 = true
		}
	}
	if rosterHasL1 || !rosterHasL2 {
		t.Fatalf("fixture: expected roster to hold L2 not L1, hasL1=%v hasL2=%v", rosterHasL1, rosterHasL2)
	}

	leaderSID := f.l2Group.GetLeader().GetSessionId()
	if !f.l2Leader || leaderSID != f.l2.id.String() {
		t.Errorf("party leader slot names a session that is not in the roster: leader_sid=%s (L1=%s), L2=%s isLeader=%v; "+
			"the same user's replacement session must hold leadership", leaderSID, f.l1.id, f.l2.id, f.l2Leader)
	}

	// Real follow path for L2 (evr_lobby_find.go TryFollowPartyLeader / poll).
	logger, logs := staleLeaderObservedLogger()
	p := newFollowPollPipeline()
	params := &LobbySessionParameters{}
	_ = p.TryFollowPartyLeader(context.Background(), logger, f.l2, params, f.l2Group)
	_ = p.pollFollowPartyLeader(context.Background(), logger, f.l2, params, f.l2Group)

	if n := logs.FilterMessage("Party leader is this player's own (different) session, not self-following").Len(); n > 0 {
		t.Errorf("TryFollowPartyLeader treated L2 as a follower of its own stale session L1 (logged %d time(s)); "+
			"in production this released the leader to solo matchmaking", n)
	}
	if n := logs.FilterMessage("Party leader is this player's own session, not self-following in poll").Len(); n > 0 {
		t.Errorf("pollFollowPartyLeader treated L2 as a follower of its own stale session L1 (logged %d time(s))", n)
	}
}

// After L1 closes, the party's leader must resolve to a live session, never
// to the closed L1 (production: member polled L1 for ~4 minutes).
func TestStaleLeaderSession_AfterOldSessionCloses_LeaderIsLive(t *testing.T) {
	f := newStaleLeaderFixture(t)

	f.tracker.closeSession(f.l1.id, f.l1.userID, f.l2Group.ph.Stream, "leader")

	leaderSID := f.l2Group.GetLeader().GetSessionId()
	live := map[string]bool{f.l2.id.String(): true, f.member.id.String(): true}
	if !live[leaderSID] {
		t.Errorf("after L1 closed the party leader is still %s (L1=%s); expected a live session (L2=%s or member=%s)",
			leaderSID, f.l1.id, f.l2.id, f.member.id)
	}
}

// The leader slot must never outlive its session, whatever path left it
// orphaned. Drop the leader's session from the roster without going through
// PartyHandler.Leave, then have a DIFFERENT user join (so Join's same-user
// transfer does not apply). When the orphaned session's Leave arrives,
// PartyHandler.Leave must re-elect the oldest member rather than returning
// early because members.Leave found nothing to remove.
func TestStaleLeaderSession_OrphanedLeaderLeaveReelectsOldest(t *testing.T) {
	tracker := &partyForwardingTracker{mockMatchmakingTracker: newMockMatchmakingTracker()}
	mm, mmCleanup := createLightMatchmaker(t, loggerForTest(t))
	t.Cleanup(mmCleanup)
	pr := NewLocalPartyRegistry(loggerForTest(t), cfg, mm, tracker, testStreamManager{}, &DummyMessageRouter{}, "testnode").(*LocalPartyRegistry)
	tracker.pr = pr

	groupName := "orphaned-leader"
	leader := newTestSessionForParty(t, "leader", tracker, pr)
	group, isLeader, err := JoinPartyGroup(leader, groupName, MatchID{})
	if err != nil || !isLeader {
		t.Fatalf("fixture: leader JoinPartyGroup err=%v isLeader=%v", err, isLeader)
	}
	member := newTestSessionForParty(t, "member", tracker, pr)
	if _, _, err := JoinPartyGroup(member, groupName, MatchID{}); err != nil {
		t.Fatalf("fixture: member JoinPartyGroup: %v", err)
	}
	ph := group.ph

	// Orphan the leader slot: the leader's session leaves the roster but
	// p.leader still names it.
	leaderPresence := &Presence{
		ID:     PresenceID{Node: "testnode", SessionID: leader.id},
		Stream: ph.Stream,
		UserID: leader.userID,
		Meta:   PresenceMeta{Username: "leader"},
	}
	ph.members.Leave([]*Presence{leaderPresence})
	if got := group.GetLeader().GetSessionId(); got != leader.id.String() {
		t.Fatalf("fixture: expected orphaned leader slot %s, got %s", leader.id, got)
	}

	// A different user joins; nothing may transfer to them.
	other := newTestSessionForParty(t, "other", tracker, pr)
	if _, _, err := JoinPartyGroup(other, groupName, MatchID{}); err != nil {
		t.Fatalf("fixture: other JoinPartyGroup: %v", err)
	}
	if got := group.GetLeader().GetSessionId(); got != leader.id.String() {
		t.Fatalf("fixture: a different user's join moved leadership to %s", got)
	}

	// The orphaned session now closes.
	tracker.closeSession(leader.id, leader.userID, ph.Stream, "leader")

	if got := group.GetLeader().GetSessionId(); got != member.id.String() {
		t.Errorf("after orphan leave leader=%s (orphan=%s), want oldest member %s", got, leader.id, member.id)
	}
}

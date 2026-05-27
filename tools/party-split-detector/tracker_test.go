package main

import (
	"testing"
	"time"
)

func newFormEvent(sid, uid, username string, at time.Time) *testEvent {
	return &testEvent{
		typ:       EvtPartyFormed,
		timestamp: at,
		sid:       sid,
		uid:       uid,
		username:  username,
	}
}

// newMemberJoinEvent creates a member-join event. The partyID is carried in
// rawLine to mirror the real PartyJoined event's PartyID field. This enables
// correlation for new members whose SID/UID are not yet indexed.
func newMemberJoinEvent(sid, uid, username, partyID string, at time.Time) *testEvent {
	return &testEvent{
		typ:       EvtMemberJoined,
		timestamp: at,
		sid:       sid,
		uid:       uid,
		username:  username,
		rawLine:   partyID,
	}
}

func newEvent(evtType, sid, uid, username string, at time.Time) *testEvent {
	return &testEvent{
		typ:       evtType,
		timestamp: at,
		sid:       sid,
		uid:       uid,
		username:  username,
	}
}

func newMatchJoinEvent(sid, uid, matchID string, at time.Time) *testEvent {
	return &testEvent{
		typ:       EvtMatchJoined,
		timestamp: at,
		sid:       sid,
		uid:       uid,
		rawLine:   matchID, // matchID conveyed via rawLine in the test interface
	}
}

// --- NewPartyTracker ---

func TestNewPartyTracker(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()
	if pt.ActiveParties() != 0 {
		t.Fatalf("new tracker should have 0 active parties, got %d", pt.ActiveParties())
	}
	if len(pt.Finalized) != 0 {
		t.Fatalf("new tracker should have 0 finalized parties, got %d", len(pt.Finalized))
	}
}

// --- Party Formation ---

func TestProcessEvent_PartyFormed(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()
	evt := newFormEvent("sid-leader", "uid-leader", "boss", baseTime)

	pt.ProcessEvent(evt)

	if pt.ActiveParties() != 1 {
		t.Fatalf("active parties = %d, want 1", pt.ActiveParties())
	}
	pl := pt.GetParty("sid-leader")
	if pl == nil {
		t.Fatal("party not found after formation")
	}
	if pl.State != StateForming {
		t.Fatalf("state = %s, want FORMING", pl.State)
	}
	if pl.Leader.UID != "uid-leader" {
		t.Fatalf("Leader.UID = %q, want %q", pl.Leader.UID, "uid-leader")
	}
	if pl.Leader.Username != "boss" {
		t.Fatalf("Leader.Username = %q, want %q", pl.Leader.Username, "boss")
	}
}

func TestProcessEvent_DuplicateFormation_Ignored(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()
	evt := newFormEvent("sid-leader", "uid-leader", "boss", baseTime)

	pt.ProcessEvent(evt)
	pt.ProcessEvent(evt) // duplicate

	if pt.ActiveParties() != 1 {
		t.Fatalf("active parties = %d, want 1 (duplicate should be ignored)", pt.ActiveParties())
	}
}

func TestProcessEvent_FormationWithNoSID_Dropped(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()
	evt := newFormEvent("", "uid-leader", "boss", baseTime)

	pt.ProcessEvent(evt)

	if pt.ActiveParties() != 0 {
		t.Fatalf("active parties = %d, want 0 (no SID)", pt.ActiveParties())
	}
}

// --- Event Correlation ---

func TestCorrelation_BySID(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()
	pt.ProcessEvent(newFormEvent("sid-leader", "uid-leader", "boss", baseTime))

	// Event with leader's SID should correlate to the party.
	evt := newEvent(EvtPartyReady, "sid-leader", "uid-leader", "boss", ts(1))
	pt.ProcessEvent(evt)

	pl := pt.GetParty("sid-leader")
	if pl.State != StateReady {
		t.Fatalf("state = %s, want READY after correlated event", pl.State)
	}
}

func TestCorrelation_ByUID(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()
	pt.ProcessEvent(newFormEvent("sid-leader", "uid-leader", "boss", baseTime))

	// Add a member so UID -> party mapping exists.
	pt.ProcessEvent(newMemberJoinEvent("sid-follower", "uid-follower", "follower", "sid-leader", ts(1)))

	// Event with follower's UID but different SID should still correlate.
	evt := &testEvent{
		typ:       EvtFollowAttempt,
		timestamp: ts(3),
		sid:       "sid-follower-new", // different SID
		uid:       "uid-follower",     // same UID
		username:  "follower",
	}

	// First transition party to READY so FOLLOWING is legal.
	pt.ProcessEvent(newEvent(EvtPartyReady, "sid-leader", "uid-leader", "boss", ts(2)))
	pt.ProcessEvent(evt)

	pl := pt.GetParty("sid-leader")
	if pl.State != StateFollowing {
		t.Fatalf("state = %s, want FOLLOWING", pl.State)
	}
}

func TestCorrelation_UnknownEvent_Dropped(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()

	// Event for a party that does not exist.
	evt := newEvent(EvtPartyReady, "unknown-sid", "unknown-uid", "nobody", baseTime)
	pt.ProcessEvent(evt) // should not panic

	if pt.ActiveParties() != 0 {
		t.Fatalf("active parties = %d, want 0", pt.ActiveParties())
	}
}

// --- Member Management ---

func TestMemberJoin_AddsToParty(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()
	pt.ProcessEvent(newFormEvent("sid-leader", "uid-leader", "boss", baseTime))
	pt.ProcessEvent(newMemberJoinEvent("sid-f1", "uid-f1", "follower1", "sid-leader", ts(1)))

	pl := pt.GetParty("sid-leader")
	if len(pl.Members) != 1 {
		t.Fatalf("members = %d, want 1", len(pl.Members))
	}
	if pl.Members[0].UID != "uid-f1" {
		t.Fatalf("member UID = %q, want %q", pl.Members[0].UID, "uid-f1")
	}
}

func TestMemberJoin_Reconnect_UpdatesSID(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()
	pt.ProcessEvent(newFormEvent("sid-leader", "uid-leader", "boss", baseTime))
	pt.ProcessEvent(newMemberJoinEvent("sid-f1", "uid-f1", "follower1", "sid-leader", ts(1)))

	// Same UID, new SID — reconnect.
	pt.ProcessEvent(newMemberJoinEvent("sid-f1-new", "uid-f1", "follower1", "sid-leader", ts(5)))

	pl := pt.GetParty("sid-leader")
	if len(pl.Members) != 1 {
		t.Fatalf("members = %d, want 1 (reconnect should not duplicate)", len(pl.Members))
	}
	if pl.Members[0].SID != "sid-f1-new" {
		t.Fatalf("member SID = %q, want %q (should be updated)", pl.Members[0].SID, "sid-f1-new")
	}
}

func TestMemberLeft_AllLeft_Abandoned(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()
	pt.ProcessEvent(newFormEvent("sid-leader", "uid-leader", "boss", baseTime))
	pt.ProcessEvent(newMemberJoinEvent("sid-f1", "uid-f1", "follower1", "sid-leader", ts(1)))

	pt.ProcessEvent(newEvent(EvtMemberLeft, "sid-leader", "uid-leader", "boss", ts(5)))
	pt.ProcessEvent(newEvent(EvtMemberLeft, "sid-f1", "uid-f1", "follower1", ts(6)))

	pl := pt.GetParty("sid-leader")
	if pl.State != StateAbandoned {
		t.Fatalf("state = %s, want ABANDONED after all members left", pl.State)
	}
}

// --- Full Lifecycle via Tracker ---

func TestTracker_TwoMembers_BothConverge(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()

	// Form party.
	pt.ProcessEvent(newFormEvent("sid-leader", "uid-leader", "boss", baseTime))
	pt.ProcessEvent(newMemberJoinEvent("sid-f1", "uid-f1", "follower1", "sid-leader", ts(1)))

	// Party ready.
	pt.ProcessEvent(newEvent(EvtPartyReady, "sid-leader", "uid-leader", "boss", ts(2)))

	// Matchmaking path.
	pt.ProcessEvent(newEvent(EvtMatchmakingSubmit, "sid-leader", "uid-leader", "boss", ts(3)))
	pt.ProcessEvent(newEvent(EvtMatchBuilt, "sid-leader", "uid-leader", "boss", ts(5)))

	// Both join the same match.
	pt.ProcessEvent(newMatchJoinEvent("sid-leader", "uid-leader", "match-A", ts(6)))
	pt.ProcessEvent(newMatchJoinEvent("sid-f1", "uid-f1", "match-A", ts(7)))

	pl := pt.GetParty("sid-leader")
	// Both members should be MemberPlaced after match join.
	leader := pl.FindMemberByUID("uid-leader")
	if leader.State != MemberPlaced {
		t.Fatalf("leader state = %s, want PLACED", leader.State)
	}
	if leader.MatchID != "match-A" {
		t.Fatalf("leader matchID = %q, want %q", leader.MatchID, "match-A")
	}

	follower := pl.FindMemberByUID("uid-f1")
	if follower.State != MemberPlaced {
		t.Fatalf("follower state = %s, want PLACED", follower.State)
	}
}

func TestTracker_TwoMembers_OneSplitReleased(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()

	pt.ProcessEvent(newFormEvent("sid-leader", "uid-leader", "boss", baseTime))
	pt.ProcessEvent(newMemberJoinEvent("sid-f1", "uid-f1", "follower1", "sid-leader", ts(1)))
	pt.ProcessEvent(newEvent(EvtPartyReady, "sid-leader", "uid-leader", "boss", ts(2)))
	pt.ProcessEvent(newEvent(EvtFollowAttempt, "sid-f1", "uid-f1", "follower1", ts(3)))

	// Follower released to independent matchmaking.
	pt.ProcessEvent(newEvent(EvtFollowReleasedIndependent, "sid-f1", "uid-f1", "follower1", ts(10)))

	pl := pt.GetParty("sid-leader")
	if pl.State != StateSplit {
		t.Fatalf("state = %s, want SPLIT", pl.State)
	}
	if pl.Outcome != OutcomeSplit {
		t.Fatalf("outcome = %s, want SPLIT", pl.Outcome)
	}
}

func TestTracker_ThreeMembers_TwoConverge_OneCrashes(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()

	pt.ProcessEvent(newFormEvent("sid-leader", "uid-leader", "boss", baseTime))
	pt.ProcessEvent(newMemberJoinEvent("sid-f1", "uid-f1", "follower1", "sid-leader", ts(1)))
	pt.ProcessEvent(newMemberJoinEvent("sid-f2", "uid-f2", "follower2", "sid-leader", ts(1)))
	pt.ProcessEvent(newEvent(EvtPartyReady, "sid-leader", "uid-leader", "boss", ts(2)))
	pt.ProcessEvent(newEvent(EvtMatchmakingSubmit, "sid-leader", "uid-leader", "boss", ts(3)))
	pt.ProcessEvent(newEvent(EvtMatchBuilt, "sid-leader", "uid-leader", "boss", ts(5)))

	// Leader and f1 join match.
	pt.ProcessEvent(newMatchJoinEvent("sid-leader", "uid-leader", "match-A", ts(6)))
	pt.ProcessEvent(newMatchJoinEvent("sid-f1", "uid-f1", "match-A", ts(7)))

	// f2 crashes during placing.
	pt.ProcessEvent(newEvent(EvtMemberCrashed, "sid-f2", "uid-f2", "follower2", ts(8)))

	pl := pt.GetParty("sid-leader")
	// At this point: 2 placed, 1 crashed. Not yet evaluated as full outcome
	// because members are placed, not converged. The leader/f1 are MemberPlaced.
	// Force outcome check by marking placed members as converged conceptually.
	// The tracker does not auto-elevate MemberPlaced -> MemberConverged; that
	// requires the match convergence check. Let's set them for this test.
	pl.FindMemberByUID("uid-leader").State = MemberConverged
	pl.FindMemberByUID("uid-f1").State = MemberConverged
	pl.EvaluateOutcome(ts(9))

	if pl.Outcome != OutcomeDegraded {
		t.Fatalf("outcome = %s, want DEGRADED", pl.Outcome)
	}
}

func TestTracker_PartyAbandoned_NoActivity(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()

	pt.ProcessEvent(newFormEvent("sid-leader", "uid-leader", "boss", baseTime))

	// Tick at 61 seconds — party should be flushed as ABANDONED.
	pt.Tick(ts(61))

	if pt.ActiveParties() != 0 {
		t.Fatalf("active parties = %d, want 0 after tick", pt.ActiveParties())
	}
	if len(pt.Finalized) != 1 {
		t.Fatalf("finalized = %d, want 1", len(pt.Finalized))
	}
	if pt.Finalized[0].Outcome != OutcomeAbandoned {
		t.Fatalf("outcome = %s, want ABANDONED", pt.Finalized[0].Outcome)
	}
}

// --- Tick / Window ---

func TestTick_DoesNotFlushYoungParties(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()
	pt.ProcessEvent(newFormEvent("sid-leader", "uid-leader", "boss", baseTime))

	pt.Tick(ts(30)) // only 30 seconds elapsed

	if pt.ActiveParties() != 1 {
		t.Fatalf("active parties = %d, want 1 (party too young to flush)", pt.ActiveParties())
	}
	if len(pt.Finalized) != 0 {
		t.Fatalf("finalized = %d, want 0", len(pt.Finalized))
	}
}

func TestTick_FlushesOldParties(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()
	pt.ProcessEvent(newFormEvent("sid-leader", "uid-leader", "boss", baseTime))

	pt.Tick(ts(61))

	if pt.ActiveParties() != 0 {
		t.Fatalf("active parties = %d, want 0", pt.ActiveParties())
	}
	if len(pt.Finalized) != 1 {
		t.Fatalf("finalized = %d, want 1", len(pt.Finalized))
	}
}

func TestTick_PreservesTerminalPartyOutcome(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()
	pt.ProcessEvent(newFormEvent("sid-leader", "uid-leader", "boss", baseTime))
	pt.ProcessEvent(newMemberJoinEvent("sid-f1", "uid-f1", "follower1", "sid-leader", ts(1)))
	pt.ProcessEvent(newEvent(EvtPartyReady, "sid-leader", "uid-leader", "boss", ts(2)))
	pt.ProcessEvent(newEvent(EvtFollowAttempt, "sid-f1", "uid-f1", "follower1", ts(3)))
	pt.ProcessEvent(newEvent(EvtFollowReleasedIndependent, "sid-f1", "uid-f1", "follower1", ts(5)))

	// Party already reached SPLIT. Tick should preserve that outcome.
	pt.Tick(ts(61))

	if len(pt.Finalized) != 1 {
		t.Fatalf("finalized = %d, want 1", len(pt.Finalized))
	}
	if pt.Finalized[0].Outcome != OutcomeSplit {
		t.Fatalf("outcome = %s, want SPLIT", pt.Finalized[0].Outcome)
	}
}

// --- FlushAll ---

func TestFlushAll(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()
	pt.ProcessEvent(newFormEvent("sid-1", "uid-1", "a", baseTime))
	pt.ProcessEvent(newFormEvent("sid-2", "uid-2", "b", ts(5)))

	pt.FlushAll(ts(30))

	if pt.ActiveParties() != 0 {
		t.Fatalf("active parties = %d, want 0", pt.ActiveParties())
	}
	if len(pt.Finalized) != 2 {
		t.Fatalf("finalized = %d, want 2", len(pt.Finalized))
	}
}

// --- Concurrent Parties ---

func TestConcurrentParties_InterleavedEvents(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()

	// Party A forms.
	pt.ProcessEvent(newFormEvent("sid-leaderA", "uid-leaderA", "bossA", baseTime))
	pt.ProcessEvent(newMemberJoinEvent("sid-fA", "uid-fA", "followerA", "sid-leaderA", ts(1)))

	// Party B forms.
	pt.ProcessEvent(newFormEvent("sid-leaderB", "uid-leaderB", "bossB", ts(2)))
	pt.ProcessEvent(newMemberJoinEvent("sid-fB", "uid-fB", "followerB", "sid-leaderB", ts(3)))

	// Interleaved: Party A ready, Party B ready.
	pt.ProcessEvent(newEvent(EvtPartyReady, "sid-leaderA", "uid-leaderA", "bossA", ts(4)))
	pt.ProcessEvent(newEvent(EvtPartyReady, "sid-leaderB", "uid-leaderB", "bossB", ts(5)))

	// Party A goes matchmaking.
	pt.ProcessEvent(newEvent(EvtMatchmakingSubmit, "sid-leaderA", "uid-leaderA", "bossA", ts(6)))
	// Party B follower attempts follow.
	pt.ProcessEvent(newEvent(EvtFollowAttempt, "sid-fB", "uid-fB", "followerB", ts(7)))

	// Party A match built.
	pt.ProcessEvent(newEvent(EvtMatchBuilt, "sid-leaderA", "uid-leaderA", "bossA", ts(8)))
	// Party B follower released.
	pt.ProcessEvent(newEvent(EvtFollowReleasedIndependent, "sid-fB", "uid-fB", "followerB", ts(9)))

	// Verify Party A state.
	plA := pt.GetParty("sid-leaderA")
	if plA.State != StatePlacing {
		t.Fatalf("Party A state = %s, want PLACING", plA.State)
	}

	// Verify Party B state — should be SPLIT.
	plB := pt.GetParty("sid-leaderB")
	if plB.State != StateSplit {
		t.Fatalf("Party B state = %s, want SPLIT", plB.State)
	}

	// Verify no cross-contamination: Party A should have 1 member, Party B should have 1.
	if len(plA.Members) != 1 {
		t.Fatalf("Party A members = %d, want 1", len(plA.Members))
	}
	if plA.Members[0].UID != "uid-fA" {
		t.Fatalf("Party A follower UID = %q, want %q", plA.Members[0].UID, "uid-fA")
	}
	if len(plB.Members) != 1 {
		t.Fatalf("Party B members = %d, want 1", len(plB.Members))
	}
	if plB.Members[0].UID != "uid-fB" {
		t.Fatalf("Party B follower UID = %q, want %q", plB.Members[0].UID, "uid-fB")
	}
}

func TestConcurrentParties_TickFlushesOnlyOld(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()

	// Party A at t=0.
	pt.ProcessEvent(newFormEvent("sid-A", "uid-A", "a", baseTime))
	// Party B at t=30.
	pt.ProcessEvent(newFormEvent("sid-B", "uid-B", "b", ts(30)))

	// Tick at t=61 — only Party A is old enough.
	pt.Tick(ts(61))

	if pt.ActiveParties() != 1 {
		t.Fatalf("active parties = %d, want 1", pt.ActiveParties())
	}
	if len(pt.Finalized) != 1 {
		t.Fatalf("finalized = %d, want 1", len(pt.Finalized))
	}
	if pt.Finalized[0].PartyID != "sid-A" {
		t.Fatalf("flushed party = %q, want %q", pt.Finalized[0].PartyID, "sid-A")
	}

	// Tick at t=91 — now Party B is also old enough.
	pt.Tick(ts(91))

	if pt.ActiveParties() != 0 {
		t.Fatalf("active parties = %d, want 0", pt.ActiveParties())
	}
	if len(pt.Finalized) != 2 {
		t.Fatalf("finalized = %d, want 2", len(pt.Finalized))
	}
}

// --- Member State Tracking Through Reconnect ---

func TestReconnect_PreservesPartyMembership(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()

	pt.ProcessEvent(newFormEvent("sid-leader", "uid-leader", "boss", baseTime))
	pt.ProcessEvent(newMemberJoinEvent("sid-f1", "uid-f1", "follower1", "sid-leader", ts(1)))
	pt.ProcessEvent(newEvent(EvtPartyReady, "sid-leader", "uid-leader", "boss", ts(2)))
	pt.ProcessEvent(newEvent(EvtFollowAttempt, "sid-f1", "uid-f1", "follower1", ts(3)))
	pt.ProcessEvent(newEvent(EvtFollowRetry, "sid-f1", "uid-f1", "follower1", ts(4)))

	// Member crashes.
	pt.ProcessEvent(newEvent(EvtMemberCrashed, "sid-f1", "uid-f1", "follower1", ts(5)))

	pl := pt.GetParty("sid-leader")
	if pl.State != StateCrashed {
		t.Fatalf("state = %s, want CRASHED", pl.State)
	}

	// Member reconnects with new SID.
	reconnectEvt := &testEvent{
		typ:       EvtMemberReconnected,
		timestamp: ts(10),
		sid:       "sid-f1-new",
		uid:       "uid-f1",
		username:  "follower1",
	}
	pt.ProcessEvent(reconnectEvt)

	if pl.State != StateFollowing {
		t.Fatalf("state = %s, want FOLLOWING after reconnect", pl.State)
	}

	// Verify the member's SID was updated.
	m := pl.FindMemberByUID("uid-f1")
	if m == nil {
		t.Fatal("member not found after reconnect")
	}
	if m.SID != "sid-f1-new" {
		t.Fatalf("member SID = %q, want %q", m.SID, "sid-f1-new")
	}

	// Verify old SID no longer maps to the party.
	oldResolved := pt.resolvePartyBySID("sid-f1")
	if oldResolved != nil {
		t.Fatal("old SID should no longer resolve to a party")
	}

	// Verify new SID maps to the party.
	newResolved := pt.resolvePartyBySID("sid-f1-new")
	if newResolved == nil {
		t.Fatal("new SID should resolve to the party")
	}
	if newResolved.PartyID != "sid-leader" {
		t.Fatalf("resolved partyID = %q, want %q", newResolved.PartyID, "sid-leader")
	}
}

// resolvePartyBySID is a test helper that directly checks SID -> party mapping.
func (pt *PartyTracker) resolvePartyBySID(sid string) *PartyLifecycle {
	pid, ok := pt.sidToParty[sid]
	if !ok {
		return nil
	}
	return pt.parties[pid]
}

// --- Events Ignored After Terminal ---

func TestEventsIgnoredAfterTerminal(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()

	pt.ProcessEvent(newFormEvent("sid-leader", "uid-leader", "boss", baseTime))
	pt.ProcessEvent(newMemberJoinEvent("sid-f1", "uid-f1", "follower1", "sid-leader", ts(1)))
	pt.ProcessEvent(newEvent(EvtPartyReady, "sid-leader", "uid-leader", "boss", ts(2)))
	pt.ProcessEvent(newEvent(EvtFollowAttempt, "sid-f1", "uid-f1", "follower1", ts(3)))
	pt.ProcessEvent(newEvent(EvtFollowReleasedIndependent, "sid-f1", "uid-f1", "follower1", ts(5)))

	pl := pt.GetParty("sid-leader")
	if pl.State != StateSplit {
		t.Fatalf("state = %s, want SPLIT", pl.State)
	}
	eventCountBefore := len(pl.Events)

	// Additional events after terminal should be ignored.
	pt.ProcessEvent(newEvent(EvtMatchmakingSubmit, "sid-leader", "uid-leader", "boss", ts(10)))
	pt.ProcessEvent(newEvent(EvtMatchBuilt, "sid-leader", "uid-leader", "boss", ts(11)))

	if len(pl.Events) != eventCountBefore {
		t.Fatalf("events after terminal: %d, want %d (no new events)", len(pl.Events), eventCountBefore)
	}
}

// --- Follow Path Full Lifecycle ---

func TestFollowPath_FollowingToConverged(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()

	pt.ProcessEvent(newFormEvent("sid-leader", "uid-leader", "boss", baseTime))
	pt.ProcessEvent(newMemberJoinEvent("sid-f1", "uid-f1", "follower1", "sid-leader", ts(1)))
	pt.ProcessEvent(newEvent(EvtPartyReady, "sid-leader", "uid-leader", "boss", ts(2)))
	pt.ProcessEvent(newEvent(EvtFollowAttempt, "sid-f1", "uid-f1", "follower1", ts(3)))
	pt.ProcessEvent(newEvent(EvtFollowSuccess, "sid-f1", "uid-f1", "follower1", ts(5)))

	pl := pt.GetParty("sid-leader")
	m := pl.FindMemberByUID("uid-f1")
	if m.State != MemberConverged {
		t.Fatalf("follower state = %s, want CONVERGED", m.State)
	}
}

func TestFollowPath_PollingToConverged(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()

	pt.ProcessEvent(newFormEvent("sid-leader", "uid-leader", "boss", baseTime))
	pt.ProcessEvent(newMemberJoinEvent("sid-f1", "uid-f1", "follower1", "sid-leader", ts(1)))
	pt.ProcessEvent(newEvent(EvtPartyReady, "sid-leader", "uid-leader", "boss", ts(2)))
	pt.ProcessEvent(newEvent(EvtFollowAttempt, "sid-f1", "uid-f1", "follower1", ts(3)))
	pt.ProcessEvent(newEvent(EvtFollowRetry, "sid-f1", "uid-f1", "follower1", ts(4)))
	pt.ProcessEvent(newEvent(EvtFollowSuccess, "sid-f1", "uid-f1", "follower1", ts(8)))

	pl := pt.GetParty("sid-leader")
	m := pl.FindMemberByUID("uid-f1")
	if m.State != MemberConverged {
		t.Fatalf("follower state = %s, want CONVERGED", m.State)
	}
}

// --- Party Dissolved ---

func TestPartyDissolved_FromReady(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()

	pt.ProcessEvent(newFormEvent("sid-leader", "uid-leader", "boss", baseTime))
	pt.ProcessEvent(newEvent(EvtPartyReady, "sid-leader", "uid-leader", "boss", ts(1)))
	pt.ProcessEvent(newEvent(EvtPartyDissolved, "sid-leader", "uid-leader", "boss", ts(5)))

	pl := pt.GetParty("sid-leader")
	if pl.State != StateAbandoned {
		t.Fatalf("state = %s, want ABANDONED", pl.State)
	}
	if pl.OutcomeInfo != "party dissolved" {
		t.Fatalf("outcomeInfo = %q, want %q", pl.OutcomeInfo, "party dissolved")
	}
}

func TestPartyDissolved_FromCrashed(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()

	pt.ProcessEvent(newFormEvent("sid-leader", "uid-leader", "boss", baseTime))
	pt.ProcessEvent(newMemberJoinEvent("sid-f1", "uid-f1", "follower1", "sid-leader", ts(1)))
	pt.ProcessEvent(newEvent(EvtPartyReady, "sid-leader", "uid-leader", "boss", ts(2)))
	pt.ProcessEvent(newEvent(EvtFollowAttempt, "sid-f1", "uid-f1", "follower1", ts(3)))
	pt.ProcessEvent(newEvent(EvtFollowRetry, "sid-f1", "uid-f1", "follower1", ts(4)))
	pt.ProcessEvent(newEvent(EvtMemberCrashed, "sid-f1", "uid-f1", "follower1", ts(5)))
	pt.ProcessEvent(newEvent(EvtPartyDissolved, "sid-leader", "uid-leader", "boss", ts(10)))

	pl := pt.GetParty("sid-leader")
	if pl.State != StateAbandoned {
		t.Fatalf("state = %s, want ABANDONED", pl.State)
	}
	if pl.OutcomeInfo != "party dissolved after crash, no reconnect" {
		t.Fatalf("outcomeInfo = %q", pl.OutcomeInfo)
	}
}

// --- Event Log ---

func TestEventLog_ChronologicalOrder(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()

	pt.ProcessEvent(newFormEvent("sid-leader", "uid-leader", "boss", baseTime))
	pt.ProcessEvent(newEvent(EvtPartyReady, "sid-leader", "uid-leader", "boss", ts(2)))
	pt.ProcessEvent(newEvent(EvtMatchmakingSubmit, "sid-leader", "uid-leader", "boss", ts(5)))

	pl := pt.GetParty("sid-leader")
	// Formation event + 2 subsequent events.
	if len(pl.Events) != 3 {
		t.Fatalf("events = %d, want 3", len(pl.Events))
	}

	for i := 1; i < len(pl.Events); i++ {
		if pl.Events[i].GetTimestamp().Before(pl.Events[i-1].GetTimestamp()) {
			t.Fatalf("event %d (%v) is before event %d (%v)",
				i, pl.Events[i].GetTimestamp(),
				i-1, pl.Events[i-1].GetTimestamp(),
			)
		}
	}
}

// --- Transition Log ---

func TestTransitionLog(t *testing.T) {
	t.Parallel()
	pt := NewPartyTracker()

	pt.ProcessEvent(newFormEvent("sid-leader", "uid-leader", "boss", baseTime))
	pt.ProcessEvent(newEvent(EvtPartyReady, "sid-leader", "uid-leader", "boss", ts(2)))
	pt.ProcessEvent(newEvent(EvtMatchmakingSubmit, "sid-leader", "uid-leader", "boss", ts(5)))

	pl := pt.GetParty("sid-leader")
	if len(pl.Transitions) != 2 {
		t.Fatalf("transitions = %d, want 2", len(pl.Transitions))
	}

	// First transition: FORMING -> READY.
	if pl.Transitions[0].From != StateForming || pl.Transitions[0].To != StateReady {
		t.Fatalf("transition[0] = %s -> %s, want FORMING -> READY",
			pl.Transitions[0].From, pl.Transitions[0].To)
	}

	// Second transition: READY -> MATCHMAKING.
	if pl.Transitions[1].From != StateReady || pl.Transitions[1].To != StateMatchmaking {
		t.Fatalf("transition[1] = %s -> %s, want READY -> MATCHMAKING",
			pl.Transitions[1].From, pl.Transitions[1].To)
	}
}

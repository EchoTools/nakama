package server

import (
	"context"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Ghost-member regression tests for JoinPartyGroup.
//
// A party "ghost member" is a session that lives in ph.members but was never
// tracked on the party stream. Because disconnect eviction is entirely
// stream-driven (session close -> tracker untrack -> partyLeaveListener ->
// partyRegistry.Leave -> ph.Leave), a member that is in ph.members but not on
// the stream can never be evicted. It lingers, gets promoted to leader via
// Oldest(), and leads a party for hours.
//
// The root cause: JoinPartyGroup was not transactional. For an already-open
// group-name party it called ph.JoinRequest (which synchronously inserts into
// ph.members) and THEN called tracker.Track. When Track failed — which it does
// when the session's context is already canceled, because LocalTracker.Track
// guards on ctx.Err() — the added member was never removed and a just-created
// party was never deleted.
//
// These tests reproduce the two failure classes and assert the invariant:
//
//	ph.members must never retain a session that is not (or never became)
//	tracked on the party stream.
// ---------------------------------------------------------------------------

// ghostCtxTracker mirrors the real LocalTracker guard: Track fails when the
// session's context is already canceled. This is the exact production
// condition that produced the confirmed ghost (session bb06b40c).
type ghostCtxTracker struct {
	*mockMatchmakingTracker
}

func (t *ghostCtxTracker) Track(ctx context.Context, sessionID uuid.UUID, stream PresenceStream, userID uuid.UUID, meta PresenceMeta) (bool, bool) {
	if ctx.Err() != nil {
		return false, false
	}
	return t.mockMatchmakingTracker.Track(ctx, sessionID, stream, userID, meta)
}

// ghostFailTracker always fails Track, regardless of context state. It models
// a Track failure on a still-live session (e.g. the tracker's getSession race
// on a session tearing down) so the rollback path can be exercised
// deterministically without depending on fail-fast.
type ghostFailTracker struct {
	*mockMatchmakingTracker
}

func (t *ghostFailTracker) Track(ctx context.Context, sessionID uuid.UUID, stream PresenceStream, userID uuid.UUID, meta PresenceMeta) (bool, bool) {
	return false, false
}

// coMemberInjectTracker fails Track, but runs an injection hook first. It is
// used to deterministically reproduce the concurrency the party-delete guard
// protects against: the creator (user A) makes the group-name party
// (created=true) and relies on the async tracker Join for its own membership;
// before A's Track resolves, a concurrent joiner (user B) joins the
// just-created party and becomes a committed member of ph.members. A's Track
// then fails. The rollback must NOT delete the party out from under B.
type coMemberInjectTracker struct {
	*mockMatchmakingTracker
	inject func()
}

func (t *coMemberInjectTracker) Track(ctx context.Context, sessionID uuid.UUID, stream PresenceStream, userID uuid.UUID, meta PresenceMeta) (bool, bool) {
	if t.inject != nil {
		t.inject()
	}
	return false, false
}

// membersContainUser reports whether ph.members holds a presence for userID.
func membersContainUser(ph *PartyHandler, userID uuid.UUID) bool {
	for _, m := range ph.members.List() {
		if m.Presence.GetUserId() == userID.String() {
			return true
		}
	}
	return false
}

// TestJoinPartyGroup_NoGhost_OnDeadSessionJoin is RED test A.
//
// A group-name party already exists. A second joiner whose context is already
// canceled calls JoinPartyGroup. It must return an error AND must NOT leave a
// ghost member in ph.members.
//
// Against the pre-fix code this FAILS: JoinRequest adds the member, Track fails
// on the canceled context, and the member lingers as a ghost.
func TestJoinPartyGroup_NoGhost_OnDeadSessionJoin(t *testing.T) {
	logger := loggerForTest(t)
	tracker := &ghostCtxTracker{mockMatchmakingTracker: newMockMatchmakingTracker()}
	mm, mmCleanup := createLightMatchmaker(t, logger)
	defer mmCleanup()

	tsm := testStreamManager{}
	dmr := &DummyMessageRouter{}
	pr := NewLocalPartyRegistry(logger, cfg, mm, tracker, tsm, dmr, "testnode")

	groupName := "monarch12"
	currentMatchID := MatchID{}

	// First joiner has a live context and creates the group-name party.
	leaderSession := newTestSessionForParty(t, "leader", tracker, pr)
	leaderGroup, _, err := JoinPartyGroup(leaderSession, groupName, currentMatchID)
	require.NoError(t, err)
	require.NotNil(t, leaderGroup)
	ph := leaderGroup.ph

	// Second joiner's connection is already dying: its context is canceled.
	ghostSession := newTestSessionForParty(t, "ghost", tracker, pr)
	ghostSession.ctxCancelFn()

	_, _, err = JoinPartyGroup(ghostSession, groupName, currentMatchID)

	require.Error(t, err, "JoinPartyGroup must return an error for a dead-session join")
	assert.False(t, membersContainUser(ph, ghostSession.UserID()),
		"GHOST: dead session must NOT remain in ph.members after a failed join")
}

// TestJoinPartyGroup_NoOrphanParty_OnDeadSessionCreate is RED test B.
//
// A brand-new group name is joined by a session whose context is already
// canceled — so JoinPartyGroup would create the party. It must return an error
// AND must NOT leave an orphaned party registered under that group name.
//
// Against the pre-fix code this FAILS: the party is created, Track fails, and
// the orphan party lingers in the registry.
func TestJoinPartyGroup_NoOrphanParty_OnDeadSessionCreate(t *testing.T) {
	logger := loggerForTest(t)
	tracker := &ghostCtxTracker{mockMatchmakingTracker: newMockMatchmakingTracker()}
	mm, mmCleanup := createLightMatchmaker(t, logger)
	defer mmCleanup()

	tsm := testStreamManager{}
	dmr := &DummyMessageRouter{}
	pr := NewLocalPartyRegistry(logger, cfg, mm, tracker, tsm, dmr, "testnode")

	groupName := "divergent"
	currentMatchID := MatchID{}

	ghostSession := newTestSessionForParty(t, "ghost", tracker, pr)
	ghostSession.ctxCancelFn()

	_, _, err := JoinPartyGroup(ghostSession, groupName, currentMatchID)

	require.Error(t, err, "JoinPartyGroup must return an error for a dead-session create")

	_, found := pr.LookupGroupPartyID(groupName)
	assert.False(t, found,
		"ORPHAN: no party must remain registered for the group name after a failed create")
}

// TestJoinPartyGroup_Rollback_RemovesMember_OnTrackFailure exercises the
// transactional rollback directly (safeguard 2), independent of fail-fast: the
// session is LIVE but Track fails. The member added by JoinRequest must be
// rolled back out of ph.members.
func TestJoinPartyGroup_Rollback_RemovesMember_OnTrackFailure(t *testing.T) {
	logger := loggerForTest(t)
	tracker := &ghostFailTracker{mockMatchmakingTracker: newMockMatchmakingTracker()}
	mm, mmCleanup := createLightMatchmaker(t, logger)
	defer mmCleanup()

	tsm := testStreamManager{}
	dmr := &DummyMessageRouter{}
	pr := NewLocalPartyRegistry(logger, cfg, mm, tracker, tsm, dmr, "testnode")

	groupName := "monarch12"

	// Pre-create the party directly so it exists without needing a Track.
	leader := makeTestUserPresence("leader")
	ph, created, err := pr.GetOrCreateByGroupName(groupName, true, 4, leader)
	require.NoError(t, err)
	require.True(t, created)

	// Live session joins the existing open party; Track fails.
	joiner := newTestSessionForParty(t, "joiner", tracker, pr)
	_, _, err = JoinPartyGroup(joiner, groupName, MatchID{})

	require.Error(t, err, "JoinPartyGroup must return an error when Track fails")
	assert.False(t, membersContainUser(ph, joiner.UserID()),
		"GHOST: member added by JoinRequest must be rolled back when Track fails")
}

// TestJoinPartyGroup_Rollback_DeletesParty_OnTrackFailure exercises the
// party-creation rollback directly: a LIVE session creates a new party but
// Track fails; the just-created party must be deleted from the registry.
func TestJoinPartyGroup_Rollback_DeletesParty_OnTrackFailure(t *testing.T) {
	logger := loggerForTest(t)
	tracker := &ghostFailTracker{mockMatchmakingTracker: newMockMatchmakingTracker()}
	mm, mmCleanup := createLightMatchmaker(t, logger)
	defer mmCleanup()

	tsm := testStreamManager{}
	dmr := &DummyMessageRouter{}
	pr := NewLocalPartyRegistry(logger, cfg, mm, tracker, tsm, dmr, "testnode")

	groupName := "divergent"

	joiner := newTestSessionForParty(t, "joiner", tracker, pr)
	_, _, err := JoinPartyGroup(joiner, groupName, MatchID{})

	require.Error(t, err, "JoinPartyGroup must return an error when Track fails on create")

	_, found := pr.LookupGroupPartyID(groupName)
	assert.False(t, found,
		"ORPHAN: the just-created party must be deleted from the registry when Track fails")
}

// TestJoinPartyGroup_Rollback_PreservesParty_WithCoMember_OnTrackFailure proves
// the party-delete rollback is guarded on emptiness. The creator (user A) makes
// a new group-name party (created=true); before A's Track resolves, a concurrent
// joiner (user B) becomes a committed member. A's Track then fails.
//
// Because B is a legitimate member, the rollback must NOT delete the party.
// Against unconditional `if created { Delete }` this FAILS: A deletes the party
// out from under B. With the `ph.members.Size() == 0` guard it PASSES: the
// non-empty party is preserved and B is retained.
func TestJoinPartyGroup_Rollback_PreservesParty_WithCoMember_OnTrackFailure(t *testing.T) {
	logger := loggerForTest(t)
	tracker := &coMemberInjectTracker{mockMatchmakingTracker: newMockMatchmakingTracker()}
	mm, mmCleanup := createLightMatchmaker(t, logger)
	defer mmCleanup()

	tsm := testStreamManager{}
	dmr := &DummyMessageRouter{}
	pr := NewLocalPartyRegistry(logger, cfg, mm, tracker, tsm, dmr, "testnode")

	groupName := "insurgent"

	// User B is the concurrent co-member. Build B's presence up front so the
	// injection hook can add it to ph.members exactly as a real JoinRequest on
	// an open party would (members.Join).
	bUserID := uuid.Must(uuid.NewV4())
	bSessionID := uuid.Must(uuid.NewV4())
	bPresence := &Presence{
		ID:     PresenceID{Node: "testnode", SessionID: bSessionID},
		UserID: bUserID,
		Meta:   PresenceMeta{Username: "co-member"},
	}

	// The hook runs during the creator's Track: the party already exists in the
	// registry by then, so look it up and add B as a committed member.
	tracker.inject = func() {
		id, ok := pr.LookupGroupPartyID(groupName)
		require.True(t, ok, "party must exist in registry during the creator's Track")
		ph, ok := pr.Get(id)
		require.True(t, ok)
		_, err := ph.members.Join([]*Presence{bPresence})
		require.NoError(t, err)
	}

	// User A (live context) creates the group-name party; its Track fails after
	// B has joined.
	creator := newTestSessionForParty(t, "creator", tracker, pr)
	_, _, err := JoinPartyGroup(creator, groupName, MatchID{})
	require.Error(t, err, "creator's join must fail when Track fails")

	// The party must be preserved because B is a committed member.
	id, found := pr.LookupGroupPartyID(groupName)
	require.True(t, found,
		"party with a co-member must NOT be deleted when the creator's Track fails")
	ph, ok := pr.Get(id)
	require.True(t, ok, "party handler must still be registered")
	assert.True(t, membersContainUser(ph, bUserID),
		"co-member B must remain in ph.members after the creator's failed join")
	assert.False(t, membersContainUser(ph, creator.UserID()),
		"creator must not linger in ph.members (its Track never succeeded)")
}

// TestJoinPartyGroup_HappyPath_JoinsExistingParty is the control/regression:
// a healthy session (live context, successful Track) joins an existing party
// and ends up in ph.members. This must pass before and after the fix.
func TestJoinPartyGroup_HappyPath_JoinsExistingParty(t *testing.T) {
	logger := loggerForTest(t)
	tracker := &ghostCtxTracker{mockMatchmakingTracker: newMockMatchmakingTracker()}
	mm, mmCleanup := createLightMatchmaker(t, logger)
	defer mmCleanup()

	tsm := testStreamManager{}
	dmr := &DummyMessageRouter{}
	pr := NewLocalPartyRegistry(logger, cfg, mm, tracker, tsm, dmr, "testnode")

	groupName := "monarch12"

	// Pre-create the open party.
	leader := makeTestUserPresence("leader")
	ph, created, err := pr.GetOrCreateByGroupName(groupName, true, 4, leader)
	require.NoError(t, err)
	require.True(t, created)

	// Healthy joiner with a live context joins successfully.
	joiner := newTestSessionForParty(t, "joiner", tracker, pr)
	lobbyGroup, _, err := JoinPartyGroup(joiner, groupName, MatchID{})

	require.NoError(t, err, "healthy join must succeed")
	require.NotNil(t, lobbyGroup)
	assert.True(t, membersContainUser(ph, joiner.UserID()),
		"healthy joiner must be present in ph.members")
}

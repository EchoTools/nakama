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

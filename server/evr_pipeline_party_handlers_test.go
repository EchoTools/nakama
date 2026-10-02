package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
)

// This file pins what the game service (Nakama) sends each game client when the in-game "tablet" SNS
// party handlers run: join, accept an invite, set the join policy, pass ownership. Every test decodes the
// bytes each session's outgoing queue received (evr.ParsePacket), so what is asserted is what the game
// client would have been sent, in the order it would have received it.
//
// The fixtures need the test database (NewDB): a member's account id on the wire is the Discord id in
// users.custom_id, and friendships are user_edge rows.

// handlerTracker is the party-stream tracker with a real Untrack and CountByStream (the party leave
// path needs them), and a switch that makes Track fail.
type handlerTracker struct {
	*partyStreamTracker
	failTrack atomic.Bool
}

func (t *handlerTracker) Track(ctx context.Context, sessionID uuid.UUID, stream PresenceStream, userID uuid.UUID, meta PresenceMeta) (bool, bool) {
	if t.failTrack.Load() {
		return false, false
	}
	return t.partyStreamTracker.Track(ctx, sessionID, stream, userID, meta)
}

func (t *handlerTracker) Untrack(sessionID uuid.UUID, stream PresenceStream, userID uuid.UUID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.presences, presenceKey{sessionID: sessionID, stream: stream, userID: userID})
}

func (t *handlerTracker) CountByStream(stream PresenceStream) int {
	return len(t.ListByStream(stream, true, true))
}

// partyHandlerEnv is a pipeline wired for every SNS party handler: a real party registry, a tracker that
// lists the party stream, the session registry, every SNS map, and the test database.
type partyHandlerEnv struct {
	t        *testing.T
	ep       *EvrPipeline
	db       *sql.DB
	pr       *LocalPartyRegistry
	tracker  *handlerTracker
	sessions *sessionMapRegistry
	accounts map[*sessionWS]uint64 // each session's account id on the wire (its Discord id)
	nextID   uint64
}

var partyHandlerAccountSeq atomic.Uint64

func newPartyHandlerEnv(t *testing.T) *partyHandlerEnv {
	t.Helper()
	db := NewDB(t)
	t.Cleanup(func() { _ = db.Close() })
	logger := loggerForTest(t)
	tracker := &handlerTracker{partyStreamTracker: newPartyStreamTracker()}
	mm, mmCleanup := createLightMatchmaker(t, logger)
	t.Cleanup(mmCleanup)
	pr := NewLocalPartyRegistry(logger, cfg, mm, tracker, testStreamManager{}, &DummyMessageRouter{}, "testnode").(*LocalPartyRegistry)
	sessions := &sessionMapRegistry{sessions: map[uuid.UUID]Session{}}
	ep := &EvrPipeline{
		node:   "testnode",
		db:     db,
		config: NewConfig(logger),
		nk: &RuntimeGoNakamaModule{
			logger:          logger,
			partyRegistry:   pr,
			tracker:         tracker,
			sessionRegistry: sessions,
			node:            "testnode",
		},
		snsPartyIDToUUID: &MapOf[uint64, uuid.UUID]{},
		snsPartyUUIDToID: &MapOf[uuid.UUID, uint64]{},
		evrUUIDToUserID:  &MapOf[uuid.UUID, uuid.UUID]{},
		snsPartyInvites:  &MapOf[uuid.UUID, *snsPartyInviteList]{},
		snsPartyPolicies: &MapOf[uuid.UUID, uint8]{},
		snsPartyData:     &MapOf[uuid.UUID, *snsPartyDataState]{},
	}
	return &partyHandlerEnv{t: t, ep: ep, db: db, pr: pr, tracker: tracker, sessions: sessions,
		accounts: map[*sessionWS]uint64{}, nextID: 1000}
}

// session is a game client session logged in as a user whose Discord id is its account id. Runtime
// clients declare social level 1 at login (they read party data), stock clients 0. The session is online
// (a status presence), as a logged-in user is.
func (e *partyHandlerEnv) session(name string, runtimeClient bool) *sessionWS {
	e.t.Helper()
	s := newPartyMemberSession(e.t, name, e.tracker, e.pr, e.ep)
	account := uint64(700_000_000_000_000_000) + uint64(time.Now().UnixNano()%1_000_000_000)*1000 + partyHandlerAccountSeq.Add(1)
	e.insertUser(s.userID, name, fmt.Sprint(account))
	e.accounts[s] = account
	params, _ := LoadParams(s.Context())
	level := 0
	if runtimeClient {
		level = 1
	}
	params.loginPayload = &evr.LoginProfile{NevrSocial: level}
	params.xpID = evr.EvrId{PlatformCode: evr.DSC, AccountId: account}
	e.sessions.sessions[s.id] = s
	e.tracker.Track(context.Background(), s.id, PresenceStream{Mode: StreamModeStatus, Subject: s.userID}, s.userID, PresenceMeta{})
	return s
}

// insertUser adds a user row (customID is the Discord id) and removes it when the test ends.
func (e *partyHandlerEnv) insertUser(userID uuid.UUID, username, customID string) {
	e.t.Helper()
	var custom any
	if customID != "" {
		custom = customID
	}
	_, err := e.db.Exec(`INSERT INTO users (id, username, custom_id) VALUES ($1, $2, $3)`,
		userID, fmt.Sprintf("%s-%s", username, userID), custom)
	require.NoError(e.t, err)
	e.t.Cleanup(func() { _, _ = e.db.Exec(`DELETE FROM users WHERE id = $1`, userID) })
}

// befriend writes the mutual friendship both directions of an accepted friend request leave behind
// (user_edge state 0).
func (e *partyHandlerEnv) befriend(a, b *sessionWS) {
	e.t.Helper()
	e.edge(a.userID, b.userID, 0)
	e.edge(b.userID, a.userID, 0)
}

func (e *partyHandlerEnv) edge(from, to uuid.UUID, state int) {
	e.t.Helper()
	e.nextID++
	_, err := e.db.Exec(`INSERT INTO user_edge (source_id, position, destination_id, state) VALUES ($1, $2, $3, $4)`,
		from, time.Now().UnixNano()+int64(e.nextID), to, state)
	require.NoError(e.t, err)
}

// party makes an open SNS party of up to 4, led by the first session, with every session a member on the
// party stream and in the party handler, as snsPartyCreateRequest and the join handlers leave it.
func (e *partyHandlerEnv) party(snsID uint64, members ...*sessionWS) *PartyHandler {
	e.t.Helper()
	leader := members[0]
	ph := e.pr.Create(true, 4, &rtapi.UserPresence{
		UserId: leader.userID.String(), SessionId: leader.id.String(), Username: leader.Username(),
	})
	presences := make([]*Presence, 0, len(members))
	stream := PresenceStream{Mode: StreamModeParty, Subject: ph.ID, Label: "testnode"}
	for _, m := range members {
		presences = append(presences, &Presence{
			ID:     PresenceID{SessionID: m.id, Node: "testnode"},
			UserID: m.userID,
			Meta:   PresenceMeta{Username: m.Username()},
		})
		e.tracker.Track(context.Background(), m.id, stream, m.userID, PresenceMeta{Username: m.Username()})
		params, _ := LoadParams(m.Context())
		params.currentSNSPartyID = snsID
		params.currentPartyID = ph.ID
		e.ep.registerEvrUUIDMapping(params.xpID.UUID(), m.userID)
	}
	ph.Join(presences)
	e.ep.snsPartyIDToUUID.Store(snsID, ph.ID)
	e.ep.snsPartyUUIDToID.Store(ph.ID, snsID)
	return ph
}

// inParty reports whether the session is on the party's stream.
func (e *partyHandlerEnv) inParty(s *sessionWS, ph *PartyHandler) bool {
	return e.tracker.hasPresence(s.id, PresenceStream{Mode: StreamModeParty, Subject: ph.ID, Label: "testnode"}, s.userID)
}

func (e *partyHandlerEnv) currentParty(s *sessionWS) (uuid.UUID, uint64) {
	params, _ := LoadParams(s.Context())
	return params.currentPartyID, params.currentSNSPartyID
}

// sentTo is every message the session's game client was sent since the last call, in order.
func sentTo(t *testing.T, s *sessionWS) []evr.Message {
	t.Helper()
	out := []evr.Message{}
	for _, payload := range drain(s.outgoingCh) {
		msgs, err := evr.ParsePacket(payload)
		require.NoError(t, err)
		out = append(out, msgs...)
	}
	return out
}

// typeNames is the message types, for asserting order at a glance.
func typeNames(msgs []evr.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, fmt.Sprintf("%T", m))
	}
	return out
}

// dataNotifyJSON decodes the JSON a party data notify carries.
func dataNotifyJSON(t *testing.T, n *evr.SNSPartyDataNotify) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(n.Json, &out))
	return out
}

const (
	typeDataNotify  = "*evr.SNSPartyDataNotify"
	typeJoinNotify  = "*evr.SNSPartyJoinNotify"
	typeJoinSuccess = "*evr.SNSPartyJoinSuccess"
)

// setOpen locks (false) or unlocks (true) a party, as the leader's Lock and Unlock do.
func (e *partyHandlerEnv) setOpen(ph *PartyHandler, open bool) {
	ph.Lock()
	ph.Open = open
	ph.Unlock()
}

// A party is open until its leader locks it: Lock and Unlock flip PartyHandler.Open, and the join
// handler reads it to decide whether the join policy applies.
func TestSNSPartyIsOpenFollowsLockAndUnlock(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader := e.session("leader", true)
	ph := e.party(10, leader)
	ctx := leader.Context()
	require.True(t, snsPartyIsOpen(ph), "a new SNS party is open")

	require.NoError(t, e.ep.snsPartyLockRequest(ctx, loggerForTest(t), leader, &evr.SNSPartyLockRequest{}))
	require.False(t, snsPartyIsOpen(ph), "locked")
	// The lock is announced to every member, the leader included, before the success.
	require.Equal(t, []string{"*evr.SNSPartyLockNotify", "*evr.SNSPartyLockSuccess"}, typeNames(sentTo(t, leader)))

	require.NoError(t, e.ep.snsPartyUnlockRequest(ctx, loggerForTest(t), leader, &evr.SNSPartyUnlockRequest{}))
	require.True(t, snsPartyIsOpen(ph), "unlocked")
}

// mutualFriendsAmong asks which of some users are accepted friends of one user. Only user_edge state 0
// counts: a sent invite (1), a blocked user (3) and a stranger are not friends, and asking about nobody
// asks the database nothing.
func TestMutualFriendsAmong(t *testing.T) {
	e := newPartyHandlerEnv(t)
	me, friend, invited, blocked, stranger := e.session("me", true), e.session("friend", true),
		e.session("invited", true), e.session("blocked", true), e.session("stranger", true)
	e.befriend(me, friend)
	e.edge(me.userID, invited.userID, 1)
	e.edge(me.userID, blocked.userID, 3)

	got, err := mutualFriendsAmong(context.Background(), e.db, me.userID,
		[]uuid.UUID{friend.userID, invited.userID, blocked.userID, stranger.userID})
	require.NoError(t, err)
	require.Equal(t, map[uuid.UUID]bool{friend.userID: true}, got)

	// No one to ask about: an empty answer, with no database (a nil one would panic if it were used).
	got, err = mutualFriendsAmong(context.Background(), nil, me.userID, nil)
	require.NoError(t, err)
	require.Empty(t, got)

	// A database that cannot answer is an error, not "no friends".
	dead := NewDB(t)
	require.NoError(t, dead.Close())
	_, err = mutualFriendsAmong(context.Background(), dead, me.userID, []uuid.UUID{friend.userID})
	require.Error(t, err)
}

// snsPartyJoinAllowed applies the party's join policy to a player who asks to join by id. Everyone admits
// anyone; invite only admits only an invited player; friends admits a mutual friend of the leader;
// friends of members admits a mutual friend of any member, the leader included. An invite beats any policy.
func TestSNSPartyJoinAllowedAppliesThePolicy(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader, member := e.session("leader", true), e.session("member", true)
	ph := e.party(20, leader, member)
	ctx := context.Background()

	leaderFriend, memberFriend, stranger := e.session("lf", true), e.session("mf", true), e.session("stranger", true)
	e.befriend(leaderFriend, leader)
	e.befriend(memberFriend, member)

	cases := []struct {
		name   string
		policy uint8
		joiner *sessionWS
		want   bool
	}{
		{"everyone admits a stranger", snsPartyPolicyEveryone, stranger, true},
		{"invite only refuses a friend of the leader", snsPartyPolicyInviteOnly, leaderFriend, false},
		{"friends admits a friend of the leader", snsPartyPolicyFriends, leaderFriend, true},
		{"friends refuses a friend of a member only", snsPartyPolicyFriends, memberFriend, false},
		{"friends refuses a stranger", snsPartyPolicyFriends, stranger, false},
		{"friends of members admits a friend of a member", snsPartyPolicyFriendsOfMembers, memberFriend, true},
		{"friends of members admits a friend of the leader", snsPartyPolicyFriendsOfMembers, leaderFriend, true},
		{"friends of members refuses a stranger", snsPartyPolicyFriendsOfMembers, stranger, false},
	}
	for _, c := range cases {
		e.ep.snsPartyPolicies.Store(ph.ID, c.policy)
		allowed, policy, err := e.ep.snsPartyJoinAllowed(ctx, c.joiner.userID, ph.ID, ph)
		require.NoError(t, err, c.name)
		require.Equal(t, c.policy, policy, c.name)
		require.Equal(t, c.want, allowed, c.name)
	}

	// An invite is admitted under every policy, a stranger included.
	list := &snsPartyInviteList{}
	list.Add(&snsPartyInvite{PartyUUID: ph.ID})
	e.ep.snsPartyInvites.Store(stranger.userID, list)
	for _, policy := range []uint8{snsPartyPolicyInviteOnly, snsPartyPolicyFriends, snsPartyPolicyFriendsOfMembers, snsPartyPolicyEveryone} {
		e.ep.snsPartyPolicies.Store(ph.ID, policy)
		allowed, _, err := e.ep.snsPartyJoinAllowed(ctx, stranger.userID, ph.ID, ph)
		require.NoError(t, err)
		require.True(t, allowed, "an invited player is admitted under policy %d", policy)
	}
}

// A friendship lookup that fails is an error to the caller, not a refusal: the join handler answers it
// with PartyJoinFailure 2 instead of telling the player they have no permission.
func TestSNSPartyJoinAllowedReportsAFailedFriendLookup(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader, joiner := e.session("leader", true), e.session("joiner", true)
	ph := e.party(21, leader)
	e.ep.snsPartyPolicies.Store(ph.ID, snsPartyPolicyFriends)
	dead := NewDB(t)
	require.NoError(t, dead.Close())
	e.ep.db = dead

	allowed, policy, err := e.ep.snsPartyJoinAllowed(context.Background(), joiner.userID, ph.ID, ph)
	require.Error(t, err)
	require.False(t, allowed)
	require.Equal(t, snsPartyPolicyFriends, policy)
}

// The party leader's game sets the join policy (SetJoinPolicyRequest, a number 0 to 3). The game service
// keeps it, tells the other members the party changed (SNSPartyUpdateNotify) and answers the leader with
// SNSPartyUpdateSuccess. The leader is not sent the notify.
func TestSNSPartySetJoinPolicyByTheLeader(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader, member := e.session("leader", true), e.session("member", true)
	ph := e.party(30, leader, member)

	require.NoError(t, e.ep.snsPartySetJoinPolicyRequest(leader.Context(), loggerForTest(t), leader,
		&evr.SNSPartySetJoinPolicyRequest{TargetParam: uint64(snsPartyPolicyFriendsOfMembers)}))

	require.Equal(t, snsPartyPolicyFriendsOfMembers, e.ep.snsPartyPolicy(ph.ID))
	got := sentTo(t, leader)
	require.Len(t, got, 1)
	require.Equal(t, &evr.SNSPartyUpdateSuccess{PartyID: 30}, got[0])
	got = sentTo(t, member)
	require.Len(t, got, 1)
	require.Equal(t, &evr.SNSPartyUpdateNotify{PartyID: 30}, got[0])
}

// Only the leader sets the policy, and only to a value the game knows (0 to 3). A member who tries, or a
// leader who sends 4, is answered SNSPartyUpdateFailure with code 2, the policy stays what it was, and
// nobody else is told anything.
func TestSNSPartySetJoinPolicyRefusals(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader, member := e.session("leader", true), e.session("member", true)
	ph := e.party(31, leader, member)
	e.ep.snsPartyPolicies.Store(ph.ID, snsPartyPolicyFriends)

	// A member is not the leader.
	require.NoError(t, e.ep.snsPartySetJoinPolicyRequest(member.Context(), loggerForTest(t), member,
		&evr.SNSPartySetJoinPolicyRequest{TargetParam: uint64(snsPartyPolicyEveryone)}))
	require.Equal(t, []evr.Message{&evr.SNSPartyUpdateFailure{ErrorCode: 2}}, sentTo(t, member))
	require.Empty(t, sentTo(t, leader))
	require.Equal(t, snsPartyPolicyFriends, e.ep.snsPartyPolicy(ph.ID))

	// The leader sends a value the game has no policy for.
	require.NoError(t, e.ep.snsPartySetJoinPolicyRequest(leader.Context(), loggerForTest(t), leader,
		&evr.SNSPartySetJoinPolicyRequest{TargetParam: uint64(snsPartyPolicyEveryone) + 1}))
	require.Equal(t, []evr.Message{&evr.SNSPartyUpdateFailure{ErrorCode: 2}}, sentTo(t, leader))
	require.Empty(t, sentTo(t, member))
	require.Equal(t, snsPartyPolicyFriends, e.ep.snsPartyPolicy(ph.ID))
}

// A player who is in no party, or whose party the game service no longer has, is answered
// SNSPartyUpdateFailure with code 1; a message of the wrong type is an error.
func TestSNSPartySetJoinPolicyWithoutAParty(t *testing.T) {
	e := newPartyHandlerEnv(t)
	solo := e.session("solo", true)
	require.NoError(t, e.ep.snsPartySetJoinPolicyRequest(solo.Context(), loggerForTest(t), solo,
		&evr.SNSPartySetJoinPolicyRequest{TargetParam: 1}))
	require.Equal(t, []evr.Message{&evr.SNSPartyUpdateFailure{ErrorCode: 1}}, sentTo(t, solo))

	params, _ := LoadParams(solo.Context())
	params.currentPartyID = uuid.Must(uuid.NewV4()) // a party the registry does not have
	require.NoError(t, e.ep.snsPartySetJoinPolicyRequest(solo.Context(), loggerForTest(t), solo,
		&evr.SNSPartySetJoinPolicyRequest{TargetParam: 1}))
	require.Equal(t, []evr.Message{&evr.SNSPartyUpdateFailure{ErrorCode: 1}}, sentTo(t, solo))

	require.Error(t, e.ep.snsPartySetJoinPolicyRequest(solo.Context(), loggerForTest(t), solo, &evr.SNSPartyLockRequest{}))
}

// Joining a party by another route than an explicit leave ends the player's old party membership the way
// an explicit leave does: the old party's other members are sent SNSPartyLeaveNotify naming the leaver by
// account id, the player leaves the old party's stream, and their party fields clear. When they were the
// last member, the old party's policy and data go with it.
func TestSNSPartyLeaveForJoinLeavesTheOldParty(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leaver, stays := e.session("leaver", true), e.session("stays", true)
	old := e.party(40, stays, leaver)
	params, _ := LoadParams(leaver.Context())

	e.ep.snsPartyLeaveForJoin(context.Background(), loggerForTest(t), leaver, params, uuid.Must(uuid.NewV4()))

	require.Equal(t, []evr.Message{&evr.SNSPartyLeaveNotify{PartyID: 40, MemberID: e.accounts[leaver]}}, sentTo(t, stays))
	require.Empty(t, sentTo(t, leaver), "the leaver is told nothing by the leave")
	require.False(t, e.inParty(leaver, old))
	require.True(t, e.inParty(stays, old))
	id, snsID := e.currentParty(leaver)
	require.Equal(t, uuid.Nil, id)
	require.Zero(t, snsID)

	// The last member leaving ends the party: its policy and data are dropped.
	e.ep.snsPartyPolicies.Store(old.ID, snsPartyPolicyFriends)
	e.ep.snsPartyData.Store(old.ID, newSNSPartyDataState())
	stayParams, _ := LoadParams(stays.Context())
	e.ep.snsPartyLeaveForJoin(context.Background(), loggerForTest(t), stays, stayParams, uuid.Must(uuid.NewV4()))
	_, kept := e.ep.snsPartyPolicies.Load(old.ID)
	require.False(t, kept)
	_, kept = e.ep.snsPartyData.Load(old.ID)
	require.False(t, kept)
}

// A player in no party, or already in the party being joined, has nothing to leave: nobody is told
// anything and the player stays where they are.
func TestSNSPartyLeaveForJoinIsANoOpWithNothingToLeave(t *testing.T) {
	e := newPartyHandlerEnv(t)
	a, b := e.session("a", true), e.session("b", true)
	ph := e.party(41, a, b)
	paramsA, _ := LoadParams(a.Context())

	e.ep.snsPartyLeaveForJoin(context.Background(), loggerForTest(t), a, paramsA, ph.ID) // the same party
	require.Empty(t, sentTo(t, b))
	require.True(t, e.inParty(a, ph))

	solo := e.session("solo", true)
	paramsSolo, _ := LoadParams(solo.Context())
	e.ep.snsPartyLeaveForJoin(context.Background(), loggerForTest(t), solo, paramsSolo, ph.ID) // no party
	require.Empty(t, sentTo(t, b))
	require.Empty(t, sentTo(t, solo))
}

// snsPartyTrackAndJoin puts the session on the party stream and records where it is: its party fields, the
// two ids other members use to address it (the UUID of the EvrId it logged in with, and the UUID of the
// OVR-ORG id built from its Discord id, which is what every other client is shown), and a watcher that
// leaves the party when the session ends.
func TestSNSPartyTrackAndJoinRecordsTheMember(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader, joiner := e.session("leader", true), e.session("joiner", true)
	ph := e.party(50, leader)
	params, _ := LoadParams(joiner.Context())

	require.NoError(t, e.ep.snsPartyTrackAndJoin(context.Background(), loggerForTest(t), joiner, ph.ID, 50, params))

	require.True(t, e.inParty(joiner, ph))
	id, snsID := e.currentParty(joiner)
	require.Equal(t, ph.ID, id)
	require.EqualValues(t, 50, snsID)
	got, ok := e.ep.resolveEvrUUIDToUserID(params.xpID.UUID())
	require.True(t, ok)
	require.Equal(t, joiner.userID, got)
	got, ok = e.ep.resolveEvrUUIDToUserID(evr.EvrId{PlatformCode: evr.OVR_ORG, AccountId: e.accounts[joiner]}.UUID())
	require.True(t, ok, "members address each other by the OVR-ORG id built from the Discord id")
	require.Equal(t, joiner.userID, got)

	// The session ending leaves the party, as a disconnected game client sends no leave.
	joiner.ctxCancelFn()
	require.Eventually(t, func() bool { return !e.inParty(joiner, ph) }, 5*time.Second, 10*time.Millisecond,
		"a closed session must leave its party")
}

// A tracker that refuses the presence fails the join with an error, and nothing about the session's party
// changes.
func TestSNSPartyTrackAndJoinFailsWhenTheTrackerRefuses(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader, joiner := e.session("leader", true), e.session("joiner", true)
	ph := e.party(51, leader)
	params, _ := LoadParams(joiner.Context())
	e.tracker.failTrack.Store(true)

	require.Error(t, e.ep.snsPartyTrackAndJoin(context.Background(), loggerForTest(t), joiner, ph.ID, 51, params))
	require.False(t, e.inParty(joiner, ph))
	id, _ := e.currentParty(joiner)
	require.Equal(t, uuid.Nil, id)
}

// With the single-party session setting on, joining a party takes the session off every other party
// stream.
func TestSNSPartyTrackAndJoinSinglePartyLeavesOtherStreams(t *testing.T) {
	e := newPartyHandlerEnv(t)
	e.ep.config.GetSession().SingleParty = true
	a, b := e.session("a", true), e.session("b", true)
	first, second := e.party(52, a), e.party(53, b)
	params, _ := LoadParams(a.Context())

	require.NoError(t, e.ep.snsPartyTrackAndJoin(context.Background(), loggerForTest(t), a, second.ID, 53, params))
	require.True(t, e.inParty(a, second))
	require.False(t, e.inParty(a, first), "single party: the old party stream is left")
}

// A game client asks to join a party by its SNS id. The game service admits it and sends, in this order:
// to the joiner, the party's data and then every other member's data, then PartyJoinSuccess naming the
// leader as owner; to each existing member, the joiner's data, then PartyJoinNotify naming the joiner by
// account id. Data first so each side already holds the other's data when the join is announced.
func TestSNSPartyJoinRequestSendsDataBeforeTheJoinIsAnnounced(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader, member, joiner := e.session("leader", true), e.session("member", true), e.session("joiner", true)
	ph := e.party(60, leader, member)
	state := e.ep.partyDataState(ph.ID)
	state.store(snsPartyDataScopeParty, leader.id, 1, map[string]any{"mode": "arena", "lobbyid": "the client lies"})

	require.NoError(t, e.ep.snsPartyJoinRequest(joiner.Context(), loggerForTest(t), joiner, &evr.SNSPartyJoinRequest{PartyID: 60}))

	// The joiner: party data, the two members' data (either order), then the success.
	got := sentTo(t, joiner)
	require.Equal(t, []string{typeDataNotify, typeDataNotify, typeDataNotify, typeJoinSuccess}, typeNames(got))
	partyData := got[0].(*evr.SNSPartyDataNotify)
	require.EqualValues(t, 60, partyData.PartyID)
	require.Zero(t, partyData.MemberID, "member id 0 is the party's data on the wire")
	require.EqualValues(t, 1, partyData.Seq)
	keys := dataNotifyJSON(t, partyData)
	require.Equal(t, "arena", keys["mode"], "the leader's script keys are kept")
	require.Equal(t, snsPartyNoLobbyID, keys["lobbyid"], "the game service's keys override the client's")
	require.Equal(t, false, keys["offline"])
	memberIDs := []uint64{got[1].(*evr.SNSPartyDataNotify).MemberID, got[2].(*evr.SNSPartyDataNotify).MemberID}
	require.ElementsMatch(t, []uint64{e.accounts[leader], e.accounts[member]}, memberIDs)
	require.Equal(t, &evr.SNSPartyJoinSuccess{PartyID: 60, OwnerID: e.accounts[leader]}, got[3])

	// Each existing member: the joiner's data, then the join announcement.
	for _, existing := range []*sessionWS{leader, member} {
		got = sentTo(t, existing)
		require.Equal(t, []string{typeDataNotify, typeJoinNotify}, typeNames(got))
		data := got[0].(*evr.SNSPartyDataNotify)
		require.Equal(t, e.accounts[joiner], data.MemberID)
		require.Equal(t, 0, int(dataNotifyJSON(t, data)["headsettype"].(float64)), "the member's data carries the headset type the game reads")
		require.Equal(t, &evr.SNSPartyJoinNotify{PartyID: 60, MemberID: e.accounts[joiner]}, got[1])
	}
	require.True(t, e.inParty(joiner, ph))
	id, snsID := e.currentParty(joiner)
	require.Equal(t, ph.ID, id)
	require.EqualValues(t, 60, snsID)
}

// Party data goes only to game clients that read it (social level 1 or more). A stock game client in the
// party is sent the join announcement and nothing else, and a stock joiner is sent only PartyJoinSuccess,
// while the runtime members are still sent the joiner's data.
func TestSNSPartyJoinRequestSendsNoDataToStockClients(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader, stock := e.session("leader", true), e.session("stock", false)
	e.party(61, leader, stock)
	joiner := e.session("stockjoiner", false)

	require.NoError(t, e.ep.snsPartyJoinRequest(joiner.Context(), loggerForTest(t), joiner, &evr.SNSPartyJoinRequest{PartyID: 61}))

	require.Equal(t, []string{typeJoinSuccess}, typeNames(sentTo(t, joiner)), "a stock joiner is sent no party data")
	require.Equal(t, []string{typeJoinNotify}, typeNames(sentTo(t, stock)), "a stock member is sent no SNSPartyDataNotify")
	require.Equal(t, []string{typeDataNotify, typeJoinNotify}, typeNames(sentTo(t, leader)),
		"the runtime leader still gets the stock joiner's data")
}

// A party of stock game clients with a stock joiner: nobody reads party data, so none is sent. The join
// itself still completes.
func TestSNSPartyJoinRequestAllStockSendsNoData(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader := e.session("leader", false)
	e.party(62, leader)
	joiner := e.session("joiner", false)

	require.NoError(t, e.ep.snsPartyJoinRequest(joiner.Context(), loggerForTest(t), joiner, &evr.SNSPartyJoinRequest{PartyID: 62}))
	require.Equal(t, []string{typeJoinSuccess}, typeNames(sentTo(t, joiner)))
	require.Equal(t, []string{typeJoinNotify}, typeNames(sentTo(t, leader)))
}

// A game client names a party the game service does not know: PartyJoinFailure code 1 (not found), and
// the client's own party is untouched. So is a request with no session parameters.
func TestSNSPartyJoinRequestUnknownParty(t *testing.T) {
	e := newPartyHandlerEnv(t)
	joiner := e.session("joiner", true)
	require.NoError(t, e.ep.snsPartyJoinRequest(joiner.Context(), loggerForTest(t), joiner, &evr.SNSPartyJoinRequest{PartyID: 999}))
	require.Equal(t, []evr.Message{&evr.SNSPartyJoinFailure{PartyID: 999, ErrorCode: 1}}, sentTo(t, joiner))

	// The id maps to a party the registry no longer has: the registry's answer is "not found" too.
	e.ep.snsPartyIDToUUID.Store(998, uuid.Must(uuid.NewV4()))
	require.NoError(t, e.ep.snsPartyJoinRequest(joiner.Context(), loggerForTest(t), joiner, &evr.SNSPartyJoinRequest{PartyID: 998}))
	require.Equal(t, []evr.Message{&evr.SNSPartyJoinFailure{PartyID: 998, ErrorCode: 1}}, sentTo(t, joiner))

	// No session parameters in the context.
	require.NoError(t, e.ep.snsPartyJoinRequest(context.Background(), loggerForTest(t), joiner, &evr.SNSPartyJoinRequest{PartyID: 999}))
	require.Equal(t, []evr.Message{&evr.SNSPartyJoinFailure{PartyID: 999, ErrorCode: 1}}, sentTo(t, joiner))

	require.Error(t, e.ep.snsPartyJoinRequest(joiner.Context(), loggerForTest(t), joiner, &evr.SNSPartyLockRequest{}))
}

// An open invite-only party refuses a stranger with PartyJoinFailure code 3 (no permission), before the
// registry is asked to admit anyone: the stranger is not a member and the members hear nothing. An invited
// stranger is admitted to the same party.
func TestSNSPartyJoinRequestRefusedByPolicy(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader, stranger := e.session("leader", true), e.session("stranger", true)
	ph := e.party(63, leader)
	e.ep.snsPartyPolicies.Store(ph.ID, snsPartyPolicyInviteOnly)

	require.NoError(t, e.ep.snsPartyJoinRequest(stranger.Context(), loggerForTest(t), stranger, &evr.SNSPartyJoinRequest{PartyID: 63}))
	require.Equal(t, []evr.Message{&evr.SNSPartyJoinFailure{PartyID: 63, ErrorCode: 3}}, sentTo(t, stranger))
	require.Empty(t, sentTo(t, leader))
	require.False(t, e.inParty(stranger, ph))
	require.Equal(t, 1, ph.members.Size())

	// An invite makes the same join admissible.
	list := &snsPartyInviteList{}
	list.Add(&snsPartyInvite{PartyUUID: ph.ID})
	e.ep.snsPartyInvites.Store(stranger.userID, list)
	require.NoError(t, e.ep.snsPartyJoinRequest(stranger.Context(), loggerForTest(t), stranger, &evr.SNSPartyJoinRequest{PartyID: 63}))
	require.Contains(t, typeNames(sentTo(t, stranger)), typeJoinSuccess)
	require.True(t, e.inParty(stranger, ph))
}

// A friends-only party admits a mutual friend of the leader.
func TestSNSPartyJoinRequestFriendsPolicyAdmitsAFriend(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader, friend := e.session("leader", true), e.session("friend", true)
	ph := e.party(64, leader)
	e.ep.snsPartyPolicies.Store(ph.ID, snsPartyPolicyFriends)
	e.befriend(leader, friend)

	require.NoError(t, e.ep.snsPartyJoinRequest(friend.Context(), loggerForTest(t), friend, &evr.SNSPartyJoinRequest{PartyID: 64}))
	require.Contains(t, typeNames(sentTo(t, friend)), typeJoinSuccess)
	require.True(t, e.inParty(friend, ph))
}

// A policy that needs the friend list, when the list cannot be read, is answered with PartyJoinFailure
// code 2 (a refusal with no reason), not code 3 and not an admission.
func TestSNSPartyJoinRequestPolicyLookupFailure(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader, joiner := e.session("leader", true), e.session("joiner", true)
	ph := e.party(65, leader)
	e.ep.snsPartyPolicies.Store(ph.ID, snsPartyPolicyFriends)
	dead := NewDB(t)
	require.NoError(t, dead.Close())
	e.ep.db = dead

	require.NoError(t, e.ep.snsPartyJoinRequest(joiner.Context(), loggerForTest(t), joiner, &evr.SNSPartyJoinRequest{PartyID: 65}))
	require.Equal(t, []evr.Message{&evr.SNSPartyJoinFailure{PartyID: 65, ErrorCode: 2}}, sentTo(t, joiner))
	require.False(t, e.inParty(joiner, ph))
}

// A locked party is not refused whatever its policy (owner ruling 2026-10-01): the join queues for the
// leader's approval and the game client is sent nothing. The policy is not even consulted: here it would
// need a friend list the dead database cannot give, and would have answered PartyJoinFailure 2 if asked.
func TestSNSPartyJoinRequestLockedPartyQueuesWithoutConsultingThePolicy(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader, joiner := e.session("leader", true), e.session("joiner", true)
	ph := e.party(66, leader)
	e.ep.snsPartyPolicies.Store(ph.ID, snsPartyPolicyFriends)
	e.setOpen(ph, false)
	dead := NewDB(t)
	require.NoError(t, dead.Close())
	e.ep.db = dead

	require.NoError(t, e.ep.snsPartyJoinRequest(joiner.Context(), loggerForTest(t), joiner, &evr.SNSPartyJoinRequest{PartyID: 66}))

	require.Empty(t, sentTo(t, joiner), "a queued join gets no reply")
	require.Empty(t, sentTo(t, leader))
	require.False(t, e.inParty(joiner, ph))
	ph.RLock()
	require.Len(t, ph.joinRequests, 1, "the join waits for the leader")
	ph.RUnlock()
}

// A full party answers PartyJoinFailure code 5 (full). A player who asks to join a party they are already
// in is answered code 2 (a refusal with no reason).
func TestSNSPartyJoinRequestFullAndAlreadyAMember(t *testing.T) {
	e := newPartyHandlerEnv(t)
	a, b, c, d := e.session("a", true), e.session("b", true), e.session("c", true), e.session("d", true)
	e.party(67, a, b, c, d)
	joiner := e.session("joiner", true)

	require.NoError(t, e.ep.snsPartyJoinRequest(joiner.Context(), loggerForTest(t), joiner, &evr.SNSPartyJoinRequest{PartyID: 67}))
	require.Equal(t, []evr.Message{&evr.SNSPartyJoinFailure{PartyID: 67, ErrorCode: 5}}, sentTo(t, joiner))

	require.NoError(t, e.ep.snsPartyJoinRequest(d.Context(), loggerForTest(t), d, &evr.SNSPartyJoinRequest{PartyID: 67}))
	require.Equal(t, []evr.Message{&evr.SNSPartyJoinFailure{PartyID: 67, ErrorCode: 2}}, sentTo(t, d))
}

// A player in a party who joins another is taken out of the first only once the second admits them: a
// join that fails (here, a full party) leaves them in their old party with its members told nothing, and a
// join that succeeds sends the old party's members PartyLeaveNotify and moves the player.
func TestSNSPartyJoinRequestLeavesTheOldPartyOnlyOnceAdmitted(t *testing.T) {
	e := newPartyHandlerEnv(t)
	oldLeader, mover := e.session("oldleader", true), e.session("mover", true)
	old := e.party(68, oldLeader, mover)
	full := e.party(69, e.session("f1", true), e.session("f2", true), e.session("f3", true), e.session("f4", true))
	open := e.party(70, e.session("o1", true))

	// The full party refuses: the player stays in the old party.
	require.NoError(t, e.ep.snsPartyJoinRequest(mover.Context(), loggerForTest(t), mover, &evr.SNSPartyJoinRequest{PartyID: 69}))
	require.Equal(t, []evr.Message{&evr.SNSPartyJoinFailure{PartyID: 69, ErrorCode: 5}}, sentTo(t, mover))
	require.Empty(t, sentTo(t, oldLeader), "the old party was not told anything")
	require.True(t, e.inParty(mover, old))
	require.False(t, e.inParty(mover, full))
	id, snsID := e.currentParty(mover)
	require.Equal(t, old.ID, id)
	require.EqualValues(t, 68, snsID)

	// The open party admits: the old party is left, with a leave notify.
	require.NoError(t, e.ep.snsPartyJoinRequest(mover.Context(), loggerForTest(t), mover, &evr.SNSPartyJoinRequest{PartyID: 70}))
	require.Contains(t, typeNames(sentTo(t, mover)), typeJoinSuccess)
	require.Equal(t, []evr.Message{&evr.SNSPartyLeaveNotify{PartyID: 68, MemberID: e.accounts[mover]}}, sentTo(t, oldLeader))
	require.False(t, e.inParty(mover, old))
	require.True(t, e.inParty(mover, open))
	id, snsID = e.currentParty(mover)
	require.Equal(t, open.ID, id)
	require.EqualValues(t, 70, snsID)
}

// A tracker that cannot track the joiner makes the join fail with PartyJoinFailure code 1 after the
// registry admitted it.
func TestSNSPartyJoinRequestTrackFailure(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader, joiner := e.session("leader", true), e.session("joiner", true)
	e.party(71, leader)
	e.tracker.failTrack.Store(true)

	require.NoError(t, e.ep.snsPartyJoinRequest(joiner.Context(), loggerForTest(t), joiner, &evr.SNSPartyJoinRequest{PartyID: 71}))
	require.Equal(t, []evr.Message{&evr.SNSPartyJoinFailure{PartyID: 71, ErrorCode: 1}}, sentTo(t, joiner))
}

// An invite from a party is sent to its target; accepting it joins that party under any policy. The
// accepting game client is sent the party's data and then PartyJoinSuccess; the members are sent the
// joiner's data and then PartyJoinNotify; the invite is used up; and the old party's members are told the
// player left.
func TestSNSPartyRespondToInviteAcceptJoinsTheParty(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader, invitee, oldMate := e.session("leader", true), e.session("invitee", true), e.session("oldmate", true)
	ph := e.party(80, leader)
	old := e.party(81, oldMate, invitee)
	e.ep.snsPartyPolicies.Store(ph.ID, snsPartyPolicyInviteOnly)
	list := &snsPartyInviteList{}
	list.Add(&snsPartyInvite{PartyUUID: ph.ID, SNSPartyID: 80, InviterID: e.accounts[leader], InviterUID: leader.userID})
	e.ep.snsPartyInvites.Store(invitee.userID, list)
	leaderParams, _ := LoadParams(leader.Context())

	require.NoError(t, e.ep.snsPartyRespondToInviteRequest(invitee.Context(), loggerForTest(t), invitee,
		&evr.SNSPartyRespondToInviteRequest{Param: 1, TargetUserUUID: leaderParams.xpID.UUID()}))

	got := sentTo(t, invitee)
	require.Equal(t, []string{typeDataNotify, typeDataNotify, typeJoinSuccess}, typeNames(got))
	require.Equal(t, &evr.SNSPartyJoinSuccess{PartyID: 80, OwnerID: e.accounts[leader]}, got[2])
	require.Equal(t, []string{typeDataNotify, typeJoinNotify}, typeNames(sentTo(t, leader)))
	require.Equal(t, []evr.Message{&evr.SNSPartyLeaveNotify{PartyID: 81, MemberID: e.accounts[invitee]}}, sentTo(t, oldMate))
	require.True(t, e.inParty(invitee, ph))
	require.False(t, e.inParty(invitee, old))
	require.Zero(t, list.Count(), "the invite is used up")
}

// Rejecting an invite uses it up and sends nothing. An accept or a reject that matches no pending invite
// (invites live in one node's memory, so a restart loses them): a reject needs no answer, an accept is
// answered PartyJoinFailure code 1 so the client's join ends.
func TestSNSPartyRespondToInviteRejectAndMiss(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader, invitee := e.session("leader", true), e.session("invitee", true)
	ph := e.party(82, leader)
	list := &snsPartyInviteList{}
	list.Add(&snsPartyInvite{PartyUUID: ph.ID, SNSPartyID: 82, InviterUID: leader.userID})
	e.ep.snsPartyInvites.Store(invitee.userID, list)

	require.NoError(t, e.ep.snsPartyRespondToInviteRequest(invitee.Context(), loggerForTest(t), invitee,
		&evr.SNSPartyRespondToInviteRequest{Param: 0}))
	require.Empty(t, sentTo(t, invitee))
	require.Empty(t, sentTo(t, leader))
	require.Zero(t, list.Count(), "a rejected invite is gone")
	require.False(t, e.inParty(invitee, ph))

	// The invite list is empty now: an accept finds none.
	require.NoError(t, e.ep.snsPartyRespondToInviteRequest(invitee.Context(), loggerForTest(t), invitee,
		&evr.SNSPartyRespondToInviteRequest{Param: 1}))
	require.Equal(t, []evr.Message{&evr.SNSPartyJoinFailure{PartyID: 0, ErrorCode: 1}}, sentTo(t, invitee))
	require.NoError(t, e.ep.snsPartyRespondToInviteRequest(invitee.Context(), loggerForTest(t), invitee,
		&evr.SNSPartyRespondToInviteRequest{Param: 0}))
	require.Empty(t, sentTo(t, invitee))

	// A user with no invite list at all is answered the same way.
	nobody := e.session("nobody", true)
	require.NoError(t, e.ep.snsPartyRespondToInviteRequest(nobody.Context(), loggerForTest(t), nobody,
		&evr.SNSPartyRespondToInviteRequest{Param: 1}))
	require.Equal(t, []evr.Message{&evr.SNSPartyJoinFailure{PartyID: 0, ErrorCode: 1}}, sentTo(t, nobody))

	require.Error(t, e.ep.snsPartyRespondToInviteRequest(invitee.Context(), loggerForTest(t), invitee, &evr.SNSPartyLockRequest{}))
}

// An accepted invite to a full party fails with the reason (code 5), the invite is still used up, and the
// player stays in the party they were in, its members told nothing. An accept for a party that is locked
// queues for the leader and sends nothing.
func TestSNSPartyRespondToInviteFailureKeepsTheOldParty(t *testing.T) {
	e := newPartyHandlerEnv(t)
	invitee, mate := e.session("invitee", true), e.session("mate", true)
	old := e.party(83, mate, invitee)
	a, b, c, d := e.session("a", true), e.session("b", true), e.session("c", true), e.session("d", true)
	full := e.party(84, a, b, c, d)
	list := &snsPartyInviteList{}
	list.Add(&snsPartyInvite{PartyUUID: full.ID, SNSPartyID: 84, InviterUID: a.userID})
	e.ep.snsPartyInvites.Store(invitee.userID, list)

	require.NoError(t, e.ep.snsPartyRespondToInviteRequest(invitee.Context(), loggerForTest(t), invitee,
		&evr.SNSPartyRespondToInviteRequest{Param: 1}))
	require.Equal(t, []evr.Message{&evr.SNSPartyJoinFailure{PartyID: 84, ErrorCode: 5}}, sentTo(t, invitee))
	require.Empty(t, sentTo(t, mate))
	require.True(t, e.inParty(invitee, old))
	require.Zero(t, list.Count())

	// A locked party queues the accept with no reply.
	locked := e.party(85, e.session("lead", true))
	e.setOpen(locked, false)
	list.Add(&snsPartyInvite{PartyUUID: locked.ID, SNSPartyID: 85})
	require.NoError(t, e.ep.snsPartyRespondToInviteRequest(invitee.Context(), loggerForTest(t), invitee,
		&evr.SNSPartyRespondToInviteRequest{Param: 1}))
	require.Empty(t, sentTo(t, invitee))
	require.True(t, e.inParty(invitee, old))
	require.False(t, e.inParty(invitee, locked))
}

// With several invites pending, the one from the inviter the game names is the one answered; the others
// stay pending.
func TestSNSPartyRespondToInvitePicksTheNamedInviter(t *testing.T) {
	e := newPartyHandlerEnv(t)
	first, second, invitee := e.session("first", true), e.session("second", true), e.session("invitee", true)
	p1, p2 := e.party(86, first), e.party(87, second)
	list := &snsPartyInviteList{}
	list.Add(&snsPartyInvite{PartyUUID: p1.ID, SNSPartyID: 86, InviterUID: first.userID})
	list.Add(&snsPartyInvite{PartyUUID: p2.ID, SNSPartyID: 87, InviterUID: second.userID})
	e.ep.snsPartyInvites.Store(invitee.userID, list)
	secondParams, _ := LoadParams(second.Context())

	require.NoError(t, e.ep.snsPartyRespondToInviteRequest(invitee.Context(), loggerForTest(t), invitee,
		&evr.SNSPartyRespondToInviteRequest{Param: 1, TargetUserUUID: secondParams.xpID.UUID()}))
	require.True(t, e.inParty(invitee, p2))
	require.False(t, e.inParty(invitee, p1))
	require.NotNil(t, list.FindByParty(p1.ID), "the other invite stays pending")
}

// A leader passes the party to another member: the members are sent PartyPassNotify naming the new owner
// by account id, and the old leader PartyPassSuccess. A target who is not in the party, or not known at
// all, is PartyPassFailure code 2; a player in no party code 1.
func TestSNSPartyPassOwnership(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader, member, outsider := e.session("leader", true), e.session("member", true), e.session("outsider", true)
	ph := e.party(90, leader, member)
	memberParams, _ := LoadParams(member.Context())
	outsiderParams, _ := LoadParams(outsider.Context())
	e.ep.registerEvrUUIDMapping(outsiderParams.xpID.UUID(), outsider.userID)

	// Not a member of the party.
	require.NoError(t, e.ep.snsPartyPassOwnershipRequest(leader.Context(), loggerForTest(t), leader,
		&evr.SNSPartyPassOwnershipRequest{TargetUserUUID: outsiderParams.xpID.UUID()}))
	require.Equal(t, []evr.Message{&evr.SNSPartyPassFailure{ErrorCode: 2}}, sentTo(t, leader))
	// Unknown.
	require.NoError(t, e.ep.snsPartyPassOwnershipRequest(leader.Context(), loggerForTest(t), leader,
		&evr.SNSPartyPassOwnershipRequest{TargetUserUUID: [16]byte{9}}))
	require.Equal(t, []evr.Message{&evr.SNSPartyPassFailure{ErrorCode: 2}}, sentTo(t, leader))
	// Not in a party.
	require.NoError(t, e.ep.snsPartyPassOwnershipRequest(outsider.Context(), loggerForTest(t), outsider,
		&evr.SNSPartyPassOwnershipRequest{TargetUserUUID: memberParams.xpID.UUID()}))
	require.Equal(t, []evr.Message{&evr.SNSPartyPassFailure{ErrorCode: 1}}, sentTo(t, outsider))
	// A member who is not the leader cannot pass it: the registry refuses.
	require.NoError(t, e.ep.snsPartyPassOwnershipRequest(member.Context(), loggerForTest(t), member,
		&evr.SNSPartyPassOwnershipRequest{TargetUserUUID: memberParams.xpID.UUID()}))
	require.Equal(t, []evr.Message{&evr.SNSPartyPassFailure{ErrorCode: 1}}, sentTo(t, member))

	require.NoError(t, e.ep.snsPartyPassOwnershipRequest(leader.Context(), loggerForTest(t), leader,
		&evr.SNSPartyPassOwnershipRequest{TargetUserUUID: memberParams.xpID.UUID()}))
	require.Equal(t, []evr.Message{&evr.SNSPartyPassNotify{PartyID: 90, NewOwnerID: e.accounts[member]}, &evr.SNSPartyPassSuccess{}}, sentTo(t, leader))
	require.Equal(t, []evr.Message{&evr.SNSPartyPassNotify{PartyID: 90, NewOwnerID: e.accounts[member]}}, sentTo(t, member))
	ph.RLock()
	require.Equal(t, member.id.String(), ph.leader.UserPresence.SessionId)
	ph.RUnlock()
}

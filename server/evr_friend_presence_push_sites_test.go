package server

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
)

// pushSites is the party handler fixture (a real database, party registry and handlers) with the friend
// list of each user named by the test. A push runs on its own goroutine, as it does in the server, and
// the test waits for what it expects.
type pushSites struct {
	*partyHandlerEnv
	mu      sync.Mutex
	friends map[uuid.UUID][]uuid.UUID
	seen    map[*sessionWS][]*evr.SNSFriendPresenceNotify
}

func newPushSites(t *testing.T) *pushSites {
	e := &pushSites{
		partyHandlerEnv: newPartyHandlerEnv(t),
		friends:         map[uuid.UUID][]uuid.UUID{},
		seen:            map[*sessionWS][]*evr.SNSFriendPresenceNotify{},
	}
	e.ep.friendPush.friends = func(_ context.Context, subject uuid.UUID) ([]uuid.UUID, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.friends[subject], nil
	}
	e.ep.friendPush.schedule = func(delay time.Duration, f func()) {
		if delay <= 0 {
			go f()
		}
	}
	return e
}

// friendOf is a runtime-client session, online, that the subject lists as a friend.
func (e *pushSites) friendOf(subject *sessionWS, name string) *sessionWS {
	f := e.session(name, true)
	e.mu.Lock()
	e.friends[subject.userID] = append(e.friends[subject.userID], f.userID)
	e.mu.Unlock()
	return f
}

// presenceFor waits for the friend's client to be sent a presence message about the account, and
// returns the latest one.
func (e *pushSites) presenceFor(t *testing.T, friend *sessionWS, account uint64) *evr.SNSFriendPresenceNotify {
	t.Helper()
	var found *evr.SNSFriendPresenceNotify
	require.Eventually(t, func() bool {
		for _, m := range sentTo(t, friend) {
			if n, ok := m.(*evr.SNSFriendPresenceNotify); ok {
				e.seen[friend] = append(e.seen[friend], n)
			}
		}
		for _, n := range e.seen[friend] {
			if n.FriendID == account {
				found = n
			}
		}
		return found != nil
	}, 3*time.Second, 10*time.Millisecond, "no presence push about account %d reached the friend", account)
	return found
}

// A player who joins a party is pushed to their friends with the party they are in, joinable.
func TestPartyJoinPushesPresenceToTheJoinersFriends(t *testing.T) {
	e := newPushSites(t)
	leader, member, joiner := e.session("leader", true), e.session("member", true), e.session("joiner", true)
	e.party(60, leader, member)
	friend := e.friendOf(joiner, "friend")

	require.NoError(t, e.ep.snsPartyJoinRequest(joiner.Context(), loggerForTest(t), joiner, &evr.SNSPartyJoinRequest{PartyID: 60}))

	n := e.presenceFor(t, friend, e.accounts[joiner])
	require.Equal(t, uint64(60), n.PartyID)
	require.Equal(t, uint8(1), n.Joinable)
}

// A member who leaves is pushed with no party: their friends are no longer offered it through them.
func TestPartyLeavePushesPresenceToTheLeaversFriends(t *testing.T) {
	e := newPushSites(t)
	leader, member := e.session("leader", true), e.session("member", true)
	e.party(61, leader, member)
	friend := e.friendOf(member, "friend")

	require.NoError(t, e.ep.snsPartyLeaveRequest(member.Context(), loggerForTest(t), member, &evr.SNSPartyLeaveRequest{}))

	n := e.presenceFor(t, friend, e.accounts[member])
	require.Zero(t, n.PartyID)
	require.Zero(t, n.Joinable)
}

// Passing the party pushes the party's members: who leads decides what a friends-policy party offers.
func TestPartyPassPushesPresenceToTheMembersFriends(t *testing.T) {
	e := newPushSites(t)
	leader, member := e.session("leader", true), e.session("member", true)
	e.party(90, leader, member)
	friend := e.friendOf(member, "friend")
	memberParams, _ := LoadParams(member.Context())

	require.NoError(t, e.ep.snsPartyPassOwnershipRequest(leader.Context(), loggerForTest(t), leader,
		&evr.SNSPartyPassOwnershipRequest{TargetUserUUID: memberParams.xpID.UUID()}))

	n := e.presenceFor(t, friend, e.accounts[member])
	require.Equal(t, uint64(90), n.PartyID)
}

// When the leader leaves and the party handler promotes the oldest member, the members' friends are
// pushed (the leader changed what a friends-policy party offers).
func TestPartyLeaderChangePushesPresenceToTheMembersFriends(t *testing.T) {
	e := newPushSites(t)
	leader, m1 := e.session("leader", true), e.session("m1", true)
	ph := e.party(110, leader, m1)
	friend := e.friendOf(m1, "friend")

	e.tracker.Untrack(leader.id, PresenceStream{Mode: StreamModeParty, Subject: ph.ID, Label: "testnode"}, leader.userID)
	ph.Leave([]*Presence{presenceOf(leader)})

	n := e.presenceFor(t, friend, e.accounts[m1])
	require.Equal(t, uint64(110), n.PartyID)
}

// The leader's join policy decides whether a friend is offered the party: a friend who was offered it
// under the default policy is pushed that it is no longer joinable once the leader makes it invite only.
func TestPartyPolicyChangePushesPresenceToTheLeadersFriends(t *testing.T) {
	e := newPushSites(t)
	leader, member := e.session("leader", true), e.session("member", true)
	e.party(30, leader, member)
	friend := e.friendOf(leader, "friend")
	before := e.ep.friendPresence(context.Background(), friend.userID, leader.userID, e.accounts[leader], true)
	require.Equal(t, uint8(1), before.Joinable, "joinable under the default policy")

	require.NoError(t, e.ep.snsPartySetJoinPolicyRequest(leader.Context(), loggerForTest(t), leader,
		&evr.SNSPartySetJoinPolicyRequest{TargetParam: uint64(snsPartyPolicyInviteOnly)}))

	n := e.presenceFor(t, friend, e.accounts[leader])
	require.Zero(t, n.Joinable)
	require.Zero(t, n.PartyID)
}

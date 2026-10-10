package server

import (
	"context"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
)

// The party handlers other than data, lock, kick and leave also resolved the party by currentPartyID,
// which a party group join overwrites (nevr-runtime #398). Each acts on the tablet party the session's
// SNS id names. tabletOverGroup builds the state: host and mate in the tablet party 206, host also in a
// group's party with a group mate, host's currentPartyID the group's.
func (e *partyHandlerEnv) tabletOverGroup() (host, mate, groupMate *sessionWS, tablet, group *PartyHandler) {
	e.t.Helper()
	host, mate, groupMate = e.session("host", true), e.session("mate", true), e.session("groupmate", true)
	tablet = e.party(206, host, mate)
	group = e.party(207, groupMate, host)
	e.ep.removeSNSPartyMapping(207, group.ID)
	params, _ := LoadParams(host.Context())
	params.currentSNSPartyID = 206
	params.currentPartyID = group.ID
	groupMateParams, _ := LoadParams(groupMate.Context())
	groupMateParams.currentSNSPartyID = 0
	return host, mate, groupMate, tablet, group
}

func TestSNSPartySendInviteAfterAPartyGroupJoinInvitesToTheTabletParty(t *testing.T) {
	e := newPartyHandlerEnv(t)
	host, _, _, tablet, _ := e.tabletOverGroup()
	target := e.session("target", true)

	require.NoError(t, e.ep.snsPartySendInviteRequest(host.Context(), loggerForTest(t), host,
		&evr.SNSPartySendInviteRequest{TargetUserID: e.accounts[target]}))

	list, ok := e.ep.snsPartyInvites.Load(target.userID)
	require.True(t, ok, "the target has an invite")
	list.RLock()
	defer list.RUnlock()
	require.Len(t, list.invites, 1)
	require.Equal(t, tablet.ID, list.invites[0].PartyUUID, "the invite is to the tablet party, not the group's")
	require.EqualValues(t, 206, list.invites[0].SNSPartyID)
}

func TestSNSPartyJoinPolicyAfterAPartyGroupJoinIsTheTabletPartys(t *testing.T) {
	e := newPartyHandlerEnv(t)
	host, _, _, tablet, group := e.tabletOverGroup()

	require.NoError(t, e.ep.snsPartySetJoinPolicyRequest(host.Context(), loggerForTest(t), host,
		&evr.SNSPartySetJoinPolicyRequest{TargetParam: uint64(snsPartyPolicyFriends)}))

	require.Equal(t, snsPartyPolicyFriends, e.ep.snsPartyPolicy(tablet.ID))
	require.Equal(t, snsPartyPolicyEveryone, e.ep.snsPartyPolicy(group.ID), "the group's party keeps its policy")
	require.Equal(t, []evr.Message{&evr.SNSPartyUpdateSuccess{PartyID: 206}}, sentTo(t, host))
}

func TestSNSPartyUpdateRelaysAfterAPartyGroupJoinGoToTheTabletParty(t *testing.T) {
	e := newPartyHandlerEnv(t)
	host, mate, groupMate, _, _ := e.tabletOverGroup()

	require.NoError(t, e.ep.snsPartyUpdateRequest(host.Context(), loggerForTest(t), host, &evr.SNSPartyUpdateRequest{}))
	require.NoError(t, e.ep.snsPartyUpdateMemberRequest(host.Context(), loggerForTest(t), host, &evr.SNSPartyUpdateMemberRequest{}))

	require.Equal(t, []evr.Message{
		&evr.SNSPartyUpdateNotify{PartyID: 206},
		&evr.SNSPartyUpdateMemberNotify{PartyID: 206, MemberID: e.accounts[host]},
	}, sentTo(t, mate))
	require.Empty(t, sentTo(t, groupMate), "the group's members are not sent the tablet party's updates")
}

func TestSNSPartyPassOwnershipAfterAPartyGroupJoinPassesTheTabletParty(t *testing.T) {
	e := newPartyHandlerEnv(t)
	host, mate, groupMate, tablet, group := e.tabletOverGroup()
	mateParams, _ := LoadParams(mate.Context())

	require.NoError(t, e.ep.snsPartyPassOwnershipRequest(host.Context(), loggerForTest(t), host,
		&evr.SNSPartyPassOwnershipRequest{TargetUserUUID: mateParams.xpID.UUID()}))

	leaderOf := func(ph *PartyHandler) string {
		ph.RLock()
		defer ph.RUnlock()
		return ph.leader.UserPresence.SessionId
	}
	require.Equal(t, mate.id.String(), leaderOf(tablet), "the mate leads the tablet party")
	require.Equal(t, groupMate.id.String(), leaderOf(group), "the group's leader is unchanged")
	require.Empty(t, sentTo(t, groupMate))
}

// A friend's party id is the tablet party's joinability, not the group's: with the tablet open and the
// group's party locked, the friend's tablet party is offered.
func TestFriendPartyAfterAPartyGroupJoinIsTheTabletParty(t *testing.T) {
	e := newPartyHandlerEnv(t)
	host, _, _, _, group := e.tabletOverGroup()
	e.setOpen(group, false)
	params, _ := LoadParams(host.Context())

	id, ok := e.ep.friendPartyFor(context.Background(), uuid.Must(uuid.NewV4()), params)

	require.True(t, ok, "the tablet party is open")
	require.EqualValues(t, 206, id)
}

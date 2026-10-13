package server

import (
	"context"
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
)

// A session a party group join has been through: JoinPartyGroup (evr_lobby_group.go) sets currentPartyID
// to the group's party, led by someone else, and leaves currentSNSPartyID naming the session's tablet
// party. This is the state of a solo tablet party after the social lobby find (nevr-runtime #398: every
// party data share was answered PartyUpdateFailure from the first find on).
func (e *partyHandlerEnv) groupJoined(s, groupLeader *sessionWS) (tablet, group *PartyHandler) {
	e.t.Helper()
	tablet = e.party(206, s)
	group = e.party(207, groupLeader, s)
	e.ep.removeSNSPartyMapping(207, group.ID) // a party group has no SNS id
	leaderParams, _ := LoadParams(groupLeader.Context())
	leaderParams.currentSNSPartyID = 0
	params, _ := LoadParams(s.Context())
	params.currentSNSPartyID = 206
	params.currentPartyID = group.ID
	return tablet, group
}

// The tablet party's leader writes the party's data after a party group join: it is the tablet party's
// data, stored there, and answered success with the tablet party's id. Resolved by currentPartyID it
// was a write to the group's party by a member who does not lead it, answered SNSPartyUpdateFailure.
func TestSNSPartyDataUpdateAfterAPartyGroupJoinIsForTheTabletParty(t *testing.T) {
	e := newPartyHandlerEnv(t)
	solo, groupLeader := e.session("solo", true), e.session("groupleader", true)
	tablet, group := e.groupJoined(solo, groupLeader)

	require.NoError(t, e.ep.snsPartyDataUpdateRequest(solo.Context(), loggerForTest(t), solo,
		updateRequest(snsPartyDataScopeParty, 1, `{"mode":"social"}`)))

	require.Equal(t, []evr.Message{&evr.SNSPartyUpdateSuccess{PartyID: 206}}, sentTo(t, solo))
	stored, seq := e.ep.partyDataStored(tablet.ID).snapshot(snsPartyDataScopeParty, solo.id)
	require.EqualValues(t, 1, seq)
	require.Equal(t, "social", stored["mode"])
	_, groupSeq := e.ep.partyDataStored(group.ID).snapshot(snsPartyDataScopeParty, solo.id)
	require.Zero(t, groupSeq, "nothing is written to the group's party")
	require.Empty(t, sentTo(t, groupLeader), "the group's members are not sent the tablet party's data")
}

// A member-scope write after a party group join is the session's own data in its tablet party.
func TestSNSPartyMemberDataUpdateAfterAPartyGroupJoinIsForTheTabletParty(t *testing.T) {
	e := newPartyHandlerEnv(t)
	solo, groupLeader := e.session("solo", true), e.session("groupleader", true)
	tablet, group := e.groupJoined(solo, groupLeader)

	require.NoError(t, e.ep.snsPartyDataUpdateRequest(solo.Context(), loggerForTest(t), solo,
		updateRequest(snsPartyDataScopeMember, 1, `{"ready":true}`)))

	require.Equal(t, []evr.Message{&evr.SNSPartyUpdateMemberSuccess{PartyID: 206}}, sentTo(t, solo))
	_, seq := e.ep.partyDataStored(tablet.ID).snapshot(snsPartyDataScopeMember, solo.id)
	require.EqualValues(t, 1, seq)
	_, groupSeq := e.ep.partyDataStored(group.ID).snapshot(snsPartyDataScopeMember, solo.id)
	require.Zero(t, groupSeq)
	require.Empty(t, sentTo(t, groupLeader))
}

// Lock and Unlock after a party group join act on the tablet party the id they answer with names, not on
// the group's party.
func TestSNSPartyLockAfterAPartyGroupJoinIsForTheTabletParty(t *testing.T) {
	e := newPartyHandlerEnv(t)
	solo, groupLeader := e.session("solo", true), e.session("groupleader", true)
	tablet, group := e.groupJoined(solo, groupLeader)
	ctx := solo.Context()

	require.NoError(t, e.ep.snsPartyLockRequest(ctx, loggerForTest(t), solo, &evr.SNSPartyLockRequest{}))
	require.False(t, snsPartyIsOpen(tablet), "the tablet party is locked")
	require.True(t, snsPartyIsOpen(group), "the group's party is not")
	require.Equal(t, []string{"*evr.SNSPartyLockNotify", "*evr.SNSPartyLockSuccess"}, typeNames(sentTo(t, solo)))
	require.Empty(t, sentTo(t, groupLeader), "the group's members are not told of the tablet party's lock")

	require.NoError(t, e.ep.snsPartyUnlockRequest(ctx, loggerForTest(t), solo, &evr.SNSPartyUnlockRequest{}))
	require.True(t, snsPartyIsOpen(tablet))
	require.Equal(t, []string{"*evr.SNSPartyUnlockNotify", "*evr.SNSPartyUnlockSuccess"}, typeNames(sentTo(t, solo)))
	require.Empty(t, sentTo(t, groupLeader))
}

// Entering a match re-sends the session's data to its tablet party only; the group's members are not
// sent the tablet party's data under the tablet party's id.
func TestSNSPartyDataMatchChangedAfterAPartyGroupJoinIsForTheTabletParty(t *testing.T) {
	e := newPartyHandlerEnv(t)
	solo, groupLeader := e.session("solo", true), e.session("groupleader", true)
	e.groupJoined(solo, groupLeader)

	e.ep.snsPartyDataMatchChanged(context.Background(), loggerForTest(t), solo)

	require.Equal(t, []string{typeDataNotify, typeDataNotify}, typeNames(sentTo(t, solo)),
		"the leader of the tablet party is sent its own party's and member data")
	require.Empty(t, sentTo(t, groupLeader), "the group's members are not sent the tablet party's data")
}

// A session in a party group and no tablet party (currentSNSPartyID 0) keeps resolving by
// currentPartyID, as before.
func TestSNSPartyDataUpdateWithNoSNSPartyUsesCurrentPartyID(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader := e.session("leader", true)
	ph := e.party(300, leader)
	params, _ := LoadParams(leader.Context())
	params.currentSNSPartyID = 0
	e.ep.removeSNSPartyMapping(300, ph.ID)

	require.NoError(t, e.ep.snsPartyDataUpdateRequest(leader.Context(), loggerForTest(t), leader,
		updateRequest(snsPartyDataScopeParty, 1, `{"mode":"arena"}`)))

	require.Equal(t, []evr.Message{&evr.SNSPartyUpdateSuccess{PartyID: 0}}, sentTo(t, leader))
	stored, _ := e.ep.partyDataStored(ph.ID).snapshot(snsPartyDataScopeParty, leader.id)
	require.Equal(t, "arena", stored["mode"])
}

package server

import (
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
)

// kickRequest is the host's request to kick a member, by the member's EvrId UUID (the id the game keys
// members by, registered by partyHandlerEnv.party).
func (e *partyHandlerEnv) kickRequest(target *sessionWS) *evr.SNSPartyKickRequest {
	params, _ := LoadParams(target.Context())
	return &evr.SNSPartyKickRequest{TargetUserUUID: params.xpID.UUID()}
}

// The kicked member is told: its client leaves the party on SNSPartyKickNotify naming itself. The notify
// went to the party's stream after the removal, so the kicked was no longer on it and never got it
// (nevr-runtime #401: "host got PartyKickNotify plus PartyKickSuccess; the kicked PC stayed in the party").
func TestSNSPartyKickNotifiesTheKickedAndTheOthers(t *testing.T) {
	e := newPartyHandlerEnv(t)
	host, kicked, other := e.session("host", true), e.session("kicked", true), e.session("other", true)
	ph := e.party(110, host, kicked, other)
	want := &evr.SNSPartyKickNotify{PartyID: 110, KickID: e.accounts[kicked]}

	require.NoError(t, e.ep.snsPartyKickRequest(host.Context(), loggerForTest(t), host, e.kickRequest(kicked)))

	require.Equal(t, []evr.Message{want, &evr.SNSPartyKickSuccess{}}, sentTo(t, host))
	require.Equal(t, []evr.Message{want}, sentTo(t, kicked), "the kicked member is sent the notify naming it")
	require.Equal(t, []evr.Message{want}, sentTo(t, other))
	require.False(t, e.inParty(kicked, ph), "the kicked member is off the party's stream")
	require.True(t, e.inParty(other, ph))
}

// The server forgets the kicked member's party as it does on a leave: its current party fields are
// cleared, so a later write of its own is not taken for a write into a party it was removed from.
func TestSNSPartyKickClearsTheKickedMembersParty(t *testing.T) {
	e := newPartyHandlerEnv(t)
	host, kicked := e.session("host", true), e.session("kicked", true)
	e.party(111, host, kicked)

	require.NoError(t, e.ep.snsPartyKickRequest(host.Context(), loggerForTest(t), host, e.kickRequest(kicked)))

	partyID, snsID := e.currentParty(kicked)
	require.Equal(t, uuid.Nil, partyID)
	require.Zero(t, snsID)
	hostParty, hostSNS := e.currentParty(host)
	require.NotEqual(t, uuid.Nil, hostParty, "the host keeps its party")
	require.EqualValues(t, 111, hostSNS)
}

// After a party group join the host's currentPartyID is the group's party, and the kick must still act
// on the tablet party named by its SNS id (as party data does, #398): the member is removed from the
// tablet party and stays in the group.
func TestSNSPartyKickAfterAPartyGroupJoinActsOnTheTabletParty(t *testing.T) {
	e := newPartyHandlerEnv(t)
	host, kicked := e.session("host", true), e.session("kicked", true)
	tablet := e.party(206, host, kicked)
	group := e.party(207, host, kicked)
	e.ep.removeSNSPartyMapping(207, group.ID) // a party group has no SNS id
	for _, s := range []*sessionWS{host, kicked} {
		params, _ := LoadParams(s.Context())
		params.currentSNSPartyID = 206
		params.currentPartyID = group.ID
	}

	require.NoError(t, e.ep.snsPartyKickRequest(host.Context(), loggerForTest(t), host, e.kickRequest(kicked)))

	require.False(t, e.inParty(kicked, tablet), "removed from the tablet party")
	require.True(t, e.inParty(kicked, group), "still in the group's party")
	require.Equal(t, []evr.Message{&evr.SNSPartyKickNotify{PartyID: 206, KickID: e.accounts[kicked]}}, sentTo(t, kicked))
	partyID, snsID := e.currentParty(kicked)
	require.Zero(t, snsID, "no tablet party")
	require.Equal(t, group.ID, partyID, "the group's party, which the kick did not touch, is still its current party")
}

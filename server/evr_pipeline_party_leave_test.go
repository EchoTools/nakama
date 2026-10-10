package server

import (
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
)

// disconnect ends a session's context and runs the close watcher the party join started, which waits for
// exactly that and then leaves the party.
func (e *partyHandlerEnv) disconnect(s *sessionWS) {
	e.t.Helper()
	s.ctxCancelFn()
	e.ep.snsPartyLeaveOnClose(loggerForTest(e.t), s)
}

// A member whose connection ends (the headset died, nevr-runtime #403: "sprockee died and hes still
// showing in my party") is taken off the party and the others are told, as an explicit leave tells them:
// their client removes a member on SNSPartyLeaveNotify and on nothing else.
func TestSNSPartyDisconnectTellsTheOtherMembers(t *testing.T) {
	e := newPartyHandlerEnv(t)
	host, gone, other := e.session("host", true), e.session("gone", true), e.session("other", true)
	ph := e.party(110, host, gone, other)
	want := &evr.SNSPartyLeaveNotify{PartyID: 110, MemberID: e.accounts[gone]}

	e.disconnect(gone)

	require.Equal(t, []evr.Message{want}, sentTo(t, host))
	require.Equal(t, []evr.Message{want}, sentTo(t, other))
	require.Empty(t, sentTo(t, gone), "the departed session is sent nothing")
	require.False(t, e.inParty(gone, ph))
	require.True(t, e.inParty(host, ph))
}

// After a party group join the departing session's currentPartyID is the group's party and its SNS id
// still names the tablet party: the tablet party's members are told, the group's are not, and the
// member leaves the tablet party's stream (not the group's).
func TestSNSPartyDisconnectAfterAPartyGroupJoinLeavesTheTabletParty(t *testing.T) {
	e := newPartyHandlerEnv(t)
	host, gone, groupMate := e.session("host", true), e.session("gone", true), e.session("groupmate", true)
	tablet := e.party(206, host, gone)
	group := e.party(207, groupMate, gone)
	e.ep.removeSNSPartyMapping(207, group.ID)
	params, _ := LoadParams(gone.Context())
	params.currentSNSPartyID = 206
	params.currentPartyID = group.ID

	e.disconnect(gone)

	require.Equal(t, []evr.Message{&evr.SNSPartyLeaveNotify{PartyID: 206, MemberID: e.accounts[gone]}}, sentTo(t, host))
	require.Empty(t, sentTo(t, groupMate), "the group's members are not told of a tablet party leave")
	require.False(t, e.inParty(gone, tablet), "off the tablet party's stream")
	require.True(t, e.inParty(gone, group), "the group's stream is not touched")
}

// An explicit leave after a party group join is the same: it is the tablet party the member leaves.
func TestSNSPartyLeaveAfterAPartyGroupJoinLeavesTheTabletParty(t *testing.T) {
	e := newPartyHandlerEnv(t)
	host, leaver, groupMate := e.session("host", true), e.session("leaver", true), e.session("groupmate", true)
	tablet := e.party(206, host, leaver)
	group := e.party(207, groupMate, leaver)
	e.ep.removeSNSPartyMapping(207, group.ID)
	params, _ := LoadParams(leaver.Context())
	params.currentSNSPartyID = 206
	params.currentPartyID = group.ID

	require.NoError(t, e.ep.snsPartyLeaveRequest(leaver.Context(), loggerForTest(t), leaver, &evr.SNSPartyLeaveRequest{}))

	require.Equal(t, []evr.Message{&evr.SNSPartyLeaveSuccess{}}, sentTo(t, leaver))
	require.Equal(t, []evr.Message{&evr.SNSPartyLeaveNotify{PartyID: 206, MemberID: e.accounts[leaver]}}, sentTo(t, host))
	require.Empty(t, sentTo(t, groupMate))
	require.False(t, e.inParty(leaver, tablet))
	require.True(t, e.inParty(leaver, group))
	partyID, snsID := e.currentParty(leaver)
	require.Zero(t, snsID)
	require.Equal(t, group.ID, partyID, "the group's party is still its current party")
}

// A session in a tablet party and no group, leaving, has no party left (unchanged).
func TestSNSPartyLeaveWithNoGroupClearsTheParty(t *testing.T) {
	e := newPartyHandlerEnv(t)
	host, leaver := e.session("host", true), e.session("leaver", true)
	e.party(111, host, leaver)

	require.NoError(t, e.ep.snsPartyLeaveRequest(leaver.Context(), loggerForTest(t), leaver, &evr.SNSPartyLeaveRequest{}))

	partyID, snsID := e.currentParty(leaver)
	require.Equal(t, uuid.Nil, partyID)
	require.Zero(t, snsID)
}

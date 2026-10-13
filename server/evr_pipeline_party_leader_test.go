package server

import (
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
)

// presenceOf is the party presence of a session, as the party registry hands it to PartyHandler.Leave.
func presenceOf(s *sessionWS) *Presence {
	return &Presence{
		ID:     PresenceID{SessionID: s.id, Node: "testnode"},
		UserID: s.userID,
		Meta:   PresenceMeta{Username: s.Username()},
	}
}

// groupOnly puts two sessions in a party group's party and in no tablet party: currentPartyID is the
// group's, the SNS id is 0 (JoinPartyGroup leaves it so).
func (e *partyHandlerEnv) groupOnly(a, b *sessionWS) *PartyHandler {
	e.t.Helper()
	group := e.party(207, a, b)
	e.ep.removeSNSPartyMapping(207, group.ID)
	for _, s := range []*sessionWS{a, b} {
		params, _ := LoadParams(s.Context())
		params.currentSNSPartyID = 0
	}
	return group
}

// A party group member that disconnects is not an SNS party leave: there is no tablet party id to name,
// and the peers are sent no SNSPartyLeaveNotify{PartyID: 0}.
func TestSNSPartyDisconnectOfAGroupOnlyMemberSendsNoSNSLeaveNotify(t *testing.T) {
	e := newPartyHandlerEnv(t)
	a, b := e.session("a", true), e.session("b", true)
	group := e.groupOnly(a, b)

	e.disconnect(b)

	require.Empty(t, sentTo(t, a))
	require.False(t, e.inParty(b, group), "it still leaves the group's stream")
}

func TestSNSPartyLeaveRequestOfAGroupOnlyMemberSendsNoSNSLeaveNotify(t *testing.T) {
	e := newPartyHandlerEnv(t)
	a, b := e.session("a", true), e.session("b", true)
	e.groupOnly(a, b)

	require.NoError(t, e.ep.snsPartyLeaveRequest(b.Context(), loggerForTest(t), b, &evr.SNSPartyLeaveRequest{}))

	require.Empty(t, sentTo(t, a))
}

// When the leader leaves, PartyHandler.Leave promotes the oldest member; the members' game clients are
// told who leads now with SNSPartyPassNotify (they do not read the stream's rtapi PartyLeader).
func TestSNSPartyLeaderLeavingTellsTheMembersWhoLeadsNow(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader, m1, m2 := e.session("leader", true), e.session("m1", true), e.session("m2", true)
	ph := e.party(110, leader, m1, m2)

	// As the tracker's party leave listener does: the presence is off the stream, then Leave runs.
	e.tracker.Untrack(leader.id, PresenceStream{Mode: StreamModeParty, Subject: ph.ID, Label: "testnode"}, leader.userID)
	ph.Leave([]*Presence{presenceOf(leader)})

	ph.RLock()
	newLeaderSession := ph.leader.UserPresence.SessionId
	ph.RUnlock()
	var newLeader *sessionWS
	for _, m := range []*sessionWS{m1, m2} {
		if m.id.String() == newLeaderSession {
			newLeader = m
		}
	}
	require.NotNil(t, newLeader, "the oldest remaining member was promoted")
	want := &evr.SNSPartyPassNotify{PartyID: 110, NewOwnerID: e.accounts[newLeader]}
	require.Equal(t, []evr.Message{want}, sentTo(t, m1))
	require.Equal(t, []evr.Message{want}, sentTo(t, m2))
	require.Empty(t, sentTo(t, leader), "the departed leader is told nothing")
}

// A member leaving who is not the leader promotes nobody and sends no pass notify; the last member
// leaving stops the party and sends none either.
func TestSNSPartyLeaveWithNoLeaderChangeSendsNoPassNotify(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader, m1 := e.session("leader", true), e.session("m1", true)
	ph := e.party(111, leader, m1)

	ph.Leave([]*Presence{presenceOf(m1)})
	require.Empty(t, sentTo(t, leader))

	ph.Leave([]*Presence{presenceOf(leader)})
	require.Empty(t, sentTo(t, leader))
	require.Empty(t, sentTo(t, m1))
}

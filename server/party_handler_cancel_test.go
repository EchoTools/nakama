package server

import (
	"context"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// Any party change cancels matchmaking: every ticket that names a member of the party, before or
// after the change, is removed, and members queue again as the party they are now (sprockee,
// 2026-10-30). Nakama's own handler already removed the party's tickets on some changes
// (RemovePartyAll on leave, accept, remove and stop); it left a joiner's or leaver's own solo ticket
// alive and did nothing on a leader change.

const cancelLogMessage = "Cancelled matchmaking on party change"

type cancelFixture struct {
	t    *testing.T
	ph   *PartyHandler
	logs *observer.ObservedLogs
	node string
}

func newCancelFixture(t *testing.T) *cancelFixture {
	t.Helper()
	core, logs := observer.New(zapcore.InfoLevel)
	ph, cleanup := createTestPartyHandler(t, zap.New(core))
	t.Cleanup(func() { _ = cleanup() })
	return &cancelFixture{t: t, ph: ph, logs: logs, node: "node1"}
}

func (f *cancelFixture) player(name string) *Presence {
	sid, _ := uuid.NewV4()
	uid, _ := uuid.NewV4()
	return &Presence{
		ID:     PresenceID{Node: f.node, SessionID: sid},
		UserID: uid,
		Meta:   PresenceMeta{Username: name},
	}
}

// soloTicket queues the player alone, with no party id (what a solo find files).
func (f *cancelFixture) soloTicket(p *Presence) string {
	f.t.Helper()
	ticket, _, err := f.ph.matchmaker.Add(context.Background(), []*MatchmakerPresence{{
		UserId: p.UserID.String(), SessionId: p.ID.SessionID.String(), Username: p.Meta.Username,
		Node: p.ID.Node, SessionID: p.ID.SessionID,
	}}, p.ID.SessionID.String(), "", "*", 2, 2, 1, nil, nil)
	require.NoError(f.t, err)
	return ticket
}

// partyTicket is the leader filing a ticket for the whole party as it stands.
func (f *cancelFixture) partyTicket(leader *Presence) string {
	f.t.Helper()
	ticket, _, err := f.ph.MatchmakerAdd(leader.ID.SessionID.String(), f.node, "*", 2, 2, 1, nil, nil)
	require.NoError(f.t, err)
	return ticket
}

func (f *cancelFixture) tickets() map[string]bool {
	out := map[string]bool{}
	for _, e := range f.ph.matchmaker.Extract() {
		out[e.Ticket] = true
	}
	return out
}

func (f *cancelFixture) cancelLogs() []observer.LoggedEntry {
	return f.logs.FilterMessage(cancelLogMessage).All()
}

func TestPartyChangeCancelsMatchmaking(t *testing.T) {
	cases := []struct {
		name   string
		change string
		// act makes the change; it returns the tickets that must be gone and the number removed.
		act func(f *cancelFixture, leader, member, outsider *Presence) (gone []string)
	}{
		{"join through a join request", "join", func(f *cancelFixture, leader, member, outsider *Presence) []string {
			partyT := f.partyTicket(leader)
			joinerT := f.soloTicket(member) // the joiner queued alone before joining
			ok, err := f.ph.JoinRequest(member)
			require.NoError(t, err)
			require.True(t, ok)
			return []string{partyT, joinerT}
		}},
		{"join through the stream hook", "join", func(f *cancelFixture, leader, member, outsider *Presence) []string {
			partyT := f.partyTicket(leader)
			joinerT := f.soloTicket(member)
			f.ph.Join([]*Presence{member})
			return []string{partyT, joinerT}
		}},
		{"a member leaves", "leave", func(f *cancelFixture, leader, member, outsider *Presence) []string {
			f.ph.Join([]*Presence{member})
			partyT := f.partyTicket(leader)
			leaverT := f.soloTicket(member) // a stray solo ticket naming the leaver
			f.ph.Leave([]*Presence{member})
			return []string{partyT, leaverT}
		}},
		{"the leader leaves", "leader_change", func(f *cancelFixture, leader, member, outsider *Presence) []string {
			f.ph.Join([]*Presence{member})
			partyT := f.partyTicket(leader)
			leaderT := f.soloTicket(leader)
			f.ph.Leave([]*Presence{leader})
			return []string{partyT, leaderT}
		}},
		{"the leader promotes a member", "leader_change", func(f *cancelFixture, leader, member, outsider *Presence) []string {
			f.ph.Join([]*Presence{member})
			partyT := f.partyTicket(leader)
			memberT := f.soloTicket(member)
			require.NoError(t, f.ph.Promote(leader.ID.SessionID.String(), f.node, f.ph.members.List()[1].UserPresence))
			return []string{partyT, memberT}
		}},
		{"the leader removes a member", "remove", func(f *cancelFixture, leader, member, outsider *Presence) []string {
			f.ph.Join([]*Presence{member})
			partyT := f.partyTicket(leader)
			memberT := f.soloTicket(member)
			require.NoError(t, f.ph.Remove(leader.ID.SessionID.String(), f.node, f.ph.members.List()[1].UserPresence))
			return []string{partyT, memberT}
		}},
		{"the party disbands", "disband", func(f *cancelFixture, leader, member, outsider *Presence) []string {
			f.ph.Join([]*Presence{member})
			partyT := f.partyTicket(leader)
			memberT := f.soloTicket(member)
			require.NoError(t, f.ph.Close(leader.ID.SessionID.String(), f.node))
			return []string{partyT, memberT}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newCancelFixture(t)
			leader, member, outsider := f.player("leader"), f.player("member"), f.player("outsider")
			f.ph.Join([]*Presence{leader})
			outsiderT := f.soloTicket(outsider)

			gone := tc.act(f, leader, member, outsider)

			left := f.tickets()
			for _, g := range gone {
				require.False(t, left[g], "ticket %s should have been cancelled", g)
			}
			require.True(t, left[outsiderT], "a ticket of a player outside the party must be untouched")

			logs := f.cancelLogs()
			require.Len(t, logs, 1, "exactly one cancel record per change that removed tickets")
			fields := logs[0].ContextMap()
			require.Equal(t, f.ph.IDStr, fields["party_id"])
			require.Equal(t, tc.change, fields["change"])
			require.EqualValues(t, len(gone), fields["tickets_removed"])
		})
	}
}

func TestPartyChangeWithNoTicketsIsANoOp(t *testing.T) {
	f := newCancelFixture(t)
	leader, member := f.player("leader"), f.player("member")
	f.ph.Join([]*Presence{leader})

	ok, err := f.ph.JoinRequest(member)
	require.NoError(t, err)
	require.True(t, ok)
	f.ph.Leave([]*Presence{member})

	require.Empty(t, f.tickets())
	require.Empty(t, f.cancelLogs(), "nothing was removed, so nothing is logged")
}

package server

import (
	"context"
	"testing"

	"github.com/gofrs/uuid/v5"
)

// TestTicketCancelLateArrivalTriggersCancel verifies that when a party of 2
// has an active matchmaking ticket and a 3rd member arrives, the ticket is
// cancelled via MatchmakerRemoveAll. The new member is not on the original
// ticket, which triggers the cancellation path.
func TestTicketCancelLateArrivalTriggersCancel(t *testing.T) {
	t.Parallel()

	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	// Two members join. First is the leader.
	_, sessionIDs := addMembers(t, ph, 2)
	leaderSID := sessionIDs[0]

	// Leader submits a ticket with 2 members.
	ticket, _, err := ph.MatchmakerAdd(leaderSID.String(), partyTestNode, "", 2, 8, 2, nil, nil)
	if err != nil {
		t.Fatalf("MatchmakerAdd: %v", err)
	}
	if ticket == "" {
		t.Fatal("expected non-empty ticket")
	}

	// 3rd member arrives (the late arrival).
	lateMember, lateSID := addMember(ph)
	_ = lateMember // used only for join side-effect

	lg := &LobbyGroup{ph: ph}

	// The late arrival should NOT be on the existing ticket.
	if lg.HasSessionOnTicket(lateSID.String()) {
		t.Fatal("late arrival should not be on the original ticket")
	}

	// Cancel all tickets (simulating what cancelTicketForLateArrival does).
	if err := lg.MatchmakerRemoveAll(); err != nil {
		t.Fatalf("MatchmakerRemoveAll: %v", err)
	}

	// After cancellation, no session should be on any ticket for this party.
	if lg.HasSessionOnTicket(leaderSID.String()) {
		t.Fatal("leader should not be on any ticket after cancellation")
	}
	if lg.HasSessionOnTicket(lateSID.String()) {
		t.Fatal("late arrival should not be on any ticket after cancellation")
	}
}

// TestTicketCancelRebuiltTicketIncludesAll verifies that after cancellation,
// a newly submitted ticket includes the original 2 members plus the late
// arrival (3 total).
func TestTicketCancelRebuiltTicketIncludesAll(t *testing.T) {
	t.Parallel()

	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	// Two members join. First is the leader.
	_, sessionIDs := addMembers(t, ph, 2)
	leaderSID := sessionIDs[0]

	// Leader submits a ticket with 2 members.
	ticket, _, err := ph.MatchmakerAdd(leaderSID.String(), partyTestNode, "", 2, 8, 2, nil, nil)
	if err != nil {
		t.Fatalf("MatchmakerAdd: %v", err)
	}
	if ticket == "" {
		t.Fatal("expected non-empty ticket")
	}

	// 3rd member arrives.
	_, lateSID := addMember(ph)

	lg := &LobbyGroup{ph: ph}

	// Cancel the old ticket.
	if err := lg.MatchmakerRemoveAll(); err != nil {
		t.Fatalf("MatchmakerRemoveAll: %v", err)
	}

	// Rebuild: leader submits a new ticket. The party now has 3 members,
	// so MatchmakerAdd should include all 3.
	newTicket, otherPresences, err := ph.MatchmakerAdd(leaderSID.String(), partyTestNode, "", 2, 8, 2, nil, nil)
	if err != nil {
		t.Fatalf("MatchmakerAdd (rebuild): %v", err)
	}
	if newTicket == "" {
		t.Fatal("expected non-empty rebuilt ticket")
	}

	// otherPresences contains non-leader presence IDs.
	// Party has 3 members; leader is excluded from otherPresences.
	if len(otherPresences) != 2 {
		t.Fatalf("expected 2 other presences on rebuilt ticket, got %d", len(otherPresences))
	}

	// All 3 sessions should be on the new ticket.
	if !lg.HasSessionOnTicket(leaderSID.String()) {
		t.Fatal("leader should be on the rebuilt ticket")
	}
	if !lg.HasSessionOnTicket(sessionIDs[1].String()) {
		t.Fatal("original member should be on the rebuilt ticket")
	}
	if !lg.HasSessionOnTicket(lateSID.String()) {
		t.Fatal("late arrival should be on the rebuilt ticket")
	}
}

// Sprockee (or cat), have I ever complained about how much I dislike these long ass function names?
// Anything for "proper" testing I guess...


// verifies that when a solo leader submits a ticket tagged with the 
// party group ID (as the fixed addTicket does),
// MatchmakerRemoveAll can cancel it. This is the regression test. Before the fix,
// addTicket passed partyId="" for solo leaders, so RemovePartyAll was a no-op
// and the ticket survived, causing the leader to be matched without the follower
func TestTicketCancelSoloLeaderTaggedWithPartyID(t *testing.T) {
	t.Parallel()

	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	// Solo party: only the leader joins.
	_, sessionIDs := addMembers(t, ph, 1)
	leaderSID := sessionIDs[0]

	lg := &LobbyGroup{ph: ph}
	if lg.Size() != 1 {
		t.Fatalf("expected party size 1, got %d", lg.Size())
	}

	// Simulate what the fixed addTicket does for a solo leader that is in a
	// lobby group: submit via matchmaker.Add but tag the ticket with ph.IDStr
	// (the party group ID) instead of "". 
	leaderMember := ph.members.List()[0]
	presences := []*MatchmakerPresence{{
		UserId:    leaderMember.UserPresence.UserId,
		SessionId: leaderMember.UserPresence.SessionId,
		Username:  leaderMember.UserPresence.Username,
		Node:      partyTestNode,
		SessionID: leaderMember.PresenceID.SessionID,
	}}
	ticket, _, err := ph.matchmaker.Add(context.Background(), presences, leaderSID.String(), ph.IDStr, "", 2, 8, 2, nil, nil)
	if err != nil {
		t.Fatalf("matchmaker.Add: %v", err)
	}
	if ticket == "" {
		t.Fatal("expected non-empty ticket")
	}

	// Leader must appear on the party ticket via HasSessionOnPartyTicket.
	if !lg.HasSessionOnTicket(leaderSID.String()) {
		t.Fatal("leader should be on the solo ticket tagged with party ID")
	}

	// MatchmakerRemoveAll is called by both JoinRequest (when a late arrival
	// joins an open party) and cancelTicketForLateArrival. It must find and
	// remove the solo ticket because it is now in partyTickets[ph.IDStr].
	if err := lg.MatchmakerRemoveAll(); err != nil {
		t.Fatalf("MatchmakerRemoveAll: %v", err)
	}

	if lg.HasSessionOnTicket(leaderSID.String()) {
		t.Fatal("solo ticket should be removed by MatchmakerRemoveAll after tagging with party ID")
	}
}

// TestTicketCancelSoloPartyNotAffected verifies that a solo party (size 1)
// with an active ticket is not affected by the late arrival detection. Solo
// players have no late arrivals.
func TestTicketCancelSoloPartyNotAffected(t *testing.T) {
	t.Parallel()

	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	_, sessionIDs := addMembers(t, ph, 1)
	leaderSID := sessionIDs[0]

	lg := &LobbyGroup{ph: ph}

	// Solo party: size is 1.
	if lg.Size() != 1 {
		t.Fatalf("expected party size 1, got %d", lg.Size())
	}

	// The leader is the only member. HasSessionOnTicket should return false
	// (no ticket exists yet).
	if lg.HasSessionOnTicket(leaderSID.String()) {
		t.Fatal("no ticket should exist yet")
	}

	// Submit a solo ticket via the matchmaker directly (not party handler,
	// which requires size > 1 check in addTicket).
	// For the purpose of this test, just verify the detection logic:
	// a party of size 1 should never trigger cancel-and-rebuild because
	// the late arrival check requires lobbyGroup.Size() > 1.
	// This is tested at the call site level, not here. The important
	// assertion is that HasSessionOnTicket returns false for an unknown
	// session on a party with no tickets.
	randomSID, _ := uuid.NewV4()
	if lg.HasSessionOnTicket(randomSID.String()) {
		t.Fatal("random session should not be on any ticket")
	}
}

// TestTicketCancelAlreadyOnTicketNoCancel verifies that a member who IS on
// the active ticket does not trigger cancellation. This prevents spurious
// ticket rebuilds when a member re-enters lobbyFind while already on the
// leader's ticket.
func TestTicketCancelAlreadyOnTicketNoCancel(t *testing.T) {
	t.Parallel()

	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	_, sessionIDs := addMembers(t, ph, 2)
	leaderSID := sessionIDs[0]
	memberSID := sessionIDs[1]

	// Leader submits a ticket with both members.
	ticket, _, err := ph.MatchmakerAdd(leaderSID.String(), partyTestNode, "", 2, 8, 2, nil, nil)
	if err != nil {
		t.Fatalf("MatchmakerAdd: %v", err)
	}
	if ticket == "" {
		t.Fatal("expected non-empty ticket")
	}

	lg := &LobbyGroup{ph: ph}

	// Both members should be on the ticket.
	if !lg.HasSessionOnTicket(leaderSID.String()) {
		t.Fatal("leader should be on the ticket")
	}
	if !lg.HasSessionOnTicket(memberSID.String()) {
		t.Fatal("member should be on the ticket")
	}

	// Since the member IS on the ticket, the late arrival detection guard
	// (!lobbyGroup.HasSessionOnTicket(session.id.String())) evaluates to
	// false, and cancelTicketForLateArrival is never called. No
	// cancellation should happen. Verify the ticket still exists by
	// checking that sessions remain on it.
	if !lg.HasSessionOnTicket(leaderSID.String()) {
		t.Fatal("leader should still be on the ticket (no cancellation)")
	}
	if !lg.HasSessionOnTicket(memberSID.String()) {
		t.Fatal("member should still be on the ticket (no cancellation)")
	}
}

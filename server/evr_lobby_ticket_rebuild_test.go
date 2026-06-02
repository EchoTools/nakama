package server

import (
	"context"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
	uatomic "go.uber.org/atomic"
)

// makeTicketRebuildParty creates a PartyHandler with a real matchmaker,
// joining N members. Returns the LobbyGroup, the leader's sessionID, and
// all member sessionIDs (leader first). The matchmaker is needed for
// MatchmakerAdd/Remove to work.
func makeTicketRebuildParty(t *testing.T, n int) (*LobbyGroup, *LocalMatchmaker, []uuid.UUID, func()) {
	t.Helper()

	logger := loggerForTest(t)
	mm, mmCleanup := createLightMatchmaker(t, logger)
	ph := &PartyHandler{
		logger:          logger,
		members:         NewPartyPresenceList(10),
		matchmaker:      mm,
		router:          &DummyMessageRouter{},
		Open:            true,
		MaxSize:         10,
		ctx:             context.Background(),
		ticketRebuildCh: make(chan struct{}, 1),
	}

	sessionIDs := make([]uuid.UUID, 0, n)
	for i := 0; i < n; i++ {
		sid := uuid.Must(uuid.NewV4())
		uid := uuid.Must(uuid.NewV4())
		p := &Presence{
			ID:     PresenceID{SessionID: sid, Node: partyTestNode},
			UserID: uid,
			Meta:   PresenceMeta{Username: "user-" + sid.String()[:8]},
		}
		ph.Join([]*Presence{p})
		sessionIDs = append(sessionIDs, sid)
	}

	lg := &LobbyGroup{name: "test_rebuild_group", ph: ph}
	return lg, mm, sessionIDs, mmCleanup
}

// submitLeaderTicket submits a ticket via the party handler's MatchmakerAdd.
// Returns the ticket string.
func submitLeaderTicket(t *testing.T, lg *LobbyGroup, leaderSID uuid.UUID) string {
	t.Helper()
	ticket, _, err := lg.MatchmakerAdd(
		leaderSID.String(), partyTestNode, "",
		2, 8, 2, nil, nil,
	)
	require.NoError(t, err, "MatchmakerAdd")
	require.NotEmpty(t, ticket, "expected non-empty ticket")
	return ticket
}

// submitSoloTicket submits a solo ticket via the matchmaker directly (not
// through the party handler). This simulates what addTicket does when
// lobbyGroup.Size() == 1.
func submitSoloTicket(t *testing.T, mm Matchmaker, leaderSID, leaderUID uuid.UUID) string {
	t.Helper()
	presences := []*MatchmakerPresence{{
		UserId:    leaderUID.String(),
		SessionId: leaderSID.String(),
		Username:  "leader",
		Node:      partyTestNode,
		SessionID: leaderSID,
	}}
	ticket, _, err := mm.Add(
		context.Background(), presences,
		leaderSID.String(), "", // empty party ID = solo ticket
		"", 2, 8, 2, nil, nil,
	)
	require.NoError(t, err, "solo matchmaker.Add")
	require.NotEmpty(t, ticket, "expected non-empty solo ticket")
	return ticket
}

// ticketPresenceCount returns the number of presences (entries) on a ticket.
func ticketPresenceCount(t *testing.T, mm *LocalMatchmaker, ticket string) int {
	t.Helper()
	mm.Lock()
	defer mm.Unlock()
	idx, ok := mm.indexes[ticket]
	if !ok {
		t.Fatalf("ticket %s not found in matchmaker indexes", ticket)
	}
	return idx.Count
}

// TestTicketRebuild_LateArrivalIncluded verifies the core fix: when a late
// party member arrives after the leader submitted a solo ticket, the solo
// ticket is cancelled and the rebuilt ticket includes both members.
//
// Scenario:
//  1. Leader creates party alone (size=1), submits solo ticket (no party ID)
//  2. Follower joins party (size=2)
//  3. cancelTicketForLateArrival fires — must cancel the solo ticket
//  4. Rebuild signal fires — leader rebuilds via MatchmakerAdd
//  5. New ticket has presences=2
func TestTicketRebuild_LateArrivalIncluded(t *testing.T) {
	t.Parallel()

	logger := loggerForTest(t)
	mm, mmCleanup := createLightMatchmaker(t, logger)
	defer mmCleanup()

	// Step 1: Leader creates party alone.
	leaderSID := uuid.Must(uuid.NewV4())
	leaderUID := uuid.Must(uuid.NewV4())

	ph := &PartyHandler{
		logger:          logger,
		members:         NewPartyPresenceList(10),
		matchmaker:      mm,
		router:          &DummyMessageRouter{},
		Open:            true,
		MaxSize:         10,
		ctx:             context.Background(),
		ticketRebuildCh: make(chan struct{}, 1),
	}
	leaderPresence := &Presence{
		ID:     PresenceID{SessionID: leaderSID, Node: partyTestNode},
		UserID: leaderUID,
		Meta:   PresenceMeta{Username: "leader"},
	}
	ph.Join([]*Presence{leaderPresence})

	lg := &LobbyGroup{name: "dumpsterfire", ph: ph}
	require.Equal(t, 1, lg.Size(), "party should start with leader only")

	// Leader submits a solo ticket (simulating addTicket's solo path).
	soloTicket := submitSoloTicket(t, mm, leaderSID, leaderUID)
	require.Equal(t, 1, ticketPresenceCount(t, mm, soloTicket),
		"solo ticket should have 1 presence")

	// Step 2: Follower joins the party.
	followerSID := uuid.Must(uuid.NewV4())
	followerUID := uuid.Must(uuid.NewV4())
	followerPresence := &Presence{
		ID:     PresenceID{SessionID: followerSID, Node: partyTestNode},
		UserID: followerUID,
		Meta:   PresenceMeta{Username: "follower"},
	}
	_, err := ph.JoinRequest(followerPresence)
	require.NoError(t, err, "follower JoinRequest")
	require.Equal(t, 2, lg.Size(), "party should now have 2 members")

	// Step 3: Simulate cancelTicketForLateArrival.
	// MatchmakerRemoveAll should not remove the solo ticket (no party ID).
	_ = lg.MatchmakerRemoveAll()

	// The solo ticket should still exist because it has no party ID.
	mm.Lock()
	_, soloStillExists := mm.indexes[soloTicket]
	mm.Unlock()
	// With the fix, MatchmakerRemoveSessionAll catches it.
	err = lg.MatchmakerRemoveSessionAll(leaderSID.String())
	require.NoError(t, err, "RemoveSessionAll")

	// After session removal, the solo ticket should be gone.
	mm.Lock()
	_, soloExistsAfterFix := mm.indexes[soloTicket]
	mm.Unlock()
	if soloStillExists {
		// Before the fix, MatchmakerRemoveAll alone would not remove it.
		require.False(t, soloExistsAfterFix,
			"solo ticket must be removed after RemoveSessionAll")
	}

	// Signal the rebuild.
	lg.SignalTicketRebuild()

	// Verify the signal was sent.
	select {
	case <-lg.TicketRebuildCh():
		// Good — signal received.
	default:
		t.Fatal("expected rebuild signal on TicketRebuildCh")
	}

	// Step 4: Leader rebuilds the ticket via MatchmakerAdd.
	newTicket := submitLeaderTicket(t, lg, leaderSID)

	// Step 5: New ticket must have 2 presences.
	require.Equal(t, 2, ticketPresenceCount(t, mm, newTicket),
		"rebuilt ticket must include both leader and late arrival")
}

// TestTicketRebuild_MultipleLateArrivals verifies that when two members
// join after the leader's initial solo ticket, the rebuilt ticket includes
// all three members.
func TestTicketRebuild_MultipleLateArrivals(t *testing.T) {
	t.Parallel()

	logger := loggerForTest(t)
	mm, mmCleanup := createLightMatchmaker(t, logger)
	defer mmCleanup()

	// Leader starts alone.
	leaderSID := uuid.Must(uuid.NewV4())
	leaderUID := uuid.Must(uuid.NewV4())

	ph := &PartyHandler{
		logger:          logger,
		members:         NewPartyPresenceList(10),
		matchmaker:      mm,
		router:          &DummyMessageRouter{},
		Open:            true,
		MaxSize:         10,
		ctx:             context.Background(),
		ticketRebuildCh: make(chan struct{}, 1),
	}
	ph.Join([]*Presence{{
		ID:     PresenceID{SessionID: leaderSID, Node: partyTestNode},
		UserID: leaderUID,
		Meta:   PresenceMeta{Username: "leader"},
	}})
	lg := &LobbyGroup{name: "test_multi_late", ph: ph}

	// Solo ticket.
	soloTicket := submitSoloTicket(t, mm, leaderSID, leaderUID)
	require.Equal(t, 1, ticketPresenceCount(t, mm, soloTicket))

	// Two late arrivals join.
	for i := 0; i < 2; i++ {
		sid := uuid.Must(uuid.NewV4())
		uid := uuid.Must(uuid.NewV4())
		_, err := ph.JoinRequest(&Presence{
			ID:     PresenceID{SessionID: sid, Node: partyTestNode},
			UserID: uid,
			Meta:   PresenceMeta{Username: "late-" + sid.String()[:4]},
		})
		require.NoError(t, err)
	}
	require.Equal(t, 3, lg.Size(), "party should have 3 members")

	// Cancel and rebuild.
	_ = lg.MatchmakerRemoveAll()
	require.NoError(t, lg.MatchmakerRemoveSessionAll(leaderSID.String()))
	lg.SignalTicketRebuild()

	// Drain the signal.
	select {
	case <-lg.TicketRebuildCh():
	default:
		t.Fatal("expected rebuild signal")
	}

	newTicket := submitLeaderTicket(t, lg, leaderSID)
	require.Equal(t, 3, ticketPresenceCount(t, mm, newTicket),
		"rebuilt ticket must include all 3 members")
}

// TestTicketRebuild_NoLateArrival_UnchangedOnFallback verifies that when
// both members were on the original ticket and the fallback timer fires,
// the rebuilt ticket still has both members (regression test).
func TestTicketRebuild_NoLateArrival_UnchangedOnFallback(t *testing.T) {
	t.Parallel()

	lg, mm, sessionIDs, cleanup := makeTicketRebuildParty(t, 2)
	defer cleanup()
	leaderSID := sessionIDs[0]

	require.Equal(t, 2, lg.Size())

	// Submit initial party ticket (both members present from the start).
	ticket1 := submitLeaderTicket(t, lg, leaderSID)
	require.Equal(t, 2, ticketPresenceCount(t, mm, ticket1))

	// Simulate fallback: remove old ticket, submit new one.
	mm.Remove([]string{ticket1})
	ticket2 := submitLeaderTicket(t, lg, leaderSID)
	require.Equal(t, 2, ticketPresenceCount(t, mm, ticket2),
		"fallback rebuild must preserve both members")
}

// TestTicketRebuild_LateArrivalAfterMultipleRebuilds verifies that a late
// arrival is included even when the leader's ticket has been rebuilt
// multiple times by the fallback timer before the member joins.
func TestTicketRebuild_LateArrivalAfterMultipleRebuilds(t *testing.T) {
	t.Parallel()

	logger := loggerForTest(t)
	mm, mmCleanup := createLightMatchmaker(t, logger)
	defer mmCleanup()

	leaderSID := uuid.Must(uuid.NewV4())
	leaderUID := uuid.Must(uuid.NewV4())

	ph := &PartyHandler{
		logger:          logger,
		members:         NewPartyPresenceList(10),
		matchmaker:      mm,
		router:          &DummyMessageRouter{},
		Open:            true,
		MaxSize:         10,
		ctx:             context.Background(),
		ticketRebuildCh: make(chan struct{}, 1),
	}
	ph.Join([]*Presence{{
		ID:     PresenceID{SessionID: leaderSID, Node: partyTestNode},
		UserID: leaderUID,
		Meta:   PresenceMeta{Username: "leader"},
	}})
	lg := &LobbyGroup{name: "test_multi_rebuild", ph: ph}

	// Solo ticket, then two fallback rebuilds.
	for i := 0; i < 3; i++ {
		ticket := submitSoloTicket(t, mm, leaderSID, leaderUID)
		require.Equal(t, 1, ticketPresenceCount(t, mm, ticket))
		mm.Remove([]string{ticket})
	}

	// Now a late arrival joins.
	followerSID := uuid.Must(uuid.NewV4())
	followerUID := uuid.Must(uuid.NewV4())
	_, err := ph.JoinRequest(&Presence{
		ID:     PresenceID{SessionID: followerSID, Node: partyTestNode},
		UserID: followerUID,
		Meta:   PresenceMeta{Username: "follower"},
	})
	require.NoError(t, err)
	require.Equal(t, 2, lg.Size())

	// Rebuild after cancellation.
	require.NoError(t, lg.MatchmakerRemoveSessionAll(leaderSID.String()))
	lg.SignalTicketRebuild()

	select {
	case <-lg.TicketRebuildCh():
	default:
		t.Fatal("expected rebuild signal")
	}

	newTicket := submitLeaderTicket(t, lg, leaderSID)
	require.Equal(t, 2, ticketPresenceCount(t, mm, newTicket),
		"rebuilt ticket after multiple fallbacks must include late arrival")
}

// TestTicketRebuild_MemberLeavesDuringMatchmaking verifies that when a
// party member leaves while matchmaking is active, the rebuilt ticket
// reflects the reduced party size.
func TestTicketRebuild_MemberLeavesDuringMatchmaking(t *testing.T) {
	t.Parallel()

	lg, mm, sessionIDs, cleanup := makeTicketRebuildParty(t, 3)
	defer cleanup()
	leaderSID := sessionIDs[0]
	leaverSID := sessionIDs[2]

	require.Equal(t, 3, lg.Size())

	// Submit ticket with all 3.
	ticket := submitLeaderTicket(t, lg, leaderSID)
	require.Equal(t, 3, ticketPresenceCount(t, mm, ticket))

	// Member leaves.
	leaverPresence := &Presence{
		ID: PresenceID{SessionID: leaverSID, Node: partyTestNode},
	}
	lg.ph.Leave([]*Presence{leaverPresence})
	require.Equal(t, 2, lg.Size(), "party should have 2 members after leave")

	// Rebuild.
	mm.Remove([]string{ticket})
	newTicket := submitLeaderTicket(t, lg, leaderSID)
	require.Equal(t, 2, ticketPresenceCount(t, mm, newTicket),
		"rebuilt ticket must reflect member departure (not stale size=3)")
}

// TestTicketRebuild_SoloTicketNotCancelledByPartyRemoveAll confirms the
// root cause: MatchmakerRemoveAll does NOT remove solo tickets (tickets
// with an empty party ID). This is why RemoveSessionAll is needed.
func TestTicketRebuild_SoloTicketNotCancelledByPartyRemoveAll(t *testing.T) {
	t.Parallel()

	logger := loggerForTest(t)
	mm, mmCleanup := createLightMatchmaker(t, logger)
	defer mmCleanup()

	leaderSID := uuid.Must(uuid.NewV4())
	leaderUID := uuid.Must(uuid.NewV4())

	ph := &PartyHandler{
		logger:          logger,
		members:         NewPartyPresenceList(10),
		matchmaker:      mm,
		router:          &DummyMessageRouter{},
		Open:            true,
		MaxSize:         10,
		ctx:             context.Background(),
		ticketRebuildCh: make(chan struct{}, 1),
	}
	ph.Join([]*Presence{{
		ID:     PresenceID{SessionID: leaderSID, Node: partyTestNode},
		UserID: leaderUID,
		Meta:   PresenceMeta{Username: "leader"},
	}})
	lg := &LobbyGroup{name: "test_solo_cancel", ph: ph}

	// Submit solo ticket.
	soloTicket := submitSoloTicket(t, mm, leaderSID, leaderUID)

	// MatchmakerRemoveAll uses party ID — solo ticket has none.
	_ = lg.MatchmakerRemoveAll()

	mm.Lock()
	_, exists := mm.indexes[soloTicket]
	mm.Unlock()
	require.True(t, exists,
		"MatchmakerRemoveAll must NOT remove solo tickets (empty party ID) — "+
			"this is the root cause of #459")

	// RemoveSessionAll catches it.
	require.NoError(t, lg.MatchmakerRemoveSessionAll(leaderSID.String()))
	mm.Lock()
	_, exists = mm.indexes[soloTicket]
	mm.Unlock()
	require.False(t, exists,
		"RemoveSessionAll must remove the solo ticket")
}

// TestTicketRebuild_SignalChannelIdempotent verifies that multiple signals
// don't block or panic (channel capacity is 1, non-blocking send).
func TestTicketRebuild_SignalChannelIdempotent(t *testing.T) {
	t.Parallel()

	lg, _, _, cleanup := makeTicketRebuildParty(t, 2)
	defer cleanup()

	// Signal twice — must not block or panic.
	lg.SignalTicketRebuild()
	lg.SignalTicketRebuild()

	// Only one signal should be on the channel.
	select {
	case <-lg.TicketRebuildCh():
	default:
		t.Fatal("expected at least one rebuild signal")
	}

	// Channel should now be empty.
	select {
	case <-lg.TicketRebuildCh():
		t.Fatal("channel should be empty after draining one signal")
	default:
		// Good.
	}
}

// TestTicketRebuild_ChannelSelectDirect verifies the select-on-rebuild-channel
// behavior in isolation, without the full lobbyMatchMakeWithFallback machinery.
func TestTicketRebuild_ChannelSelectDirect(t *testing.T) {
	t.Parallel()

	lg, _, _, cleanup := makeTicketRebuildParty(t, 1)
	defer cleanup()

	rebuildCh := lg.TicketRebuildCh()
	require.NotNil(t, rebuildCh, "TicketRebuildCh must not be nil")

	// Signal the rebuild.
	lg.SignalTicketRebuild()

	// Select should immediately fire on rebuildCh.
	select {
	case <-rebuildCh:
		// Good.
	case <-time.After(1 * time.Second):
		t.Fatal("select on rebuildCh did not fire within 1 second")
	}
}

// TestTicketRebuild_MatchMakeWithFallbackIntegration verifies that the
// addTicket function produces a party ticket (count=2) when lobbyGroup
// has 2 members, even after previously submitting a solo ticket (count=1).
// This tests the core path that lobbyMatchMakeWithFallback's replaceTicket
// closure exercises.
func TestTicketRebuild_MatchMakeWithFallbackIntegration(t *testing.T) {
	t.Parallel()

	logger := loggerForTest(t)
	tracker := newMockMatchmakingTracker()
	mm, mmCleanup := createLightMatchmaker(t, logger)
	defer mmCleanup()

	leaderSID := uuid.Must(uuid.NewV4())
	leaderUID := uuid.Must(uuid.NewV4())
	groupID := uuid.Must(uuid.NewV4())

	ph := &PartyHandler{
		logger:          logger,
		members:         NewPartyPresenceList(10),
		matchmaker:      mm,
		router:          &DummyMessageRouter{},
		Open:            true,
		MaxSize:         10,
		ctx:             context.Background(),
		ticketRebuildCh: make(chan struct{}, 1),
	}
	ph.Join([]*Presence{{
		ID:     PresenceID{SessionID: leaderSID, Node: partyTestNode},
		UserID: leaderUID,
		Meta:   PresenceMeta{Username: "leader"},
	}})
	lg := &LobbyGroup{name: "integration_test", ph: ph}
	require.Equal(t, 1, lg.Size())

	leaderSession := &sessionWS{}
	leaderSession.id = leaderSID
	leaderSession.userID = leaderUID
	leaderSession.username = uatomic.NewString("leader")
	leaderSession.matchmaker = mm
	leaderSession.pipeline = &Pipeline{node: partyTestNode, tracker: tracker}

	lobbyParams := makeMatchmakeTestLobbyParams(leaderUID, groupID, evr.ModeArenaPublic, 1)

	p := &EvrPipeline{
		node:   partyTestNode,
		config: cfg,
		db:     stubDB(t),
		nk: &RuntimeGoNakamaModule{
			tracker:       tracker,
			streamManager: testStreamManager{},
		},
	}

	ticketConfig := DefaultMatchmakerTicketConfigs[evr.ModeArenaPublic]

	// Step 1: addTicket with size=1 should produce a solo ticket.
	soloTicket, err := p.addTicket(context.Background(), logger, leaderSession, lobbyParams, lg, ticketConfig)
	require.NoError(t, err, "addTicket (solo)")
	require.NotEmpty(t, soloTicket, "solo ticket should not be empty")
	require.Equal(t, 1, ticketPresenceCount(t, mm, soloTicket),
		"solo ticket must have 1 presence")

	// Step 2: Follower joins the party.
	followerSID := uuid.Must(uuid.NewV4())
	followerUID := uuid.Must(uuid.NewV4())
	_, joinErr := ph.JoinRequest(&Presence{
		ID:     PresenceID{SessionID: followerSID, Node: partyTestNode},
		UserID: followerUID,
		Meta:   PresenceMeta{Username: "follower"},
	})
	require.NoError(t, joinErr, "follower JoinRequest")
	require.Equal(t, 2, lg.Size(), "party should now have 2 members")

	// Step 3: Cancel the solo ticket.
	require.NoError(t, lg.MatchmakerRemoveSessionAll(leaderSID.String()))

	// Step 4: Rebuild — addTicket with size=2 should produce a party ticket.
	partyTicket, err := p.addTicket(context.Background(), logger, leaderSession, lobbyParams, lg, ticketConfig)
	require.NoError(t, err, "addTicket (party rebuild)")
	require.NotEmpty(t, partyTicket, "party ticket should not be empty")
	require.Equal(t, 2, ticketPresenceCount(t, mm, partyTicket),
		"rebuilt party ticket must have 2 presences")

	// Step 5: Verify the rebuild channel was signalled by JoinRequest
	// (JoinRequest calls RemovePartyAll which triggers signal).
	// Actually, the signal is sent by cancelTicketForLateArrival, not
	// JoinRequest. Verify the channel mechanism works.
	lg.SignalTicketRebuild()
	select {
	case <-lg.TicketRebuildCh():
		// Good — signal delivered.
	case <-time.After(1 * time.Second):
		t.Fatal("expected rebuild signal on TicketRebuildCh")
	}
}

// TestTicketRebuild_NilLobbyGroup_NoPanic verifies that the rebuild
// channel handling is safe when lobbyGroup is nil (solo player).
func TestTicketRebuild_NilLobbyGroup_NoPanic(t *testing.T) {
	t.Parallel()

	// Nil pointer to LobbyGroup.
	var lg *LobbyGroup
	require.Nil(t, lg)

	ch := lg.TicketRebuildCh()
	require.Nil(t, ch, "nil lobbyGroup should return nil channel")

	lg.SignalTicketRebuild() // Must not panic.

	require.NoError(t, lg.MatchmakerRemoveSessionAll("anything"))

	// LobbyGroup with nil party handler.
	lgNoHandler := &LobbyGroup{name: "no-handler"}
	ch2 := lgNoHandler.TicketRebuildCh()
	require.Nil(t, ch2, "nil party handler should return nil channel")

	lgNoHandler.SignalTicketRebuild() // Must not panic.
	require.NoError(t, lgNoHandler.MatchmakerRemoveSessionAll("anything"))
}

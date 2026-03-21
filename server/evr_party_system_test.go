package server

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/blugelabs/bluge"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/atomic"
	"go.uber.org/zap"
)

const partyTestNode = "testnode"

// createLightMatchmaker creates a minimal LocalMatchmaker that supports Add/Remove
// without needing the full Runtime (avoids EVR module initialization).
func createLightMatchmaker(t *testing.T, logger *zap.Logger) (*LocalMatchmaker, func()) {
	t.Helper()

	cfg := NewConfig(logger)
	cfg.Matchmaker.MaxIntervals = 5
	cfg.Matchmaker.MaxTickets = 10

	indexWriter, err := bluge.OpenWriter(BlugeInMemoryConfig())
	if err != nil {
		t.Fatalf("open index writer: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	mm := &LocalMatchmaker{
		logger:         logger,
		node:           cfg.GetName(),
		config:         cfg,
		router:         &DummyMessageRouter{},
		metrics:        &testMetrics{},
		runtime:        &Runtime{},
		active:         atomic.NewUint32(1),
		stopped:        atomic.NewBool(false),
		ctx:            ctx,
		ctxCancelFn:    cancel,
		indexWriter:    indexWriter,
		sessionTickets: make(map[string]map[string]struct{}),
		partyTickets:   make(map[string]map[string]struct{}),
		indexes:        make(map[string]*MatchmakerIndex),
		activeIndexes:  make(map[string]*MatchmakerIndex),
		revCache:       &MapOf[string, map[string]bool]{},
	}

	return mm, func() {
		cancel()
		_ = indexWriter.Close()
	}
}

// createLightPartyHandler creates a PartyHandler using the light matchmaker.
func createLightPartyHandler(t *testing.T, logger *zap.Logger, open bool, maxSize int, expectedLeader *rtapi.UserPresence) (*PartyHandler, func()) {
	t.Helper()

	mm, mmCleanup := createLightMatchmaker(t, logger)
	tt := &testTracker{}
	tsm := testStreamManager{}
	dmr := &DummyMessageRouter{}
	pr := NewLocalPartyRegistry(logger, cfg, mm, tt, tsm, dmr, partyTestNode)
	ph := NewPartyHandler(logger, pr, mm, tt, tsm, dmr, uuid.UUID{}, partyTestNode, open, maxSize, expectedLeader)

	return ph, mmCleanup
}

// createDefaultPartyHandler creates an open party handler with max 10 members.
func createDefaultPartyHandler(t *testing.T) (*PartyHandler, func()) {
	t.Helper()
	return createLightPartyHandler(t, loggerForTest(t), true, 10, nil)
}

// newPresence creates a Presence with random session/user IDs.
func newPresence(node string) (*Presence, uuid.UUID, uuid.UUID) {
	sessionID, _ := uuid.NewV4()
	userID, _ := uuid.NewV4()
	return &Presence{
		ID:     PresenceID{Node: node, SessionID: sessionID},
		UserID: userID,
		Meta:   PresenceMeta{Username: "user-" + sessionID.String()[:8]},
	}, sessionID, userID
}

// newPresenceWithIDs creates a Presence with specific IDs.
func newPresenceWithIDs(node string, sessionID, userID uuid.UUID) *Presence {
	return &Presence{
		ID:     PresenceID{Node: node, SessionID: sessionID},
		UserID: userID,
		Meta:   PresenceMeta{Username: "user-" + sessionID.String()[:8]},
	}
}

// addMember creates and joins a member, returning the presence and sessionID.
func addMember(ph *PartyHandler) (*Presence, uuid.UUID) {
	p, sid, _ := newPresence(partyTestNode)
	ph.Join([]*Presence{p})
	return p, sid
}

// addMembers creates N members. First is leader.
func addMembers(t *testing.T, ph *PartyHandler, n int) ([]*Presence, []uuid.UUID) {
	t.Helper()
	presences := make([]*Presence, 0, n)
	sessionIDs := make([]uuid.UUID, 0, n)
	for i := 0; i < n; i++ {
		p, sid := addMember(ph)
		presences = append(presences, p)
		sessionIDs = append(sessionIDs, sid)
	}
	return presences, sessionIDs
}

func getMemberUP(ph *PartyHandler, sessionID uuid.UUID) *rtapi.UserPresence {
	for _, m := range ph.members.List() {
		if m.UserPresence.SessionId == sessionID.String() {
			return m.UserPresence
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// A. PartyHandler Core
// ---------------------------------------------------------------------------

func TestPartyHandler_JoinFirstMemberBecomesLeader(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	_, sid := addMember(ph)

	ph.RLock()
	defer ph.RUnlock()
	if ph.leader == nil {
		t.Fatal("expected leader to be set")
	}
	if ph.leader.UserPresence.SessionId != sid.String() {
		t.Fatalf("expected leader session %s, got %s", sid, ph.leader.UserPresence.SessionId)
	}
}

func TestPartyHandler_JoinWithExpectedInitialLeader(t *testing.T) {
	expectedSID, _ := uuid.NewV4()
	expectedUID, _ := uuid.NewV4()
	expectedUP := &rtapi.UserPresence{
		UserId:    expectedUID.String(),
		SessionId: expectedSID.String(),
		Username:  "expected-leader",
	}

	ph, cleanup := createLightPartyHandler(t, loggerForTest(t), true, 10, expectedUP)
	defer cleanup()

	other, _, _ := newPresence(partyTestNode)
	expected := newPresenceWithIDs(partyTestNode, expectedSID, expectedUID)
	ph.Join([]*Presence{other, expected})

	ph.RLock()
	defer ph.RUnlock()
	if ph.leader == nil {
		t.Fatal("expected leader to be set")
	}
	if ph.leader.UserPresence.SessionId != expectedSID.String() {
		t.Fatalf("expected leader session %s, got %s", expectedSID, ph.leader.UserPresence.SessionId)
	}
}

func TestPartyHandler_JoinExpectedLeaderNotInPresences(t *testing.T) {
	ghostSID, _ := uuid.NewV4()
	ghostUID, _ := uuid.NewV4()
	ghostUP := &rtapi.UserPresence{
		UserId:    ghostUID.String(),
		SessionId: ghostSID.String(),
		Username:  "ghost",
	}

	ph, cleanup := createLightPartyHandler(t, loggerForTest(t), true, 10, ghostUP)
	defer cleanup()

	_, sid := addMember(ph)

	ph.RLock()
	defer ph.RUnlock()
	if ph.leader.UserPresence.SessionId != sid.String() {
		t.Fatalf("expected fallback leader session %s, got %s", sid, ph.leader.UserPresence.SessionId)
	}
}

func TestPartyHandler_JoinMultipleMembers(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	_, sessionIDs := addMembers(t, ph, 3)

	ph.RLock()
	defer ph.RUnlock()
	if ph.members.Size() != 3 {
		t.Fatalf("expected 3 members, got %d", ph.members.Size())
	}
	if ph.leader.UserPresence.SessionId != sessionIDs[0].String() {
		t.Fatalf("expected first joiner as leader")
	}
}

func TestPartyHandler_JoinStoppedParty(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	_, sid := addMember(ph)
	_ = ph.Close(sid.String(), partyTestNode)

	sizeBefore := ph.members.Size()
	p2, _, _ := newPresence(partyTestNode)
	ph.Join([]*Presence{p2})

	// Close doesn't clear the members list, but Join on a stopped party is a no-op.
	// Size should not increase.
	if ph.members.Size() != sizeBefore {
		t.Fatalf("expected size unchanged at %d after join on stopped party, got %d", sizeBefore, ph.members.Size())
	}
}

func TestPartyHandler_JoinEmptyPresences(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	ph.Join([]*Presence{})
	if ph.members.Size() != 0 {
		t.Fatalf("expected 0 members, got %d", ph.members.Size())
	}
}

func TestPartyHandler_LeaveLeaderPromotesOldest(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	presences, sessionIDs := addMembers(t, ph, 3)
	ph.Leave([]*Presence{presences[0]})

	ph.RLock()
	defer ph.RUnlock()
	if ph.leader == nil {
		t.Fatal("expected new leader")
	}
	if ph.leader.UserPresence.SessionId != sessionIDs[1].String() {
		t.Fatalf("expected promoted leader %s, got %s", sessionIDs[1], ph.leader.UserPresence.SessionId)
	}
	if ph.members.Size() != 2 {
		t.Fatalf("expected 2 members, got %d", ph.members.Size())
	}
}

func TestPartyHandler_LeaveLastMemberStopsParty(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	presences, _ := addMembers(t, ph, 1)
	ph.Leave([]*Presence{presences[0]})

	ph.RLock()
	defer ph.RUnlock()
	if !ph.stopped {
		t.Fatal("expected party stopped after last member leaves")
	}
}

func TestPartyHandler_LeaveNonLeader(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	presences, sessionIDs := addMembers(t, ph, 3)
	ph.Leave([]*Presence{presences[2]})

	ph.RLock()
	defer ph.RUnlock()
	if ph.leader.UserPresence.SessionId != sessionIDs[0].String() {
		t.Fatal("leader should be unchanged")
	}
	if ph.members.Size() != 2 {
		t.Fatalf("expected 2 members, got %d", ph.members.Size())
	}
}

func TestPartyHandler_LeaveStoppedParty(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	p, sid := addMember(ph)
	_ = ph.Close(sid.String(), partyTestNode)
	ph.Leave([]*Presence{p}) // no panic = pass
}

func TestPartyHandler_LeaveCancelsMatchmaking(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	presences, sessionIDs := addMembers(t, ph, 2)

	ticket, _, err := ph.MatchmakerAdd(sessionIDs[0].String(), partyTestNode, "", 2, 2, 1, nil, nil)
	if err != nil {
		t.Fatalf("MatchmakerAdd: %s", err)
	}
	if ticket == "" {
		t.Fatal("expected ticket")
	}

	ph.Leave([]*Presence{presences[1]})

	// After leave, RemovePartyAll was called. New ticket should work.
	ticket2, _, err := ph.MatchmakerAdd(sessionIDs[0].String(), partyTestNode, "", 1, 1, 1, nil, nil)
	if err != nil {
		t.Fatalf("MatchmakerAdd after leave: %s", err)
	}
	if ticket2 == "" {
		t.Fatal("expected ticket after leave")
	}
}

func TestPartyHandler_PromoteByLeader(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	_, sessionIDs := addMembers(t, ph, 2)

	member1UP := getMemberUP(ph, sessionIDs[1])
	if err := ph.Promote(sessionIDs[0].String(), partyTestNode, member1UP); err != nil {
		t.Fatalf("Promote: %s", err)
	}

	ph.RLock()
	defer ph.RUnlock()
	if ph.leader.UserPresence.SessionId != sessionIDs[1].String() {
		t.Fatalf("expected promoted leader %s, got %s", sessionIDs[1], ph.leader.UserPresence.SessionId)
	}
}

func TestPartyHandler_PromoteByNonLeader(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	_, sessionIDs := addMembers(t, ph, 2)

	leaderUP := getMemberUP(ph, sessionIDs[0])
	err := ph.Promote(sessionIDs[1].String(), partyTestNode, leaderUP)
	if !errors.Is(err, runtime.ErrPartyNotLeader) {
		t.Fatalf("expected ErrPartyNotLeader, got %v", err)
	}
}

func TestPartyHandler_PromoteNonMember(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	_, sessionIDs := addMembers(t, ph, 1)

	ghostUP := &rtapi.UserPresence{
		UserId:    uuid.Must(uuid.NewV4()).String(),
		SessionId: uuid.Must(uuid.NewV4()).String(),
		Username:  "ghost",
	}
	err := ph.Promote(sessionIDs[0].String(), partyTestNode, ghostUP)
	if !errors.Is(err, runtime.ErrPartyNotMember) {
		t.Fatalf("expected ErrPartyNotMember, got %v", err)
	}
}

func TestPartyHandler_PromoteStoppedParty(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	_, sid := addMember(ph)
	_ = ph.Close(sid.String(), partyTestNode)

	err := ph.Promote(sid.String(), partyTestNode, &rtapi.UserPresence{})
	if !errors.Is(err, runtime.ErrPartyClosed) {
		t.Fatalf("expected ErrPartyClosed, got %v", err)
	}
}

func TestPartyHandler_CloseByLeader(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	_, sessionIDs := addMembers(t, ph, 2)

	if err := ph.Close(sessionIDs[0].String(), partyTestNode); err != nil {
		t.Fatalf("Close: %s", err)
	}
	ph.RLock()
	defer ph.RUnlock()
	if !ph.stopped {
		t.Fatal("expected stopped")
	}
}

func TestPartyHandler_CloseByNonLeader(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	_, sessionIDs := addMembers(t, ph, 2)

	err := ph.Close(sessionIDs[1].String(), partyTestNode)
	if !errors.Is(err, runtime.ErrPartyNotLeader) {
		t.Fatalf("expected ErrPartyNotLeader, got %v", err)
	}
}

func TestPartyHandler_CloseStoppedParty(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	_, sessionIDs := addMembers(t, ph, 1)
	_ = ph.Close(sessionIDs[0].String(), partyTestNode)

	err := ph.Close(sessionIDs[0].String(), partyTestNode)
	if !errors.Is(err, runtime.ErrPartyClosed) {
		t.Fatalf("expected ErrPartyClosed, got %v", err)
	}
}

func TestPartyHandler_RemoveByLeader(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	_, sessionIDs := addMembers(t, ph, 2)

	memberUP := getMemberUP(ph, sessionIDs[1])
	if err := ph.Remove(sessionIDs[0].String(), partyTestNode, memberUP); err != nil {
		t.Fatalf("Remove: %s", err)
	}
	if ph.members.Size() != 1 {
		t.Fatalf("expected 1 member, got %d", ph.members.Size())
	}
}

func TestPartyHandler_RemoveByNonLeader(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	_, sessionIDs := addMembers(t, ph, 2)

	leaderUP := getMemberUP(ph, sessionIDs[0])
	err := ph.Remove(sessionIDs[1].String(), partyTestNode, leaderUP)
	if !errors.Is(err, runtime.ErrPartyNotLeader) {
		t.Fatalf("expected ErrPartyNotLeader, got %v", err)
	}
}

func TestPartyHandler_RemoveSelf(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	_, sessionIDs := addMembers(t, ph, 2)

	leaderUP := getMemberUP(ph, sessionIDs[0])
	err := ph.Remove(sessionIDs[0].String(), partyTestNode, leaderUP)
	if !errors.Is(err, runtime.ErrPartyRemoveSelf) {
		t.Fatalf("expected ErrPartyRemoveSelf, got %v", err)
	}
}

func TestPartyHandler_RemoveNonMember(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	_, sessionIDs := addMembers(t, ph, 1)

	ghostUP := &rtapi.UserPresence{
		UserId:    uuid.Must(uuid.NewV4()).String(),
		SessionId: uuid.Must(uuid.NewV4()).String(),
		Username:  "ghost",
	}
	err := ph.Remove(sessionIDs[0].String(), partyTestNode, ghostUP)
	if !errors.Is(err, runtime.ErrPartyNotMember) {
		t.Fatalf("expected ErrPartyNotMember, got %v", err)
	}
}

func TestPartyHandler_RemoveRejectsJoinRequest(t *testing.T) {
	ph, cleanup := createLightPartyHandler(t, loggerForTest(t), false, 10, nil)
	defer cleanup()

	_, leaderSID := addMember(ph)

	requester, _, _ := newPresence(partyTestNode)
	accepted, err := ph.JoinRequest(requester)
	if err != nil {
		t.Fatalf("JoinRequest: %s", err)
	}
	if accepted {
		t.Fatal("expected queued, not accepted")
	}

	requesterUP := &rtapi.UserPresence{
		UserId:    requester.GetUserId(),
		SessionId: requester.GetSessionId(),
		Username:  requester.GetUsername(),
	}
	if err := ph.Remove(leaderSID.String(), partyTestNode, requesterUP); err != nil {
		t.Fatalf("Remove join request: %s", err)
	}

	requests, _ := ph.JoinRequestList(leaderSID.String(), partyTestNode)
	if len(requests) != 0 {
		t.Fatalf("expected 0 requests after rejection, got %d", len(requests))
	}
}

func TestPartyHandler_DataSendBetweenMembers(t *testing.T) {
	var mu sync.Mutex
	var sentTo []*PresenceID

	logger := loggerForTest(t)
	mm, mmCleanup := createLightMatchmaker(t, logger)
	defer mmCleanup()

	router := &testMessageRouter{
		sendToPresence: func(presences []*PresenceID, _ *rtapi.Envelope) {
			mu.Lock()
			sentTo = presences
			mu.Unlock()
		},
	}
	pr := NewLocalPartyRegistry(logger, cfg, mm, &testTracker{}, testStreamManager{}, router, partyTestNode)
	ph := NewPartyHandler(logger, pr, mm, &testTracker{}, testStreamManager{}, router, uuid.UUID{}, partyTestNode, true, 10, nil)

	_, sid0 := addMember(ph)
	addMember(ph)
	addMember(ph)

	if err := ph.DataSend(sid0.String(), partyTestNode, 1, []byte("hello")); err != nil {
		t.Fatalf("DataSend: %s", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(sentTo) != 2 {
		t.Fatalf("expected 2 recipients, got %d", len(sentTo))
	}
	for _, p := range sentTo {
		if p.SessionID == sid0 {
			t.Fatal("data should not be sent to sender")
		}
	}
}

func TestPartyHandler_DataSendNonMember(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	addMembers(t, ph, 2)

	fakeSID := uuid.Must(uuid.NewV4())
	err := ph.DataSend(fakeSID.String(), partyTestNode, 1, []byte("hello"))
	if !errors.Is(err, runtime.ErrPartyNotMember) {
		t.Fatalf("expected ErrPartyNotMember, got %v", err)
	}
}

func TestPartyHandler_DataSendSoloNoRecipients(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	_, sessionIDs := addMembers(t, ph, 1)

	if err := ph.DataSend(sessionIDs[0].String(), partyTestNode, 1, []byte("hello")); err != nil {
		t.Fatalf("expected no error for solo data send, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// B. Join Requests (closed party)
// ---------------------------------------------------------------------------

func createClosedParty(t *testing.T) (*PartyHandler, func(), uuid.UUID) {
	t.Helper()
	ph, cleanup := createLightPartyHandler(t, loggerForTest(t), false, 4, nil)
	_, leaderSID := addMember(ph)
	return ph, cleanup, leaderSID
}

func TestPartyHandler_JoinRequestClosedParty(t *testing.T) {
	ph, cleanup, _ := createClosedParty(t)
	defer cleanup()

	requester, _, _ := newPresence(partyTestNode)
	accepted, err := ph.JoinRequest(requester)
	if err != nil {
		t.Fatalf("JoinRequest: %s", err)
	}
	if accepted {
		t.Fatal("expected queued")
	}
}

func TestPartyHandler_JoinRequestPartyFull(t *testing.T) {
	ph, cleanup := createLightPartyHandler(t, loggerForTest(t), true, 1, nil)
	defer cleanup()

	addMember(ph)

	requester, _, _ := newPresence(partyTestNode)
	_, err := ph.JoinRequest(requester)
	if !errors.Is(err, runtime.ErrPartyFull) {
		t.Fatalf("expected ErrPartyFull, got %v", err)
	}
}

func TestPartyHandler_JoinRequestDuplicate(t *testing.T) {
	ph, cleanup, _ := createClosedParty(t)
	defer cleanup()

	requester, _, _ := newPresence(partyTestNode)
	_, _ = ph.JoinRequest(requester)

	_, err := ph.JoinRequest(requester)
	if !errors.Is(err, runtime.ErrPartyJoinRequestDuplicate) {
		t.Fatalf("expected ErrPartyJoinRequestDuplicate, got %v", err)
	}
}

func TestPartyHandler_JoinRequestAlreadyMember(t *testing.T) {
	ph, cleanup := createLightPartyHandler(t, loggerForTest(t), false, 10, nil)
	defer cleanup()

	leader, _ := addMember(ph)

	_, err := ph.JoinRequest(leader)
	if !errors.Is(err, runtime.ErrPartyJoinRequestAlreadyMember) {
		t.Fatalf("expected ErrPartyJoinRequestAlreadyMember, got %v", err)
	}
}

func TestPartyHandler_JoinRequestQueueFull(t *testing.T) {
	ph, cleanup := createLightPartyHandler(t, loggerForTest(t), false, 2, nil)
	defer cleanup()

	addMember(ph)

	for i := 0; i < 2; i++ {
		req, _, _ := newPresence(partyTestNode)
		_, _ = ph.JoinRequest(req)
	}

	extra, _, _ := newPresence(partyTestNode)
	_, err := ph.JoinRequest(extra)
	if !errors.Is(err, runtime.ErrPartyJoinRequestsFull) {
		t.Fatalf("expected ErrPartyJoinRequestsFull, got %v", err)
	}
}

func TestPartyHandler_AcceptJoinRequest(t *testing.T) {
	ph, cleanup, leaderSID := createClosedParty(t)
	defer cleanup()

	requester, _, _ := newPresence(partyTestNode)
	_, _ = ph.JoinRequest(requester)

	requesterUP := &rtapi.UserPresence{
		UserId:    requester.GetUserId(),
		SessionId: requester.GetSessionId(),
		Username:  requester.GetUsername(),
	}

	if err := ph.Accept(leaderSID.String(), partyTestNode, requesterUP, false); err != nil {
		t.Fatalf("Accept: %s", err)
	}
	if ph.members.Size() != 2 {
		t.Fatalf("expected 2 members after accept, got %d", ph.members.Size())
	}
}

func TestPartyHandler_AcceptByNonLeader(t *testing.T) {
	ph, cleanup, _ := createClosedParty(t)
	defer cleanup()

	requester, _, _ := newPresence(partyTestNode)
	_, _ = ph.JoinRequest(requester)

	requesterUP := &rtapi.UserPresence{
		UserId:    requester.GetUserId(),
		SessionId: requester.GetSessionId(),
		Username:  requester.GetUsername(),
	}

	fakeSID := uuid.Must(uuid.NewV4())
	err := ph.Accept(fakeSID.String(), partyTestNode, requesterUP, false)
	if !errors.Is(err, runtime.ErrPartyNotLeader) {
		t.Fatalf("expected ErrPartyNotLeader, got %v", err)
	}
}

func TestPartyHandler_AcceptNoRequest(t *testing.T) {
	ph, cleanup, leaderSID := createClosedParty(t)
	defer cleanup()

	ghostUP := &rtapi.UserPresence{
		UserId:    uuid.Must(uuid.NewV4()).String(),
		SessionId: uuid.Must(uuid.NewV4()).String(),
		Username:  "ghost",
	}
	err := ph.Accept(leaderSID.String(), partyTestNode, ghostUP, false)
	if !errors.Is(err, runtime.ErrPartyNotRequest) {
		t.Fatalf("expected ErrPartyNotRequest, got %v", err)
	}
}

func TestPartyHandler_JoinRequestListByLeader(t *testing.T) {
	ph, cleanup, leaderSID := createClosedParty(t)
	defer cleanup()

	r1, _, _ := newPresence(partyTestNode)
	r2, _, _ := newPresence(partyTestNode)
	_, _ = ph.JoinRequest(r1)
	_, _ = ph.JoinRequest(r2)

	requests, err := ph.JoinRequestList(leaderSID.String(), partyTestNode)
	if err != nil {
		t.Fatalf("JoinRequestList: %s", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(requests))
	}
}

func TestPartyHandler_JoinRequestListByNonLeader(t *testing.T) {
	ph, cleanup, _ := createClosedParty(t)
	defer cleanup()

	fakeSID := uuid.Must(uuid.NewV4())
	_, err := ph.JoinRequestList(fakeSID.String(), partyTestNode)
	if !errors.Is(err, runtime.ErrPartyNotLeader) {
		t.Fatalf("expected ErrPartyNotLeader, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// C. Matchmaker
// ---------------------------------------------------------------------------

func TestPartyHandler_MatchmakerAddByLeader(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	_, sessionIDs := addMembers(t, ph, 2)

	ticket, presences, err := ph.MatchmakerAdd(sessionIDs[0].String(), partyTestNode, "", 2, 2, 1, nil, nil)
	if err != nil {
		t.Fatalf("MatchmakerAdd: %s", err)
	}
	if ticket == "" {
		t.Fatal("expected ticket")
	}
	if len(presences) != 1 {
		t.Fatalf("expected 1 non-leader presence, got %d", len(presences))
	}
}

func TestPartyHandler_MatchmakerAddByNonLeader(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	_, sessionIDs := addMembers(t, ph, 2)

	_, _, err := ph.MatchmakerAdd(sessionIDs[1].String(), partyTestNode, "", 2, 2, 1, nil, nil)
	if !errors.Is(err, runtime.ErrPartyNotLeader) {
		t.Fatalf("expected ErrPartyNotLeader, got %v", err)
	}
}

func TestPartyHandler_MatchmakerAddStoppedParty(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	_, sessionIDs := addMembers(t, ph, 1)
	_ = ph.Close(sessionIDs[0].String(), partyTestNode)

	_, _, err := ph.MatchmakerAdd(sessionIDs[0].String(), partyTestNode, "", 1, 1, 1, nil, nil)
	if !errors.Is(err, runtime.ErrPartyClosed) {
		t.Fatalf("expected ErrPartyClosed, got %v", err)
	}
}

func TestPartyHandler_MatchmakerAddReturnsAllMemberPresences(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	_, sessionIDs := addMembers(t, ph, 4)

	_, presences, err := ph.MatchmakerAdd(sessionIDs[0].String(), partyTestNode, "", 4, 4, 1, nil, nil)
	if err != nil {
		t.Fatalf("MatchmakerAdd: %s", err)
	}
	if len(presences) != 3 {
		t.Fatalf("expected 3 non-leader presences, got %d", len(presences))
	}
	for _, p := range presences {
		if p.SessionID == sessionIDs[0] {
			t.Fatal("leader should not be in non-leader presences")
		}
	}
}

func TestPartyHandler_MatchmakerRemoveByLeader(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	_, sessionIDs := addMembers(t, ph, 2)

	ticket, _, _ := ph.MatchmakerAdd(sessionIDs[0].String(), partyTestNode, "", 2, 2, 1, nil, nil)
	if err := ph.MatchmakerRemove(sessionIDs[0].String(), partyTestNode, ticket); err != nil {
		t.Fatalf("MatchmakerRemove: %s", err)
	}
}

func TestPartyHandler_MatchmakerRemoveByNonLeader(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	_, sessionIDs := addMembers(t, ph, 2)

	ticket, _, _ := ph.MatchmakerAdd(sessionIDs[0].String(), partyTestNode, "", 2, 2, 1, nil, nil)
	err := ph.MatchmakerRemove(sessionIDs[1].String(), partyTestNode, ticket)
	if !errors.Is(err, runtime.ErrPartyNotLeader) {
		t.Fatalf("expected ErrPartyNotLeader, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// D. PartyPresenceList
// ---------------------------------------------------------------------------

func TestPartyPresenceList_JoinAndList(t *testing.T) {
	pl := NewPartyPresenceList(10)

	p1, _, _ := newPresence(partyTestNode)
	p2, _, _ := newPresence(partyTestNode)
	if _, err := pl.Join([]*Presence{p1, p2}); err != nil {
		t.Fatalf("Join: %s", err)
	}
	if len(pl.List()) != 2 {
		t.Fatalf("expected 2, got %d", len(pl.List()))
	}
}

func TestPartyPresenceList_JoinDuplicate(t *testing.T) {
	pl := NewPartyPresenceList(10)

	p1, _, _ := newPresence(partyTestNode)
	_, _ = pl.Join([]*Presence{p1})
	_, _ = pl.Join([]*Presence{p1})

	if pl.Size() != 1 {
		t.Fatalf("expected 1 after duplicate, got %d", pl.Size())
	}
}

func TestPartyPresenceList_JoinOverCapacity(t *testing.T) {
	pl := NewPartyPresenceList(1)

	p1, _, _ := newPresence(partyTestNode)
	_, _ = pl.Join([]*Presence{p1})

	p2, _, _ := newPresence(partyTestNode)
	_, err := pl.Join([]*Presence{p2})
	if !errors.Is(err, runtime.ErrPartyFull) {
		t.Fatalf("expected ErrPartyFull, got %v", err)
	}
}

func TestPartyPresenceList_LeaveAndSize(t *testing.T) {
	pl := NewPartyPresenceList(10)

	p1, _, _ := newPresence(partyTestNode)
	p2, _, _ := newPresence(partyTestNode)
	_, _ = pl.Join([]*Presence{p1, p2})

	pl.Leave([]*Presence{p1})
	if pl.Size() != 1 {
		t.Fatalf("expected 1 after leave, got %d", pl.Size())
	}
}

func TestPartyPresenceList_LeaveNonMember(t *testing.T) {
	pl := NewPartyPresenceList(10)

	p1, _, _ := newPresence(partyTestNode)
	_, _ = pl.Join([]*Presence{p1})

	nonMember, _, _ := newPresence(partyTestNode)
	processed, _ := pl.Leave([]*Presence{nonMember})
	if len(processed) != 0 {
		t.Fatalf("expected 0 processed, got %d", len(processed))
	}
}

func TestPartyPresenceList_Oldest(t *testing.T) {
	pl := NewPartyPresenceList(10)

	p1, sid1, _ := newPresence(partyTestNode)
	p2, _, _ := newPresence(partyTestNode)
	_, _ = pl.Join([]*Presence{p1, p2})

	pid, up := pl.Oldest()
	if pid == nil || up == nil {
		t.Fatal("expected non-nil oldest")
	}
	if pid.SessionID != sid1 {
		t.Fatalf("expected oldest session %s, got %s", sid1, pid.SessionID)
	}
}

func TestPartyPresenceList_OldestEmpty(t *testing.T) {
	pl := NewPartyPresenceList(10)
	pid, up := pl.Oldest()
	if pid != nil || up != nil {
		t.Fatal("expected nil for empty list")
	}
}

func TestPartyPresenceList_ReserveAndRelease(t *testing.T) {
	pl := NewPartyPresenceList(2)

	p1, _, _ := newPresence(partyTestNode)
	_, _ = pl.Join([]*Presence{p1})

	p2, _, _ := newPresence(partyTestNode)
	if err := pl.Reserve(p2); err != nil {
		t.Fatalf("Reserve: %s", err)
	}
	if pl.Size() != 2 {
		t.Fatalf("expected 2 (1+reserved), got %d", pl.Size())
	}

	pl.Release(p2)
	if pl.Size() != 1 {
		t.Fatalf("expected 1 after release, got %d", pl.Size())
	}
}

func TestPartyPresenceList_ReserveCountsTowardCapacity(t *testing.T) {
	pl := NewPartyPresenceList(2)

	p1, _, _ := newPresence(partyTestNode)
	_, _ = pl.Join([]*Presence{p1})

	p2, _, _ := newPresence(partyTestNode)
	_ = pl.Reserve(p2)

	p3, _, _ := newPresence(partyTestNode)
	err := pl.Reserve(p3)
	if !errors.Is(err, runtime.ErrPartyFull) {
		t.Fatalf("expected ErrPartyFull, got %v", err)
	}
}

func TestPartyPresenceList_JoinClearsReservation(t *testing.T) {
	pl := NewPartyPresenceList(2)

	p1, _, _ := newPresence(partyTestNode)
	_, _ = pl.Join([]*Presence{p1})

	p2, _, _ := newPresence(partyTestNode)
	_ = pl.Reserve(p2)

	if _, err := pl.Join([]*Presence{p2}); err != nil {
		t.Fatalf("Join after reserve: %s", err)
	}
	if pl.Size() != 2 {
		t.Fatalf("expected 2, got %d", pl.Size())
	}
	if len(pl.List()) != 2 {
		t.Fatalf("expected 2 in list, got %d", len(pl.List()))
	}
}

// ---------------------------------------------------------------------------
// E. LobbyGroup Wrapper
// ---------------------------------------------------------------------------

func TestLobbyGroup_GetLeaderReturnsExpectedInitialLeader(t *testing.T) {
	expectedSID, _ := uuid.NewV4()
	expectedUID, _ := uuid.NewV4()
	expectedUP := &rtapi.UserPresence{
		UserId:    expectedUID.String(),
		SessionId: expectedSID.String(),
		Username:  "expected",
	}

	ph, cleanup := createLightPartyHandler(t, loggerForTest(t), true, 10, expectedUP)
	defer cleanup()

	lg := &LobbyGroup{ph: ph}
	leader := lg.GetLeader()
	if leader == nil {
		t.Fatal("expected leader")
	}
	if leader.SessionId != expectedSID.String() {
		t.Fatalf("expected %s, got %s", expectedSID, leader.SessionId)
	}
}

func TestLobbyGroup_GetLeaderReturnsActualLeader(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	_, sid := addMember(ph)

	lg := &LobbyGroup{ph: ph}
	leader := lg.GetLeader()
	if leader == nil {
		t.Fatal("expected leader")
	}
	if leader.SessionId != sid.String() {
		t.Fatalf("expected %s, got %s", sid, leader.SessionId)
	}
}

func TestLobbyGroup_SizeWithNilHandler(t *testing.T) {
	lg := &LobbyGroup{ph: nil}
	if lg.Size() != 1 {
		t.Fatalf("expected 1 for nil handler, got %d", lg.Size())
	}
}

func TestLobbyGroup_MatchmakerAddDelegatesToHandler(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	_, sessionIDs := addMembers(t, ph, 2)

	lg := &LobbyGroup{ph: ph}
	ticket, presences, err := lg.MatchmakerAdd(sessionIDs[0].String(), partyTestNode, "", 2, 2, 1, nil, nil)
	if err != nil {
		t.Fatalf("MatchmakerAdd via LobbyGroup: %s", err)
	}
	if ticket == "" {
		t.Fatal("expected ticket")
	}
	if len(presences) != 1 {
		t.Fatalf("expected 1, got %d", len(presences))
	}
}

// ---------------------------------------------------------------------------
// G. Edge Cases and Concurrency
// ---------------------------------------------------------------------------

func TestPartyHandler_ConcurrentJoinLeave(t *testing.T) {
	ph, cleanup := createLightPartyHandler(t, loggerForTest(t), true, 200, nil)
	defer cleanup()

	addMember(ph) // seed leader

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, _, _ := newPresence(partyTestNode)
			ph.Join([]*Presence{p})
			ph.Leave([]*Presence{p})
		}()
	}
	wg.Wait()

	if ph.members.Size() < 1 {
		t.Fatalf("expected at least 1 member, got %d", ph.members.Size())
	}
}

func TestPartyHandler_LeaderLeaveDuringMatchmaking(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	presences, sessionIDs := addMembers(t, ph, 3)

	ticket, _, err := ph.MatchmakerAdd(sessionIDs[0].String(), partyTestNode, "", 2, 2, 1, nil, nil)
	if err != nil {
		t.Fatalf("MatchmakerAdd: %s", err)
	}
	if ticket == "" {
		t.Fatal("expected ticket")
	}

	ph.Leave([]*Presence{presences[0]})

	ph.RLock()
	newLeader := ph.leader.UserPresence.SessionId
	ph.RUnlock()

	if newLeader != sessionIDs[1].String() {
		t.Fatalf("expected new leader %s, got %s", sessionIDs[1], newLeader)
	}

	// New leader can matchmake.
	ticket2, _, err := ph.MatchmakerAdd(sessionIDs[1].String(), partyTestNode, "", 2, 2, 1, nil, nil)
	if err != nil {
		t.Fatalf("MatchmakerAdd by new leader: %s", err)
	}
	if ticket2 == "" {
		t.Fatal("expected ticket from new leader")
	}
}

func TestPartyHandler_MemberJoinCancelsMatchmaking(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	_, leaderSID := addMember(ph)

	ticket, _, err := ph.MatchmakerAdd(leaderSID.String(), partyTestNode, "", 2, 2, 1, nil, nil)
	if err != nil {
		t.Fatalf("MatchmakerAdd: %s", err)
	}
	if ticket == "" {
		t.Fatal("expected ticket")
	}

	// New member joins (open party auto-accepts via JoinRequest).
	newcomer, _, _ := newPresence(partyTestNode)
	accepted, err := ph.JoinRequest(newcomer)
	if err != nil {
		t.Fatalf("JoinRequest: %s", err)
	}
	if !accepted {
		t.Fatal("expected auto-accept")
	}

	// New ticket should work (old one removed by JoinRequest).
	ticket2, _, err := ph.MatchmakerAdd(leaderSID.String(), partyTestNode, "", 2, 2, 1, nil, nil)
	if err != nil {
		t.Fatalf("MatchmakerAdd after join: %s", err)
	}
	if ticket2 == "" {
		t.Fatal("expected new ticket")
	}
}

func TestPartyHandler_PromoteThenMatchmake(t *testing.T) {
	ph, cleanup := createDefaultPartyHandler(t)
	defer cleanup()

	_, sessionIDs := addMembers(t, ph, 2)

	member1UP := getMemberUP(ph, sessionIDs[1])
	if err := ph.Promote(sessionIDs[0].String(), partyTestNode, member1UP); err != nil {
		t.Fatalf("Promote: %s", err)
	}

	// Old leader cannot matchmake.
	_, _, err := ph.MatchmakerAdd(sessionIDs[0].String(), partyTestNode, "", 2, 2, 1, nil, nil)
	if !errors.Is(err, runtime.ErrPartyNotLeader) {
		t.Fatalf("expected ErrPartyNotLeader for old leader, got %v", err)
	}

	// New leader can matchmake.
	ticket, _, err := ph.MatchmakerAdd(sessionIDs[1].String(), partyTestNode, "", 2, 2, 1, nil, nil)
	if err != nil {
		t.Fatalf("MatchmakerAdd by new leader: %s", err)
	}
	if ticket == "" {
		t.Fatal("expected ticket")
	}
}

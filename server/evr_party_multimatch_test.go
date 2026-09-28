package server

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama/v3/server/evr"
	uatomic "go.uber.org/atomic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// Regression tests: a party queueing for arena ends up with TWO live
// matchmaker tickets, so it is matched and built into more than one match
// (production, 2026-09-27: 13 of 73 arena entrants placed into 9-13 distinct
// matches in 30 minutes; "the opposite team is empty").
//
// The race (lobbyMatchMakeWithFallback, evr_lobby_matchmake.go):
//
//   - cancelTicketForLateArrival signals the party's ticketRebuildCh
//     (buffer 1) whenever the leader is on the matchmaking stream, including
//     while the leader has no ticket yet (configureParty tracks the leader on
//     the stream before the 15 s formation phase).
//   - The leader's loop submits ticket #1 after formation, then selects on
//     rebuildCh, finds the stale signal, sets currentTicket = "" on the
//     assumption that the canceller already removed the ticket (it did not:
//     ticket #1 did not exist when it ran), and submits ticket #2.
//   - Ticket #1 is orphaned: nothing tracks it, and the loop's deferred
//     Remove removes only currentTicket.
//
// The trigger: since 995bfd879 (#620, part B), a member who queues for arena
// from the leader's social lobby is no longer skipped by
// isFollowerAlreadyInLeaderMatch, so its find reaches the late-arrival check
// while the leader is in formation. Before it, that member's find returned at
// the fast path and never cancelled anything.
//
// Invariant asserted by both tests: after the leader's loop has submitted,
// the party holds exactly one live ticket.

// multiMatchParty is the shared fixture: leader L and member M in one party,
// both in social lobby S, L on the matchmaking stream for arena as
// configureParty leaves it (evr_lobby_find.go, configureParty), and no ticket.
type multiMatchParty struct {
	mm            *LocalMatchmaker
	tracker       *mockMatchmakingTracker
	ph            *PartyHandler
	lobbyGroup    *LobbyGroup
	pipeline      *EvrPipeline
	leaderSession *sessionWS
	memberSession *sessionWS
	leaderParams  *LobbySessionParameters
	memberParams  *LobbySessionParameters
	logger        *zap.Logger
	logs          *observer.ObservedLogs
}

func newMultiMatchParty(t *testing.T) *multiMatchParty {
	t.Helper()

	core, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)

	mm, mmCleanup := createLightMatchmaker(t, loggerForTest(t))
	t.Cleanup(mmCleanup)
	tracker := newMockMatchmakingTracker()

	const node = "testnode"
	leaderSID := uuid.FromStringOrNil("00000000-0000-0000-0000-0000000000a1")
	leaderUID := uuid.FromStringOrNil("00000000-0000-0000-0000-0000000000b1")
	memberSID := uuid.FromStringOrNil("00000000-0000-0000-0000-0000000000a2")
	memberUID := uuid.FromStringOrNil("00000000-0000-0000-0000-0000000000b2")
	groupID := uuid.FromStringOrNil("00000000-0000-0000-0000-0000000000c1")
	partyUUID := uuid.FromStringOrNil("00000000-0000-0000-0000-0000000000d1")
	socialLobby := MatchID{UUID: uuid.FromStringOrNil("00000000-0000-0000-0000-0000000000e1"), Node: node}

	ph := &PartyHandler{
		logger:          loggerForTest(t),
		matchmaker:      mm,
		router:          &DummyMessageRouter{},
		ID:              partyUUID,
		Node:            node,
		IDStr:           fmt.Sprintf("%v.%v", partyUUID.String(), node),
		Open:            true,
		MaxSize:         8,
		ctx:             context.Background(),
		members:         NewPartyPresenceList(8),
		ticketRebuildCh: make(chan struct{}, 1),
	}
	ph.leader = &PartyLeader{
		UserPresence: &rtapi.UserPresence{UserId: leaderUID.String(), SessionId: leaderSID.String(), Username: "leader"},
		PresenceID:   &PresenceID{SessionID: leaderSID, Node: node},
	}
	if _, err := ph.members.Join([]*Presence{
		{ID: PresenceID{SessionID: leaderSID, Node: node}, UserID: leaderUID, Meta: PresenceMeta{Username: "leader"}},
		{ID: PresenceID{SessionID: memberSID, Node: node}, UserID: memberUID, Meta: PresenceMeta{Username: "member"}},
	}); err != nil {
		t.Fatalf("party members.Join: %v", err)
	}
	lobbyGroup := &LobbyGroup{name: "multimatch", ph: ph}
	if lobbyGroup.Size() != 2 {
		t.Fatalf("fixture: party size %d, want 2", lobbyGroup.Size())
	}

	// Both are in social lobby S, per the tracker and the label.
	registry := newMockFollowMatchRegistry()
	registry.SetMatch(socialLobby, &MatchLabel{
		ID:          socialLobby,
		Mode:        evr.ModeSocialPublic,
		Open:        true,
		PlayerLimit: 12,
		Players: []PlayerInfo{
			{UserID: leaderUID.String(), SessionID: leaderSID.String(), Team: 0},
			{UserID: memberUID.String(), SessionID: memberSID.String(), Team: 0},
		},
	})
	for _, s := range []struct{ sid, uid uuid.UUID }{{leaderSID, leaderUID}, {memberSID, memberUID}} {
		tracker.Track(context.Background(), s.sid,
			PresenceStream{Mode: StreamModeService, Subject: s.sid, Label: StreamLabelMatchService},
			s.uid, PresenceMeta{Status: socialLobby.String()})
	}

	pipeline := &EvrPipeline{
		node:                       node,
		config:                     cfg,
		db:                         stubDB(t),
		partyFormationTimeout:      50 * time.Millisecond,
		partyFormationPollInterval: 5 * time.Millisecond,
		pollFollowInterval:         10 * time.Millisecond,
		pollFollowMaxDuration:      200 * time.Millisecond,
		nk: &RuntimeGoNakamaModule{
			logger:        loggerForTest(t),
			tracker:       tracker,
			streamManager: testStreamManager{},
			matchRegistry: registry,
			metrics:       &testMetrics{},
			node:          node,
		},
	}

	newSession := func(sid, uid uuid.UUID, username string) *sessionWS {
		ctx, cancel := context.WithCancel(context.WithValue(context.Background(),
			ctxSessionParametersKey{}, uatomic.NewPointer(&SessionParameters{})))
		t.Cleanup(cancel)
		s := &sessionWS{}
		s.id = sid
		s.userID = uid
		s.username = uatomic.NewString(username)
		s.ctx = ctx
		s.ctxCancelFn = cancel
		s.logger = loggerForTest(t)
		s.format = SessionFormatProtobuf
		s.outgoingCh = make(chan []byte, 16)
		s.matchmaker = mm
		s.tracker = tracker
		s.pipeline = &Pipeline{node: node, tracker: tracker, router: &DummyMessageRouter{}}
		return s
	}

	leaderParams := makeMatchmakeTestLobbyParams(leaderUID, groupID, evr.ModeArenaPublic, 2)
	leaderParams.PartyGroupName = "multimatch"
	leaderParams.CurrentMatchID = socialLobby
	leaderParams.MatchmakingTimeout = 30 * time.Second

	memberParams := makeMatchmakeTestLobbyParams(memberUID, groupID, evr.ModeArenaPublic, 2)
	memberParams.PartyGroupName = "multimatch"
	memberParams.CurrentMatchID = socialLobby
	memberParams.MatchmakingTimeout = 5 * time.Second

	// configureParty tracks the leader on the matchmaking stream, with its
	// parameters as the status, before the formation phase: from here the
	// leader "is matchmaking" but has no ticket.
	tracker.Track(context.Background(), leaderSID, leaderParams.MatchmakingStream(), leaderUID,
		PresenceMeta{Status: leaderParams.String()})

	return &multiMatchParty{
		mm:            mm,
		tracker:       tracker,
		ph:            ph,
		lobbyGroup:    lobbyGroup,
		pipeline:      pipeline,
		leaderSession: newSession(leaderSID, leaderUID, "leader"),
		memberSession: newSession(memberSID, memberUID, "member"),
		leaderParams:  leaderParams,
		memberParams:  memberParams,
		logger:        logger,
		logs:          logs,
	}
}

// liveTickets returns the party's tickets that are live in the matchmaker.
func (f *multiMatchParty) liveTickets() []string {
	f.mm.Lock()
	defer f.mm.Unlock()
	var tickets []string
	for ticket := range f.mm.partyTickets[f.ph.IDStr] {
		if _, ok := f.mm.indexes[ticket]; ok {
			tickets = append(tickets, ticket)
		}
	}
	return tickets
}

// runLeaderLoop starts the leader's lobbyMatchMakeWithFallback, waits for its
// first ticket, then gives it settle to submit anything else it is going to.
// A rebuild on a pending signal is immediate (the loop selects on rebuildCh
// right after the first submit), so settle is ample. It returns the party's
// live tickets at that point, and a func that stops the loop and waits for it
// to exit (also run at test cleanup).
func (f *multiMatchParty) runLeaderLoop(t *testing.T, settle time.Duration) ([]string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- f.pipeline.lobbyMatchMakeWithFallback(ctx, f.logger, f.leaderSession, f.leaderParams, f.lobbyGroup)
	}()
	var stopped bool
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("leader's matchmaking loop did not exit after cancel")
		}
	}
	t.Cleanup(stop)

	deadline := time.Now().Add(5 * time.Second)
	for len(f.liveTickets()) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("fixture: the leader's loop never submitted a party ticket; logs: %v", f.logs.All())
		}
		time.Sleep(5 * time.Millisecond)
	}
	settleDeadline := time.Now().Add(settle)
	for time.Now().Before(settleDeadline) && len(f.liveTickets()) < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	return f.liveTickets(), stop
}

// multiMatchMemberFind runs the non-leader branch of lobbyFind for a member
// (evr_lobby_find.go, lobbyFind), in lobbyFind's order, against the real
// functions. Two steps are left out:
//
//   - configureParty: the member is already in the party, and the test hands
//     lobbyFind's result (lobbyGroup, isLeader=false) in directly.
//   - lobbyAuthorize: it reads enforcement journals and writes a server
//     profile through storage, which needs a database. In production a
//     member in good standing passes it, and nothing it does feeds the steps
//     below.
//
// The monitor goroutine and the lifecycle observer are also omitted; neither
// affects tickets.
func multiMatchMemberFind(ctx context.Context, t *testing.T, logger *zap.Logger, p *EvrPipeline, session *sessionWS, lobbyParams *LobbySessionParameters, lobbyGroup *LobbyGroup) {
	t.Helper()

	lobbyParams.captureMemberRecordAtFind(session)

	// The fast path. If it holds, lobbyFind returns here.
	if p.isFollowerAlreadyInLeaderMatch(ctx, logger, session, lobbyGroup, lobbyParams.CurrentMatchID, lobbyParams.Mode) {
		logger.Debug("Follower already in leader's match, skipping follow path")
		return
	}

	if p.isLeaderHeadingToSocial(ctx, logger, session, lobbyParams, lobbyGroup) {
		// The leader is on the matchmaking stream for arena. Reading that as
		// "heading to social" would rewrite the member's mode to social and
		// skip the late-arrival check: the test would pass for the wrong reason.
		t.Fatal("fixture: leader queueing for arena was read as heading to social")
	}

	// lobbyAuthorize is skipped here (see above).

	ctx, cancel := context.WithTimeoutCause(ctx, lobbyParams.MatchmakingTimeout, ErrMatchmakingTimeout)
	defer cancel()

	if err := JoinMatchmakingStream(logger, session, lobbyParams); err != nil {
		t.Fatalf("JoinMatchmakingStream: %v", err)
	}

	if reservationMatchID, found := p.findReservation(ctx, logger, session, lobbyGroup); found {
		t.Fatalf("fixture: unexpected reservation for the member in %s", reservationMatchID.String())
	}

	if !shouldFollowerFindOrCreateSocial(lobbyParams.Mode) &&
		lobbyGroup.Size() > 1 &&
		!lobbyGroup.HasSessionOnTicket(session.id.String()) {
		p.cancelTicketForLateArrival(ctx, logger, session, lobbyParams, lobbyGroup)
	}

	if p.TryFollowPartyLeader(ctx, logger, session, lobbyParams, lobbyGroup) {
		return
	}
	p.pollFollowPartyLeader(ctx, logger, session, lobbyParams, lobbyGroup)
}

// checkOneTicket asserts the invariant: while the leader's loop runs the party
// holds exactly one live ticket, and once the loop has exited (the leader
// joined a match or cancelled) it holds none.
func (f *multiMatchParty) checkOneTicket(t *testing.T, settle time.Duration) {
	t.Helper()
	tickets, stop := f.runLeaderLoop(t, settle)
	if len(tickets) != 1 {
		t.Errorf("the party holds %d live matchmaker tickets after the leader's loop submitted, want exactly 1: %v. "+
			"%q logged %d time(s), %q logged %d time(s). Every ticket but the loop's currentTicket is orphaned: "+
			"the loop's deferred Remove never removes it, so the matchmaker (and the overflow builder, which "+
			"builds from Extract() without removing) can place this party into another match",
			len(tickets), tickets,
			"Cancelling matchmaking ticket for late party arrival",
			f.logs.FilterMessage("Cancelling matchmaking ticket for late party arrival").Len(),
			"Ticket rebuild triggered by late party arrival",
			f.logs.FilterMessage("Ticket rebuild triggered by late party arrival").Len())
	}
	stop()
	if left := f.liveTickets(); len(left) != 0 {
		t.Errorf("after the leader's matchmaking loop exited, the party still holds %d live ticket(s): %v. "+
			"Nothing will remove them; they stay matchable until they expire", len(left), left)
	}
}

// (a) The race in isolation. A late-arrival cancel lands while the leader is
// on the matchmaking stream with no ticket yet (formation). The cancel has
// nothing to remove but leaves a rebuild signal; the loop's first submit is
// then followed by a rebuild that does not remove it.
func TestPartyTicketRebuild_CancelBeforeFirstTicket_LeavesOneLiveTicket(t *testing.T) {
	f := newMultiMatchParty(t)

	if n := len(f.liveTickets()); n != 0 {
		t.Fatalf("fixture: party already holds %d tickets", n)
	}
	f.pipeline.cancelTicketForLateArrival(context.Background(), f.logger, f.memberSession, f.memberParams, f.lobbyGroup)

	f.checkOneTicket(t, 500*time.Millisecond)
}

// (b) The trigger, end to end. M sits in L's social lobby and queues for
// arena (CurrentMatchID = that lobby) while L is in formation. M's find goes
// through lobbyFind's member branch, which since 995bfd879 reaches the
// late-arrival check instead of returning at the fast path.
func TestPartyFormation_MemberQueuesArenaFromLeaderSocialLobby_LeavesOneLiveTicket(t *testing.T) {
	f := newMultiMatchParty(t)

	multiMatchMemberFind(context.Background(), t, f.logger, f.pipeline, f.memberSession, f.memberParams, f.lobbyGroup)
	if n := len(f.liveTickets()); n != 0 {
		t.Fatalf("fixture: party holds %d tickets before the leader's loop ran", n)
	}

	f.checkOneTicket(t, 500*time.Millisecond)
}

// sessionLiveTickets returns the live tickets that carry sessionID, with the
// session IDs on each. Unlike liveTickets it also sees a solo ticket, which
// has no party ID.
func (f *multiMatchParty) sessionLiveTickets(sessionID string) map[string][]string {
	f.mm.Lock()
	defer f.mm.Unlock()
	tickets := make(map[string][]string)
	for ticket := range f.mm.sessionTickets[sessionID] {
		index, ok := f.mm.indexes[ticket]
		if !ok {
			continue
		}
		sessions := make([]string, 0, len(index.Entries))
		for _, entry := range index.Entries {
			sessions = append(sessions, entry.Presence.SessionId)
		}
		tickets[ticket] = sessions
	}
	return tickets
}

// (c) What the rebuild is for (#459): a member who joins after the leader's
// loop has submitted is put on the leader's ticket immediately, not at the
// fallback timer, and the party still holds exactly one live ticket.
//
// The leader starts alone, so its loop skips formation and submits a solo
// ticket. The member then joins and its find cancels that ticket. The
// rebuilt ticket must be the leader's only live ticket and carry both.
func TestPartyTicketRebuild_LateArrivalAfterFirstTicket_OneLiveTicketWithLateMember(t *testing.T) {
	f := newMultiMatchParty(t)
	leaderSID := f.leaderSession.id.String()
	memberSID := f.memberSession.id.String()
	memberPresence := &Presence{
		ID:     PresenceID{SessionID: f.memberSession.id, Node: f.pipeline.node},
		UserID: f.memberSession.userID,
		Meta:   PresenceMeta{Username: "member"},
	}

	// The member is not in the party yet.
	if left, _ := f.ph.members.Leave([]*Presence{memberPresence}); len(left) != 1 || f.lobbyGroup.Size() != 1 {
		t.Fatalf("fixture: party size %d after removing the member, want 1", f.lobbyGroup.Size())
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- f.pipeline.lobbyMatchMakeWithFallback(ctx, f.logger, f.leaderSession, f.leaderParams, f.lobbyGroup)
	}()
	var stopped bool
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("leader's matchmaking loop did not exit after cancel")
		}
	}
	t.Cleanup(stop)

	// waitFor polls the leader's live tickets until ok holds.
	waitFor := func(what string, ok func(map[string][]string) bool) map[string][]string {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for {
			tickets := f.sessionLiveTickets(leaderSID)
			if ok(tickets) {
				return tickets
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: leader's live tickets %v; logs: %v", what, tickets, f.logs.All())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	first := waitFor("the leader's loop never submitted its solo ticket", func(m map[string][]string) bool { return len(m) == 1 })
	var soloTicket string
	for ticket, sessions := range first {
		soloTicket = ticket
		if len(sessions) != 1 || sessions[0] != leaderSID {
			t.Fatalf("fixture: first ticket carries %v, want the leader alone", sessions)
		}
	}

	// The member joins, then its find reaches the late-arrival check.
	if _, err := f.ph.members.Join([]*Presence{memberPresence}); err != nil {
		t.Fatalf("member rejoin: %v", err)
	}
	f.pipeline.cancelTicketForLateArrival(context.Background(), f.logger, f.memberSession, f.memberParams, f.lobbyGroup)

	rebuilt := waitFor("the leader's ticket was not rebuilt with the late member", func(m map[string][]string) bool {
		if len(m) != 1 {
			return false
		}
		for ticket, sessions := range m {
			return ticket != soloTicket && len(sessions) == 2
		}
		return false
	})
	for _, sessions := range rebuilt {
		if !slices.Contains(sessions, memberSID) {
			t.Errorf("rebuilt ticket carries %v, want the late member %s on it", sessions, memberSID)
		}
	}
	if n := f.logs.FilterMessage("Matchmaking fallback, refreshing ticket with relaxed criteria").Len(); n != 0 {
		t.Errorf("the ticket was rebuilt by the fallback timer (%d fallback(s)), want the late-arrival signal", n)
	}
	if n := f.logs.FilterMessage("Ticket rebuild triggered by late party arrival").Len(); n != 1 {
		t.Errorf("late-arrival rebuild logged %d time(s), want 1", n)
	}
	// Give any further submit a chance to land, then re-check the invariant.
	time.Sleep(100 * time.Millisecond)
	if tickets := f.sessionLiveTickets(leaderSID); len(tickets) != 1 {
		t.Errorf("the leader holds %d live tickets after the rebuild, want exactly 1: %v", len(tickets), tickets)
	}

	stop()
	if left := f.sessionLiveTickets(leaderSID); len(left) != 0 {
		t.Errorf("after the leader's matchmaking loop exited, it still holds %d live ticket(s): %v", len(left), left)
	}
}

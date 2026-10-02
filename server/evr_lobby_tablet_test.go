package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
)

// tabletEnv is a pipeline with a party registry and two or more sessions, some in an SNS (tablet)
// party, as the SNS party handlers leave them (currentSNSPartyID, currentPartyID).
type tabletEnv struct {
	t        *testing.T
	ep       *EvrPipeline
	pr       PartyRegistry
	tracker  *partyStreamTracker
	sessions *sessionMapRegistry
}

func newTabletEnv(t *testing.T) *tabletEnv {
	t.Helper()
	logger := loggerForTest(t)
	tracker := newPartyStreamTracker()
	mm, mmCleanup := createLightMatchmaker(t, logger)
	t.Cleanup(mmCleanup)
	pr := NewLocalPartyRegistry(logger, cfg, mm, tracker, testStreamManager{}, &DummyMessageRouter{}, "testnode")
	sessions := &sessionMapRegistry{sessions: map[uuid.UUID]Session{}}
	ep := &EvrPipeline{
		node: "testnode",
		nk: &RuntimeGoNakamaModule{
			logger:          logger,
			partyRegistry:   pr,
			tracker:         tracker,
			sessionRegistry: sessions,
			node:            "testnode",
		},
		snsPartyIDToUUID: &MapOf[uint64, uuid.UUID]{},
	}
	return &tabletEnv{t: t, ep: ep, pr: pr, tracker: tracker, sessions: sessions}
}

// session is a client session; runtime clients declare social level 1 at login, stock ones 0.
func (e *tabletEnv) session(name string, runtimeClient bool) *sessionWS {
	s := newPartyMemberSession(e.t, name, e.tracker, e.pr, e.ep)
	params, _ := LoadParams(s.Context())
	level := 0
	if runtimeClient {
		level = 1
	}
	params.loginPayload = &evr.LoginProfile{NevrSocial: level}
	e.sessions.sessions[s.id] = s
	return s
}

// tabletParty makes an SNS party led by the first session with all of them as members, the way
// snsPartyCreateRequest/snsPartyTrackAndJoin leave it.
func (e *tabletEnv) tabletParty(snsID uint64, members ...*sessionWS) *PartyHandler {
	leader := members[0]
	ph := e.pr.(*LocalPartyRegistry).Create(true, 4, &rtapi.UserPresence{
		UserId: leader.userID.String(), SessionId: leader.id.String(), Username: leader.Username(),
	})
	presences := make([]*Presence, 0, len(members))
	for _, m := range members {
		presences = append(presences, &Presence{
			ID:     PresenceID{SessionID: m.id, Node: "testnode"},
			UserID: m.userID,
			Meta:   PresenceMeta{Username: m.Username()},
		})
	}
	ph.Join(presences)
	e.ep.snsPartyIDToUUID.Store(snsID, ph.ID)
	for _, m := range members {
		params, _ := LoadParams(m.Context())
		params.currentSNSPartyID = snsID
		params.currentPartyID = ph.ID
	}
	return ph
}

func findParams(group string) *LobbySessionParameters {
	return &LobbySessionParameters{PartyGroupName: group, Mode: evr.ModeArenaPublic}
}

// Rule branch 1: a runtime client in a tablet party of 2+ matchmakes with it, and its party group name
// does not apply: the group party is never created or joined.
func TestTabletPartyWinsOverThePartyGroup(t *testing.T) {
	for _, group := range []string{"", "tablet", "friends"} {
		e := newTabletEnv(t)
		leader, member := e.session("leader", true), e.session("member", true)
		ph := e.tabletParty(77, leader, member)

		require.True(t, e.ep.lobbyPartyApplies(leader, findParams(group)), "group %q", group)
		got, isLeader, err := e.ep.joinLobbyParty(loggerForTest(t), leader, findParams(group))
		require.NoError(t, err)
		require.Equal(t, ph.ID, got.ID(), "the tablet party is the lobby party (group %q)", group)
		require.True(t, isLeader)
		_, memberIsLeader, err := e.ep.joinLobbyParty(loggerForTest(t), member, findParams(group))
		require.NoError(t, err)
		require.False(t, memberIsLeader)
		if group == "friends" {
			_, found := e.pr.(*LocalPartyRegistry).LookupGroupPartyID("friends")
			require.False(t, found, "the party group was joined despite the tablet party")
		}
		params, _ := LoadParams(leader.Context())
		require.Equal(t, ph.ID, params.currentPartyID)
	}
}

// Rule branch 2: not in a tablet party (a party of one, as every tablet player is from login, or no SNS
// party at all) with a party group name: the party group applies, as before.
func TestNoTabletPartyThePartyGroupApplies(t *testing.T) {
	e := newTabletEnv(t)
	solo := e.session("solo", true)
	ph := e.tabletParty(78, solo) // party of one
	require.True(t, e.ep.lobbyPartyApplies(solo, findParams("friends")))
	got, _, err := e.ep.joinLobbyParty(loggerForTest(t), solo, findParams("friends"))
	require.NoError(t, err)
	require.NotEqual(t, ph.ID, got.ID(), "a party of one is not a tablet party")
	groupID, found := e.pr.(*LocalPartyRegistry).LookupGroupPartyID("friends")
	require.True(t, found)
	require.Equal(t, groupID, got.ID())

	stock := e.session("stock", false) // no SNS party
	require.True(t, e.ep.lobbyPartyApplies(stock, findParams("friends")))
	got, _, err = e.ep.joinLobbyParty(loggerForTest(t), stock, findParams("friends"))
	require.NoError(t, err)
	require.Equal(t, groupID, got.ID())
}

// Rule branch 3: no tablet party and no group name: no lobby party, as before. "tablet" is no longer a
// placeholder for "no group" (owner, 2026-10-02: only he ever used it); it is a group name like any other.
func TestNoTabletPartyNoGroupNoLobbyParty(t *testing.T) {
	e := newTabletEnv(t)
	solo := e.session("solo", true)
	e.tabletParty(79, solo)
	require.False(t, e.ep.lobbyPartyApplies(solo, findParams("")))
	require.True(t, e.ep.lobbyPartyApplies(solo, findParams("tablet")))
}

// Guard: a tablet party with a stock member is not used (the stock client is never told where the
// leader goes), and the party group still does not apply, since the session is in a tablet party.
func TestTabletPartyWithAStockMemberIsNotUsed(t *testing.T) {
	e := newTabletEnv(t)
	leader, stock := e.session("leader", true), e.session("stock", false)
	e.tabletParty(80, leader, stock)
	require.False(t, e.ep.lobbyPartyApplies(leader, findParams("friends")))
	_, _, err := e.ep.joinLobbyParty(loggerForTest(t), leader, findParams("friends"))
	require.True(t, errors.Is(err, errTabletPartyNotUsable))
	_, found := e.pr.(*LocalPartyRegistry).LookupGroupPartyID("friends")
	require.False(t, found)
}

// Guard: the tablet party is found by its SNS id, so a currentPartyID a party group join overwrote is
// put back to the tablet party, which every SNS party handler reads.
func TestTabletPartyRestoresAnOverwrittenCurrentPartyID(t *testing.T) {
	e := newTabletEnv(t)
	leader, member := e.session("leader", true), e.session("member", true)
	ph := e.tabletParty(81, leader, member)
	params, _ := LoadParams(leader.Context())
	params.currentPartyID = uuid.Must(uuid.NewV4()) // what JoinPartyGroup did before the rule
	got, _, err := e.ep.joinLobbyParty(loggerForTest(t), leader, findParams("friends"))
	require.NoError(t, err)
	require.Equal(t, ph.ID, got.ID())
	params, _ = LoadParams(leader.Context())
	require.Equal(t, ph.ID, params.currentPartyID)
}

// Guard: an SNS id that maps to a party this session is not in (a stale id) is no tablet party.
func TestTabletPartyMustContainTheSession(t *testing.T) {
	e := newTabletEnv(t)
	a, b, outsider := e.session("a", true), e.session("b", true), e.session("outsider", true)
	e.tabletParty(82, a, b)
	params, _ := LoadParams(outsider.Context())
	params.currentSNSPartyID = 82
	require.False(t, e.ep.lobbyPartyApplies(outsider, findParams("")))
}

// newFormationParty is the multimatch fixture's party of two as the formation wait sees it: both
// members on the party stream, the leader on the matchmaking stream (it pressed Find) and the member not,
// with a formation window long enough that only skipping it can file a ticket within the test.
func newFormationParty(t *testing.T) *multiMatchParty {
	f := newMultiMatchParty(t)
	f.pipeline.partyFormationTimeout = time.Minute
	partyStream := PresenceStream{Mode: StreamModeParty, Subject: f.lobbyGroup.ID(), Label: f.pipeline.node}
	for _, s := range []*sessionWS{f.leaderSession, f.memberSession} {
		f.tracker.Track(context.Background(), s.id, partyStream, s.userID, PresenceMeta{Username: s.Username()})
	}
	// The formation wait reads the party stream through the leader's tracker; the fixture's mock lists
	// no stream, which would read as "every member ready" and file at once for any party.
	listing := &listingTracker{mockMatchmakingTracker: f.tracker}
	f.leaderSession.tracker = listing
	require.Len(t, listing.ListByStream(partyStream, true, true), 2)
	return f
}

// listingTracker is the mock tracker with ListByStream answered from what was tracked.
type listingTracker struct {
	*mockMatchmakingTracker
}

func (t *listingTracker) ListByStream(stream PresenceStream, _, _ bool) []*Presence {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var out []*Presence
	for key, meta := range t.presences {
		if key.stream == stream {
			out = append(out, &Presence{ID: PresenceID{SessionID: key.sessionID}, Stream: stream, UserID: key.userID, Meta: *meta})
		}
	}
	return out
}

// The formation wait (lobbyMatchMakeWithFallback) gives a party group's members time to hit
// matchmaking before the leader's ticket goes in. A tablet party's members never hit matchmaking: the
// game client drops a member's find (CR15NetGame::FindIfPartyHost, echovr.exe 0x14016afc0) and members
// follow the leader from party data. So a tablet party's ticket goes in at once (owner, 2026-10-02),
// where the wait would always have run its full 15 s.
func TestTabletPartySkipsTheFormationWait(t *testing.T) {
	f := newFormationParty(t)
	f.lobbyGroup.tablet = true

	start := time.Now()
	tickets, stop := f.runLeaderLoop(t, 0)
	require.Len(t, tickets, 1, "the tablet party's one ticket")
	require.Less(t, time.Since(start), 5*time.Second, "the ticket waited for formation")
	require.Equal(t, 1, f.logs.FilterMessage("Tablet party: submitting ticket without the formation wait").Len())
	require.Zero(t, f.logs.FilterMessage("Formation timeout -- submitting ticket with ready members").Len())
	stop()
}

// Control: a party group in the same state still waits for its members (here, the whole window, since
// the member never comes), so nothing is filed while it does.
func TestPartyGroupStillWaitsForFormation(t *testing.T) {
	f := newFormationParty(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- f.pipeline.lobbyMatchMakeWithFallback(ctx, f.logger, f.leaderSession, f.leaderParams, f.lobbyGroup)
	}()
	time.Sleep(300 * time.Millisecond)
	require.Empty(t, f.liveTickets(), "a party group filed before its members were ready")
	require.Zero(t, f.logs.FilterMessage("Tablet party: submitting ticket without the formation wait").Len())
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, "a cancel during formation returns nil")
	case <-time.After(5 * time.Second):
		t.Fatal("the leader's loop did not exit on cancel during formation")
	}
}

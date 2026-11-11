package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// ===========================================================================
// Party joins must pre-check capacity (nevr-runtime #1413)
// ===========================================================================
//
// A party joining a social lobby can be sent to a lobby with fewer open seats
// than the party. The join gate (evr_match.go, MatchJoinAttempt) refuses it as
// lobby_full, burning the social find attempts.
//
// Each test drives one real call site with an undersized lobby. Two observables
// are used, and each is checked against the attempt record of THAT call site:
//
//   - attempt: a log record the call site writes on the way to the join. Its
//     presence shows the call site's own join was attempted, not a fall-through
//     from another path. Main has it; the fix removes it for an undersized lobby.
//   - skip: the Info "party join skipped: insufficient capacity" record the fix
//     adds, with the gate's own numbers. Main must not have it.
//
// LobbyJoinEntrants call counts are kept where the path reaches it directly (P1,
// P2, P5). P3, P4 and P6 go through lobbyAuthorize first, whose profile build runs
// a statistics query that this DB-free suite cannot satisfy, so the attempt record
// is the observable there: it is written before the join and after the authorize
// call, so it is written whether or not the join succeeds.
//
// Paths (evr_lobby_find.go, as of de76354a7):
//   P1 priority_leader   member joins the party leader's social lobby
//   P2 priority_follower leader joins a follower's social lobby
//   P3 reservation       follower joins the lobby holding its reservation
//   P4 follow            TryFollowPartyLeader joins the leader's lobby
//   P5 social_search     the generic social search loop
//   P6 vibinator         vibinator's gravity join (not drivable, see its test)

// partyCapFixture wires a tablet-party pipeline (real party registry and
// matchmaker) to the fake match registry, so the real find functions run.
type partyCapFixture struct {
	t         *testing.T
	logger    *zap.Logger
	logs      *observer.ObservedLogs
	env       *tabletEnv
	registry  *socialFindMockRegistry
	serverSID uuid.UUID
	groupID   uuid.UUID
}

func newPartyCapFixture(t *testing.T) *partyCapFixture {
	t.Helper()
	disablePing := false
	ServiceSettingsUpdate(&ServiceSettingsData{
		Matchmaking: GlobalMatchmakingSettings{RequirePreMatchPing: &disablePing},
	})
	t.Cleanup(func() { ServiceSettingsUpdate(nil) })

	logger, logs := followSkipObservedLogger()
	env := newTabletEnv(t)
	registry := &socialFindMockRegistry{
		mockFollowMatchRegistry: newMockFollowMatchRegistry(),
		labelJSON:               make(map[string]string),
	}
	env.ep.nk.matchRegistry = registry
	env.ep.nk.metrics = &testMetrics{}
	env.ep.nk.db = stubDB(t)
	env.ep.db = stubDB(t)

	// lobbyAuthorize (every join path) needs the lobby's guild in the registry.
	groupID := uuid.Must(uuid.NewV4())
	groups := map[string]*GuildGroup{groupID.String(): newTestGuildGroup(groupID, "")}
	guildRegistry := newTestGuildGroupRegistry()
	guildRegistry.guildGroups.Store(&groups)
	env.ep.guildGroupRegistry = guildRegistry

	serverSID := uuid.Must(uuid.NewV4())
	server := newPartyMemberSession(t, "gameserver", env.tracker, env.pr, env.ep)
	server.id = serverSID
	env.sessions.sessions[serverSID] = server

	return &partyCapFixture{
		t:         t,
		logger:    logger,
		logs:      logs,
		env:       env,
		registry:  registry,
		serverSID: serverSID,
		groupID:   groupID,
	}
}

// session is env.session with a profile whose account has a user and a "{}"
// wallet, so lobbyAuthorize's profile build gets past its JSON and name reads.
// It is local to this file: the shared newPartyMemberSession helper is untouched.
func (f *partyCapFixture) session(name string, runtimeClient bool) *sessionWS {
	f.t.Helper()
	s := f.env.session(name, runtimeClient)
	params, ok := LoadParams(s.Context())
	require.True(f.t, ok, "session parameters missing for %s", name)
	params.profile = &EVRProfile{account: &api.Account{User: &api.User{Username: name}, Wallet: "{}"}}
	return s
}

// socialLabel builds a social lobby as the registry reports it: Size and
// ReservationCount are the counters the gate reads, and Players is the roster.
func (f *partyCapFixture) socialLabel(size, reservationCount int, players ...PlayerInfo) *MatchLabel {
	gid := f.groupID
	return &MatchLabel{
		ID:               MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"},
		Open:             true,
		LobbyType:        PublicLobby,
		Mode:             evr.ModeSocialPublic,
		Level:            evr.LevelSocial,
		GroupID:          &gid,
		MaxSize:          SocialLobbyMaxSize,
		PlayerLimit:      SocialLobbyMaxSize,
		Size:             size,
		ReservationCount: reservationCount,
		Players:          players,
		GameServer:       &GameServerPresence{SessionID: f.serverSID},
	}
}

// register makes the labels visible to the find (ListMatches + GetState) and
// to MatchLabelByID (GetMatch).
func (f *partyCapFixture) register(labels ...*MatchLabel) {
	f.registry.mu.Lock()
	defer f.registry.mu.Unlock()
	for _, l := range labels {
		b, err := json.Marshal(l)
		require.NoError(f.t, err)
		f.registry.labelJSON[l.ID.String()] = string(b)
		f.registry.SetMatch(l.ID, l)
		f.registry.listMatches = append(f.registry.listMatches, &api.Match{MatchId: l.ID.String()})
	}
}

// registerSnapshotLive registers one lobby whose list snapshot (ListMatches +
// GetState, what the find's match list holds) differs from its live label
// (GetMatch, what MatchLabelByID returns).
func (f *partyCapFixture) registerSnapshotLive(snapshot, live *MatchLabel) {
	f.registry.mu.Lock()
	defer f.registry.mu.Unlock()
	require.Equal(f.t, snapshot.ID, live.ID)
	b, err := json.Marshal(snapshot)
	require.NoError(f.t, err)
	f.registry.labelJSON[snapshot.ID.String()] = string(b)
	f.registry.SetMatch(live.ID, live)
	f.registry.listMatches = append(f.registry.listMatches, &api.Match{MatchId: snapshot.ID.String()})
}

// trackService records the session's current match in the matchservice stream.
func (f *partyCapFixture) trackService(sess *sessionWS, mid MatchID) {
	f.env.tracker.Track(context.Background(), sess.id,
		PresenceStream{Mode: StreamModeService, Subject: sess.id, Label: StreamLabelMatchService},
		sess.userID, PresenceMeta{Status: mid.String()})
}

func (f *partyCapFixture) presence(sess *sessionWS) *EvrMatchPresence {
	return &EvrMatchPresence{SessionID: sess.id, UserID: sess.userID, Username: sess.Username()}
}

// joinsTo returns how many LobbyJoinEntrants attempts reached the registry for mid.
func (f *partyCapFixture) joinsTo(mid MatchID) int {
	f.registry.mu.Lock()
	defer f.registry.mu.Unlock()
	n := 0
	for _, c := range f.registry.joinCalls {
		if c.matchUUID == mid.UUID {
			n++
		}
	}
	return n
}

// requireNotReached fails if the undersized lobby received a LobbyJoinEntrants call.
func (f *partyCapFixture) requireNotReached(mid MatchID) {
	f.t.Helper()
	if n := f.joinsTo(mid); n != 0 {
		f.t.Errorf("LobbyJoinEntrants was reached %d time(s) for undersized lobby %s: "+
			"the join gate refuses it as lobby_full", n, mid.UUID)
	}
}

// findLog returns the context of the first record with msg at level whose context
// carries every field in fields (values compared with ==).
func (f *partyCapFixture) findLog(level zapcore.Level, msg string, fields map[string]any) (map[string]any, bool) {
	for _, e := range f.logs.FilterMessage(msg).All() {
		if e.Level != level {
			continue
		}
		ctx := e.ContextMap()
		match := true
		for k, v := range fields {
			if got, ok := ctx[k]; !ok || got != v {
				match = false
				break
			}
		}
		if match {
			return ctx, true
		}
	}
	return nil, false
}

// requireLog asserts that the call site's own attempt record exists.
func (f *partyCapFixture) requireLog(level zapcore.Level, msg string, fields map[string]any) {
	f.t.Helper()
	_, ok := f.findLog(level, msg, fields)
	require.Truef(f.t, ok, "no %s record %q with %v: the call site's join was not attempted", level, msg, fields)
}

// requireNoLog asserts that no record at level exists for msg with fields.
func (f *partyCapFixture) requireNoLog(level zapcore.Level, msg string, fields map[string]any) {
	f.t.Helper()
	_, ok := f.findLog(level, msg, fields)
	require.Falsef(f.t, ok, "unexpected %s record %q with %v", level, msg, fields)
}

// requireSkipLogged asserts the Info "party join skipped" record for path and mid,
// carrying the fields the capacity decision was made from.
func (f *partyCapFixture) requireSkipLogged(path string, mid MatchID) {
	f.t.Helper()
	ctx, ok := f.findLog(zapcore.InfoLevel, partyJoinSkippedMsg, map[string]any{"path": path, "mid": mid.String()})
	require.Truef(f.t, ok, "no %q record with path=%s mid=%s (found %d skip records)",
		partyJoinSkippedMsg, path, mid.String(), len(f.logs.FilterMessage(partyJoinSkippedMsg).All()))
	for _, k := range []string{"party_size", "open_slots", "reservation_count", "held_reservation", "seated_entrants", "needed_seats"} {
		require.Containsf(f.t, ctx, k, "skip record missing field %q", k)
	}
}

// tabletPartyPair makes a usable two-member tablet party and returns the lobby
// group the find would see (joinLobbyParty, as production calls it).
func (f *partyCapFixture) tabletPartyPair(leader, member *sessionWS, lobbyParams *LobbySessionParameters) *LobbyGroup {
	f.t.Helper()
	f.env.tabletParty(101, leader, member)
	group, _, err := f.env.ep.joinLobbyParty(f.logger, member, lobbyParams)
	require.NoError(f.t, err)
	require.NotNil(f.t, group)
	return group
}

// seatedIn is the roster entry for a party member who is already a connected
// presence in the lobby, as production has it on the priority paths: the member
// whose lobby the joiner is sent to is sitting in it.
func seatedIn(sess *sessionWS) PlayerInfo {
	return PlayerInfo{
		UserID: sess.userID.String(), SessionID: sess.id.String(),
		Team: TeamIndex(evr.TeamSocial),
	}
}

// TestPartyJoinCapacity_P1_MemberJoinsLeaderSocialLobby drives
// lobbyFindOrCreateSocial, member session, into the leader's lobby with the leader
// seated in it. The leader needs no seat (the gate drops a seated entrant), so the
// member needs one. Full lobby: no seat for the member, the join is skipped.
func TestPartyJoinCapacity_P1_MemberJoinsLeaderSocialLobby(t *testing.T) {
	f := newPartyCapFixture(t)
	leader := f.session("leader", true)
	member := f.session("member", true)
	lobbyParams := makeMatchmakeTestLobbyParams(member.userID, f.groupID, evr.ModeSocialPublic, 2)
	group := f.tabletPartyPair(leader, member, lobbyParams)

	undersized := f.socialLabel(SocialLobbyMaxSize, 0, seatedIn(leader)) // OpenSlots() == 0
	roomy := f.socialLabel(0, 0)
	f.register(undersized, roomy)
	f.trackService(leader, undersized.ID)

	_ = f.env.ep.lobbyFindOrCreateSocial(member.Context(), f.logger, member, lobbyParams, group, f.presence(member))

	f.requireLog(zapcore.InfoLevel, "Priority join: Found party leader's social lobby",
		map[string]any{"mid": undersized.ID.String()})
	f.requireNotReached(undersized.ID)
	f.requireSkipLogged("priority_leader", undersized.ID)
}

// TestPartyJoinCapacity_P1_SeatedLeaderMakesItFit: one open seat, party of two,
// leader seated. It looks undersized before the gate drops the seated leader and
// fits after, so the join must be attempted and not skipped.
func TestPartyJoinCapacity_P1_SeatedLeaderMakesItFit(t *testing.T) {
	f := newPartyCapFixture(t)
	leader := f.session("leader", true)
	member := f.session("member", true)
	lobbyParams := makeMatchmakeTestLobbyParams(member.userID, f.groupID, evr.ModeSocialPublic, 2)
	group := f.tabletPartyPair(leader, member, lobbyParams)

	oneOpen := f.socialLabel(SocialLobbyMaxSize-1, 0, seatedIn(leader)) // OpenSlots() == 1
	f.register(oneOpen)
	f.trackService(leader, oneOpen.ID)

	_ = f.env.ep.lobbyFindOrCreateSocial(member.Context(), f.logger, member, lobbyParams, group, f.presence(member))

	f.requireLog(zapcore.InfoLevel, "Priority join: Found party leader's social lobby",
		map[string]any{"mid": oneOpen.ID.String()})
	f.requireNoLog(zapcore.InfoLevel, partyJoinSkippedMsg, map[string]any{"path": "priority_leader"})
	require.Positivef(t, f.joinsTo(oneOpen.ID), "the join to %s was not attempted", oneOpen.ID.UUID)
}

// TestPartyJoinCapacity_P2_LeaderJoinsFollowerSocialLobby drives
// lobbyFindOrCreateSocial, leader session, into the follower's lobby with the
// follower seated in it (entrants: leader plus the follower's placeholder). Full
// lobby: no seat for the leader, the join is skipped.
func TestPartyJoinCapacity_P2_LeaderJoinsFollowerSocialLobby(t *testing.T) {
	f := newPartyCapFixture(t)
	leader := f.session("leader", true)
	member := f.session("member", true)
	lobbyParams := makeMatchmakeTestLobbyParams(leader.userID, f.groupID, evr.ModeSocialPublic, 2)
	group := f.tabletPartyPair(leader, member, lobbyParams)

	undersized := f.socialLabel(SocialLobbyMaxSize, 0, seatedIn(member)) // OpenSlots() == 0
	roomy := f.socialLabel(0, 0)
	f.register(undersized, roomy)
	f.trackService(member, undersized.ID)

	_ = f.env.ep.lobbyFindOrCreateSocial(leader.Context(), f.logger, leader, lobbyParams, group, f.presence(leader))

	f.requireLog(zapcore.InfoLevel, "Priority join: Leader joining follower's social lobby",
		map[string]any{"mid": undersized.ID.String()})
	f.requireNotReached(undersized.ID)
	f.requireSkipLogged("priority_follower", undersized.ID)
}

// TestPartyJoinCapacity_P2_SeatedFollowerMakesItFit: one open seat, leader plus
// the seated follower's placeholder. The gate drops the seated follower, so the
// leader fits: the join must be attempted and not skipped.
func TestPartyJoinCapacity_P2_SeatedFollowerMakesItFit(t *testing.T) {
	f := newPartyCapFixture(t)
	leader := f.session("leader", true)
	member := f.session("member", true)
	lobbyParams := makeMatchmakeTestLobbyParams(leader.userID, f.groupID, evr.ModeSocialPublic, 2)
	group := f.tabletPartyPair(leader, member, lobbyParams)

	oneOpen := f.socialLabel(SocialLobbyMaxSize-1, 0, seatedIn(member)) // OpenSlots() == 1
	f.register(oneOpen)
	f.trackService(member, oneOpen.ID)

	_ = f.env.ep.lobbyFindOrCreateSocial(leader.Context(), f.logger, leader, lobbyParams, group, f.presence(leader))

	f.requireLog(zapcore.InfoLevel, "Priority join: Leader joining follower's social lobby",
		map[string]any{"mid": oneOpen.ID.String()})
	f.requireNoLog(zapcore.InfoLevel, partyJoinSkippedMsg, map[string]any{"path": "priority_follower"})
	require.Positivef(t, f.joinsTo(oneOpen.ID), "the join to %s was not attempted", oneOpen.ID.UUID)
}

// TestPartyJoinCapacity_P1_StaleFullSnapshotLiveRoomyJoins: the list snapshot
// says the leader's lobby is full, the live label has a seat. The priority path
// must check the live label, so the join is attempted and not skipped.
func TestPartyJoinCapacity_P1_StaleFullSnapshotLiveRoomyJoins(t *testing.T) {
	f := newPartyCapFixture(t)
	leader := f.session("leader", true)
	member := f.session("member", true)
	lobbyParams := makeMatchmakeTestLobbyParams(member.userID, f.groupID, evr.ModeSocialPublic, 2)
	group := f.tabletPartyPair(leader, member, lobbyParams)

	snapshot := f.socialLabel(SocialLobbyMaxSize, 0, seatedIn(leader)) // OpenSlots() == 0
	live := *snapshot
	live.Size = SocialLobbyMaxSize - 1 // OpenSlots() == 1
	f.registerSnapshotLive(snapshot, &live)
	f.trackService(leader, snapshot.ID)

	_ = f.env.ep.lobbyFindOrCreateSocial(member.Context(), f.logger, member, lobbyParams, group, f.presence(member))

	f.requireLog(zapcore.InfoLevel, "Priority join: Found party leader's social lobby",
		map[string]any{"mid": snapshot.ID.String()})
	f.requireNoLog(zapcore.InfoLevel, partyJoinSkippedMsg, map[string]any{"path": "priority_leader"})
	require.Positivef(t, f.joinsTo(snapshot.ID), "the join to %s was not attempted", snapshot.ID.UUID)
}

// TestPartyJoinCapacity_P1_LiveFullSkips: the snapshot has a seat, the live label
// is full. The priority path checks the live label and skips.
func TestPartyJoinCapacity_P1_LiveFullSkips(t *testing.T) {
	f := newPartyCapFixture(t)
	leader := f.session("leader", true)
	member := f.session("member", true)
	lobbyParams := makeMatchmakeTestLobbyParams(member.userID, f.groupID, evr.ModeSocialPublic, 2)
	group := f.tabletPartyPair(leader, member, lobbyParams)

	snapshot := f.socialLabel(SocialLobbyMaxSize-1, 0, seatedIn(leader)) // OpenSlots() == 1
	live := *snapshot
	live.Size = SocialLobbyMaxSize // OpenSlots() == 0
	f.registerSnapshotLive(snapshot, &live)
	f.trackService(leader, snapshot.ID)

	_ = f.env.ep.lobbyFindOrCreateSocial(member.Context(), f.logger, member, lobbyParams, group, f.presence(member))

	f.requireSkipLogged("priority_leader", snapshot.ID)
}

// TestPartyJoinCapacity_P1_LiveLabelUnreadableJoinsWithoutPreCheck: the list
// snapshot is full and the live label cannot be read (GetMatch has no match for
// the id, so MatchLabelByID fails). joinPartyEntrantsLive must skip the pre-check,
// write the Warn, and attempt the join. The gate decides, so no skip is logged.
func TestPartyJoinCapacity_P1_LiveLabelUnreadableJoinsWithoutPreCheck(t *testing.T) {
	f := newPartyCapFixture(t)
	leader := f.session("leader", true)
	member := f.session("member", true)
	lobbyParams := makeMatchmakeTestLobbyParams(member.userID, f.groupID, evr.ModeSocialPublic, 2)
	group := f.tabletPartyPair(leader, member, lobbyParams)

	snapshot := f.socialLabel(SocialLobbyMaxSize, 0, seatedIn(leader)) // OpenSlots() == 0
	f.register(snapshot)
	// The snapshot stays in the list and GetState; only the match-by-id lookup
	// that MatchLabelByID makes is removed.
	f.registry.mockFollowMatchRegistry.mu.Lock()
	delete(f.registry.matches, snapshot.ID.String())
	f.registry.mockFollowMatchRegistry.mu.Unlock()
	f.trackService(leader, snapshot.ID)

	_ = f.env.ep.lobbyFindOrCreateSocial(member.Context(), f.logger, member, lobbyParams, group, f.presence(member))

	f.requireLog(zapcore.InfoLevel, "Priority join: Found party leader's social lobby",
		map[string]any{"mid": snapshot.ID.String()})
	f.requireLog(zapcore.WarnLevel, "Failed to refresh lobby label for capacity check; joining without pre-check",
		map[string]any{"path": "priority_leader", "mid": snapshot.ID.String()})
	// The mock rejects every join, so the search falls through to later call sites
	// (social_search) that may skip the full snapshot. Only this path's skip is checked.
	f.requireNoLog(zapcore.InfoLevel, partyJoinSkippedMsg, map[string]any{"path": "priority_leader", "mid": snapshot.ID.String()})
	require.Positivef(t, f.joinsTo(snapshot.ID), "the join to %s was not attempted", snapshot.ID.UUID)
}

// TestPartyJoinCapacity_P2_StaleFullSnapshotLiveRoomyJoins is the P2 counterpart:
// the leader joins the follower's lobby, snapshot full, live with a seat.
func TestPartyJoinCapacity_P2_StaleFullSnapshotLiveRoomyJoins(t *testing.T) {
	f := newPartyCapFixture(t)
	leader := f.session("leader", true)
	member := f.session("member", true)
	lobbyParams := makeMatchmakeTestLobbyParams(leader.userID, f.groupID, evr.ModeSocialPublic, 2)
	group := f.tabletPartyPair(leader, member, lobbyParams)

	snapshot := f.socialLabel(SocialLobbyMaxSize, 0, seatedIn(member)) // OpenSlots() == 0
	live := *snapshot
	live.Size = SocialLobbyMaxSize - 1 // OpenSlots() == 1
	f.registerSnapshotLive(snapshot, &live)
	f.trackService(member, snapshot.ID)

	_ = f.env.ep.lobbyFindOrCreateSocial(leader.Context(), f.logger, leader, lobbyParams, group, f.presence(leader))

	f.requireLog(zapcore.InfoLevel, "Priority join: Leader joining follower's social lobby",
		map[string]any{"mid": snapshot.ID.String()})
	f.requireNoLog(zapcore.InfoLevel, partyJoinSkippedMsg, map[string]any{"path": "priority_follower"})
	require.Positivef(t, f.joinsTo(snapshot.ID), "the join to %s was not attempted", snapshot.ID.UUID)
}

// TestPartyJoinCapacity_P2_LiveFullSkips: snapshot with a seat, live full: skip.
func TestPartyJoinCapacity_P2_LiveFullSkips(t *testing.T) {
	f := newPartyCapFixture(t)
	leader := f.session("leader", true)
	member := f.session("member", true)
	lobbyParams := makeMatchmakeTestLobbyParams(leader.userID, f.groupID, evr.ModeSocialPublic, 2)
	group := f.tabletPartyPair(leader, member, lobbyParams)

	snapshot := f.socialLabel(SocialLobbyMaxSize-1, 0, seatedIn(member)) // OpenSlots() == 1
	live := *snapshot
	live.Size = SocialLobbyMaxSize // OpenSlots() == 0
	f.registerSnapshotLive(snapshot, &live)
	f.trackService(member, snapshot.ID)

	_ = f.env.ep.lobbyFindOrCreateSocial(leader.Context(), f.logger, leader, lobbyParams, group, f.presence(leader))

	f.requireSkipLogged("priority_follower", snapshot.ID)
}

// TestPartyJoinCapacity_P3_ReservationPath drives the reservation path in
// lobbyFindOrCreateSocial. The lobby holds the joiner's own reservation and is
// overbooked: Size 12, ReservationCount 1, so OpenSlots() == -1 and the real gate
// also refuses (reservation_violated). On main the reservation path's own attempt
// is the Info "Found party reservation" record (reservation_mid) followed by the
// lobbyJoin failure Warn. The fix must leave neither record, and must log the skip.
// The LobbyJoinEntrants count is NOT asserted here: the generic priority joins
// that run after the reservation path also reach it, so it is not P3's own signal.
func TestPartyJoinCapacity_P3_ReservationPath(t *testing.T) {
	f := newPartyCapFixture(t)
	leader := f.session("leader", true)
	member := f.session("member", true)
	lobbyParams := makeMatchmakeTestLobbyParams(member.userID, f.groupID, evr.ModeSocialPublic, 2)
	group := f.tabletPartyPair(leader, member, lobbyParams)

	held := PlayerInfo{
		UserID: member.userID.String(), SessionID: member.id.String(),
		Team: TeamIndex(evr.TeamSocial), IsReservation: true,
	}
	overbooked := f.socialLabel(SocialLobbyMaxSize, 1, held)
	roomy := f.socialLabel(0, 0)
	f.register(overbooked, roomy)
	f.trackService(leader, overbooked.ID)

	_ = f.env.ep.lobbyFindOrCreateSocial(member.Context(), f.logger, member, lobbyParams, group, f.presence(member))

	f.requireNoLog(zapcore.InfoLevel, "Found party reservation in social lobby path, joining directly",
		map[string]any{"reservation_mid": overbooked.ID.String()})
	f.requireNoLog(zapcore.WarnLevel, "Failed to join via reservation in social path, falling through to legacy priority join", nil)
	f.requireSkipLogged("reservation", overbooked.ID)
}

// TestPartyJoinCapacity_P3_ReservedFollowerInFullLobbyStillJoins is the
// green-both-sides case: a follower holding a reservation in a lobby with 0 open
// seats fits (the gate consumes its own reservation first), so the reservation
// path must still attempt the join and must not skip.
func TestPartyJoinCapacity_P3_ReservedFollowerInFullLobbyStillJoins(t *testing.T) {
	f := newPartyCapFixture(t)
	leader := f.session("leader", true)
	member := f.session("member", true)
	lobbyParams := makeMatchmakeTestLobbyParams(member.userID, f.groupID, evr.ModeSocialPublic, 2)
	group := f.tabletPartyPair(leader, member, lobbyParams)

	held := PlayerInfo{
		UserID: member.userID.String(), SessionID: member.id.String(),
		Team: TeamIndex(evr.TeamSocial), IsReservation: true,
	}
	fullWithReservation := f.socialLabel(SocialLobbyMaxSize-1, 1, held) // OpenSlots() == 0
	f.register(fullWithReservation)
	f.trackService(leader, fullWithReservation.ID)

	_ = f.env.ep.lobbyFindOrCreateSocial(member.Context(), f.logger, member, lobbyParams, group, f.presence(member))

	f.requireLog(zapcore.InfoLevel, "Found party reservation in social lobby path, joining directly",
		map[string]any{"reservation_mid": fullWithReservation.ID.String()})
	f.requireNoLog(zapcore.InfoLevel, partyJoinSkippedMsg, map[string]any{"path": "reservation"})
}

// TestPartyJoinCapacity_P4_FollowLeaderLobby drives TryFollowPartyLeader; the
// leader's lobby is full (0 open) and the follower holds no reservation. On main
// the follow path's own attempt is the Debug "Failed to join leader's lobby"
// record, written in the lobbyJoin error branch. The fix must leave it out and
// log the skip.
func TestPartyJoinCapacity_P4_FollowLeaderLobby(t *testing.T) {
	f := newPartyCapFixture(t)
	leader := f.session("leader", true)
	member := f.session("member", true)
	lobbyParams := makeMatchmakeTestLobbyParams(member.userID, f.groupID, evr.ModeSocialPublic, 2)
	group := f.tabletPartyPair(leader, member, lobbyParams)

	full := f.socialLabel(SocialLobbyMaxSize, 0) // OpenSlots() == 0
	f.register(full)
	f.trackService(leader, full.ID)

	f.env.ep.TryFollowPartyLeader(member.Context(), f.logger, member, lobbyParams, group)

	f.requireNoLog(zapcore.DebugLevel, "Failed to join leader's lobby", nil)
	f.requireSkipLogged("follow", full.ID)
}

// TestPartyJoinCapacity_P4_ReservedFollowerInFullLeaderLobbyStillJoins is the
// green-both-sides case for the follow path: a reserved follower in a 0-open lobby
// fits, so the follow path must attempt the join and must not skip.
func TestPartyJoinCapacity_P4_ReservedFollowerInFullLeaderLobbyStillJoins(t *testing.T) {
	f := newPartyCapFixture(t)
	leader := f.session("leader", true)
	member := f.session("member", true)
	lobbyParams := makeMatchmakeTestLobbyParams(member.userID, f.groupID, evr.ModeSocialPublic, 2)
	group := f.tabletPartyPair(leader, member, lobbyParams)

	held := PlayerInfo{
		UserID: member.userID.String(), SessionID: member.id.String(),
		Team: TeamIndex(evr.TeamSocial), IsReservation: true,
	}
	fullWithReservation := f.socialLabel(SocialLobbyMaxSize-1, 1, held) // OpenSlots() == 0
	f.register(fullWithReservation)
	f.trackService(leader, fullWithReservation.ID)

	f.env.ep.TryFollowPartyLeader(member.Context(), f.logger, member, lobbyParams, group)

	f.requireLog(zapcore.DebugLevel, "Failed to join leader's lobby", nil)
	f.requireNoLog(zapcore.InfoLevel, partyJoinSkippedMsg, map[string]any{"path": "follow"})
}

// pollFollowLeader runs pollFollowPartyLeader once against the leader's lobby. The
// follower's own find is arena (a social find never reaches the poll's join) and
// the leader's lobby is social; the poll is short so the test is bounded by the
// ServerIsFull retry wait at most once.
func (f *partyCapFixture) pollFollowLeader(lobby *MatchLabel) {
	f.t.Helper()
	leader := f.session("leader", true)
	member := f.session("member", true)
	lobbyParams := makeMatchmakeTestLobbyParams(member.userID, f.groupID, evr.ModeSocialPublic, 2)
	group := f.tabletPartyPair(leader, member, lobbyParams)
	lobbyParams.Mode = evr.ModeArenaPublic

	f.register(lobby)
	f.trackService(leader, lobby.ID)
	f.env.ep.pollFollowInterval = time.Millisecond
	f.env.ep.pollFollowMaxDuration = 50 * time.Millisecond

	_ = f.env.ep.pollFollowPartyLeader(member.Context(), f.logger, member, lobbyParams, group)
}

// TestPartyJoinCapacity_P4_PollFullLeaderLobbySkips: the poll's own pre-check
// (OpenPlayerSlots, which reads the roster) admits a lobby whose counters leave no
// seat (Size 12, roster empty, the 10-28 shape), and the gate refuses it. The poll
// must skip it. The attempt marker is the Warn "Failed to join leader's lobby
// during poll": in this DB-free harness the join dies at the statistics query, and
// that failure stands in for the join having been attempted.
func TestPartyJoinCapacity_P4_PollFullLeaderLobbySkips(t *testing.T) {
	f := newPartyCapFixture(t)
	full := f.socialLabel(SocialLobbyMaxSize, 0) // OpenSlots() == 0, OpenPlayerSlots() == 12
	f.pollFollowLeader(full)

	f.requireNoLog(zapcore.WarnLevel, "Failed to join leader's lobby during poll", nil)
	f.requireSkipLogged("follow_poll", full.ID)
}

// TestPartyJoinCapacity_P4_PollLeaderLobbyWithSeatStillJoins: one open seat, the
// follower alone needs one, so the poll must attempt the join and not skip.
func TestPartyJoinCapacity_P4_PollLeaderLobbyWithSeatStillJoins(t *testing.T) {
	f := newPartyCapFixture(t)
	oneOpen := f.socialLabel(SocialLobbyMaxSize-1, 0) // OpenSlots() == 1
	f.pollFollowLeader(oneOpen)

	f.requireLog(zapcore.WarnLevel, "Failed to join leader's lobby during poll", nil)
	f.requireNoLog(zapcore.InfoLevel, partyJoinSkippedMsg, map[string]any{"path": "follow_poll"})
}

// TestPartyJoinCapacity_P5_SocialSearchLoop reproduces the 10-28 production shape:
// MaxSize 12, PlayerLimit 12, Size 10, ReservationCount 1, Players empty.
// OpenSlots() == 1 (what the gate sees) while OpenSlotsByRole(TeamSocial) == 12
// (what the :944 pre-check sees), so the pre-check admits a party of 2.
func TestPartyJoinCapacity_P5_SocialSearchLoop(t *testing.T) {
	f := newPartyCapFixture(t)
	leader := f.session("leader", false)
	member := f.session("member", false)
	lobbyParams := makeMatchmakeTestLobbyParams(member.userID, f.groupID, evr.ModeSocialPublic, 2)
	group := makeMatchmakeTestParty(leader.id, leader.userID, member.id, member.userID, nil)

	undersized := f.socialLabel(10, 1) // OpenSlots() == 12-10-1 == 1; Players empty
	require.Equal(t, 1, undersized.OpenSlots())
	n, err := undersized.OpenSlotsByRole(evr.TeamSocial)
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, 2, "fixture must pass the :944 role pre-check for a party of 2")

	roomy := f.socialLabel(0, 0)
	f.register(undersized, roomy)

	_ = f.env.ep.lobbyFindOrCreateSocial(member.Context(), f.logger, member, lobbyParams, group, f.presence(member))

	f.requireLog(zapcore.DebugLevel, "Joining social lobby.", map[string]any{"mid": undersized.ID.UUID.String()})
	f.requireNotReached(undersized.ID)
	f.requireSkipLogged("social_search", undersized.ID)
}

// TestPartyJoinCapacity_P6_VibinatorJoin drives lobbyFind's vibinator's-gravity join.
//
// NOT DRIVABLE in this harness (verified on main, see the report): lobbyFind calls
// lobbyAuthorize at evr_lobby_find.go:92, before the vibinator block at :329. That
// authorize call builds the user's profile, whose statistics query fails against
// the DB-free harness ("failed to query leaderboard records"), so lobbyFind returns
// at :92 and never reaches vibinatorsGravityCheck or the Warn "vibinatorsGravity:
// join failed, continuing normally". The body below is the intended assertion
// (attempt = that Warn, skip = the fix's Info record) and runs once the harness can
// satisfy the authorize call.
func TestPartyJoinCapacity_P6_VibinatorJoin(t *testing.T) {
	t.Skip("not drivable in the DB-free harness: lobbyAuthorize (evr_lobby_find.go:92) fails on the statistics query before the vibinator block at :329")

	f := newPartyCapFixture(t)
	ServiceSettingsUpdate(&ServiceSettingsData{EnableVibinatorsGravity: true})
	t.Cleanup(func() { ServiceSettingsUpdate(nil) })

	player := f.session("player", false) // PCVR: loginPayload set, build number not standalone
	lobbyParams := makeMatchmakeTestLobbyParams(player.userID, f.groupID, evr.ModeSocialPublic, 1)

	combat := f.socialLabel(0, 0)
	combat.Mode = evr.ModeCombatPublic
	combat.Level = evr.LevelUnspecified
	combat.MaxSize = 8
	combat.PlayerLimit = 8
	combat.TeamSize = 4
	combat.Size = 8 // OpenSlots() == 0
	f.register(combat)

	vibinator := uuid.FromStringOrNil(vibinatorsUserID)
	f.env.tracker.Track(context.Background(), uuid.Must(uuid.NewV4()),
		PresenceStream{Mode: StreamModeService, Subject: vibinator, Label: StreamLabelMatchService},
		vibinator, PresenceMeta{Status: combat.ID.String()})

	roomy := f.socialLabel(0, 0)
	f.register(roomy)

	_ = f.env.ep.lobbyFind(player.Context(), f.logger, player, lobbyParams)

	f.requireLog(zapcore.WarnLevel, "vibinatorsGravity: join failed, continuing normally", nil)
	f.requireSkipLogged("vibinator", combat.ID)
}

// reserveForJoiner gives entrants[0] a live reservation in the lobby, the way the
// leader's join leaves a follower's seat held. rebuildCache folds it into
// ReservationCount and Players, as it does in production.
func reserveForJoiner(state *MatchLabel, joiner *EvrMatchPresence) {
	state.reservationMap[joiner.GetSessionId()] = &slotReservation{
		Presence: joiner,
		Expiry:   time.Now().Add(time.Hour),
	}
	state.rebuildCache()
}

// TestCanSeatEntrants_MatchesJoinGate checks the pre-check's formula against the real
// join gate (MatchJoinAttempt) over the states a party join meets. The pre-check
// must say "fits" exactly when the gate does not refuse for capacity (lobby_full,
// or reservation_violated when the joiner's own reservation was consumed). A
// DuplicateJoin is not a capacity refusal: the caller re-sends success. Each row
// also pins the gate's own reason, so a row that misdescribes the gate fails.
func TestCanSeatEntrants_MatchesJoinGate(t *testing.T) {
	type follower struct {
		seated   bool // already a connected presence in the lobby
		reserved bool // holds a reservation in the lobby
		// reservedOtherSession: holds a reservation under a session id other than
		// its current one. The gate frees reservations by entrant session id only.
		reservedOtherSession bool
		// staleSeat: connected under another session id with the same user and
		// EVR-ID. The gate evicts it before the count (evr_match.go:430-445).
		staleSeat bool
	}
	tests := []struct {
		name            string
		maxSize         int
		existingPlayers int // strangers, connected
		joinerReserved  bool
		joinerSeated    bool
		// joinerStaleSeat: the joiner's user is connected under another session
		// with the same EVR-ID. The gate evicts it (evr_match.go:430-445).
		joinerStaleSeat bool
		// joinerReservedByUser: a reservation for the joiner's user under another
		// session id (the gate's by-user-id fallback, heldReservationFor).
		joinerReservedByUser bool
		// joinerReconnect: a crash-recovery reconnect reservation for the joiner.
		joinerReconnect bool
		// duplicateFollower: entrants[1:] lists its last follower twice.
		duplicateFollower bool
		followers         []follower // entrants[1:]
		wantReason        string     // the gate's refusal reason; "" = accepted
	}{
		{name: "room for the whole party", maxSize: 12, existingPlayers: 0, followers: []follower{{}}},
		{name: "one open seat, party of two", maxSize: 12, existingPlayers: 11, followers: []follower{{}},
			wantReason: ErrJoinRejectReasonLobbyFull.Error()},
		{name: "one open seat, solo", maxSize: 12, existingPlayers: 11},
		{name: "full lobby, solo, no reservation", maxSize: 12, existingPlayers: 12,
			wantReason: ErrJoinRejectReasonLobbyFull.Error()},
		{name: "joiner holds the seat in a full lobby, solo", maxSize: 12, existingPlayers: 11, joinerReserved: true},
		{name: "joiner holds a seat, party of two, one other seat free", maxSize: 12, existingPlayers: 10,
			joinerReserved: true, followers: []follower{{}}},
		{name: "joiner holds a seat, party of two, none free", maxSize: 12, existingPlayers: 11,
			joinerReserved: true, followers: []follower{{}}, wantReason: ErrJoinRejectReasonReservationViolated.Error()},
		{name: "overbooked by the joiner's own reservation", maxSize: 12, existingPlayers: 12,
			joinerReserved: true, followers: []follower{{}}, wantReason: ErrJoinRejectReasonReservationViolated.Error()},

		// entrants[1:] already seated: the gate drops them, they need no seat.
		{name: "leader joins follower's lobby, follower seated, one seat free", maxSize: 12, existingPlayers: 10,
			followers: []follower{{seated: true}}},
		{name: "leader joins follower's lobby, follower seated, full", maxSize: 12, existingPlayers: 11,
			followers: []follower{{seated: true}}, wantReason: ErrJoinRejectReasonLobbyFull.Error()},
		// entrants[1:] holding a reservation: the gate deletes it, freeing its seat.
		{name: "follower holds a reservation, one other seat", maxSize: 12, existingPlayers: 10,
			followers: []follower{{reserved: true}}},
		{name: "two followers hold reservations, one other seat", maxSize: 12, existingPlayers: 9,
			followers: []follower{{reserved: true}, {reserved: true}}},
		{name: "follower reserved, another not, one other seat", maxSize: 12, existingPlayers: 10,
			followers: []follower{{reserved: true}, {}}, wantReason: ErrJoinRejectReasonLobbyFull.Error()},
		// Both: the reservation is freed and the seated follower needs nothing.
		{name: "follower both reserved and seated, one seat free", maxSize: 12, existingPlayers: 9,
			followers: []follower{{seated: true, reserved: true}}},
		{name: "follower both reserved and seated, lobby full", maxSize: 12, existingPlayers: 10,
			followers: []follower{{seated: true, reserved: true}}},
		// entrants[0] already seated: DuplicateJoin, not a capacity refusal.
		{name: "joiner already seated in a full lobby", maxSize: 12, existingPlayers: 11, joinerSeated: true,
			followers: []follower{{}}, wantReason: ErrJoinRejectReasonDuplicateJoin.Error()},
		// entrants[0] reserved plus entrants[1:] seated.
		{name: "joiner reserved, follower seated, lobby full", maxSize: 12, existingPlayers: 10,
			joinerReserved: true, followers: []follower{{seated: true}}},
		{name: "joiner reserved, one follower seated, one not, lobby full", maxSize: 12, existingPlayers: 10,
			joinerReserved: true, followers: []follower{{seated: true}, {}},
			wantReason: ErrJoinRejectReasonReservationViolated.Error()},
		// Genuinely does not fit after trimming.
		{name: "party of three, one seated, one seat free", maxSize: 12, existingPlayers: 10,
			followers: []follower{{seated: true}, {}}, wantReason: ErrJoinRejectReasonLobbyFull.Error()},

		// F1: a stale same-user presence is evicted before the count.
		{name: "joiner's stale same-user presence in a full lobby", maxSize: 12, existingPlayers: 11,
			joinerStaleSeat: true},
		{name: "joiner's stale same-user presence, party of two, full", maxSize: 12, existingPlayers: 11,
			joinerStaleSeat: true, followers: []follower{{}}, wantReason: ErrJoinRejectReasonLobbyFull.Error()},
		// F1, follower side: a follower's stale same-user presence is evicted too.
		{name: "follower's stale same-user presence, party of two, one open seat", maxSize: 12, existingPlayers: 10,
			followers: []follower{{staleSeat: true}}},
		{name: "follower's stale same-user presence, party of two, still one seat short", maxSize: 12, existingPlayers: 11,
			followers: []follower{{staleSeat: true}}, wantReason: ErrJoinRejectReasonLobbyFull.Error()},
		// F2: slot reservation and reconnect reservation both free a seat.
		{name: "joiner holds slot and reconnect reservations, party of two, full", maxSize: 12, existingPlayers: 10,
			joinerReserved: true, joinerReconnect: true, followers: []follower{{}}},
		{name: "joiner holds a reconnect reservation, solo, full", maxSize: 12, existingPlayers: 11,
			joinerReconnect: true},
		// The gate's by-user-id fallback for entrants[0].
		{name: "joiner reserved by user id under another session, solo, full", maxSize: 12, existingPlayers: 11,
			joinerReservedByUser: true},
		// entrants[1:] reserved under another session: the gate does not free it.
		{name: "follower reserved under another session, one seat free", maxSize: 12, existingPlayers: 10,
			followers: []follower{{reservedOtherSession: true}}, wantReason: ErrJoinRejectReasonLobbyFull.Error()},
		// The same entrant listed twice needs a seat each time, at the gate.
		{name: "duplicate follower entrant, two seats free", maxSize: 12, existingPlayers: 10,
			followers: []follower{{}}, duplicateFollower: true, wantReason: ErrJoinRejectReasonLobbyFull.Error()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := atJoinSocialLabel(t, tt.maxSize, tt.maxSize, tt.existingPlayers)
			joiner := atJoinPresence(evr.TeamSocial, 1)
			entrants := []*EvrMatchPresence{joiner}
			for i, fw := range tt.followers {
				e := atJoinPresence(evr.TeamSocial, uint64(100+i))
				entrants = append(entrants, e)
				if fw.seated {
					state.presenceMap[e.GetSessionId()] = e
					state.presenceByEvrID[e.EvrID] = e
				}
				if fw.reserved {
					state.reservationMap[e.GetSessionId()] = &slotReservation{Presence: e, Expiry: time.Now().Add(time.Hour)}
				}
				if fw.reservedOtherSession {
					old := *e
					old.SessionID = uuid.Must(uuid.NewV4())
					state.reservationMap[old.GetSessionId()] = &slotReservation{Presence: &old, Expiry: time.Now().Add(time.Hour)}
				}
				if fw.staleSeat {
					old := *e
					old.SessionID = uuid.Must(uuid.NewV4())
					state.presenceMap[old.GetSessionId()] = &old
				}
			}
			if tt.duplicateFollower {
				entrants = append(entrants, entrants[len(entrants)-1])
			}
			if tt.joinerStaleSeat {
				stale := *joiner
				stale.SessionID = uuid.Must(uuid.NewV4())
				state.presenceMap[stale.GetSessionId()] = &stale
			}
			if tt.joinerReservedByUser {
				old := *joiner
				old.SessionID = uuid.Must(uuid.NewV4())
				state.reservationMap[old.GetSessionId()] = &slotReservation{Presence: &old, Expiry: time.Now().Add(time.Hour)}
			}
			if tt.joinerReconnect {
				old := *joiner
				old.SessionID = uuid.Must(uuid.NewV4())
				state.reconnectReservations[joiner.GetUserId()] = &reconnectReservation{
					Presence: &old, Expiry: time.Now().Add(time.Hour), UserID: joiner.GetUserId(),
				}
			}
			if tt.joinerSeated {
				state.presenceMap[joiner.GetSessionId()] = joiner
				state.presenceByEvrID[joiner.EvrID] = joiner
			}
			state.rebuildCache()
			if tt.joinerReserved {
				reserveForJoiner(state, joiner)
			}

			// The pre-check reads the lobby before the gate mutates it.
			helper := state.CanSeatEntrants(entrants...)

			m := &EvrMatch{}
			meta := EntrantMetadata{Presence: joiner, Reservations: entrants[1:]}
			// An accepted reconnect reads early-quit history through nk.
			var nk runtime.NakamaModule
			if tt.joinerReconnect {
				nk = &reconnectTestNakamaModule{}
			}
			_, accepted, reason := m.MatchJoinAttempt(
				context.Background(), reconnectTestLogger(), nil, nk, nil, 10,
				state, joiner, meta.ToMatchMetadata())

			// On accept the gate's third value is the presence, not a reason.
			require.Equalf(t, tt.wantReason == "", accepted,
				"the gate's own answer differs from the table (reason=%q): the row is wrong", reason)
			if !accepted {
				require.Equalf(t, tt.wantReason, reason, "the gate refused for a different reason: the row is wrong")
			}
			capacityRefused := !accepted && (reason == ErrJoinRejectReasonLobbyFull.Error() ||
				reason == ErrJoinRejectReasonReservationViolated.Error())
			require.Equalf(t, !capacityRefused, helper,
				"CanSeatEntrants disagrees with the join gate (gate accepted=%v, reason=%q)", accepted, reason)
		})
	}
}

// TestCanSeatEntrants_NilEntrants: a nil entrant must not panic. A nil entrants[0]
// answers fits (nothing to refuse); a nil in entrants[1:] is skipped.
func TestCanSeatEntrants_NilEntrants(t *testing.T) {
	state := atJoinSocialLabel(t, 12, 12, 11) // one open seat
	joiner := atJoinPresence(evr.TeamSocial, 1)

	require.NotPanics(t, func() {
		require.True(t, state.CanSeatEntrants(nil), "nil entrants[0] must answer fits")
	})
	require.NotPanics(t, func() {
		require.True(t, state.CanSeatEntrants(nil, joiner), "nil entrants[0] must answer fits")
	})
	require.NotPanics(t, func() {
		require.True(t, state.CanSeatEntrants(joiner, nil), "a nil follower needs no seat")
	})
	require.NotPanics(t, func() {
		require.NoError(t, checkPartyCapacity(zap.NewNop(), "test", state, nil))
	})
	require.NotPanics(t, func() {
		require.NoError(t, checkPartyCapacity(zap.NewNop(), "test", state, joiner, nil))
	})
}

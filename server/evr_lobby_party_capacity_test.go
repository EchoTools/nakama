package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
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

const partyJoinSkippedMsg = "party join skipped: insufficient capacity"

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
		f.t.Errorf("LobbyJoinEntrants was reached %d time(s) for undersized lobby %s "+
			"(open slots 1, party of 2): the join gate refuses it as lobby_full", n, mid.UUID)
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
	for _, k := range []string{"party_size", "open_slots", "reservation_count", "held_reservation"} {
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

// TestPartyJoinCapacity_P1_MemberJoinsLeaderSocialLobby drives
// lobbyFindOrCreateSocial, member session, leader's lobby undersized (1 open, party of 2).
func TestPartyJoinCapacity_P1_MemberJoinsLeaderSocialLobby(t *testing.T) {
	f := newPartyCapFixture(t)
	leader := f.session("leader", true)
	member := f.session("member", true)
	lobbyParams := makeMatchmakeTestLobbyParams(member.userID, f.groupID, evr.ModeSocialPublic, 2)
	group := f.tabletPartyPair(leader, member, lobbyParams)

	undersized := f.socialLabel(SocialLobbyMaxSize-1, 0)
	roomy := f.socialLabel(0, 0)
	f.register(undersized, roomy)
	f.trackService(leader, undersized.ID)

	_ = f.env.ep.lobbyFindOrCreateSocial(member.Context(), f.logger, member, lobbyParams, group, f.presence(member))

	f.requireLog(zapcore.InfoLevel, "Priority join: Found party leader's social lobby",
		map[string]any{"mid": undersized.ID.String()})
	f.requireNotReached(undersized.ID)
	f.requireSkipLogged("priority_leader", undersized.ID)
}

// TestPartyJoinCapacity_P2_LeaderJoinsFollowerSocialLobby drives
// lobbyFindOrCreateSocial, leader session, follower's lobby undersized.
func TestPartyJoinCapacity_P2_LeaderJoinsFollowerSocialLobby(t *testing.T) {
	f := newPartyCapFixture(t)
	leader := f.session("leader", true)
	member := f.session("member", true)
	lobbyParams := makeMatchmakeTestLobbyParams(leader.userID, f.groupID, evr.ModeSocialPublic, 2)
	group := f.tabletPartyPair(leader, member, lobbyParams)

	undersized := f.socialLabel(SocialLobbyMaxSize-1, 0)
	roomy := f.socialLabel(0, 0)
	f.register(undersized, roomy)
	f.trackService(member, undersized.ID)

	_ = f.env.ep.lobbyFindOrCreateSocial(leader.Context(), f.logger, leader, lobbyParams, group, f.presence(leader))

	f.requireLog(zapcore.InfoLevel, "Priority join: Leader joining follower's social lobby",
		map[string]any{"mid": undersized.ID.String()})
	f.requireNotReached(undersized.ID)
	f.requireSkipLogged("priority_follower", undersized.ID)
}

// TestPartyJoinCapacity_P3_ReservationPath drives the reservation path in
// lobbyFindOrCreateSocial. The lobby holds the joiner's own reservation and is
// overbooked: Size 12, ReservationCount 1, so OpenSlots() == -1 and the real gate
// also refuses (reservation_violated). The reservation path's own attempt is the
// "Found party reservation" Info record with reservation_mid, followed by the
// lobbyJoin failure Warn. The LobbyJoinEntrants count is NOT asserted here: the
// generic priority joins after the reservation path also reach it, so it would
// not be P3's own signal.
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

	f.requireLog(zapcore.InfoLevel, "Found party reservation in social lobby path, joining directly",
		map[string]any{"reservation_mid": overbooked.ID.String()})
	f.requireLog(zapcore.WarnLevel, "Failed to join via reservation in social path, falling through to legacy priority join", nil)
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
// leader's lobby is full (0 open) and the follower holds no reservation. The
// follow path's own attempt is the Debug "Failed to join leader's lobby" record,
// which is written only in the lobbyJoin error branch.
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

	f.requireLog(zapcore.DebugLevel, "Failed to join leader's lobby", nil)
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

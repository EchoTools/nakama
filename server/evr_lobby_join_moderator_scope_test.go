package server

import (
	"context"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
)

// SEC-5 — guild-scoped moderator claim.
//
// NewLobbyParametersFromRequest validates a client-claimed evr.TeamModerator
// entrant role against the group ID carried on the request. A
// LobbyJoinSessionRequest carries no group ID (evr.LobbyJoinSessionRequest.GetGroupID
// returns uuid.Nil), so the validation falls back to the user's *active* guild.
// lobbyJoin then overwrites lobbyParams.GroupID with the guild that actually
// owns the match and pushes lobbyParams.Role straight into the entrant
// presence — so an enforcer in guild A can join a guild-B lobby as
// TeamModerator.
//
// These tests drive the real p.lobbyJoin. They deliberately stop short of a
// successful join: the guild group registry is empty, so lobbyAuthorize errors
// out right after the re-validation point. What is asserted is the state
// lobbyJoin left on lobbyParams before that error — the group it re-scoped to,
// and the role it resolved for that group.

// lobbyJoinModeratorFixture builds the minimum pipeline/session/context needed
// to run p.lobbyJoin up to (and through) the guild re-scoping step.
type lobbyJoinModeratorFixture struct {
	pipeline    *EvrPipeline
	session     *sessionWS
	ctx         context.Context
	lobbyParams *LobbySessionParameters
	matchID     MatchID
}

func newLobbyJoinModeratorFixture(t *testing.T, lobbyGroupID uuid.UUID, userID uuid.UUID, sessionParams *SessionParameters, requestedRole int) *lobbyJoinModeratorFixture {
	t.Helper()

	registry := newMockFollowMatchRegistry()
	matchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	gid := lobbyGroupID
	registry.SetMatch(matchID, &MatchLabel{
		ID:      matchID,
		GroupID: &gid,
		Mode:    evr.ModeArenaPublic,
	})

	p := &EvrPipeline{
		nk: &RuntimeGoNakamaModule{
			matchRegistry:   registry,
			sessionRegistry: &testSessionRegistry{},
			metrics:         &testMetrics{},
		},
		guildGroupRegistry: newTestGuildGroupRegistry(),
	}

	session := &sessionWS{}
	session.id = uuid.Must(uuid.NewV4())
	session.userID = userID
	session.pipeline = &Pipeline{node: "testnode"}
	session.pipeline.tracker = newMockMatchmakingTracker()

	ctx := context.WithValue(context.Background(), ctxSessionParametersKey{}, atomic.NewPointer(sessionParams))

	return &lobbyJoinModeratorFixture{
		pipeline: p,
		session:  session,
		ctx:      ctx,
		lobbyParams: &LobbySessionParameters{
			UserID: userID,
			// The role and group as resolved from the *request*: the client
			// asked for moderator, and the request had no group so the
			// parameters carry the user's active guild.
			Role:    requestedRole,
			GroupID: uuid.Nil,
			Mode:    evr.ModeArenaPublic,
		},
		matchID: matchID,
	}
}

func TestLobbyJoin_ModeratorClaimIsScopedToTheLobbysGuild(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())
	guildA := uuid.Must(uuid.NewV4()) // user is an enforcer here
	guildB := uuid.Must(uuid.NewV4()) // user is an ordinary member here

	// The exploit setup: enforcer in A, plain member of B, active group A.
	sessionParams := &SessionParameters{
		guildGroups: map[string]*GuildGroup{
			guildA.String(): newTestGuildGroup(guildA, userID.String()),
			guildB.String(): newTestGuildGroup(guildB, uuid.Must(uuid.NewV4()).String()),
		},
	}

	f := newLobbyJoinModeratorFixture(t, guildB, userID, sessionParams, evr.TeamModerator)

	// The join cannot complete (empty guild group registry), but lobbyJoin must
	// have re-scoped and re-validated before it bailed.
	_ = f.pipeline.lobbyJoin(f.ctx, loggerForTest(t), f.session, f.lobbyParams, f.matchID)

	require.Equal(t, guildB, f.lobbyParams.GroupID,
		"lobbyJoin must re-scope the lobby parameters onto the guild that owns the match")
	require.Equal(t, evr.TeamUnassigned, f.lobbyParams.Role,
		"SEC-5: a TeamModerator claim must be re-validated against the lobby's own guild. "+
			"The user is an enforcer in guild A but only an ordinary member of guild B, "+
			"so joining a guild-B lobby must downgrade the role to TeamUnassigned.")
	require.False(t, f.lobbyParams.IsModerator,
		"SEC-5: IsModerator must reflect the lobby's guild, not the user's active guild")
}

func TestLobbyJoin_ModeratorClaimSurvivesForEnforcerOfTheLobbysGuild(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())
	guildB := uuid.Must(uuid.NewV4())

	sessionParams := &SessionParameters{
		guildGroups: map[string]*GuildGroup{
			guildB.String(): newTestGuildGroup(guildB, userID.String()),
		},
	}

	f := newLobbyJoinModeratorFixture(t, guildB, userID, sessionParams, evr.TeamModerator)

	_ = f.pipeline.lobbyJoin(f.ctx, loggerForTest(t), f.session, f.lobbyParams, f.matchID)

	require.Equal(t, evr.TeamModerator, f.lobbyParams.Role,
		"a genuine enforcer of the lobby's own guild must keep the moderator role")
	require.True(t, f.lobbyParams.IsModerator)
}

func TestLobbyJoin_ModeratorClaimSurvivesForGlobalOperator(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())
	guildB := uuid.Must(uuid.NewV4())

	// Global operator with no membership in the lobby's guild at all.
	sessionParams := &SessionParameters{
		isGlobalOperator: true,
		guildGroups:      map[string]*GuildGroup{},
	}

	f := newLobbyJoinModeratorFixture(t, guildB, userID, sessionParams, evr.TeamModerator)

	_ = f.pipeline.lobbyJoin(f.ctx, loggerForTest(t), f.session, f.lobbyParams, f.matchID)

	require.Equal(t, evr.TeamModerator, f.lobbyParams.Role,
		"global operators are moderators everywhere")
	require.True(t, f.lobbyParams.IsModerator)
}
func TestLobbyJoin_NonModeratorRolesAreNotTouched(t *testing.T) {
	// The downgrade must be narrow: only a TeamModerator claim is affected.
	for _, role := range []int{evr.TeamBlue, evr.TeamOrange, evr.TeamSpectator, evr.TeamSocial, evr.TeamUnassigned} {
		userID := uuid.Must(uuid.NewV4())
		guildB := uuid.Must(uuid.NewV4())

		sessionParams := &SessionParameters{
			guildGroups: map[string]*GuildGroup{
				guildB.String(): newTestGuildGroup(guildB, uuid.Must(uuid.NewV4()).String()),
			},
		}

		f := newLobbyJoinModeratorFixture(t, guildB, userID, sessionParams, role)
		_ = f.pipeline.lobbyJoin(f.ctx, loggerForTest(t), f.session, f.lobbyParams, f.matchID)

		require.Equal(t, role, f.lobbyParams.Role,
			"role %d must be left alone by the moderator re-validation", role)
	}
}

func TestLobbyJoin_ModeratorClaimFailsClosedWhenGuildIsUnknown(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())
	guildA := uuid.Must(uuid.NewV4())
	guildB := uuid.Must(uuid.NewV4())

	// The user is an enforcer in A, and the lobby's guild B is absent from the
	// session's guild group map entirely (non-member, or a registry race).
	sessionParams := &SessionParameters{
		guildGroups: map[string]*GuildGroup{
			guildA.String(): newTestGuildGroup(guildA, userID.String()),
		},
	}

	f := newLobbyJoinModeratorFixture(t, guildB, userID, sessionParams, evr.TeamModerator)
	_ = f.pipeline.lobbyJoin(f.ctx, loggerForTest(t), f.session, f.lobbyParams, f.matchID)

	require.Equal(t, evr.TeamUnassigned, f.lobbyParams.Role,
		"an unknown guild must fail closed, not inherit the active guild's privileges")
}

func TestLobbyJoin_ModeratorClaimFailsClosedWithoutSessionParameters(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())
	guildB := uuid.Must(uuid.NewV4())

	f := newLobbyJoinModeratorFixture(t, guildB, userID, &SessionParameters{}, evr.TeamModerator)
	// Drop the session parameters from the context entirely.
	f.ctx = context.Background()

	_ = f.pipeline.lobbyJoin(f.ctx, loggerForTest(t), f.session, f.lobbyParams, f.matchID)

	require.Equal(t, evr.TeamUnassigned, f.lobbyParams.Role,
		"missing session parameters must fail closed — no unverified moderator role may reach the presence")
}

// isModeratorOfGroup is the guild-scoped moderator predicate the join path
// depends on. These cases pin its boundaries directly.
func TestIsModeratorOfGroup(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())
	groupID := uuid.Must(uuid.NewV4())
	otherID := uuid.Must(uuid.NewV4())

	enforcerGroup := newTestGuildGroup(groupID, userID.String())
	strangerGroup := newTestGuildGroup(groupID, otherID.String())

	for _, tc := range []struct {
		name             string
		isGlobalOperator bool
		guildGroups      map[string]*GuildGroup
		groupID          uuid.UUID
		want             bool
	}{
		{"global operator, no groups", true, nil, groupID, true},
		{"enforcer of the group", false, map[string]*GuildGroup{groupID.String(): enforcerGroup}, groupID, true},
		{"member but not enforcer", false, map[string]*GuildGroup{groupID.String(): strangerGroup}, groupID, false},
		{"group absent from map", false, map[string]*GuildGroup{}, groupID, false},
		{"nil group entry", false, map[string]*GuildGroup{groupID.String(): nil}, groupID, false},
		{"nil map", false, nil, groupID, false},
		{"nil group id", false, map[string]*GuildGroup{groupID.String(): enforcerGroup}, uuid.Nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isModeratorOfGroup(tc.isGlobalOperator, tc.guildGroups, tc.groupID, userID.String()))
		})
	}
}

package server

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// ===========================================================================
// Follower social-lobby party-cohesion regression (production 2026-06-25)
// ===========================================================================
//
// Bug: a party of 2 (leader + follower) leaves an arena match for a social
// lobby. The follower cannot follow the leader via reservation (leader's
// dying-arena label already gone / leader still matchmaking), so it falls
// through to the INDEPENDENT social-search path at evr_lobby_find.go:~189:
//
//     followerEntrants, err := PrepareEntrantPresences(ctx, logger, p.nk,
//         p.nk.sessionRegistry, lobbyParams, session.id)   // self.id ONLY
//     return p.lobbyFindOrCreateSocial(ctx, logger, session, lobbyParams,
//         lobbyGroup, followerEntrants...)
//
// Because the entrant list is self-only, len(entrants) == 1. Inside
// lobbyFindOrCreateSocial the Priority-2 open-slots gate is:
//
//     if n, err := l.OpenSlotsByRole(team); err != nil { ... }
//         else if n < len(entrants) { continue }
//
// With a self-only list (len 1) a social lobby that has only ONE open slot
// passes the gate (1 < 1 == false), so the follower books it solo and creates
// NO reservation for the rest of the party. The second party member then tries
// to converge into that lobby and is rejected with "server is full" — the party
// fragments. The comment "(party reservations will converge)" at line ~189 is
// the false assumption: nothing reserved a slot for the second member.
//
// Party-cohesion contract: when a follower in a 2-person party performs the
// social find, the lobby it ends up booking MUST have room for the whole party
// (open slots >= party size), so the second member can still join. The fix
// makes this path party-aware (e.g. appendPartyReservationPlaceholders, mirroring
// the leader path at evr_lobby_find.go:271, using the lobbyGroup that
// lobbyFindOrCreateSocial already receives), so the open-slots gate is evaluated
// against the party size (2) and the 1-slot lobby is correctly skipped.
//
// This test drives the real production lobbyFindOrCreateSocial with the follower's
// current self-only entrant list and a real 2-member LobbyGroup, against a search
// result set whose first candidate has exactly ONE open slot and whose second has
// room for the whole party. It asserts the follower books a lobby with room for
// the party. It FAILS today (follower books the 1-slot lobby) and PASSES once the
// social-find path prepares party-aware entrants/reservations.

// socialFindJoinCall records a single MatchJoinAttempt the production code made.
type socialFindJoinCall struct {
	matchUUID uuid.UUID
}

// socialFindMockRegistry serves a controlled social-lobby search result set and
// records every JoinAttempt so the test can observe which lobby the follower
// actually booked. JoinAttempt rejects (found=false) so execution returns from
// LobbyJoinEntrants immediately after the attempt is recorded — before the
// tracker/network machinery the unit harness cannot satisfy.
type socialFindMockRegistry struct {
	*mockFollowMatchRegistry

	mu         sync.Mutex
	listMatches []*api.Match
	labelJSON  map[string]string // matchID string -> label JSON (for GetState)
	joinCalls  []socialFindJoinCall
}

func (r *socialFindMockRegistry) ListMatches(_ context.Context, _ int, _ *wrapperspb.BoolValue, _ *wrapperspb.StringValue, _ *wrapperspb.Int32Value, _ *wrapperspb.Int32Value, _ *wrapperspb.StringValue, _ *wrapperspb.StringValue) ([]*api.Match, []string, error) {
	return r.listMatches, nil, nil
}

func (r *socialFindMockRegistry) GetState(_ context.Context, id uuid.UUID, node string) ([]*rtapi.UserPresence, int64, string, error) {
	mid := MatchID{UUID: id, Node: node}
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.labelJSON[mid.String()]
	if !ok {
		return nil, 0, "", ErrMatchNotFound
	}
	return nil, 10, j, nil
}

func (r *socialFindMockRegistry) JoinAttempt(_ context.Context, id uuid.UUID, _ string, _, _ uuid.UUID, _ string, _ int64, _ map[string]string, _, _, _ string, _ map[string]string) (bool, bool, bool, string, string, []*MatchPresence) {
	r.mu.Lock()
	r.joinCalls = append(r.joinCalls, socialFindJoinCall{matchUUID: id})
	r.mu.Unlock()
	// Reject with "not found" so LobbyJoinEntrants returns right after recording,
	// without reaching the entrant-stream tracking / SendEVRMessages path.
	return false, false, false, "", "", nil
}

// sessionMapRegistry is a SessionRegistry whose Get resolves from a fixed map,
// so LobbyJoinEntrants can fetch the player and game-server sessions it requires.
type sessionMapRegistry struct {
	testSessionRegistry
	sessions map[uuid.UUID]Session
}

func (r *sessionMapRegistry) Get(sessionID uuid.UUID) Session {
	return r.sessions[sessionID]
}

func TestFollowerSocialFind_PartyOfTwo_DoesNotBookOneSlotLobbySolo(t *testing.T) {
	logger := loggerForTest(t)

	// Disable pre-match ping so prewarmEntrantPings / validatePreJoinPing no-op
	// (the unit harness has no ping infrastructure). Defaults to true otherwise.
	disablePing := false
	ServiceSettingsUpdate(&ServiceSettingsData{
		Matchmaking: GlobalMatchmakingSettings{RequirePreMatchPing: &disablePing},
	})
	defer ServiceSettingsUpdate(nil)

	leaderSID := uuid.Must(uuid.NewV4())
	leaderUID := uuid.Must(uuid.NewV4())
	followerSID := uuid.Must(uuid.NewV4())
	followerUID := uuid.Must(uuid.NewV4())
	serverSID := uuid.Must(uuid.NewV4())
	groupID := uuid.Must(uuid.NewV4())

	// Real 2-member party (leader + follower); the follower is the session
	// performing the find. This is the lobbyGroup the production fallthrough
	// passes into lobbyFindOrCreateSocial.
	lobbyGroup := makeMatchmakeTestParty(leaderSID, leaderUID, followerSID, followerUID, nil)
	require.Equal(t, 2, lobbyGroup.Size(), "party must have 2 members")
	partySize := lobbyGroup.Size()

	// Search results: candidate A has exactly ONE open slot; candidate B has
	// room for the whole party. Candidate A is first so the buggy gate books it.
	constrainedID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	roomyID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	mkSocialLabel := func(id MatchID, playerLimit int) *MatchLabel {
		gid := groupID
		return &MatchLabel{
			ID:          id,
			Open:        true,
			LobbyType:   PublicLobby,
			Mode:        evr.ModeSocialPublic,
			Level:       evr.LevelSocial,
			GroupID:     &gid,
			MaxSize:     SocialLobbyMaxSize,
			PlayerLimit: playerLimit, // social roleLimit == PlayerLimit; empty => open == PlayerLimit
			Players:     make([]PlayerInfo, 0),
			GameServer:  &GameServerPresence{SessionID: serverSID},
		}
	}
	constrainedLabel := mkSocialLabel(constrainedID, 1) // OpenSlotsByRole(TeamSocial) == 1
	roomyLabel := mkSocialLabel(roomyID, SocialLobbyMaxSize)

	// Sanity: confirm the open-slot fixtures are what the gate will see.
	if n, err := constrainedLabel.OpenSlotsByRole(evr.TeamSocial); err != nil || n != 1 {
		t.Fatalf("fixture error: constrained lobby open slots = %d (err=%v), want 1", n, err)
	}
	if n, err := roomyLabel.OpenSlotsByRole(evr.TeamSocial); err != nil || n < partySize {
		t.Fatalf("fixture error: roomy lobby open slots = %d (err=%v), want >= %d", n, err, partySize)
	}

	labelByUUID := map[uuid.UUID]*MatchLabel{
		constrainedID.UUID: constrainedLabel,
		roomyID.UUID:       roomyLabel,
	}

	mustJSON := func(l *MatchLabel) string {
		b, err := json.Marshal(l)
		require.NoError(t, err)
		return string(b)
	}
	registry := &socialFindMockRegistry{
		mockFollowMatchRegistry: newMockFollowMatchRegistry(),
		listMatches: []*api.Match{
			{MatchId: constrainedID.String()},
			{MatchId: roomyID.String()},
		},
		labelJSON: map[string]string{
			constrainedID.String(): mustJSON(constrainedLabel),
			roomyID.String():       mustJSON(roomyLabel),
		},
	}

	tracker := newMockMatchmakingTracker()

	// Player + game-server sessions that LobbyJoinEntrants resolves via the
	// session registry before issuing the join attempt.
	followerSession := &sessionWS{}
	followerSession.id = followerSID
	followerSession.userID = followerUID
	followerSession.ctx = context.Background()
	followerSession.pipeline = &Pipeline{node: "testnode", tracker: tracker}

	serverSession := &sessionWS{}
	serverSession.id = serverSID
	serverSession.ctx = context.Background()
	serverSession.pipeline = &Pipeline{node: "testnode", tracker: tracker}

	sessionRegistry := &sessionMapRegistry{
		sessions: map[uuid.UUID]Session{
			followerSID: followerSession,
			serverSID:   serverSession,
		},
	}

	pipeline := &EvrPipeline{
		node: "testnode",
		db:   stubDB(t), // blacklist reads fail open against an unreachable host
		nk: &RuntimeGoNakamaModule{
			logger:          logger,
			db:              stubDB(t),
			matchRegistry:   registry,
			sessionRegistry: sessionRegistry,
			tracker:         tracker,
			metrics:         &testMetrics{},
			node:            "testnode",
		},
	}

	lobbyParams := makeMatchmakeTestLobbyParams(followerUID, groupID, evr.ModeSocialPublic, int64(partySize))
	lobbyParams.PartyGroupName = "" // skip the reservation-follow / Priority-1 tracker paths

	// The follower's CURRENT (buggy) entrant list: self only — exactly what
	// PrepareEntrantPresences(..., session.id) produces at evr_lobby_find.go:~189.
	followerEntrants := []*EvrMatchPresence{
		{SessionID: followerSID, UserID: followerUID, Username: "follower"},
	}

	// Drive the real production social find.
	_ = pipeline.lobbyFindOrCreateSocial(context.Background(), logger, followerSession, lobbyParams, lobbyGroup, followerEntrants...)

	registry.mu.Lock()
	joinCalls := append([]socialFindJoinCall(nil), registry.joinCalls...)
	registry.mu.Unlock()

	require.Lenf(t, joinCalls, 1,
		"expected exactly one social-lobby join attempt, got %d", len(joinCalls))

	bookedLabel := labelByUUID[joinCalls[0].matchUUID]
	require.NotNil(t, bookedLabel, "join attempt targeted an unknown match")

	openSlots, err := bookedLabel.OpenSlotsByRole(evr.TeamSocial)
	require.NoError(t, err)

	if openSlots < partySize {
		t.Fatalf("PARTY FRAGMENTATION: follower (member of a %d-person party) booked "+
			"social lobby %s which has only %d open slot(s) — no room reserved for the "+
			"rest of the party. The second member's convergence join will be rejected with "+
			"ServerIsFull (evr_lobby_joinentrant.go:139). Root cause: the independent "+
			"social-search fallthrough (evr_lobby_find.go:~189) prepares self-only entrants, "+
			"so the Priority-2 open-slots gate (n < len(entrants), len==1) admits a 1-slot "+
			"lobby. The find must reserve room for all %d members.",
			partySize, joinCalls[0].matchUUID, openSlots, partySize)
	}
}

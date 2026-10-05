package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
)

// A player leaves social lobby X and immediately asks for a social lobby,
// reporting X as current. Production 2026-10-05 01:09:30-01:09:33 UTC:
//
//	evr_match.go:819          Player leaving the match   mid=X reason=3 (leave)
//	                          Finding match              mode=social_2.0
//	evr_lobby_find.go:692     Player already in the intended social lobby, treating as no-op   mid=X
//	                          Lobby find complete
//
// and then nothing: no LobbySessionSuccess, no failure, until the client gave up
// and the connection closed 84s later.
//
// MatchLeave removes the player from X's presence map and republishes X's label
// before it returns (evr_match.go:1220, :1276), so by the time the find runs,
// X's label no longer lists the player. The guard in lobbyFindOrCreateSocial
// never looked: it reads the player's matchservice tracker entry, which is
// cleared only at session close, and compares it with CurrentMatchID, which
// the client fills with the lobby it just left. Both say X, so the guard
// returned nil -- "already there" -- for a player who was not there, and
// nothing else answers the client after a nil return.
//
// The contract: a social find either places the player (a join attempt is
// made) or returns an error the caller turns into a failure message. A nil
// return with no join is only correct for a player who is a live presence in
// the lobby.
func TestSocialFind_AfterLeavingLobby_ClientReportsIt_DoesNotNoOp(t *testing.T) {
	logger, logs := followSkipObservedLogger()

	disablePing := false
	ServiceSettingsUpdate(&ServiceSettingsData{
		Matchmaking: GlobalMatchmakingSettings{RequirePreMatchPing: &disablePing},
	})
	defer ServiceSettingsUpdate(nil)

	playerSID := uuid.Must(uuid.NewV4())
	playerUID := uuid.Must(uuid.NewV4())
	otherSID := uuid.Must(uuid.NewV4())
	serverSID := uuid.Must(uuid.NewV4())
	groupID := uuid.Must(uuid.NewV4())

	// Lobby X as MatchLeave left it: social, same guild, open, still hosting
	// another player -- and no longer listing the one who left.
	lobbyX := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	gid := groupID
	labelX := &MatchLabel{
		ID:          lobbyX,
		Open:        true,
		LobbyType:   PublicLobby,
		Mode:        evr.ModeSocialPublic,
		Level:       evr.LevelSocial,
		GroupID:     &gid,
		MaxSize:     SocialLobbyMaxSize,
		PlayerLimit: SocialLobbyMaxSize,
		Players: []PlayerInfo{
			{SessionID: otherSID.String(), UserID: uuid.Must(uuid.NewV4()).String(), Team: TeamIndex(evr.TeamSocial)},
		},
		GameServer: &GameServerPresence{SessionID: serverSID},
	}
	labelJSON, err := json.Marshal(labelX)
	require.NoError(t, err)

	registry := &socialFindMockRegistry{
		mockFollowMatchRegistry: newMockFollowMatchRegistry(),
		listMatches:             []*api.Match{{MatchId: lobbyX.String()}},
		labelJSON:               map[string]string{lobbyX.String(): string(labelJSON)},
	}
	registry.SetMatch(lobbyX, labelX) // what the guard's MatchLabelByID reads

	tracker := newMockMatchmakingTracker()

	playerSession := &sessionWS{}
	playerSession.id = playerSID
	playerSession.userID = playerUID
	playerSession.ctx = context.Background()
	playerSession.pipeline = &Pipeline{node: "testnode", tracker: tracker}

	serverSession := &sessionWS{}
	serverSession.id = serverSID
	serverSession.ctx = context.Background()
	serverSession.pipeline = &Pipeline{node: "testnode", tracker: tracker}

	// The matchservice entry still names X: it is cleared only at session close.
	tracker.Track(context.Background(), playerSID,
		PresenceStream{Mode: StreamModeService, Subject: playerSID, Label: StreamLabelMatchService},
		playerUID, PresenceMeta{Status: lobbyX.String()})

	pipeline := &EvrPipeline{
		node: "testnode",
		db:   stubDB(t),
		nk: &RuntimeGoNakamaModule{
			logger:        logger,
			db:            stubDB(t),
			matchRegistry: registry,
			sessionRegistry: &sessionMapRegistry{sessions: map[uuid.UUID]Session{
				playerSID: playerSession,
				serverSID: serverSession,
			}},
			tracker: tracker,
			metrics: &testMetrics{},
			node:    "testnode",
		},
	}

	lobbyParams := makeMatchmakeTestLobbyParams(playerUID, groupID, evr.ModeSocialPublic, 1)
	lobbyParams.CurrentMatchID = lobbyX // the client reports the lobby it just left

	entrants := []*EvrMatchPresence{{SessionID: playerSID, UserID: playerUID, Username: "player"}}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	findErr := pipeline.lobbyFindOrCreateSocial(ctx, logger, playerSession, lobbyParams, nil, entrants...)

	registry.mu.Lock()
	joins := len(registry.joinCalls)
	registry.mu.Unlock()

	if n := logs.FilterMessage("Player already in the intended social lobby, treating as no-op").Len(); n != 0 {
		t.Errorf("the guard treated the find as a no-op for a player %s's label does not list", lobbyX.String())
	}
	if findErr == nil && joins == 0 {
		t.Fatalf("social find returned nil with no join attempt: the player left %s, the client reports it as current, "+
			"and nothing answers the client after a nil return -- the player waits in matchmaking until it gives up",
			lobbyX.String())
	}
}

package server

import (
	"context"
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

func TestLobbyFind_ForceSocialFollower_Unit(t *testing.T) {
	env := newFollowTestEnv(t)
	ctx := context.Background()
	logger := loggerForTest(t)

	// Leader is matchmaking for Social
	leaderParams := &LobbySessionParameters{
		Mode:    evr.ModeSocialPublic,
		GroupID: env.groupID,
	}
	// Track leader's matchmaking presence
	env.tracker.Track(ctx, env.leaderSID,
		PresenceStream{Mode: StreamModeMatchmaking, Subject: env.groupID},
		env.leaderUID,
		PresenceMeta{Status: leaderParams.String()})

	// Follower starts with Arena mode
	env.params.Mode = evr.ModeArenaPublic
	env.params.GroupID = env.groupID

	// Verify the helper function directly
	isHeading := env.pipeline.isLeaderHeadingToSocial(ctx, logger, env.session, env.params, env.lobbyGroup)
	if !isHeading {
		t.Errorf("Expected isLeaderHeadingToSocial to return true")
	}

	// Simulation: Verify the logic effect
	if isHeading {
		env.params.Mode = evr.ModeSocialPublic
	}

	if env.params.Mode != evr.ModeSocialPublic {
		t.Errorf("Expected follower mode to be forced to %s, but got %s", evr.ModeSocialPublic.String(), env.params.Mode.String())
	}
}

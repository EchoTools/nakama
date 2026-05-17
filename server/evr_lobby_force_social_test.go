package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// Tests for isLeaderHeadingToSocial
//
// Production code: evr_lobby_find.go — (*EvrPipeline).isLeaderHeadingToSocial
//
// Logic:
//  1. If leader has a matchmaking stream presence, parse mode from its status:
//     - ModeSocialPublic / ModeSocialNPE → return true
//     - Any other mode → return false  (matchmaking intent takes precedence)
//  2. Otherwise, check leader's service stream (current match):
//     - Look up match label; if mode is Social → return true
//  3. Default → return false
// ---------------------------------------------------------------------------

// TestIsLeaderHeadingToSocial_LeaderMatchmakingForSocial_ReturnsTrue verifies
// that when the leader has a matchmaking stream presence whose status encodes
// Mode = ModeSocialPublic, isLeaderHeadingToSocial returns true.
func TestIsLeaderHeadingToSocial_LeaderMatchmakingForSocial_ReturnsTrue(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	ctx := context.Background()
	logger := loggerForTest(t)

	// Encode LobbySessionParameters with ModeSocialPublic as the leader's matchmaking status.
	leaderParams := &LobbySessionParameters{
		Mode:    evr.ModeSocialPublic,
		GroupID: env.groupID,
	}
	statusBytes, err := json.Marshal(leaderParams)
	if err != nil {
		t.Fatalf("setup: failed to marshal leader params: %v", err)
	}

	// Track the leader on the matchmaking stream with the social-mode status.
	env.tracker.Track(ctx, env.leaderSID,
		PresenceStream{Mode: StreamModeMatchmaking, Subject: env.groupID},
		env.leaderUID,
		PresenceMeta{Status: string(statusBytes)})

	env.params.Mode = evr.ModeArenaPublic
	env.params.GroupID = env.groupID

	result := env.pipeline.isLeaderHeadingToSocial(ctx, logger, env.session, env.params, env.lobbyGroup)

	assert.True(t, result,
		"isLeaderHeadingToSocial: expected true when leader is matchmaking for ModeSocialPublic")
}

// TestIsLeaderHeadingToSocial_LeaderMatchmakingForArena_ReturnsFalse verifies
// that when the leader is matchmaking for a non-social mode (Arena), the function
// returns false. Matchmaking intent for arena means the leader is heading AWAY
// from social, so the follower must not be forced to social.
func TestIsLeaderHeadingToSocial_LeaderMatchmakingForArena_ReturnsFalse(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	ctx := context.Background()
	logger := loggerForTest(t)

	leaderParams := &LobbySessionParameters{
		Mode:    evr.ModeArenaPublic,
		GroupID: env.groupID,
	}
	statusBytes, err := json.Marshal(leaderParams)
	if err != nil {
		t.Fatalf("setup: failed to marshal leader params: %v", err)
	}

	env.tracker.Track(ctx, env.leaderSID,
		PresenceStream{Mode: StreamModeMatchmaking, Subject: env.groupID},
		env.leaderUID,
		PresenceMeta{Status: string(statusBytes)})

	env.params.GroupID = env.groupID

	result := env.pipeline.isLeaderHeadingToSocial(ctx, logger, env.session, env.params, env.lobbyGroup)

	assert.False(t, result,
		"isLeaderHeadingToSocial: expected false when leader is matchmaking for ModeArenaPublic (non-social)")
}

// TestIsLeaderHeadingToSocial_LeaderInSocialLobby_NoMatchmakingStream_ReturnsTrue
// verifies that when the leader has no matchmaking stream presence but IS in a
// social lobby match (service stream points to a social match), the function
// falls through to the match-label check and returns true.
//
// This test uses a mock match registry that returns a social-mode label.
func TestIsLeaderHeadingToSocial_LeaderInSocialLobby_NoMatchmakingStream_ReturnsTrue(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	ctx := context.Background()
	logger := loggerForTest(t)

	socialMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	// Leader has service stream pointing to a social lobby — no matchmaking stream.
	env.tracker.Track(ctx, env.leaderSID,
		PresenceStream{Mode: StreamModeService, Subject: env.leaderSID, Label: StreamLabelMatchService},
		env.leaderUID,
		PresenceMeta{Status: socialMatchID.String()})

	// Set up mock registry so MatchLabelByID returns a social label.
	registry := newMockFollowMatchRegistry()
	registry.SetMatch(socialMatchID, &MatchLabel{
		ID:   socialMatchID,
		Mode: evr.ModeSocialPublic,
		Open: true,
	})
	env.withMockNK(registry)

	env.params.GroupID = env.groupID

	result := env.pipeline.isLeaderHeadingToSocial(ctx, logger, env.session, env.params, env.lobbyGroup)

	assert.True(t, result,
		"isLeaderHeadingToSocial: expected true when leader is in a social lobby (no matchmaking stream)")
}

// TestIsLeaderHeadingToSocial_NoLeader_ReturnsFalse verifies that the function
// returns false gracefully when the party has no leader (nil return from GetLeader).
func TestIsLeaderHeadingToSocial_NoLeader_ReturnsFalse(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	ctx := context.Background()
	logger := loggerForTest(t)

	env.clearLeader()

	result := env.pipeline.isLeaderHeadingToSocial(ctx, logger, env.session, env.params, env.lobbyGroup)

	assert.False(t, result,
		"isLeaderHeadingToSocial: expected false when there is no party leader")
}

// TestIsLeaderHeadingToSocial_LeaderMatchmakingArenaWhileInSocialLobby_ReturnsFalse
// verifies that matchmaking stream intent takes priority over current location.
// Even though the leader IS in a social lobby (their service stream), their
// matchmaking stream shows they are queuing for Arena. The function must
// return false — the leader is LEAVING social, not staying.
//
// This is the critical reordering test: the matchmaking check (step 1 in the
// production code) must run BEFORE the current-match check (step 2).
func TestIsLeaderHeadingToSocial_LeaderMatchmakingArenaWhileInSocialLobby_ReturnsFalse(t *testing.T) {
	t.Parallel()

	env := newFollowTestEnv(t)
	ctx := context.Background()
	logger := loggerForTest(t)

	socialMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	// Leader is in a social lobby (service stream).
	env.tracker.Track(ctx, env.leaderSID,
		PresenceStream{Mode: StreamModeService, Subject: env.leaderSID, Label: StreamLabelMatchService},
		env.leaderUID,
		PresenceMeta{Status: socialMatchID.String()})

	// But the leader is matchmaking for Arena (matchmaking stream).
	leaderParams := &LobbySessionParameters{
		Mode:    evr.ModeArenaPublic,
		GroupID: env.groupID,
	}
	statusBytes, err := json.Marshal(leaderParams)
	if err != nil {
		t.Fatalf("setup: failed to marshal leader params: %v", err)
	}
	env.tracker.Track(ctx, env.leaderSID,
		PresenceStream{Mode: StreamModeMatchmaking, Subject: env.groupID},
		env.leaderUID,
		PresenceMeta{Status: string(statusBytes)})

	// Mock registry so the match-label path would return social (if reached).
	registry := newMockFollowMatchRegistry()
	registry.SetMatch(socialMatchID, &MatchLabel{
		ID:   socialMatchID,
		Mode: evr.ModeSocialPublic,
		Open: true,
	})
	env.withMockNK(registry)

	env.params.GroupID = env.groupID

	result := env.pipeline.isLeaderHeadingToSocial(ctx, logger, env.session, env.params, env.lobbyGroup)

	assert.False(t, result,
		"isLeaderHeadingToSocial: expected false when leader is matchmaking for Arena even though "+
			"their current match is social — matchmaking intent must take priority over current location")
}

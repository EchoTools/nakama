package server

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestErrPreJoinPingFailed_Sentinel verifies that the error returned by
// validatePreJoinPing (simulated here via the same wrapping pattern) satisfies
// errors.Is(err, ErrPreJoinPingFailed) so callers can skip the server rather
// than returning the failure to the player.
func TestErrPreJoinPingFailed_Sentinel(t *testing.T) {
	// Reproduce the exact wrapping done in validatePreJoinPing.
	inner := NewLobbyErrorf(InternalError, "pre-join ping validation failed: 1 of 1 party members could not reach server 157.211.9.85:6792")
	wrapped := fmt.Errorf("%w: %w", ErrPreJoinPingFailed, inner)

	t.Run("errors.Is detects sentinel", func(t *testing.T) {
		require.True(t, errors.Is(wrapped, ErrPreJoinPingFailed),
			"caller must be able to detect ping failure via errors.Is to skip the server")
	})

	t.Run("LobbyErrorCode still returns InternalError", func(t *testing.T) {
		assert.Equal(t, InternalError, LobbyErrorCode(wrapped),
			"LobbyErrorCode must unwrap to InternalError so the client error code is preserved on direct-join paths")
	})

	t.Run("unrelated error is not ErrPreJoinPingFailed", func(t *testing.T) {
		unrelated := NewLobbyErrorf(ServerIsFull, "server is full")
		require.False(t, errors.Is(unrelated, ErrPreJoinPingFailed))
	})
}

// TestValidatePreJoinPing_SocialModeSkipsRTT verifies that validatePreJoinPing
// returns nil for all social lobby modes without performing any RTT check.
// This is a regression test for the early-return added for social modes.
//
// Before the fix, social modes fell through to the GameServer nil check and then
// the session-registry walk. The RTT check itself would have been reached for any
// label with a valid GameServer endpoint, potentially rejecting social lobbies
// whose players had high latency.
//
// The test calls validatePreJoinPing with a nil *EvrPipeline, which is safe
// because the social mode early-return fires before p.nk is ever accessed.
func TestValidatePreJoinPing_SocialModeSkipsRTT(t *testing.T) {
	// Ensure RequiresPreMatchPing() returns true so the function does not
	// short-circuit before reaching the social-mode check.
	trueVal := true
	ServiceSettingsUpdate(&ServiceSettingsData{
		Matchmaking: GlobalMatchmakingSettings{
			RequirePreMatchPing: &trueVal,
		},
	})
	defer ServiceSettingsUpdate(nil)

	socialModes := []evr.Symbol{
		evr.ModeSocialPublic,
		evr.ModeSocialPrivate,
		evr.ModeSocialNPE,
	}

	for _, mode := range socialModes {
		t.Run(mode.String(), func(t *testing.T) {
			label := &MatchLabel{
				Mode: mode,
			}

			// A nil pipeline is intentional — the function must return before
			// accessing any pipeline fields for social modes.
			var p *EvrPipeline
			err := p.validatePreJoinPing(context.Background(), nil, label, nil)
			require.NoError(t, err, "social mode %s must skip RTT validation and return nil", mode)
		})
	}
}

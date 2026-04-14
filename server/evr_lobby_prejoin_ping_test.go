package server

import (
	"errors"
	"fmt"
	"testing"

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

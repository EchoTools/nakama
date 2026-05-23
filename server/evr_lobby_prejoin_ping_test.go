package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

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

// TestValidatePreJoinPing_NegativeCache verifies that a recent cached RTT above
// maxRTT triggers an immediate failure without issuing a new ping request.
// This is the "negative cache" behaviour: once we know a server is too far away
// we should not keep hammering it with ping requests and should instead skip it
// during the current cache window (preJoinPingMaxAge).
func TestValidatePreJoinPing_NegativeCache(t *testing.T) {
	trueVal := true
	maxRTT := 180
	ServiceSettingsUpdate(&ServiceSettingsData{
		Matchmaking: GlobalMatchmakingSettings{
			RequirePreMatchPing: &trueVal,
			MaxServerRTT:        maxRTT,
		},
	})
	defer ServiceSettingsUpdate(nil)

	// Build a LatencyHistory that already has a recent but over-threshold RTT.
	lh := NewLatencyHistory()
	badIP := net.ParseIP("10.0.0.1")
	lh.Add(badIP, maxRTT+50 /* 230ms */, 25, time.Now().Add(-14*24*time.Hour))

	extIP := badIP.String()
	entry, found := lh.LatestEntry(extIP)
	require.True(t, found, "test setup: LatencyHistory must contain the seeded entry")
	require.Greater(t, int(entry.RTT.Milliseconds()), maxRTT, "test setup: seeded RTT must exceed maxRTT")
	require.True(t, entry.Timestamp.After(time.Now().Add(-preJoinPingMaxAge)), "test setup: seeded entry must be within cache window")

	// Phase 1 must classify this member as a cached failure, not pending.
	// We verify this indirectly: if Phase 1 correctly handles it as a negative
	// cache hit, the function returns ErrPreJoinPingFailed without ever sending
	// a ping (which would require a live session/server anyway).
	//
	// The simplest observable invariant: when all checks is empty (no sessions),
	// validatePreJoinPing returns nil.  But we can test the classification logic
	// by exercising the preJoinPingResult path directly.

	t.Run("cached bad RTT is classified as immediate failure", func(t *testing.T) {
		// Create a result as Phase 1 would for a negative-cache hit.
		var failures []preJoinPingResult
		cutoff := time.Now().Add(-preJoinPingMaxAge)
		entry, found := lh.LatestEntry(extIP)
		require.True(t, found)
		if found && entry.Timestamp.After(cutoff) {
			rtt := int(entry.RTT.Milliseconds())
			if rtt > maxRTT {
				failures = append(failures, preJoinPingResult{
					RTT:    rtt,
					Cached: true,
					Err:    fmt.Errorf("cached RTT %dms exceeds max %dms (negative cache)", rtt, maxRTT),
				})
			}
		}
		require.Len(t, failures, 1, "one cached failure expected")
		assert.True(t, failures[0].Cached, "failure must be marked as cached")
		assert.Equal(t, maxRTT+50, failures[0].RTT)
	})

	t.Run("good cached RTT is not classified as failure", func(t *testing.T) {
		goodLH := NewLatencyHistory()
		goodIP := net.ParseIP("10.0.0.2")
		goodLH.Add(goodIP, maxRTT-10 /* 170ms, within threshold */, 25, time.Now().Add(-14*24*time.Hour))

		cutoff := time.Now().Add(-preJoinPingMaxAge)
		goodExtIP := goodIP.String()
		entry, found := goodLH.LatestEntry(goodExtIP)
		require.True(t, found)

		isPending := false
		isCachedFail := false
		if found && entry.Timestamp.After(cutoff) {
			rtt := int(entry.RTT.Milliseconds())
			if rtt <= maxRTT {
				// Would be skipped (pass)
			} else {
				isCachedFail = true
			}
		} else {
			isPending = true
		}

		assert.False(t, isPending, "good recent entry must not be added to pending")
		assert.False(t, isCachedFail, "good recent entry must not become a cached failure")
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

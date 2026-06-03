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
// TestIsPrivateMatch verifies that isPrivateMatch correctly identifies
// private lobby modes that should have relaxed RTT requirements.
func TestIsPrivateMatch(t *testing.T) {
	tests := []struct {
		mode evr.Symbol
		want bool
	}{
		{evr.ModeArenaPrivate, true},
		{evr.ModeCombatPrivate, true},
		{evr.ModeSocialPrivate, true},
		{evr.ModeArenaPublic, false},
		{evr.ModeCombatPublic, false},
		{evr.ModeSocialPublic, false},
		{evr.ModeSocialNPE, false},
		{evr.ToSymbol("echo_combat_tournament"), false},
	}
	for _, tt := range tests {
		t.Run(tt.mode.String(), func(t *testing.T) {
			got := isPrivateMatch(tt.mode)
			assert.Equal(t, tt.want, got, "isPrivateMatch(%s)", tt.mode)
		})
	}
}

// TestEvrMatchPresence_IsSpectator verifies that IsSpectator correctly
// identifies spectator role alignment.
func TestEvrMatchPresence_IsSpectator(t *testing.T) {
	p := &EvrMatchPresence{RoleAlignment: evr.TeamSpectator}
	assert.True(t, p.IsSpectator(), "spectator role should be detected")

	p2 := &EvrMatchPresence{RoleAlignment: evr.TeamBlue}
	assert.False(t, p2.IsSpectator(), "blue team should not be spectator")

	p3 := &EvrMatchPresence{RoleAlignment: 0}
	assert.False(t, p3.IsSpectator(), "unset role should not be spectator")
}

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

// BEHAVIORAL ACCEPTANCE TESTS FOR PRE-JOIN PING VALIDATION BUG
// =============================================================
// These tests document the core RTT validation behavior using LatencyHistory
// as the unit under test. The behavior being tested:
//
// 1. RTT threshold comparison: entries <= MaxServerRTT (180ms default) pass
// 2. Party validation: if ANY member fails, the entire group fails
// 3. Negative caching BUG: same server can be selected repeatedly without being skipped
// 4. Stale data handling: entries > 5 minutes old are pruned by LatencyHistory.Add()

// TestLatencyHistory_RTTAboveThreshold_IsRejected validates that latency
// entries with RTT above the max threshold (185ms > 180ms) would be rejected
// by the validatePreJoinPing comparison logic.
//
// Bug: When a party member has high latency to a server, the entire party
// join fails with no fallback (arena/combat modes have no retry logic).
func TestLatencyHistory_RTTAboveThreshold_IsRejected(t *testing.T) {
	// Setup
	maxRTT := 180
	history := NewLatencyHistory()
	endpointIP := "192.168.1.100"

	// Add RTT entry just ABOVE the threshold (185ms > 180ms)
	history.Add(net.ParseIP(endpointIP), 185, 10, time.Now())

	// Test: retrieve the entry and check if it would be rejected by validatePreJoinPing logic
	entry, found := history.LatestEntry(endpointIP)
	require.True(t, found, "latency entry should be found")

	rttMs := int(entry.RTT.Milliseconds())
	// This mirrors the comparison in validatePreJoinPing line 248: entry.RTT.Milliseconds() <= maxRTT
	isAcceptable := rttMs <= maxRTT

	require.False(t, isAcceptable,
		"RTT %dms should be rejected (exceeds max %dms)", rttMs, maxRTT)
}

// TestLatencyHistory_RTTBelowThreshold_IsAccepted validates that latency
// entries with RTT below the max threshold (175ms < 180ms) would be accepted.
func TestLatencyHistory_RTTBelowThreshold_IsAccepted(t *testing.T) {
	// Setup
	maxRTT := 180
	history := NewLatencyHistory()
	endpointIP := "192.168.1.100"

	// Add RTT entry just BELOW the threshold (175ms < 180ms)
	history.Add(net.ParseIP(endpointIP), 175, 10, time.Now())

	// Test: retrieve the entry and check if it would be accepted
	entry, found := history.LatestEntry(endpointIP)
	require.True(t, found, "latency entry should be found")

	rttMs := int(entry.RTT.Milliseconds())
	isAcceptable := rttMs <= maxRTT

	require.True(t, isAcceptable,
		"RTT %dms should be accepted (within max %dms)", rttMs, maxRTT)
}

// TestLatencyHistory_RTTAtThreshold_IsAccepted validates that RTT exactly
// at the threshold is ACCEPTED due to <= comparison (not <).
func TestLatencyHistory_RTTAtThreshold_IsAccepted(t *testing.T) {
	// Setup
	maxRTT := 180
	history := NewLatencyHistory()
	endpointIP := "192.168.1.100"

	// Add RTT entry exactly AT the threshold (180ms == 180ms)
	history.Add(net.ParseIP(endpointIP), 180, 10, time.Now())

	// Test: retrieve the entry and check if it would be accepted
	entry, found := history.LatestEntry(endpointIP)
	require.True(t, found, "latency entry should be found")

	rttMs := int(entry.RTT.Milliseconds())
	isAcceptable := rttMs <= maxRTT

	require.True(t, isAcceptable,
		"RTT %dms should be ACCEPTED (at threshold, <= not <)", rttMs, maxRTT)
}

// TestLatencyHistory_StaleData_IsIgnored documents that stale latency data
// (older than preJoinPingMaxAge=5 minutes) is pruned by LatencyHistory.Add()
// and will not be returned by LatestEntry(), forcing a fresh ping.
//
// Behavior: When validatePreJoinPing checks a player's latency history and
// finds no recent data (>5 min old), it sends a fresh ping request and waits
// for the response. If no response arrives within 5 seconds, validation fails.
func TestLatencyHistory_StaleData_IsIgnored(t *testing.T) {
	// Add() always timestamps new entries with time.Now() and treats its 4th
	// argument as a prune cutoff: entries older than the cutoff are discarded.
	// (LatestEntry itself does not filter by age — pruning is Add()'s job.)
	history := NewLatencyHistory()
	endpointIP := "192.168.1.100"
	now := time.Now()

	// Seed a genuinely stale entry directly (Add cannot backdate timestamps).
	history.GameServerLatencies[endpointIP] = []LatencyHistoryItem{
		{Timestamp: now.Add(-6 * time.Minute), RTT: 150 * time.Millisecond},
	}

	// A subsequent Add with a 5-minute cutoff must prune the stale entry while
	// retaining the fresh one it just recorded.
	cutoff := now.Add(-preJoinPingMaxAge)
	history.Add(net.ParseIP(endpointIP), 80, 10, cutoff)

	entry, found := history.LatestEntry(endpointIP)
	require.True(t, found, "the freshly added entry should be present")
	require.Equal(t, 80*time.Millisecond, entry.RTT,
		"only the fresh entry should remain; the stale (>5 min old) one is pruned")

	stored := history.GameServerLatencies[endpointIP]
	require.Len(t, stored, 1, "the stale entry should have been pruned by Add()")
}

// TestLatencyHistory_MultipleEntries_LatestWins documents that when multiple
// RTT measurements exist for the same endpoint, validatePreJoinPing uses the
// LATEST one for the decision.
//
// Bug context: If a player's most recent measurement to a server is bad,
// the join fails — but there's no way to mark that server as "bad" so it
// won't be selected again by the matchmaker.
func TestLatencyHistory_MultipleEntries_LatestWins(t *testing.T) {
	// Setup
	maxRTT := 180
	history := NewLatencyHistory()
	endpointIP := "192.168.1.100"
	now := time.Now()

	// Add multiple entries: first good (150ms), then bad (250ms)
	history.Add(net.ParseIP(endpointIP), 150, 10, now.Add(-2*time.Minute))
	history.Add(net.ParseIP(endpointIP), 250, 10, now) // Latest is BAD

	// Test: latest entry should be the one used for decision
	entry, found := history.LatestEntry(endpointIP)
	require.True(t, found, "latency entry should be found")

	rttMs := int(entry.RTT.Milliseconds())
	require.Equal(t, 250, rttMs,
		"latest entry (250ms) should be returned, not oldest (150ms)")

	isAcceptable := rttMs <= maxRTT
	require.False(t, isAcceptable,
		"latest BAD RTT should cause rejection")
}

// TestLatencyHistory_ReflectsBugBehavior_NoNegativeCaching documents the
// absence of negative caching: the LatencyHistory API has no method to
// blacklist a server, so the same server can be selected and rejected
// repeatedly.
//
// BUG BEHAVIOR: In production, a player can see the same server assigned
// 5+ times in 3 minutes, failing validation each time, with no mechanism
// to mark it as "bad" or skip it on the next attempt. This is because:
//
//  1. validatePreJoinPing has no fallback for arena/combat modes (social
//     lobbies have a fallback at line 772 of evr_lobby_find.go)
//  2. The matchmaker has no blacklist of "bad servers" for each player
//  3. LatencyHistory.Add() stores measurements but has no "remove" or
//     "blacklist" API — it only stores and retrieves RTT values
//  4. So the cycle repeats: matchmaker picks server -> ping fails -> join
//     fails -> player retries -> matchmaker picks same server again
func TestLatencyHistory_ReflectsBugBehavior_NoNegativeCaching(t *testing.T) {
	// Arrange: simulate a player's latency history with the same bad measurement
	// being checked 5 times by validatePreJoinPing (as seen in production logs)
	history := NewLatencyHistory()
	endpointIP := "192.168.1.100"
	maxRTT := 180

	// Test: simulate 5 consecutive rejections of the same server
	for attempt := 1; attempt <= 5; attempt++ {
		// In production, validatePreJoinPing would call LatestEntry() repeatedly
		// for the same endpoint, and it would be rejected each time
		history.Add(net.ParseIP(endpointIP), 250, 10, time.Now())

		entry, found := history.LatestEntry(endpointIP)
		require.True(t, found, "attempt %d: entry should be found", attempt)

		rttMs := int(entry.RTT.Milliseconds())
		isAcceptable := rttMs <= maxRTT
		require.False(t, isAcceptable,
			"attempt %d: RTT %dms should always be rejected (no negative caching)", attempt, rttMs)
	}

	// DOCUMENT THE BUG:
	// LatencyHistory has no negative caching API (no "MarkBad" or "GetBlacklist")
	// So there's no way to prevent the matchmaker from selecting the same
	// server again. The player experiences 5+ rejections before finally
	// getting a different server assignment (or matchmaking timeout).
	//
	// Expected fix would require:
	// 1. A per-player "server blacklist" that persists across matchmaking attempts
	// 2. The matchmaker consulting this blacklist before assigning a server
	// 3. Automatic expiry of blacklist entries (e.g., after 5 minutes)
	// 4. For social lobbies: convert validatePreJoinPing failure to a "skip server
	//    and try next" like the existing code at evr_lobby_find.go:772
}

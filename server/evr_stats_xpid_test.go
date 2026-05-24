package server

import (
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
)

// TestXPIDValidationLogic_BugDemonstration demonstrates the current bug:
// a game server operator with an unknown XPID (UNK-*) submitting stats for
// a player with OVR-ORG-* XPID is rejected by the validation check.
func TestXPIDValidationLogic_BugDemonstration(t *testing.T) {
	// Game server operator with unknown platform code
	operatorXPID := evr.EvrId{
		PlatformCode: 999, // Unknown platform code, will render as UNK-*
		AccountId:    12345,
	}

	// Player with OVR-ORG platform code
	playerXPID := evr.EvrId{
		PlatformCode: evr.OVR_ORG,
		AccountId:    67890,
	}

	// Create the stats message as if submitted by the game server operator
	statsMsg := &evr.RemoteLogPostMatchTypeStats{
		GenericRemoteLog: evr.GenericRemoteLog{
			XPID: playerXPID.String(), // Stats claim to be for the player
		},
		Stats: evr.MatchTypeStats{
			Goals:   2,
			Assists: 1,
		},
	}

	// Simulate the validation check from line 618 of evr_runtime_event_remotelogset.go
	// This is the bug: the submitter (operatorXPID) doesn't match the claimed player (playerXPID)
	submitterXPID := operatorXPID
	claimedXPID, err := evr.ParseEvrId(statsMsg.XPID)
	assert.NoError(t, err)

	// The current code rejects this because submitter != claimed player
	if submitterXPID != *claimedXPID {
		// This is where the bug occurs: the stats are silently dropped
		t.Logf("BUG DEMONSTRATED: Stats submission rejected because submitter %s != claimed player %s",
			submitterXPID.String(), claimedXPID.String())
		// In the actual code, this would log:
		// "Rejected stats submission for mismatched XPID"
		// and the stats would be lost.
		t.Log("Expected: Game server operator should be able to submit stats for players")
		t.Log("Actual: Stats are rejected as 'mismatched XPID'")
	}
}

// TestXPIDValidationLogic_DesiredBehavior shows the desired behavior:
// a game server operator should be allowed to submit stats for any player,
// because the game server is a trusted authority and operators are not players.
func TestXPIDValidationLogic_DesiredBehavior(t *testing.T) {
	// Game server operator with unknown platform code (UNK-*)
	operatorXPID := evr.EvrId{
		PlatformCode: 999, // Unknown platform code
		AccountId:    12345,
	}

	// Player 1 with OVR-ORG platform code
	player1XPID := evr.EvrId{
		PlatformCode: evr.OVR_ORG,
		AccountId:    67890,
	}

	// Player 2 with OVR platform code
	player2XPID := evr.EvrId{
		PlatformCode: evr.OVR,
		AccountId:    11111,
	}

	// Stats submission for Player 1
	player1StatsMsg := &evr.RemoteLogPostMatchTypeStats{
		GenericRemoteLog: evr.GenericRemoteLog{
			XPID: player1XPID.String(),
		},
		Stats: evr.MatchTypeStats{
			Goals:   2,
			Assists: 1,
		},
	}

	// Stats submission for Player 2
	player2StatsMsg := &evr.RemoteLogPostMatchTypeStats{
		GenericRemoteLog: evr.GenericRemoteLog{
			XPID: player2XPID.String(),
		},
		Stats: evr.MatchTypeStats{
			Goals:   1,
			Assists: 3,
		},
	}

	// Verify the test setup: operator XPID is unknown (UNK)
	assert.Equal(t, "UNK", operatorXPID.String()[:3],
		"Test setup: operator should have unknown platform code")

	// Verify the test setup: player XPIDs are known platforms
	assert.Equal(t, "OVR-ORG", player1XPID.String()[:7],
		"Test setup: player 1 should have OVR-ORG platform code")
	assert.Equal(t, "OVR", player2XPID.String()[:3],
		"Test setup: player 2 should have OVR platform code")

	// The desired behavior: these submissions should be ACCEPTED
	// because the operator is a game server (trusted authority), not a player.
	// The validation should allow game server operators to submit stats for any player.

	// For Player 1 stats
	claimedXPID1, err := evr.ParseEvrId(player1StatsMsg.XPID)
	assert.NoError(t, err)
	t.Logf("Player 1 stats claim: %s (submitted by operator %s)",
		claimedXPID1.String(), operatorXPID.String())
	t.Log("Expected: Stats submission ACCEPTED (operator submitting for player)")

	// For Player 2 stats
	claimedXPID2, err := evr.ParseEvrId(player2StatsMsg.XPID)
	assert.NoError(t, err)
	t.Logf("Player 2 stats claim: %s (submitted by operator %s)",
		claimedXPID2.String(), operatorXPID.String())
	t.Log("Expected: Stats submission ACCEPTED (operator submitting for player)")

	// Verify that players can still submit their own stats (self-reporting)
	selfReportingXPID := player1XPID
	selfReportingMsg := &evr.RemoteLogPostMatchTypeStats{
		GenericRemoteLog: evr.GenericRemoteLog{
			XPID: player1XPID.String(),
		},
		Stats: evr.MatchTypeStats{
			Goals: 2,
		},
	}

	claimedXPID3, err := evr.ParseEvrId(selfReportingMsg.XPID)
	assert.NoError(t, err)
	assert.Equal(t, selfReportingXPID, *claimedXPID3,
		"Self-reporting player stats should always be accepted")
	t.Log("Self-reporting: Player submitting their own stats should be ACCEPTED")
}

// TestIsGameServerOperator tests a helper function to identify game server operators.
// This could be used to implement the fix for the XPID mismatch bug.
//
// A game server operator is an entity that submits stats on behalf of players.
// They should be distinguishable from regular players to allow the validation logic to:
// 1. Accept self-reported stats from players (submitter XPID == claimed XPID)
// 2. Accept stats submitted by game server operators (submitter is a game server, not a player)
func TestIsGameServerOperator(t *testing.T) {
	testCases := []struct {
		name         string
		xpid         evr.EvrId
		isGameServer bool
		description  string
	}{
		{
			name: "unknown platform code",
			xpid: evr.EvrId{
				PlatformCode: 999, // Unknown platform
				AccountId:    12345,
			},
			isGameServer: true,
			description:  "Unknown platform codes indicate game server operators",
		},
		{
			name: "OVR-ORG platform",
			xpid: evr.EvrId{
				PlatformCode: evr.OVR_ORG,
				AccountId:    67890,
			},
			isGameServer: false,
			description:  "Regular player platform",
		},
		{
			name: "OVR platform",
			xpid: evr.EvrId{
				PlatformCode: evr.OVR,
				AccountId:    11111,
			},
			isGameServer: false,
			description:  "Regular player platform",
		},
		{
			name: "Steam platform",
			xpid: evr.EvrId{
				PlatformCode: evr.STM,
				AccountId:    22222,
			},
			isGameServer: false,
			description:  "Regular player platform",
		},
		{
			name: "Bot platform",
			xpid: evr.EvrId{
				PlatformCode: evr.BOT,
				AccountId:    33333,
			},
			isGameServer: false,
			description:  "Bot, not a game server operator",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// A game server operator has an unknown platform code
			// The presence of UNK prefix in the string representation indicates this
			isUnknown := tc.xpid.String()[:3] == "UNK"

			if tc.isGameServer {
				assert.True(t, isUnknown,
					"Game server operators should have unknown platform codes (%s): %s",
					tc.xpid.String(), tc.description)
			} else {
				assert.False(t, isUnknown,
					"Regular players should have known platform codes (%s): %s",
					tc.xpid.String(), tc.description)
			}
		})
	}
}

// TestXPIDValidationPredicate tests the logic for determining whether a stats
// submission should be accepted or rejected based on XPID validation.
//
// The fix should implement this logic:
// 1. If submitter XPID == claimed player XPID, ACCEPT (player self-reporting)
// 2. If submitter is a game server operator (UNK-*), ACCEPT (trusted authority)
// 3. Otherwise, REJECT (prevent stat injection)
func TestXPIDValidationPredicate(t *testing.T) {
	testCases := []struct {
		name              string
		submitterXPID     evr.EvrId
		claimedPlayerXPID evr.EvrId
		shouldAccept      bool
		reason            string
	}{
		{
			name: "player self-reporting",
			submitterXPID: evr.EvrId{
				PlatformCode: evr.OVR_ORG,
				AccountId:    12345,
			},
			claimedPlayerXPID: evr.EvrId{
				PlatformCode: evr.OVR_ORG,
				AccountId:    12345,
			},
			shouldAccept: true,
			reason:       "Player submitting their own stats (self-reporting)",
		},
		{
			name: "game server submitting for player",
			submitterXPID: evr.EvrId{
				PlatformCode: 999, // Unknown platform code (game server)
				AccountId:    99999,
			},
			claimedPlayerXPID: evr.EvrId{
				PlatformCode: evr.OVR_ORG,
				AccountId:    12345,
			},
			shouldAccept: true,
			reason:       "Game server operator submitting stats for a player",
		},
		{
			name: "game server submitting for multiple players",
			submitterXPID: evr.EvrId{
				PlatformCode: 999, // Unknown platform code (game server)
				AccountId:    99999,
			},
			claimedPlayerXPID: evr.EvrId{
				PlatformCode: evr.OVR,
				AccountId:    67890,
			},
			shouldAccept: true,
			reason:       "Game server operator submitting stats for a different player",
		},
		{
			name: "player trying to submit for another player",
			submitterXPID: evr.EvrId{
				PlatformCode: evr.OVR_ORG,
				AccountId:    12345,
			},
			claimedPlayerXPID: evr.EvrId{
				PlatformCode: evr.OVR_ORG,
				AccountId:    67890,
			},
			shouldAccept: false,
			reason:       "Regular player cannot submit stats for another player (stat injection attack)",
		},
		{
			name: "steam player submitting for OVR player",
			submitterXPID: evr.EvrId{
				PlatformCode: evr.STM,
				AccountId:    11111,
			},
			claimedPlayerXPID: evr.EvrId{
				PlatformCode: evr.OVR,
				AccountId:    22222,
			},
			shouldAccept: false,
			reason:       "Player from different platform cannot submit for another player",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Determine if submission should be accepted based on the fix logic:
			// 1. Self-reporting (submitter == claimed)
			isSelfReporting := tc.submitterXPID == tc.claimedPlayerXPID

			// 2. Game server operator (unknown platform code)
			isGameServer := tc.submitterXPID.String()[:3] == "UNK"

			// Decision: accept if either condition is true
			shouldAccept := isSelfReporting || isGameServer

			assert.Equal(t, tc.shouldAccept, shouldAccept,
				"Test case '%s': %s\nSubmitter: %s, Claimed: %s",
				tc.name, tc.reason, tc.submitterXPID.String(), tc.claimedPlayerXPID.String())

			if shouldAccept {
				t.Logf("✓ ACCEPT: %s", tc.reason)
			} else {
				t.Logf("✗ REJECT: %s", tc.reason)
			}
		})
	}
}

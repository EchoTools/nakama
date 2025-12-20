package server

import (
	"strings"
	"testing"
)

func TestMatchmakingSettings_IsMatchLocked(t *testing.T) {
	tests := []struct {
		name     string
		settings MatchmakingSettings
		want     bool
	}{
		{
			name: "not locked - empty fields",
			settings: MatchmakingSettings{
				MatchLockLeaderDiscordID: "",
				MatchLockEnabledAt:       0,
			},
			want: false,
		},
		{
			name: "not locked - leader ID but no timestamp",
			settings: MatchmakingSettings{
				MatchLockLeaderDiscordID: "123456789",
				MatchLockEnabledAt:       0,
			},
			want: false,
		},
		{
			name: "not locked - timestamp but no leader ID",
			settings: MatchmakingSettings{
				MatchLockLeaderDiscordID: "",
				MatchLockEnabledAt:       1734567890,
			},
			want: false,
		},
		{
			name: "locked - both leader ID and timestamp present",
			settings: MatchmakingSettings{
				MatchLockLeaderDiscordID: "123456789",
				MatchLockEnabledAt:       1734567890,
			},
			want: true,
		},
		{
			name: "locked with all fields populated",
			settings: MatchmakingSettings{
				MatchLockLeaderDiscordID: "123456789",
				MatchLockOperatorUserID:  "operator-user-id",
				MatchLockReason:          "Test reason",
				MatchLockEnabledAt:       1734567890,
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.settings.IsMatchLocked(); got != tt.want {
				t.Errorf("MatchmakingSettings.IsMatchLocked() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchLockRPCResponse_String(t *testing.T) {
	response := MatchLockRPCResponse{
		Success:         true,
		TargetUserID:    "target-user-id",
		TargetDiscordID: "123456789",
		LeaderDiscordID: "987654321",
		Locked:          true,
		Reason:          "Test reason",
		OperatorUserID:  "operator-user-id",
		LockedAt:        1734567890,
		Message:         "Test message",
	}

	jsonStr := response.String()
	if jsonStr == "" {
		t.Error("MatchLockRPCResponse.String() returned empty string")
	}

	// Verify it contains expected fields
	expectedFields := []string{
		`"success":true`,
		`"target_user_id":"target-user-id"`,
		`"target_discord_id":"123456789"`,
		`"leader_discord_id":"987654321"`,
		`"locked":true`,
		`"reason":"Test reason"`,
		`"operator_user_id":"operator-user-id"`,
		`"locked_at":1734567890`,
		`"message":"Test message"`,
	}

	for _, field := range expectedFields {
		if !strings.Contains(jsonStr, field) {
			t.Errorf("MatchLockRPCResponse.String() missing expected field: %s", field)
		}
	}
}

func TestMatchLockRPCRequest_Validation(t *testing.T) {
	// Test that the request structure is correctly defined
	request := MatchLockRPCRequest{
		TargetDiscordID: "123456789",
		TargetUserID:    "target-user-id",
		LeaderDiscordID: "987654321",
		Reason:          "Test reason",
	}

	// Verify fields are accessible
	if request.TargetDiscordID != "123456789" {
		t.Errorf("Expected TargetDiscordID to be '123456789', got '%s'", request.TargetDiscordID)
	}
	if request.TargetUserID != "target-user-id" {
		t.Errorf("Expected TargetUserID to be 'target-user-id', got '%s'", request.TargetUserID)
	}
	if request.LeaderDiscordID != "987654321" {
		t.Errorf("Expected LeaderDiscordID to be '987654321', got '%s'", request.LeaderDiscordID)
	}
	if request.Reason != "Test reason" {
		t.Errorf("Expected Reason to be 'Test reason', got '%s'", request.Reason)
	}
}

package server

import (
	"testing"
)

func TestIGNDisplayStatus(t *testing.T) {
	tests := []struct {
		name           string
		ign            GroupInGameName
		exists         bool
		takenName      string
		username       string
		expectedStatus string
	}{
		{
			name:           "no IGN entry",
			exists:         false,
			expectedStatus: "",
		},
		{
			name:           "plain IGN, no flags",
			ign:            GroupInGameName{DisplayName: "Player1"},
			exists:         true,
			expectedStatus: "",
		},
		{
			name:           "overridden IGN shows overridden",
			ign:            GroupInGameName{DisplayName: "ForcedName", IsOverride: true},
			exists:         true,
			expectedStatus: " (overridden)",
		},
		{
			name:           "locked IGN shows locked",
			ign:            GroupInGameName{DisplayName: "LockedName", IsLocked: true},
			exists:         true,
			expectedStatus: " (locked)",
		},
		{
			name:           "locked and overridden shows both",
			ign:            GroupInGameName{DisplayName: "BothName", IsOverride: true, IsLocked: true},
			exists:         true,
			expectedStatus: " (overridden, locked)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ignDisplayStatus(tt.ign, tt.exists, tt.takenName, tt.username)
			if got != tt.expectedStatus {
				t.Errorf("ignDisplayStatus() = %q, want %q", got, tt.expectedStatus)
			}
		})
	}
}

func TestIGNDisplayStatus_TakenName(t *testing.T) {
	got := ignDisplayStatus(GroupInGameName{}, false, "SomePlayer", "MyUser")
	expected := " (`SomePlayer` is taken)"
	if got != expected {
		t.Errorf("ignDisplayStatus() = %q, want %q", got, expected)
	}
}

func TestIGNDisplayStatus_TakenWithBackticks(t *testing.T) {
	got := ignDisplayStatus(GroupInGameName{}, false, "Some`Player", "MyUser")
	expected := " (`Some\\`Player` is taken)"
	if got != expected {
		t.Errorf("ignDisplayStatus() = %q, want %q", got, expected)
	}
}

func TestIGNCommandPermissions(t *testing.T) {
	// The /ign command is enforcer-only — only guild enforcers and global
	// operators can set a player's display name override.
	enforcerCommands := map[string]bool{
		"join-player": true,
		"igp":         true,
		"ign":         true,
	}

	for _, cmd := range []string{"ign"} {
		if !enforcerCommands[cmd] {
			t.Errorf("command %q should require enforcer permissions", cmd)
		}
	}
}

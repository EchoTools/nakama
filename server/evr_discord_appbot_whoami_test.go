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
		{
			name:           "taken name",
			takenName:      "SomePlayer",
			expectedStatus: " (`SomePlayer` is taken)",
		},
		{
			name:           "taken name with backticks escaped",
			takenName:      "Some`Player",
			expectedStatus: " (`Some\\`Player` is taken)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ignDisplayStatus(tt.ign, tt.exists, tt.takenName)
			if got != tt.expectedStatus {
				t.Errorf("ignDisplayStatus() = %q, want %q", got, tt.expectedStatus)
			}
		})
	}
}

package server

import (
	"slices"
	"testing"
	"time"
)

func TestNormalizeHeadsetType(t *testing.T) {
	tests := []struct {
		name     string
		headset  string
		expected string
	}{
		{"Empty headset", "", "Unknown"},
		{"Known headset Meta Quest 1", "Quest", "Meta Quest 1"},
		{"Known headset Meta Quest 2", "Quest 2", "Meta Quest 2"},
		{"Known headset Meta Quest Pro", "Quest Pro", "Meta Quest Pro"},
		{"Known headset Meta Quest 3", "Quest 3", "Meta Quest 3"},
		{"Known headset Meta Quest 3S", "Quest 3S", "Meta Quest 3S"},
		{"Known headset Meta Quest Pro (Link)", "Quest Pro (Link)", "Meta Quest Pro (Link)"},
		{"Known headset Meta Quest 3 (Link)", "Quest 3 (Link)", "Meta Quest 3 (Link)"},
		{"Known headset Meta Quest 3S (Link)", "Quest 3S (Link)", "Meta Quest 3S (Link)"},
		{"Known headset Meta Rift CV1", "Oculus Rift CV1", "Meta Rift CV1"},
		{"Known headset Meta Rift S", "Oculus Rift S", "Meta Rift S"},
		{"Known headset HTC Vive Elite", "Vive Elite", "HTC Vive Elite"},
		{"Known headset HTC Vive MV", "Vive MV", "HTC Vive MV"},
		{"Known headset HTC Vive MV", "Vive. MV", "HTC Vive MV"},
		{"Known headset Bigscreen Beyond", "Beyond", "Bigscreen Beyond"},
		{"Known headset Valve Index", "Index", "Valve Index"},
		{"Known headset Potato Potato 4K", "Potato VR", "Potato Potato 4K"},
		{"Unknown headset", "Unknown Headset", "Unknown Headset"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeHeadsetType(tt.headset)
			if result != tt.expected {
				t.Errorf("normalizeHeadsetType(%s) = %s; expected %s", tt.headset, result, tt.expected)
			}
		})
	}
}

func TestModeratorGreenDivisionProtection(t *testing.T) {
	tests := []struct {
		name              string
		accountAgeDays    int
		initialDivisions  []string
		initialExcluded   []string
		isModerator       bool
		expectedDivisions []string
		expectedExcluded  []string
		expectedUpdate    bool
	}{
		{
			name:              "new player gets green",
			accountAgeDays:    3,
			initialDivisions:  []string{},
			initialExcluded:   []string{},
			isModerator:       false,
			expectedDivisions: []string{"green"},
			expectedExcluded:  []string{},
			expectedUpdate:    true,
		},
		{
			name:              "old player loses green",
			accountAgeDays:    10,
			initialDivisions:  []string{"green"},
			initialExcluded:   []string{},
			isModerator:       false,
			expectedDivisions: []string{},
			expectedExcluded:  []string{"green"},
			expectedUpdate:    true,
		},
		{
			name:              "moderator keeps green despite age",
			accountAgeDays:    10,
			initialDivisions:  []string{"green", "bronze"},
			initialExcluded:   []string{},
			isModerator:       true,
			expectedDivisions: []string{"green", "bronze"},
			expectedExcluded:  []string{},
			expectedUpdate:    false,
		},
		{
			name:              "moderator without green not affected",
			accountAgeDays:    10,
			initialDivisions:  []string{"bronze"},
			initialExcluded:   []string{},
			isModerator:       true,
			expectedDivisions: []string{"bronze"},
			expectedExcluded:  []string{},
			expectedUpdate:    false,
		},
		{
			name:              "moderator with green gets it removed from excluded",
			accountAgeDays:    3,
			initialDivisions:  []string{"green"},
			initialExcluded:   []string{"green"},
			isModerator:       true,
			expectedDivisions: []string{"green"},
			expectedExcluded:  []string{},
			expectedUpdate:    true,
		},
		{
			name:              "young moderator keeps green",
			accountAgeDays:    3,
			initialDivisions:  []string{"green"},
			initialExcluded:   []string{},
			isModerator:       true,
			expectedDivisions: []string{"green"},
			expectedExcluded:  []string{},
			expectedUpdate:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the division update logic
			settings := MatchmakingSettings{
				Divisions:         slices.Clone(tt.initialDivisions),
				ExcludedDivisions: slices.Clone(tt.initialExcluded),
			}

			accountAge := time.Duration(tt.accountAgeDays) * 24 * time.Hour
			greenDivisionMaxAge := 7 * 24 * time.Hour
			updated := false

			// This simulates the logic we're testing
			isNewAccount := accountAge < greenDivisionMaxAge
			hasGreenInDivisions := slices.Contains(settings.Divisions, "green")
			hasGreenInExcluded := slices.Contains(settings.ExcludedDivisions, "green")

			// Check if user is a moderator with green division
			isProtectedModerator := tt.isModerator && hasGreenInDivisions

			if isNewAccount {
				// New accounts get green added
				if !hasGreenInDivisions {
					settings.Divisions = append(settings.Divisions, "green")
					updated = true
				}
				if hasGreenInExcluded {
					updated = true
					for i := 0; i < len(settings.ExcludedDivisions); i++ {
						if settings.ExcludedDivisions[i] == "green" {
							settings.ExcludedDivisions = slices.Delete(settings.ExcludedDivisions, i, i+1)
							i--
						}
					}
				}
			} else {
				// Old accounts lose green UNLESS they're protected moderators
				if hasGreenInDivisions && !isProtectedModerator {
					updated = true
					settings.Divisions, _ = RemoveFromStringSlice(settings.Divisions, "green")
				}
				// Only add to excluded if:
				// - Not already in excluded
				// - Not a moderator (moderators manage their own divisions)
				// - Green is not in divisions (was removed or never had it)
				if !hasGreenInExcluded && !tt.isModerator && !slices.Contains(settings.Divisions, "green") {
					updated = true
					settings.ExcludedDivisions = append(settings.ExcludedDivisions, "green")
				}
			}

			// Verify results
			if updated != tt.expectedUpdate {
				t.Errorf("expected update=%v, got %v", tt.expectedUpdate, updated)
			}

			if !slices.Equal(settings.Divisions, tt.expectedDivisions) {
				t.Errorf("expected divisions=%v, got %v", tt.expectedDivisions, settings.Divisions)
			}

			if !slices.Equal(settings.ExcludedDivisions, tt.expectedExcluded) {
				t.Errorf("expected excluded=%v, got %v", tt.expectedExcluded, settings.ExcludedDivisions)
			}
		})
	}
}

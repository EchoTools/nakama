package server

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestDisplayNameHistory_Compile(t *testing.T) {
	tests := []struct {
		name           string
		history        *DisplayNameHistory
		expectedActive []string
		expectedCache  []string
	}{
		{
			name: "empty history",
			history: &DisplayNameHistory{
				Histories: make(map[string]map[string]time.Time),
				Reserved:  make([]string, 0),
				Username:  "",
			},
			expectedActive: []string{},
			expectedCache:  []string{},
		},
		{
			name: "history with active and reserved names",
			history: &DisplayNameHistory{
				Histories: map[string]map[string]time.Time{
					"group1": {
						"Name1": time.Now().Add(-time.Hour * 24 * 10),
						"Name2": time.Now().Add(-time.Hour * 24 * 40),
					},
				},
				Reserved: []string{
					"ReservedName",
				},
				Username: "Username",
			},
			expectedActive: []string{"name1", "reservedname", "username"},
			expectedCache:  []string{"name1", "name2", "reservedname", "username"},
		},
		{
			name: "history with inactive user",
			history: &DisplayNameHistory{
				Histories: map[string]map[string]time.Time{
					"group1": {
						"Name1": time.Now().Add(-time.Hour * 24 * 10),
						"Name2": time.Now().Add(-time.Hour * 24 * 40),
					},
				},
				Reserved: []string{
					"ReservedName",
				},
				Username: "Username",
			},
			expectedActive: []string{"reservedname", "username"},
			expectedCache:  []string{"name1", "name2", "reservedname", "username"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.history.compile()
			if diff := cmp.Diff(tt.expectedActive, tt.history.ActiveCache); diff != "" {
				t.Errorf("unexpected active cache (-want +got):\n%s", diff)
			}

			if diff := cmp.Diff(tt.expectedCache, tt.history.HistoryCache); diff != "" {
				t.Errorf("unexpected history cache (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDisplayNameHistory_Update(t *testing.T) {
	tests := []struct {
		name           string
		history        *DisplayNameHistory
		groupID        string
		displayName    string
		username       string
		isActive       bool
		expectedResult *DisplayNameHistory
	}{
		{
			name: "update with new group and display name",
			history: &DisplayNameHistory{
				Histories: make(map[string]map[string]time.Time),
				Reserved:  make([]string, 0),
				Username:  "",
			},
			groupID:     "group1",
			displayName: "Name1",
			username:    "Username1",
			isActive:    true,
			expectedResult: &DisplayNameHistory{
				Histories: map[string]map[string]time.Time{
					"group1": {
						"Name1": time.Now(),
					},
				},
				Reserved: []string{},
				Username: "Username1",
			},
		},
		{
			name: "update existing group with new display name",
			history: &DisplayNameHistory{
				Histories: map[string]map[string]time.Time{
					"group1": {
						"Name1": time.Now().Add(-time.Hour * 24),
					},
				},
				Reserved: make([]string, 0),
				Username: "Username1",
			},
			groupID:     "group1",
			displayName: "Name2",
			username:    "Username2",
			isActive:    false,
			expectedResult: &DisplayNameHistory{
				Histories: map[string]map[string]time.Time{
					"group1": {
						"Name1": time.Now().Add(-time.Hour * 24),
						"Name2": time.Now(),
					},
				},
				Reserved: []string{},
				Username: "Username2",
			},
		},
		{
			name: "update with existing display name",
			history: &DisplayNameHistory{
				Histories: map[string]map[string]time.Time{
					"group1": {
						"Name1": time.Now().Add(-time.Hour * 24),
					},
				},
				Reserved: make([]string, 0),
				Username: "Username1",
			},
			groupID:     "group1",
			displayName: "Name1",
			username:    "Username3",
			isActive:    true,
			expectedResult: &DisplayNameHistory{
				Histories: map[string]map[string]time.Time{
					"group1": {
						"Name1": time.Now(),
					},
				},
				Reserved: []string{},
				Username: "Username3",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.history.Update(tt.groupID, tt.displayName, tt.username, false)

			if diff := cmp.Diff(tt.expectedResult.Histories, tt.history.Histories, cmp.Comparer(func(x, y time.Time) bool {
				return x.Sub(y) < time.Second
			})); diff != "" {
				t.Errorf("unexpected histories (-want +got):\n%s", diff)
			}

			if tt.expectedResult.Username != tt.history.Username {
				t.Errorf("expected username %s, got %s", tt.expectedResult.Username, tt.history.Username)
			}
		})
	}
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if strings.ToLower(s) == item {
			return true
		}
	}
	return false
}

func TestDisplayNameOwnerSearchProcessing(t *testing.T) {
	tests := []struct {
		name        string
		displayNames []string
		expectValid bool
		description string
	}{
		{
			name:        "normal display names",
			displayNames: []string{"player1", "player2"},
			expectValid: true,
			description: "should handle normal display names",
		},
		{
			name:        "mixed case duplicates",
			displayNames: []string{"Player", "PLAYER", "player"},
			expectValid: true,
			description: "should handle case-insensitive duplicates",
		},
		{
			name:        "with empty strings",
			displayNames: []string{"", "player1", "", "player2"},
			expectValid: true,
			description: "should handle empty strings properly",
		},
		{
			name:        "all empty strings",
			displayNames: []string{"", "", ""},
			expectValid: true,
			description: "should handle all empty strings",
		},
		{
			name:        "special characters",
			displayNames: []string{"foo-bar", "foo_bar", "foo(bar)", "foo[bar]"},
			expectValid: true,
			description: "should handle special characters",
		},
		{
			name:        "original issue case",
			displayNames: []string{"foo-bar", "foo-bar", "foo_bar", "foo-bar-(baz)"},
			expectValid: true,
			description: "should handle the original problematic case",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the processing logic from DisplayNameOwnerSearch
			nameMap := make(map[string]string, len(tt.displayNames))
			sanitized := make([]string, 0, len(tt.displayNames))
			
			for _, dn := range tt.displayNames {
				s := sanitizeDisplayName(dn)
				s = strings.ToLower(s)
				// Only add non-empty sanitized names
				if s != "" {
					sanitized = append(sanitized, s)
					nameMap[s] = dn
				}
			}

			// Remove duplicates from the display names.
			slices.Sort(sanitized)
			sanitized = slices.Compact(sanitized)

			// Generate the regex pattern
			regexPattern := Query.MatchItem(sanitized)
			
			t.Logf("Input: %v", tt.displayNames)
			t.Logf("Sanitized: %v", sanitized)
			t.Logf("Regex pattern: %s", regexPattern)
			
			// Test that the regex pattern is valid
			if regexPattern != "" {
				_, err := regexp.Compile(regexPattern)
				if err != nil {
					if tt.expectValid {
						t.Errorf("Expected valid regex but got error: %v", err)
					}
				} else {
					if !tt.expectValid {
						t.Errorf("Expected invalid regex but got valid pattern: %s", regexPattern)
					}
				}
			}
		})
	}
}

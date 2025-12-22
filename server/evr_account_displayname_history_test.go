package server

import (
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

package server

import (
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
				IsActive:  false,
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
				IsActive: true,
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
				IsActive: false,
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
				IsActive:  false,
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
				IsActive: true,
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
				IsActive: true,
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
				IsActive: false,
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
				IsActive: true,
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
				IsActive: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.history.Update(tt.groupID, tt.displayName, tt.username, tt.isActive)

			if diff := cmp.Diff(tt.expectedResult.Histories, tt.history.Histories, cmp.Comparer(func(x, y time.Time) bool {
				return x.Sub(y) < time.Second
			})); diff != "" {
				t.Errorf("unexpected histories (-want +got):\n%s", diff)
			}

			if tt.expectedResult.Username != tt.history.Username {
				t.Errorf("expected username %s, got %s", tt.expectedResult.Username, tt.history.Username)
			}

			if tt.expectedResult.IsActive != tt.history.IsActive {
				t.Errorf("expected isActive %v, got %v", tt.expectedResult.IsActive, tt.history.IsActive)
			}
		})
	}
}

func TestDisplayNameHistory_Latest(t *testing.T) {
	tests := []struct {
		name         string
		history      *DisplayNameHistory
		groupID      string
		expectedName string
		expectedTime time.Time
	}{
		{
			name: "empty history",
			history: &DisplayNameHistory{
				Histories: make(map[string]map[string]time.Time),
			},
			groupID:      "group1",
			expectedName: "",
			expectedTime: time.Time{},
		},
		{
			name: "single entry",
			history: &DisplayNameHistory{
				Histories: map[string]map[string]time.Time{
					"group1": {
						"Name1": time.Now().Add(-time.Hour * 24),
					},
				},
			},
			groupID:      "group1",
			expectedName: "Name1",
			expectedTime: time.Now().Add(-time.Hour * 24),
		},
		{
			name: "multiple entries",
			history: &DisplayNameHistory{
				Histories: map[string]map[string]time.Time{
					"group1": {
						"Name1": time.Now().Add(-time.Hour * 24),
						"Name2": time.Now().Add(-time.Hour * 12),
					},
				},
			},
			groupID:      "group1",
			expectedName: "Name2",
			expectedTime: time.Now().Add(-time.Hour * 12),
		},
		{
			name: "multiple groups",
			history: &DisplayNameHistory{
				Histories: map[string]map[string]time.Time{
					"group1": {
						"Name1": time.Now().Add(-time.Hour * 24),
					},
					"group2": {
						"Name2": time.Now().Add(-time.Hour * 12),
					},
				},
			},
			groupID:      "group2",
			expectedName: "Name2",
			expectedTime: time.Now().Add(-time.Hour * 12),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, updateTime := tt.history.Latest(tt.groupID)

			if name != tt.expectedName {
				t.Errorf("expected name %s, got %s", tt.expectedName, name)
			}

			if updateTime.Unix() != tt.expectedTime.Unix() {
				t.Errorf("expected time %v, got %v", tt.expectedTime, updateTime)
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

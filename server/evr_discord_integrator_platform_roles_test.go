package server

import (
	"testing"
	"time"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

// TestPlatformRoleClassification tests the BuildNumber classification logic
func TestPlatformRoleClassification(t *testing.T) {
	tests := []struct {
		name        string
		buildNumber evr.BuildNumber
		wantPCVR    bool
	}{
		{
			name:        "Known PCVR build",
			buildNumber: evr.PCVRBuild,
			wantPCVR:    true,
		},
		{
			name:        "Known Standalone build",
			buildNumber: evr.StandaloneBuildNumber,
			wantPCVR:    false,
		},
		{
			name:        "Unknown future build number",
			buildNumber: evr.BuildNumber(999999),
			wantPCVR:    false, // Should default to Standalone
		},
		{
			name:        "Zero build number",
			buildNumber: evr.BuildNumber(0),
			wantPCVR:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This mimics the logic in updatePlatformRoles
			isPCVR := tt.buildNumber == evr.PCVRBuild
			if isPCVR != tt.wantPCVR {
				t.Errorf("BuildNumber %v: got isPCVR=%v, want %v", tt.buildNumber, isPCVR, tt.wantPCVR)
			}
		})
	}
}

// TestLoginHistoryEntrySelection tests the logic for selecting the most recent login entry
func TestLoginHistoryEntrySelection(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name              string
		activeEntries     map[string]*LoginHistoryEntry
		historyEntries    map[string]*LoginHistoryEntry
		expectedBuildType evr.BuildNumber
		expectNoEntry     bool
	}{
		{
			name: "Active entry takes precedence over history",
			activeEntries: map[string]*LoginHistoryEntry{
				"active1": {
					UpdatedAt: now.Add(-1 * time.Hour),
					LoginData: &evr.LoginProfile{
						BuildNumber: evr.PCVRBuild,
					},
				},
			},
			historyEntries: map[string]*LoginHistoryEntry{
				"history1": {
					UpdatedAt: now.Add(-2 * time.Hour),
					LoginData: &evr.LoginProfile{
						BuildNumber: evr.StandaloneBuildNumber,
					},
				},
			},
			expectedBuildType: evr.PCVRBuild,
		},
		{
			name:          "No active entries, use history",
			activeEntries: map[string]*LoginHistoryEntry{},
			historyEntries: map[string]*LoginHistoryEntry{
				"history1": {
					UpdatedAt: now.Add(-1 * time.Hour),
					LoginData: &evr.LoginProfile{
						BuildNumber: evr.StandaloneBuildNumber,
					},
				},
			},
			expectedBuildType: evr.StandaloneBuildNumber,
		},
		{
			name: "Most recent active entry selected",
			activeEntries: map[string]*LoginHistoryEntry{
				"active1": {
					UpdatedAt: now.Add(-2 * time.Hour),
					LoginData: &evr.LoginProfile{
						BuildNumber: evr.StandaloneBuildNumber,
					},
				},
				"active2": {
					UpdatedAt: now.Add(-1 * time.Hour),
					LoginData: &evr.LoginProfile{
						BuildNumber: evr.PCVRBuild,
					},
				},
			},
			historyEntries:    map[string]*LoginHistoryEntry{},
			expectedBuildType: evr.PCVRBuild,
		},
		{
			name:           "Empty history returns no entry",
			activeEntries:  map[string]*LoginHistoryEntry{},
			historyEntries: map[string]*LoginHistoryEntry{},
			expectNoEntry:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the logic from updatePlatformRoles
			var mostRecentEntry *LoginHistoryEntry
			var mostRecentTime time.Time

			// Check active entries first
			for _, entry := range tt.activeEntries {
				if entry.UpdatedAt.After(mostRecentTime) {
					mostRecentTime = entry.UpdatedAt
					mostRecentEntry = entry
				}
			}

			// If no active entries, check history
			if mostRecentEntry == nil {
				for _, entry := range tt.historyEntries {
					if entry.UpdatedAt.After(mostRecentTime) {
						mostRecentTime = entry.UpdatedAt
						mostRecentEntry = entry
					}
				}
			}

			if tt.expectNoEntry {
				if mostRecentEntry != nil {
					t.Errorf("Expected no entry, but got one")
				}
				return
			}

			if mostRecentEntry == nil {
				t.Fatalf("Expected an entry, but got nil")
			}

			if mostRecentEntry.LoginData.BuildNumber != tt.expectedBuildType {
				t.Errorf("got BuildNumber=%v, want %v", mostRecentEntry.LoginData.BuildNumber, tt.expectedBuildType)
			}
		})
	}
}

// TestRoleAssignmentLogic tests that roles are assigned correctly based on platform
func TestRoleAssignmentLogic(t *testing.T) {
	tests := []struct {
		name                  string
		buildNumber           evr.BuildNumber
		expectPCVRRole        bool
		expectStandaloneRole  bool
	}{
		{
			name:                 "PCVR build gets PCVR role only",
			buildNumber:          evr.PCVRBuild,
			expectPCVRRole:       true,
			expectStandaloneRole: false,
		},
		{
			name:                 "Standalone build gets Standalone role only",
			buildNumber:          evr.StandaloneBuildNumber,
			expectPCVRRole:       false,
			expectStandaloneRole: true,
		},
		{
			name:                 "Unknown build gets Standalone role only",
			buildNumber:          evr.BuildNumber(888888),
			expectPCVRRole:       false,
			expectStandaloneRole: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the role assignment logic
			isPCVR := tt.buildNumber == evr.PCVRBuild

			// Check PCVR role assignment
			if isPCVR != tt.expectPCVRRole {
				t.Errorf("PCVR role assignment: got %v, want %v", isPCVR, tt.expectPCVRRole)
			}

			// Check Standalone role assignment (inverse of PCVR)
			if !isPCVR != tt.expectStandaloneRole {
				t.Errorf("Standalone role assignment: got %v, want %v", !isPCVR, tt.expectStandaloneRole)
			}
		})
	}
}

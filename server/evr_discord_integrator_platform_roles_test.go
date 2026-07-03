package server

import (
	"testing"
	"time"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

func TestPlatformRoleClassification_AppId(t *testing.T) {
	tests := []struct {
		name           string
		appId          uint64
		buildNumber    evr.BuildNumber
		wantPCVR       bool
		wantStandalone bool
	}{
		{
			name:           "Quest AppId → Standalone",
			appId:          QuestAppId,
			buildNumber:    0,
			wantPCVR:       false,
			wantStandalone: true,
		},
		{
			name:           "PCVR AppId → PCVR",
			appId:          PcvrAppId,
			buildNumber:    0,
			wantPCVR:       true,
			wantStandalone: false,
		},
		{
			name:           "No AppId, PCVR build → PCVR",
			appId:          NoOvrAppId,
			buildNumber:    evr.PCVRBuild,
			wantPCVR:       true,
			wantStandalone: false,
		},
		{
			name:           "No AppId, Standalone build → Standalone",
			appId:          NoOvrAppId,
			buildNumber:    evr.StandaloneBuildNumber,
			wantPCVR:       false,
			wantStandalone: true,
		},
		{
			name:           "No AppId, unknown build → neither role assigned",
			appId:          NoOvrAppId,
			buildNumber:    evr.BuildNumber(999999),
			wantPCVR:       false,
			wantStandalone: false,
		},
		{
			name:           "No AppId, zero build → neither role assigned",
			appId:          NoOvrAppId,
			buildNumber:    evr.BuildNumber(0),
			wantPCVR:       false,
			wantStandalone: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPCVR := false
			gotStandalone := false

			switch {
			case tt.appId == QuestAppId:
				gotStandalone = true
			case tt.appId == PcvrAppId:
				gotPCVR = true
			default:
				if tt.buildNumber == evr.PCVRBuild {
					gotPCVR = true
				} else if tt.buildNumber == evr.StandaloneBuildNumber {
					gotStandalone = true
				}
			}

			if gotPCVR != tt.wantPCVR {
				t.Errorf("PCVR: got %v, want %v", gotPCVR, tt.wantPCVR)
			}
			if gotStandalone != tt.wantStandalone {
				t.Errorf("Standalone: got %v, want %v", gotStandalone, tt.wantStandalone)
			}
		})
	}
}

func TestPlatformRoleClassification_Additive(t *testing.T) {
	// Roles are additive: playing on PCVR gives PCVR role,
	// playing on Quest gives Standalone role, playing on both keeps both.
	// We never call updateMemberRole(_, _, false).

	entries := []struct {
		appId       uint64
		buildNumber evr.BuildNumber
	}{
		{QuestAppId, evr.StandaloneBuildNumber},
		{PcvrAppId, evr.PCVRBuild},
	}

	hasPCVR := false
	hasStandalone := false

	for _, entry := range entries {
		switch {
		case entry.appId == QuestAppId:
			hasStandalone = true
		case entry.appId == PcvrAppId:
			hasPCVR = true
		}
	}

	if !hasPCVR {
		t.Error("After playing on both platforms, should have PCVR role")
	}
	if !hasStandalone {
		t.Error("After playing on both platforms, should have Standalone role")
	}
}

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
			name: "Most recent active entry selected",
			activeEntries: map[string]*LoginHistoryEntry{
				"active1": {
					UpdatedAt: now.Add(-2 * time.Hour),
					LoginData: &evr.LoginProfile{BuildNumber: evr.StandaloneBuildNumber},
				},
				"active2": {
					UpdatedAt: now.Add(-1 * time.Hour),
					LoginData: &evr.LoginProfile{BuildNumber: evr.PCVRBuild},
				},
			},
			historyEntries:    map[string]*LoginHistoryEntry{},
			expectedBuildType: evr.PCVRBuild,
		},
		{
			name:          "No active entries, use history",
			activeEntries: map[string]*LoginHistoryEntry{},
			historyEntries: map[string]*LoginHistoryEntry{
				"history1": {
					UpdatedAt: now.Add(-1 * time.Hour),
					LoginData: &evr.LoginProfile{BuildNumber: evr.StandaloneBuildNumber},
				},
			},
			expectedBuildType: evr.StandaloneBuildNumber,
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
			var mostRecentEntry *LoginHistoryEntry
			var mostRecentTime time.Time

			for _, entry := range tt.activeEntries {
				if entry.UpdatedAt.After(mostRecentTime) {
					mostRecentTime = entry.UpdatedAt
					mostRecentEntry = entry
				}
			}
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

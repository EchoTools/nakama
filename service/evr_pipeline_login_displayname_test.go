package service

import (
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
)

func TestDisplayNameCurrentValueFix(t *testing.T) {
	// This test verifies that display names use the current value instead of second-to-last

	groupID := "test-group-id"
	username := "testuser"
	oldDisplayName := "OldDisplayName"
	newDisplayName := "NewDisplayName"

	// Create a display name history with an old entry
	history := &DisplayNameHistory{
		Histories: map[string]map[string]time.Time{
			groupID: {
				oldDisplayName: time.Now().Add(-time.Hour), // Old entry
			},
		},
		Username: username,
	}

	// Create a profile
	profile := &EVRProfile{
		account:     &api.Account{User: &api.User{Username: username}},
		InGameNames: make(map[string]GroupInGameName),
	}

	// Simulate the scenario where a display name override is applied
	// This mimics the login flow in evr_pipeline_login.go

	// Step 1: Get the "default" display name from history (returns old value)
	defaultDisplayName, _ := history.LatestGroup(groupID)
	if defaultDisplayName != oldDisplayName {
		t.Errorf("Expected old display name %s, got %s", oldDisplayName, defaultDisplayName)
	}

	// Step 2: Apply a display name override (simulating user preference/override)
	defaultDisplayName = newDisplayName

	// Step 3: Set the group display name in the profile
	profile.SetGroupDisplayName(groupID, defaultDisplayName)

	// Step 4: get the current value from the profile
	activeGroupDisplayNameFixed := profile.GetGroupIGN(groupID)

	// Verify the fix: should use the new display name, not the old one
	if activeGroupDisplayNameFixed != newDisplayName {
		t.Errorf("Expected fixed display name %s, got %s", newDisplayName, activeGroupDisplayNameFixed)
	}

	// Step 5: Update the history with the current (correct) display name
	history.Update(groupID, activeGroupDisplayNameFixed, username, true)

	// Step 6: Verify that history now returns the correct (current) display name
	latestFromHistory, _ := history.LatestGroup(groupID)
	if latestFromHistory != newDisplayName {
		t.Errorf("Expected latest from history to be %s, got %s", newDisplayName, latestFromHistory)
	}

	// Additional verification: ensure the history contains both old and new entries
	// Check that both entries exist in the history
	if _, exists := history.Histories[groupID][oldDisplayName]; !exists {
		t.Error("Old display name should still exist in history")
	}
	if _, exists := history.Histories[groupID][newDisplayName]; !exists {
		t.Error("New display name should exist in history")
	}

	// Verify that the new name has a more recent timestamp
	oldTime := history.Histories[groupID][oldDisplayName]
	newTime := history.Histories[groupID][newDisplayName]
	if !newTime.After(oldTime) {
		t.Error("New display name should have a more recent timestamp than old display name")
	}

	// Most importantly: verify LatestGroup now returns the NEW name
	if latest, _ := history.LatestGroup(groupID); latest != newDisplayName {
		t.Errorf("LatestGroup should return the most recent display name %s, got %s", newDisplayName, latest)
	}
}

func TestDisplayNameHistoryConsistency(t *testing.T) {
	// Test that profile and history stay consistent after updates

	groupID := "test-group"
	username := "user123"
	displayName1 := "FirstName"
	displayName2 := "SecondName"
	displayName3 := "ThirdName"

	history := &DisplayNameHistory{
		Histories: make(map[string]map[string]time.Time),
		Username:  username,
	}

	profile := &EVRProfile{
		account:     &api.Account{User: &api.User{Username: username}},
		InGameNames: make(map[string]GroupInGameName),
	}

	// Update sequence: 1 -> 2 -> 3
	sequences := []string{displayName1, displayName2, displayName3}

	for i, name := range sequences {
		// Set in profile
		profile.SetGroupDisplayName(groupID, name)

		// Get current name from profile (the fixed approach)
		currentName := profile.GetGroupIGN(groupID)

		// Update history with current name
		history.Update(groupID, currentName, username, true)

		// Verify consistency
		latestFromHistory, _ := history.LatestGroup(groupID)
		if latestFromHistory != name {
			t.Errorf("Step %d: Expected latest from history to be %s, got %s", i+1, name, latestFromHistory)
		}

		if currentName != name {
			t.Errorf("Step %d: Expected current name from profile to be %s, got %s", i+1, name, currentName)
		}

		// Brief pause to ensure timestamps are different
		time.Sleep(time.Millisecond)
	}

	// Final verification: latest should be the third name
	latestFromHistory, _ := history.LatestGroup(groupID)
	currentFromProfile := profile.GetGroupIGN(groupID)

	if latestFromHistory != displayName3 {
		t.Errorf("Final: Expected latest from history to be %s, got %s", displayName3, latestFromHistory)
	}

	if currentFromProfile != displayName3 {
		t.Errorf("Final: Expected current from profile to be %s, got %s", displayName3, currentFromProfile)
	}

	// Verify all names are in history
	if len(history.Histories[groupID]) != 3 {
		t.Errorf("Expected 3 entries in history, got %d", len(history.Histories[groupID]))
	}
}

package server

import (
	"strings"
	"testing"
	"time"
)

func TestFormatDuration(t *testing.T) {
	testCases := []struct {
		duration     time.Duration
		expected     string
		roundMinutes bool
	}{
		{0 * time.Second, "0s", false},
		{1 * time.Second, "1s", false},
		{30 * time.Second, "30s", false},
		{60 * time.Second, "1m", false},
		{90 * time.Second, "1m30s", false},
		{60 * time.Minute, "1h", false},
		{90 * time.Minute, "1h30m", false},
		{24 * time.Hour, "1d", false},
		{36 * time.Hour, "1d12h", false},
		{24*time.Hour + 60*time.Minute, "1d1h", false},
		{24*time.Hour + 30*time.Minute, "1d1h", false},
		{24*time.Hour + 1*time.Second, "1d", false},
		{2*time.Hour + 30*time.Minute + 15*time.Second, "2h30m", false},
		{2*time.Hour + 15*time.Second, "2h", false},
		{1*time.Minute + 15*time.Second, "1m15s", false},
		{1*time.Hour + 15*time.Minute, "1h15m", false},
		{1*time.Hour + 2*time.Minute + 3*time.Second, "1h2m", true},
		{25*time.Hour + 2*time.Minute + 3*time.Second, "1d1h", true},
		{2*time.Hour + 59*time.Minute + 59*time.Second, "3h", true},
		{-1 * (2*time.Hour + 59*time.Minute + 59*time.Second), "-3h", true},
	}

	for _, tc := range testCases {
		t.Run(tc.duration.String(), func(t *testing.T) {
			actual := FormatDuration(tc.duration)
			if actual != tc.expected {
				t.Errorf("Expected %s, but got %s", tc.expected, actual)
			}
		})
	}
}

func TestCheckEnforcementSuspensions(t *testing.T) {
	groupA := "group-a"
	groupB := "group-b"
	userID1 := "user-1"
	userID2 := "user-2"

	// Create journal 1 with suspension in groupA
	journal1 := NewGuildEnforcementJournal(userID1)
	newRecord := journal1.AddRecord(
		groupA,
		"enforcer-1",
		"enforcer-discord-1",
		"Test suspension",
		"Test notes",
		false,     // requireCommunityValues
		true,      // allowPrivateLobbies
		time.Hour, // 1 hour suspension
	)
	// Override the record ID to match our test
	journal1.RecordsByGroupID[groupA][0].ID = newRecord.ID

	// Create journal 2 with void for the same record ID but in groupB
	journal2 := NewGuildEnforcementJournal(userID2)
	journal2.VoidRecord(groupB, newRecord.ID, "voider-1", "voider-discord-1", "Test void")

	journals := GuildEnforcementJournalList{
		userID1: journal1,
		userID2: journal2,
	}

	// map[parentGroupID]map[childGroupID]bool
	inheritanceMap := map[string][]string{
		groupA: {}, // groupB inherits from groupA
		groupB: {},
	}

	activeEnforcements, err := CheckEnforcementSuspensions(journals, inheritanceMap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The suspension in groupA should still be active because the void is in groupB
	if _, exists := activeEnforcements[groupA]; !exists {
		t.Errorf("expected active enforcement in groupA, but none found")
	}

	// Verify the suspension record is present and correct
	for _, modeRecords := range activeEnforcements[groupA] {
		if modeRecords.ID != newRecord.ID {
			t.Errorf("expected record ID %s, got %s", newRecord.ID, modeRecords.ID)
		}
		if modeRecords.UserID != userID1 {
			t.Errorf("expected user ID %s, got %s", userID1, modeRecords.UserID)
		}
		if modeRecords.GroupID != groupA {
			t.Errorf("expected group ID %s, got %s", groupA, modeRecords.GroupID)
		}
		break // Only need to check one record since they should all be the same
	}

	// GroupB should not have any active enforcements
	if _, exists := activeEnforcements[groupB]; exists {
		t.Errorf("expected no active enforcement in groupB, but found one")
	}
}

func TestCheckEnforcementSuspensions_VoidInheritedSuspension(t *testing.T) {
	groupA := "group-a"
	groupB := "group-b"
	userID1 := "user-1"
	userID2 := "user-2"

	// Create journal 1 with suspension in groupA
	journal1 := NewGuildEnforcementJournal(userID1)
	newRecord := journal1.AddRecord(
		groupA,
		"enforcer-1",
		"enforcer-discord-1",
		"Test suspension",
		"Test notes",
		false,     // requireCommunityValues
		true,      // allowPrivateLobbies
		time.Hour, // 1 hour suspension
	)

	// Create journal 2
	journal2 := NewGuildEnforcementJournal(userID2)

	// void the groupA record ID but in groupB
	journal1.VoidRecord(groupB, newRecord.ID, "voider-1", "voider-discord-1", "Test void")

	journals := GuildEnforcementJournalList{
		userID1: journal1,
		userID2: journal2,
	}

	// map[parentGroupID]map[childGroupID]bool
	inheritanceMap := map[string][]string{
		groupA: {groupB}, // groupB inherits from groupA
		groupB: {},
	}

	activeEnforcements, err := CheckEnforcementSuspensions(journals, inheritanceMap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The suspension in groupA should still be active because the void is in groupB
	if _, exists := activeEnforcements[groupA]; !exists {
		t.Errorf("expected active enforcement in groupA, but none found")
	}

	// Verify the suspension record is present and correct
	for _, modeRecords := range activeEnforcements[groupA] {
		if modeRecords.ID != newRecord.ID {
			t.Errorf("expected record ID %s, got %s", newRecord.ID, modeRecords.ID)
		}
		if modeRecords.UserID != userID1 {
			t.Errorf("expected user ID %s, got %s", userID1, modeRecords.UserID)
		}
		if modeRecords.GroupID != groupA {
			t.Errorf("expected group ID %s, got %s", groupA, modeRecords.GroupID)
		}
		break // Only need to check one record since they should all be the same
	}

	// GroupB should not have any active enforcements
	if _, exists := activeEnforcements[groupB]; exists {
		t.Errorf("expected no active enforcement in groupB, but found one")
	}
}

func TestCreateSuspensionDetailsEmbedField(t *testing.T) {
	groupA := "group-a"
	groupB := "group-b"
	userID := "user-1"
	enforcerUserID := "enforcer-1"
	enforcerDiscordID := "enforcer-discord-1"

	testCases := []struct {
		name                string
		guildName           string
		records             []GuildEnforcementRecord
		voids               map[string]GuildEnforcementRecordVoid
		includeInactive     bool
		includeAuditorNotes bool
		showEnforcerID      bool
		currentGuildID      string
		expectedContains    []string
		expectedNotContains []string
	}{
		{
			name:      "Show enforcer ID when moderator views current guild suspension",
			guildName: "Guild A",
			records: []GuildEnforcementRecord{
				{
					ID:                userID + "-record-1",
					UserID:            userID,
					GroupID:           groupA,
					EnforcerUserID:    enforcerUserID,
					EnforcerDiscordID: enforcerDiscordID,
					CreatedAt:         time.Now().Add(-1 * time.Hour),
					UpdatedAt:         time.Now().Add(-1 * time.Hour),
					UserNoticeText:    "Test suspension",
					Expiry:            time.Now().Add(1 * time.Hour),
					AuditorNotes:      "Test notes",
				},
			},
			voids:               nil,
			includeInactive:     false,
			includeAuditorNotes: true,
			showEnforcerID:      true,
			currentGuildID:      groupA,
			expectedContains:    []string{"<@!" + enforcerDiscordID + ">", "Test suspension"},
			expectedNotContains: []string{},
		},
		{
			name:      "Hide enforcer ID when non-moderator views suspension",
			guildName: "Guild A",
			records: []GuildEnforcementRecord{
				{
					ID:                userID + "-record-1",
					UserID:            userID,
					GroupID:           groupA,
					EnforcerUserID:    enforcerUserID,
					EnforcerDiscordID: enforcerDiscordID,
					CreatedAt:         time.Now().Add(-1 * time.Hour),
					UpdatedAt:         time.Now().Add(-1 * time.Hour),
					UserNoticeText:    "Test suspension",
					Expiry:            time.Now().Add(1 * time.Hour),
					AuditorNotes:      "Test notes",
				},
			},
			voids:               nil,
			includeInactive:     false,
			includeAuditorNotes: false,
			showEnforcerID:      false,
			currentGuildID:      groupA,
			expectedContains:    []string{"Test suspension"},
			expectedNotContains: []string{"<@!" + enforcerDiscordID + ">"},
		},
		{
			name:      "Hide enforcer ID when moderator views different guild suspension",
			guildName: "Guild B",
			records: []GuildEnforcementRecord{
				{
					ID:                userID + "-record-1",
					UserID:            userID,
					GroupID:           groupA,
					EnforcerUserID:    enforcerUserID,
					EnforcerDiscordID: enforcerDiscordID,
					CreatedAt:         time.Now().Add(-1 * time.Hour),
					UpdatedAt:         time.Now().Add(-1 * time.Hour),
					UserNoticeText:    "Test suspension",
					Expiry:            time.Now().Add(1 * time.Hour),
					AuditorNotes:      "Test notes",
				},
			},
			voids:               nil,
			includeInactive:     false,
			includeAuditorNotes: true,
			showEnforcerID:      true,
			currentGuildID:      groupB,
			expectedContains:    []string{"Test suspension"},
			expectedNotContains: []string{"<@!" + enforcerDiscordID + ">"},
		},
		{
			name:      "Hide auditor notes when not moderator",
			guildName: "Guild A",
			records: []GuildEnforcementRecord{
				{
					ID:                userID + "-record-1",
					UserID:            userID,
					GroupID:           groupA,
					EnforcerUserID:    enforcerUserID,
					EnforcerDiscordID: enforcerDiscordID,
					CreatedAt:         time.Now().Add(-1 * time.Hour),
					UpdatedAt:         time.Now().Add(-1 * time.Hour),
					UserNoticeText:    "Test suspension",
					Expiry:            time.Now().Add(1 * time.Hour),
					AuditorNotes:      "Test notes",
				},
			},
			voids:               nil,
			includeInactive:     false,
			includeAuditorNotes: false,
			showEnforcerID:      false,
			currentGuildID:      groupA,
			expectedContains:    []string{"Test suspension"},
			expectedNotContains: []string{"Test notes", "<@!" + enforcerDiscordID + ">"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			field := createSuspensionDetailsEmbedField(
				tc.guildName,
				tc.records,
				tc.voids,
				tc.includeInactive,
				tc.includeAuditorNotes,
				tc.showEnforcerID,
				tc.currentGuildID,
			)

			if field == nil {
				t.Fatal("expected field to be non-nil")
			}

			for _, expected := range tc.expectedContains {
				if !strings.Contains(field.Value, expected) {
					t.Errorf("expected field value to contain %q, but it did not. Field value: %s", expected, field.Value)
				}
			}

			for _, notExpected := range tc.expectedNotContains {
				if strings.Contains(field.Value, notExpected) {
					t.Errorf("expected field value to NOT contain %q, but it did. Field value: %s", notExpected, field.Value)
				}
			}
		})
	}
}

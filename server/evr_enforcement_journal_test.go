package server

import (
	"encoding/json"
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

func TestGuildEnforcementJournalStorageIndexes(t *testing.T) {
	journal := NewGuildEnforcementJournal("test-user-id")

	indexes := journal.StorageIndexes()

	if len(indexes) == 0 {
		t.Fatal("expected at least one index, but got none")
	}

	idx := indexes[0]

	if idx.Name != StorageIndexEnforcementJournalByGuildID {
		t.Errorf("expected index name %q, got %q", StorageIndexEnforcementJournalByGuildID, idx.Name)
	}

	if idx.Collection != StorageCollectionEnforcementJournal {
		t.Errorf("expected collection %q, got %q", StorageCollectionEnforcementJournal, idx.Collection)
	}

	if idx.Key != StorageKeyEnforcementJournal {
		t.Errorf("expected key %q, got %q", StorageKeyEnforcementJournal, idx.Key)
	}

	if len(idx.Fields) == 0 {
		t.Error("expected fields to be non-empty")
	}

	if idx.Fields[0] != "records" {
		t.Errorf("expected first field to be %q, got %q", "records", idx.Fields[0])
	}

	if idx.MaxEntries <= 0 {
		t.Errorf("expected positive MaxEntries, got %d", idx.MaxEntries)
	}
}

func TestEnforcementJournalEntryConversion(t *testing.T) {
	userID := "test-user-id"
	groupID := "test-group-id"
	enforcerUserID := "enforcer-user-id"
	enforcerDiscordID := "123456789"

	journal := NewGuildEnforcementJournal(userID)

	// Add a record
	record := journal.AddRecord(
		groupID,
		enforcerUserID,
		enforcerDiscordID,
		"User violated policy X",
		"Auditor notes: severe infraction",
		true,      // requireCommunityValues
		false,     // allowPrivateLobbies (suspension applies to all lobbies)
		24 * time.Hour,
	)

	// Verify the record was added
	records := journal.GroupRecords(groupID)
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}

	retrievedRecord := records[0]

	if retrievedRecord.ID != record.ID {
		t.Errorf("expected ID %q, got %q", record.ID, retrievedRecord.ID)
	}

	if retrievedRecord.UserID != userID {
		t.Errorf("expected user ID %q, got %q", userID, retrievedRecord.UserID)
	}

	if retrievedRecord.GroupID != groupID {
		t.Errorf("expected group ID %q, got %q", groupID, retrievedRecord.GroupID)
	}

	if retrievedRecord.EnforcerUserID != enforcerUserID {
		t.Errorf("expected enforcer user ID %q, got %q", enforcerUserID, retrievedRecord.EnforcerUserID)
	}

	if retrievedRecord.EnforcerDiscordID != enforcerDiscordID {
		t.Errorf("expected enforcer Discord ID %q, got %q", enforcerDiscordID, retrievedRecord.EnforcerDiscordID)
	}

	if retrievedRecord.UserNoticeText != "User violated policy X" {
		t.Errorf("expected notice text %q, got %q", "User violated policy X", retrievedRecord.UserNoticeText)
	}

	if retrievedRecord.AuditorNotes != "Auditor notes: severe infraction" {
		t.Errorf("expected auditor notes %q, got %q", "Auditor notes: severe infraction", retrievedRecord.AuditorNotes)
	}

	if !retrievedRecord.CommunityValuesRequired {
		t.Error("expected community values required to be true")
	}

	if retrievedRecord.AllowPrivateLobbies {
		t.Error("expected allow private lobbies to be false")
	}

	if !retrievedRecord.IsSuspension() {
		t.Error("expected IsSuspension to return true")
	}
}

func TestEnforcementJournalEntryVoid(t *testing.T) {
	userID := "test-user-id"
	groupID := "test-group-id"
	enforcerUserID := "enforcer-user-id"
	enforcerDiscordID := "123456789"
	voidAuthorID := "void-author-id"
	voidAuthorDiscordID := "987654321"

	journal := NewGuildEnforcementJournal(userID)

	// Add a record
	record := journal.AddRecord(
		groupID,
		enforcerUserID,
		enforcerDiscordID,
		"Test suspension",
		"Test notes",
		false,
		false,
		24 * time.Hour,
	)

	// Verify the record is not voided
	if journal.IsVoid(groupID, record.ID) {
		t.Error("expected record to not be voided initially")
	}

	// Void the record
	journal.VoidRecord(groupID, record.ID, voidAuthorID, voidAuthorDiscordID, "Voidance reason")

	// Verify the record is now voided
	if !journal.IsVoid(groupID, record.ID) {
		t.Error("expected record to be voided after calling VoidRecord")
	}

	// Get the void details
	void, found := journal.GetVoid(groupID, record.ID)
	if !found {
		t.Fatal("expected void to be found")
	}

	if void.RecordID != record.ID {
		t.Errorf("expected record ID %q, got %q", record.ID, void.RecordID)
	}

	if void.GroupID != groupID {
		t.Errorf("expected group ID %q, got %q", groupID, void.GroupID)
	}

	if void.AuthorID != voidAuthorID {
		t.Errorf("expected author ID %q, got %q", voidAuthorID, void.AuthorID)
	}

	if void.AuthorDiscordID != voidAuthorDiscordID {
		t.Errorf("expected author Discord ID %q, got %q", voidAuthorDiscordID, void.AuthorDiscordID)
	}

	if void.Notes != "Voidance reason" {
		t.Errorf("expected void notes %q, got %q", "Voidance reason", void.Notes)
	}
}

func TestEnforcementJournalEntryJSONMarshaling(t *testing.T) {
	userID := "test-user-id"
	groupID := "test-group-id"

	journal := NewGuildEnforcementJournal(userID)
	journal.AddRecord(groupID, "enforcer-1", "discord-1", "Test", "Notes", false, false, 24*time.Hour)

	// Marshal to JSON
	data, err := json.Marshal(journal)
	if err != nil {
		t.Fatalf("failed to marshal journal: %v", err)
	}

	// Unmarshal back
	newJournal := &GuildEnforcementJournal{}
	if err := json.Unmarshal(data, newJournal); err != nil {
		t.Fatalf("failed to unmarshal journal: %v", err)
	}

	// Verify the data matches
	records := newJournal.GroupRecords(groupID)
	if len(records) != 1 {
		t.Fatalf("expected 1 record after unmarshal, got %d", len(records))
	}

	if records[0].UserID != userID {
		t.Errorf("expected user ID %q after unmarshal, got %q", userID, records[0].UserID)
	}

	if records[0].GroupID != groupID {
		t.Errorf("expected group ID %q after unmarshal, got %q", groupID, records[0].GroupID)
	}
}

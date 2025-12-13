package server

import (
	"strings"
	"testing"
	"time"
)

func TestGuildEnforcementRecord_GetNotificationMessage(t *testing.T) {
	tests := []struct {
		name      string
		record    GuildEnforcementRecord
		guildName string
		want      []string // Strings that should be present in the message
		notWant   []string // Strings that should NOT be present
	}{
		{
			name: "Basic suspension with rule violated",
			record: GuildEnforcementRecord{
				ID:                      "test-id-1",
				UserID:                  "user-1",
				GroupID:                 "group-1",
				EnforcerUserID:          "enforcer-1",
				EnforcerDiscordID:       "123456789",
				CreatedAt:               time.Now().Add(-1 * time.Hour),
				UpdatedAt:               time.Now().Add(-1 * time.Hour),
				UserNoticeText:          "Repeated harassment in voice chat",
				Expiry:                  time.Now().Add(23 * time.Hour),
				CommunityValuesRequired: false,
				AuditorNotes:            "Internal notes for auditors only",
				AllowPrivateLobbies:     false,
				RuleViolated:            "Harassment or Hate Speech",
				IsPubliclyVisible:       true,
			},
			guildName: "Test Guild",
			want: []string{
				"⚠️",
				"Suspension",
				"Test Guild",
				"Harassment or Hate Speech",
				"Repeated harassment in voice chat",
				"Duration:",
				"1d",
				"All gameplay suspended",
				"/whoami",
			},
			notWant: []string{
				"Internal notes for auditors only", // Auditor notes should not appear in user message
			},
		},
		{
			name: "Kick without suspension",
			record: GuildEnforcementRecord{
				ID:                "test-id-2",
				UserID:            "user-2",
				GroupID:           "group-2",
				CreatedAt:         time.Now(),
				UpdatedAt:         time.Now(),
				UserNoticeText:    "Spamming chat",
				Expiry:            time.Time{}, // Zero time means kick, not suspension
				RuleViolated:      "Spam or Advertising",
				AllowPrivateLobbies: false,
			},
			guildName: "Another Guild",
			want: []string{
				"⚠️",
				"Kick",
				"Another Guild",
				"Spam or Advertising",
				"Spamming chat",
				"/whoami",
			},
			notWant: []string{
				"Duration:", // No duration for kicks
				"expires",
			},
		},
		{
			name: "Suspension with private lobbies allowed",
			record: GuildEnforcementRecord{
				ID:                  "test-id-3",
				UserID:              "user-3",
				GroupID:             "group-3",
				CreatedAt:           time.Now(),
				UpdatedAt:           time.Now(),
				UserNoticeText:      "Toxic behavior in public matches",
				Expiry:              time.Now().Add(7 * 24 * time.Hour), // 7 days
				AllowPrivateLobbies: true,
				RuleViolated:        "Toxic Behavior",
			},
			guildName: "Private Test Guild",
			want: []string{
				"⚠️",
				"Suspension",
				"Private Test Guild",
				"Toxic Behavior",
				"7d",
				"Private lobbies allowed",
			},
			notWant: []string{
				"All gameplay suspended",
			},
		},
		{
			name: "Suspension with community values required",
			record: GuildEnforcementRecord{
				ID:                      "test-id-4",
				UserID:                  "user-4",
				GroupID:                 "group-4",
				CreatedAt:               time.Now(),
				UpdatedAt:               time.Now(),
				UserNoticeText:          "Serious rule violation",
				Expiry:                  time.Now().Add(14 * 24 * time.Hour), // 14 days
				CommunityValuesRequired: true,
				AllowPrivateLobbies:     false,
				RuleViolated:            "Other Rule Violation",
			},
			guildName: "Strict Guild",
			want: []string{
				"⚠️",
				"Suspension",
				"14d",
				"Action Required",
				"community values",
			},
		},
		{
			name: "Suspension without guild name",
			record: GuildEnforcementRecord{
				ID:                  "test-id-5",
				UserID:              "user-5",
				GroupID:             "group-5",
				CreatedAt:           time.Now(),
				UpdatedAt:           time.Now(),
				UserNoticeText:      "Test reason",
				Expiry:              time.Now().Add(1 * time.Hour),
				AllowPrivateLobbies: false,
			},
			guildName: "", // No guild name
			want: []string{
				"⚠️",
				"Suspension",
				"Test reason",
				"1h",
			},
			notWant: []string{
				"Guild:", // Should not show guild field if name is empty
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.record.GetNotificationMessage(tt.guildName)

			// Check for expected strings
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("GetNotificationMessage() missing expected string: %q\nGot: %s", want, got)
				}
			}

			// Check for strings that should NOT be present
			for _, notWant := range tt.notWant {
				if strings.Contains(got, notWant) {
					t.Errorf("GetNotificationMessage() contains unexpected string: %q\nGot: %s", notWant, got)
				}
			}
		})
	}
}

func TestGuildEnforcementJournal_AddRecordWithOptions(t *testing.T) {
	userID := "test-user-1"
	journal := NewGuildEnforcementJournal(userID)

	groupID := "test-group-1"
	enforcerUserID := "enforcer-1"
	enforcerDiscordID := "123456789"
	suspensionNotice := "Test suspension notice"
	notes := "Internal auditor notes"
	ruleViolated := "Harassment or Hate Speech"
	requireCommunityValues := true
	allowPrivateLobbies := false
	isPubliclyVisible := true
	suspensionDuration := 24 * time.Hour

	record := journal.AddRecordWithOptions(
		groupID,
		enforcerUserID,
		enforcerDiscordID,
		suspensionNotice,
		notes,
		ruleViolated,
		requireCommunityValues,
		allowPrivateLobbies,
		isPubliclyVisible,
		suspensionDuration,
	)

	// Verify the record was created correctly
	if record.UserID != userID {
		t.Errorf("Expected UserID %s, got %s", userID, record.UserID)
	}
	if record.GroupID != groupID {
		t.Errorf("Expected GroupID %s, got %s", groupID, record.GroupID)
	}
	if record.UserNoticeText != suspensionNotice {
		t.Errorf("Expected UserNoticeText %s, got %s", suspensionNotice, record.UserNoticeText)
	}
	if record.AuditorNotes != notes {
		t.Errorf("Expected AuditorNotes %s, got %s", notes, record.AuditorNotes)
	}
	if record.RuleViolated != ruleViolated {
		t.Errorf("Expected RuleViolated %s, got %s", ruleViolated, record.RuleViolated)
	}
	if record.CommunityValuesRequired != requireCommunityValues {
		t.Errorf("Expected CommunityValuesRequired %v, got %v", requireCommunityValues, record.CommunityValuesRequired)
	}
	if record.AllowPrivateLobbies != allowPrivateLobbies {
		t.Errorf("Expected AllowPrivateLobbies %v, got %v", allowPrivateLobbies, record.AllowPrivateLobbies)
	}
	if record.IsPubliclyVisible != isPubliclyVisible {
		t.Errorf("Expected IsPubliclyVisible %v, got %v", isPubliclyVisible, record.IsPubliclyVisible)
	}
	if record.DMNotificationSent != false {
		t.Errorf("Expected DMNotificationSent to be false initially, got %v", record.DMNotificationSent)
	}

	// Verify the record was added to the journal
	records := journal.GroupRecords(groupID)
	if len(records) != 1 {
		t.Fatalf("Expected 1 record in journal, got %d", len(records))
	}
	if records[0].ID != record.ID {
		t.Errorf("Expected record ID %s in journal, got %s", record.ID, records[0].ID)
	}
}

func TestGuildEnforcementJournal_UpdateRecordNotificationStatus(t *testing.T) {
	userID := "test-user-1"
	journal := NewGuildEnforcementJournal(userID)

	groupID := "test-group-1"
	record := journal.AddRecord(groupID, "enforcer-1", "123456789", "Test notice", "Test notes", false, false, 24*time.Hour)

	// Update notification status
	err := journal.UpdateRecordNotificationStatus(groupID, record.ID, true)
	if err != nil {
		t.Fatalf("Unexpected error updating notification status: %v", err)
	}

	// Verify the update
	records := journal.GroupRecords(groupID)
	if len(records) != 1 {
		t.Fatalf("Expected 1 record, got %d", len(records))
	}

	updatedRecord := records[0]
	if !updatedRecord.DMNotificationSent {
		t.Error("Expected DMNotificationSent to be true")
	}
	if updatedRecord.DMNotificationAttempted.IsZero() {
		t.Error("Expected DMNotificationAttempted to be set")
	}
	if updatedRecord.UpdatedAt.IsZero() {
		t.Error("Expected UpdatedAt to be set")
	}

	// Test error case: non-existent group
	err = journal.UpdateRecordNotificationStatus("non-existent-group", record.ID, true)
	if err == nil {
		t.Error("Expected error for non-existent group, got nil")
	}

	// Test error case: non-existent record
	err = journal.UpdateRecordNotificationStatus(groupID, "non-existent-record", true)
	if err == nil {
		t.Error("Expected error for non-existent record, got nil")
	}
}

func TestStandardEnforcementRules(t *testing.T) {
	// Verify that standard rules are defined
	if len(StandardEnforcementRules) == 0 {
		t.Error("StandardEnforcementRules should not be empty")
	}

	// Verify expected rules are present
	expectedRules := []string{
		"Harassment or Hate Speech",
		"Cheating or Exploiting",
		"Toxic Behavior",
		"Other Rule Violation",
	}

	for _, expected := range expectedRules {
		found := false
		for _, rule := range StandardEnforcementRules {
			if rule == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected rule %q not found in StandardEnforcementRules", expected)
		}
	}
}

func TestGuildEnforcementJournal_BackwardCompatibility(t *testing.T) {
	// Test that the old AddRecord method still works (backward compatibility)
	userID := "test-user-1"
	journal := NewGuildEnforcementJournal(userID)

	groupID := "test-group-1"
	record := journal.AddRecord(
		groupID,
		"enforcer-1",
		"123456789",
		"Test suspension notice",
		"Test notes",
		false,
		false,
		24*time.Hour,
	)

	// Verify basic fields are set
	if record.UserID != userID {
		t.Errorf("Expected UserID %s, got %s", userID, record.UserID)
	}
	if record.GroupID != groupID {
		t.Errorf("Expected GroupID %s, got %s", groupID, record.GroupID)
	}

	// Verify new optional fields have default values
	if record.RuleViolated != "" {
		t.Errorf("Expected RuleViolated to be empty for old AddRecord method, got %s", record.RuleViolated)
	}
	if record.IsPubliclyVisible {
		t.Error("Expected IsPubliclyVisible to be false for old AddRecord method")
	}
}

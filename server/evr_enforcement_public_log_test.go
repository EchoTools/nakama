package server

import (
	"testing"
	"time"
)

func TestPublicEnforcementLog_AddEntry(t *testing.T) {
	groupID := "test-group-1"
	log := NewPublicEnforcementLog(groupID)
	log.Enabled = true // Enable public logging

	// Test adding a suspension record
	record := GuildEnforcementRecord{
		ID:                "record-1",
		UserID:            "user-1",
		GroupID:           groupID,
		EnforcerUserID:    "enforcer-1",
		EnforcerDiscordID: "123456789",
		CreatedAt:         time.Now().Add(-1 * time.Hour),
		UpdatedAt:         time.Now().Add(-1 * time.Hour),
		UserNoticeText:    "Harassment in voice chat",
		Expiry:            time.Now().Add(23 * time.Hour),
		AuditorNotes:      "Secret internal notes", // Should NOT appear in public log
		RuleViolated:      "Harassment or Hate Speech",
		IsPubliclyVisible: true,
	}

	log.AddEntry(record, false, time.Time{})

	if len(log.Entries) != 1 {
		t.Fatalf("Expected 1 entry, got %d", len(log.Entries))
	}

	entry := log.Entries[0]
	if entry.ID != record.ID {
		t.Errorf("Expected ID %s, got %s", record.ID, entry.ID)
	}
	if entry.Reason != record.UserNoticeText {
		t.Errorf("Expected Reason %s, got %s", record.UserNoticeText, entry.Reason)
	}
	if entry.RuleViolated != record.RuleViolated {
		t.Errorf("Expected RuleViolated %s, got %s", record.RuleViolated, entry.RuleViolated)
	}
	if entry.ActionType != "suspension" {
		t.Errorf("Expected ActionType 'suspension', got %s", entry.ActionType)
	}
	// Verify sensitive information is NOT present
	// (The entry struct doesn't have enforcer fields, which is correct)
}

func TestPublicEnforcementLog_AddEntry_Disabled(t *testing.T) {
	groupID := "test-group-1"
	log := NewPublicEnforcementLog(groupID)
	log.Enabled = false // Disabled by default

	record := GuildEnforcementRecord{
		ID:                "record-1",
		UserID:            "user-1",
		GroupID:           groupID,
		CreatedAt:         time.Now(),
		UserNoticeText:    "Test",
		IsPubliclyVisible: true,
	}

	log.AddEntry(record, false, time.Time{})

	if len(log.Entries) != 0 {
		t.Errorf("Expected 0 entries when logging is disabled, got %d", len(log.Entries))
	}
}

func TestPublicEnforcementLog_AddEntry_PrivateRecord(t *testing.T) {
	groupID := "test-group-1"
	log := NewPublicEnforcementLog(groupID)
	log.Enabled = true

	record := GuildEnforcementRecord{
		ID:                "record-1",
		UserID:            "user-1",
		GroupID:           groupID,
		CreatedAt:         time.Now(),
		UserNoticeText:    "Test",
		IsPubliclyVisible: false, // Marked as private
	}

	log.AddEntry(record, false, time.Time{})

	if len(log.Entries) != 0 {
		t.Errorf("Expected 0 entries for private record, got %d", len(log.Entries))
	}
}

func TestPublicEnforcementLog_CleanupExpiredEntries(t *testing.T) {
	groupID := "test-group-1"
	log := NewPublicEnforcementLog(groupID)
	log.Enabled = true
	log.RetentionDays = 30

	// Add an old entry (40 days ago)
	oldRecord := GuildEnforcementRecord{
		ID:                "old-record",
		GroupID:           groupID,
		CreatedAt:         time.Now().AddDate(0, 0, -40),
		UserNoticeText:    "Old violation",
		IsPubliclyVisible: true,
	}
	log.AddEntry(oldRecord, false, time.Time{})

	// Add a recent entry (10 days ago)
	recentRecord := GuildEnforcementRecord{
		ID:                "recent-record",
		GroupID:           groupID,
		CreatedAt:         time.Now().AddDate(0, 0, -10),
		UserNoticeText:    "Recent violation",
		IsPubliclyVisible: true,
	}
	log.AddEntry(recentRecord, false, time.Time{})

	if len(log.Entries) != 2 {
		t.Fatalf("Expected 2 entries before cleanup, got %d", len(log.Entries))
	}

	removed := log.CleanupExpiredEntries()

	if removed != 1 {
		t.Errorf("Expected 1 entry removed, got %d", removed)
	}
	if len(log.Entries) != 1 {
		t.Errorf("Expected 1 entry after cleanup, got %d", len(log.Entries))
	}
	if log.Entries[0].ID != "recent-record" {
		t.Errorf("Expected recent-record to remain, got %s", log.Entries[0].ID)
	}
}

func TestPublicEnforcementLog_GetActiveEntries(t *testing.T) {
	groupID := "test-group-1"
	log := NewPublicEnforcementLog(groupID)
	log.Enabled = true

	// Add an active suspension
	activeRecord := GuildEnforcementRecord{
		ID:                "active-record",
		GroupID:           groupID,
		CreatedAt:         time.Now().Add(-1 * time.Hour),
		UserNoticeText:    "Active suspension",
		Expiry:            time.Now().Add(23 * time.Hour),
		IsPubliclyVisible: true,
	}
	log.AddEntry(activeRecord, false, time.Time{})

	// Add an expired suspension
	expiredRecord := GuildEnforcementRecord{
		ID:                "expired-record",
		GroupID:           groupID,
		CreatedAt:         time.Now().Add(-48 * time.Hour),
		UserNoticeText:    "Expired suspension",
		Expiry:            time.Now().Add(-1 * time.Hour), // Expired 1 hour ago
		IsPubliclyVisible: true,
	}
	log.AddEntry(expiredRecord, false, time.Time{})

	// Add a voided record
	voidedRecord := GuildEnforcementRecord{
		ID:                "voided-record",
		GroupID:           groupID,
		CreatedAt:         time.Now().Add(-2 * time.Hour),
		UserNoticeText:    "Voided suspension",
		Expiry:            time.Now().Add(22 * time.Hour),
		IsPubliclyVisible: true,
	}
	log.AddEntry(voidedRecord, true, time.Now().Add(-1*time.Hour)) // Voided

	if len(log.Entries) != 3 {
		t.Fatalf("Expected 3 total entries, got %d", len(log.Entries))
	}

	active := log.GetActiveEntries()
	if len(active) != 1 {
		t.Errorf("Expected 1 active entry, got %d", len(active))
	}
	if len(active) > 0 && active[0].ID != "active-record" {
		t.Errorf("Expected active-record to be active, got %s", active[0].ID)
	}
}

func TestPublicEnforcementLog_GetRecentEntries(t *testing.T) {
	groupID := "test-group-1"
	log := NewPublicEnforcementLog(groupID)
	log.Enabled = true

	// Add entries at different times
	for i := 0; i < 5; i++ {
		daysAgo := i * 10 // 0, 10, 20, 30, 40 days ago
		record := GuildEnforcementRecord{
			ID:                string(rune('a' + i)),
			GroupID:           groupID,
			CreatedAt:         time.Now().AddDate(0, 0, -daysAgo),
			UserNoticeText:    "Test violation",
			IsPubliclyVisible: true,
		}
		log.AddEntry(record, false, time.Time{})
	}

	if len(log.Entries) != 5 {
		t.Fatalf("Expected 5 total entries, got %d", len(log.Entries))
	}

	// Get entries from last 25 days
	recent := log.GetRecentEntries(25)
	if len(recent) != 3 {
		t.Errorf("Expected 3 entries within 25 days, got %d", len(recent))
	}

	// Get all entries (0 or negative days)
	all := log.GetRecentEntries(0)
	if len(all) != 5 {
		t.Errorf("Expected all 5 entries, got %d", len(all))
	}
}

func TestPublicEnforcementLog_KickVsSuspension(t *testing.T) {
	groupID := "test-group-1"
	log := NewPublicEnforcementLog(groupID)
	log.Enabled = true

	// Test kick (no expiry)
	kickRecord := GuildEnforcementRecord{
		ID:                "kick-record",
		GroupID:           groupID,
		CreatedAt:         time.Now(),
		UserNoticeText:    "Kicked for spam",
		Expiry:            time.Time{}, // Zero time = kick
		IsPubliclyVisible: true,
	}
	log.AddEntry(kickRecord, false, time.Time{})

	// Test suspension (with expiry)
	suspensionRecord := GuildEnforcementRecord{
		ID:                "suspension-record",
		GroupID:           groupID,
		CreatedAt:         time.Now(),
		UserNoticeText:    "Suspended for toxicity",
		Expiry:            time.Now().Add(24 * time.Hour),
		IsPubliclyVisible: true,
	}
	log.AddEntry(suspensionRecord, false, time.Time{})

	if len(log.Entries) != 2 {
		t.Fatalf("Expected 2 entries, got %d", len(log.Entries))
	}

	// Verify kick entry
	kickEntry := log.Entries[0]
	if kickEntry.ActionType != "kick" {
		t.Errorf("Expected ActionType 'kick', got %s", kickEntry.ActionType)
	}
	if kickEntry.Duration != "immediate" {
		t.Errorf("Expected Duration 'immediate' for kick, got %s", kickEntry.Duration)
	}

	// Verify suspension entry
	suspensionEntry := log.Entries[1]
	if suspensionEntry.ActionType != "suspension" {
		t.Errorf("Expected ActionType 'suspension', got %s", suspensionEntry.ActionType)
	}
	if suspensionEntry.Duration == "immediate" {
		t.Errorf("Expected Duration not to be 'immediate' for suspension, got %s", suspensionEntry.Duration)
	}
}

func TestPublicEnforcementLog_DefaultValues(t *testing.T) {
	groupID := "test-group-1"
	log := NewPublicEnforcementLog(groupID)

	if log.Enabled {
		t.Error("Expected public logging to be disabled by default")
	}
	if log.RetentionDays != DefaultPublicLogRetentionDays {
		t.Errorf("Expected default retention of %d days, got %d", DefaultPublicLogRetentionDays, log.RetentionDays)
	}
	if len(log.Entries) != 0 {
		t.Errorf("Expected 0 entries in new log, got %d", len(log.Entries))
	}
}

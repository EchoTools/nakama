package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
)

// TestAddRecordWithOptions_ReporterRoundTrip verifies that the reporter
// identity (who filed/reported the action) is captured on the enforcement
// record and survives a JSON marshal/unmarshal round-trip. This is the core
// of issue #466: the reporter was previously not stored at all.
func TestAddRecordWithOptions_ReporterRoundTrip(t *testing.T) {
	t.Parallel()

	userID := "target-user-1"
	journal := NewGuildEnforcementJournal(userID)

	const (
		groupID        = "group-1"
		reporterUserID = "reporter-user-7"
		reporterDiscID = "987654321"
		reportID       = "report-abc-123"
	)

	record := journal.AddRecordWithOptions(
		groupID,
		"enforcer-1",
		"123456789",
		"Test notice",
		"Internal notes",
		"Harassment or Hate Speech",
		false, // requireCommunityValues
		false, // allowPrivateLobbies
		false, // isPubliclyVisible
		24*time.Hour,
		reporterUserID,
		reporterDiscID,
		reportID,
	)

	if record.ReporterUserID != reporterUserID {
		t.Errorf("expected ReporterUserID %q, got %q", reporterUserID, record.ReporterUserID)
	}
	if record.ReporterDiscordID != reporterDiscID {
		t.Errorf("expected ReporterDiscordID %q, got %q", reporterDiscID, record.ReporterDiscordID)
	}
	if record.ReportID != reportID {
		t.Errorf("expected ReportID %q, got %q", reportID, record.ReportID)
	}

	// JSON round-trip: the fields must persist through storage.
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded GuildEnforcementRecord
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if decoded.ReporterUserID != reporterUserID {
		t.Errorf("after round-trip: expected ReporterUserID %q, got %q", reporterUserID, decoded.ReporterUserID)
	}
	if decoded.ReporterDiscordID != reporterDiscID {
		t.Errorf("after round-trip: expected ReporterDiscordID %q, got %q", reporterDiscID, decoded.ReporterDiscordID)
	}
	if decoded.ReportID != reportID {
		t.Errorf("after round-trip: expected ReportID %q, got %q", reportID, decoded.ReportID)
	}
}

// TestAddRecord_NoReporter verifies that the simpler AddRecord path (used by
// operator-initiated kicks where no reporter is known) leaves the reporter
// fields empty rather than crashing or fabricating a value.
func TestAddRecord_NoReporter(t *testing.T) {
	t.Parallel()

	journal := NewGuildEnforcementJournal("target-user-1")
	record := journal.AddRecord("group-1", "enforcer-1", "123456789", "notice", "notes", false, false, time.Hour)

	if record.ReporterUserID != "" {
		t.Errorf("expected empty ReporterUserID, got %q", record.ReporterUserID)
	}
	if record.ReporterDiscordID != "" {
		t.Errorf("expected empty ReporterDiscordID, got %q", record.ReporterDiscordID)
	}
	if record.ReportID != "" {
		t.Errorf("expected empty ReportID, got %q", record.ReportID)
	}
}

// TestCreateEnforcementRecordEmbed_Reporter verifies the operator-facing embed
// surfaces the reporter when present, and degrades gracefully (no Reporter
// field, no crash) for legacy records that never stored one.
func TestCreateEnforcementRecordEmbed_Reporter(t *testing.T) {
	t.Parallel()

	gg := &GuildGroup{Group: &api.Group{Name: "Test Guild"}}

	t.Run("reporter present is rendered", func(t *testing.T) {
		t.Parallel()
		record := GuildEnforcementRecord{
			ID:                "rec-1",
			GroupID:           "group-1",
			EnforcerDiscordID: "111",
			ReporterUserID:    "reporter-nk-id",
			ReporterDiscordID: "222",
			CreatedAt:         time.Now().Add(-time.Hour),
			UpdatedAt:         time.Now().Add(-time.Hour),
			UserNoticeText:    "notice",
			Expiry:            time.Now().Add(time.Hour),
		}
		embed := createEnforcementRecordEmbed("Title", record, gg)

		var reporterField string
		for _, f := range embed.Fields {
			if f.Name == "Reporter" {
				reporterField = f.Value
			}
		}
		if reporterField == "" {
			t.Fatal("expected a Reporter field to be present")
		}
		if !strings.Contains(reporterField, "222") {
			t.Errorf("expected reporter field to reference reporter Discord ID, got %q", reporterField)
		}
	})

	t.Run("legacy record without reporter omits the field", func(t *testing.T) {
		t.Parallel()
		record := GuildEnforcementRecord{
			ID:                "rec-2",
			GroupID:           "group-1",
			EnforcerDiscordID: "111",
			CreatedAt:         time.Now().Add(-time.Hour),
			UpdatedAt:         time.Now().Add(-time.Hour),
			UserNoticeText:    "notice",
			Expiry:            time.Now().Add(time.Hour),
		}
		embed := createEnforcementRecordEmbed("Title", record, gg)
		for _, f := range embed.Fields {
			if f.Name == "Reporter" {
				t.Errorf("expected no Reporter field for legacy record, got value %q", f.Value)
			}
		}
	})
}

// TestCreateSuspensionDetailsEmbedField_Reporter verifies the journal/whoami
// embed surfaces the reporter when present and omits it otherwise.
func TestCreateSuspensionDetailsEmbedField_Reporter(t *testing.T) {
	t.Parallel()

	groupA := "group-a"

	withReporter := []GuildEnforcementRecord{{
		ID:                "rec-with-reporter",
		GroupID:           groupA,
		EnforcerDiscordID: "enforcer-disc",
		ReporterDiscordID: "reporter-disc-555",
		ReportID:          "report-xyz",
		CreatedAt:         time.Now().Add(-time.Hour),
		UpdatedAt:         time.Now().Add(-time.Hour),
		UserNoticeText:    "Notice text",
		Expiry:            time.Now().Add(time.Hour),
	}}

	t.Run("reporter present is rendered", func(t *testing.T) {
		t.Parallel()
		field := createSuspensionDetailsEmbedField("Guild A", withReporter, nil, false, true, true, groupA, "", false)
		if field == nil {
			t.Fatal("expected non-nil field")
		}
		if !strings.Contains(field.Value, "reporter-disc-555") {
			t.Errorf("expected field to mention reporter Discord ID, got: %s", field.Value)
		}
	})

	t.Run("legacy record without reporter does not crash or mislabel", func(t *testing.T) {
		t.Parallel()
		legacy := []GuildEnforcementRecord{{
			ID:                "rec-legacy",
			GroupID:           groupA,
			EnforcerDiscordID: "enforcer-disc",
			CreatedAt:         time.Now().Add(-time.Hour),
			UpdatedAt:         time.Now().Add(-time.Hour),
			UserNoticeText:    "Notice text",
			Expiry:            time.Now().Add(time.Hour),
		}}
		field := createSuspensionDetailsEmbedField("Guild A", legacy, nil, false, true, true, groupA, "", false)
		if field == nil {
			t.Fatal("expected non-nil field")
		}
		if strings.Contains(field.Value, "Reporter") || strings.Contains(field.Value, "reported by") {
			t.Errorf("expected no reporter mention for legacy record, got: %s", field.Value)
		}
	})
}

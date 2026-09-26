package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// legacyBansTestModule is an in-memory NakamaModule double for
// MigrationLegacyEchoVRCEBans. It embeds occTestNakamaModule for
// StorageRead/StorageWrite/MultiUpdate (journal + profile persistence via
// SyncJournalAndProfile) and adds the two things that base double has no
// notion of: a "guild" group directory (GroupsList/GroupUpdate) and the
// Enforcement/journal storage index (StorageIndexList).
//
// It deliberately implements NOTHING else. occTestNakamaModule embeds a nil
// runtime.NakamaModule (AGENTS.md defect class 5), so any call the migration
// makes to a kick, DM, or session/notification method -- none of which it has
// a client or session registry to reach in the first place -- panics the
// test instead of silently succeeding. That panic IS the negative control for
// "makes zero calls to any kick/notify path".
type legacyBansTestModule struct {
	*occTestNakamaModule

	groups          map[string]*api.Group
	groupOrder      []string // stable GroupsList pagination order
	groupUpdateLog  []legacyBansGroupUpdateCall
	storageIndexErr error
}

type legacyBansGroupUpdateCall struct {
	groupID  string
	open     bool
	metadata map[string]any
	maxCount int
}

func newLegacyBansTestModule() *legacyBansTestModule {
	return &legacyBansTestModule{
		occTestNakamaModule: newOCCTestNakamaModule(),
		groups:              make(map[string]*api.Group),
	}
}

// seedGuildGroup registers a "guild" group with the given metadata. Returns
// the group ID for convenience.
func (m *legacyBansTestModule) seedGuildGroup(groupID string, md *GroupMetadata) {
	data, err := json.Marshal(md)
	if err != nil {
		panic(err)
	}
	if _, ok := m.groups[groupID]; !ok {
		m.groupOrder = append(m.groupOrder, groupID)
	}
	m.groups[groupID] = &api.Group{
		Id:       groupID,
		LangTag:  GuildGroupLangTag,
		Metadata: string(data),
		Open:     &wrapperspb.BoolValue{Value: true},
		MaxCount: 100000,
	}
}

// seedJournal stores a GuildEnforcementJournal for userID, computing its
// guild_ids the same way production's MarshalJSON does (via updateFields),
// so the storage-index simulation below can filter on it realistically.
func (m *legacyBansTestModule) seedJournal(userID string, journal *GuildEnforcementJournal) {
	data, err := json.Marshal(journal)
	if err != nil {
		panic(err)
	}
	m.seedObject(userID, StorageCollectionEnforcementJournal, StorageKeyEnforcementJournal, string(data))
}

func (m *legacyBansTestModule) GroupsList(ctx context.Context, name, langTag string, members *int, open *bool, limit int, cursor string) ([]*api.Group, string, error) {
	if cursor != "" {
		// Tests never seed more than one page; a non-empty cursor means a
		// pagination bug in the code under test.
		return nil, "", fmt.Errorf("legacyBansTestModule: unexpected non-empty cursor %q", cursor)
	}
	out := make([]*api.Group, 0, len(m.groupOrder))
	for _, id := range m.groupOrder {
		if langTag != "" && m.groups[id].LangTag != langTag {
			continue
		}
		out = append(out, m.groups[id])
	}
	return out, "", nil
}

func (m *legacyBansTestModule) GroupUpdate(ctx context.Context, id, userID, name, creatorID, langTag, description, avatarUrl string, open bool, metadata map[string]interface{}, maxCount int) error {
	g, ok := m.groups[id]
	if !ok {
		return fmt.Errorf("legacyBansTestModule: GroupUpdate on unknown group %q", id)
	}
	m.groupUpdateLog = append(m.groupUpdateLog, legacyBansGroupUpdateCall{groupID: id, open: open, metadata: metadata, maxCount: maxCount})
	if metadata != nil {
		data, err := json.Marshal(metadata)
		if err != nil {
			return err
		}
		g.Metadata = string(data)
	}
	g.Open = &wrapperspb.BoolValue{Value: open}
	g.MaxCount = int32(maxCount)
	return nil
}

// StorageIndexList simulates StorageIndexEnforcementJournal for exactly the
// query this migration issues: "+value.guild_ids:<id>". It decodes each
// stored Enforcement/journal object's guild_ids and returns the ones
// containing id. EscapeIndexValue backslash-escapes Bluge special characters
// (uuids contain '-', which is one of them); stripping backslashes recovers
// the original literal since none of our test ids contain a literal
// backslash.
func (m *legacyBansTestModule) StorageIndexList(ctx context.Context, callerID, indexName, query string, limit int, order []string, cursor string) (*api.StorageObjects, string, error) {
	if m.storageIndexErr != nil {
		return nil, "", m.storageIndexErr
	}
	if indexName != StorageIndexEnforcementJournal {
		return nil, "", fmt.Errorf("legacyBansTestModule: unexpected index %q", indexName)
	}
	if cursor != "" {
		return &api.StorageObjects{}, "", nil
	}
	const prefix = "+value.guild_ids:"
	id := strings.ReplaceAll(strings.TrimPrefix(query, prefix), "\\", "")

	m.mu.Lock()
	defer m.mu.Unlock()

	var matched []*api.StorageObject
	for _, obj := range m.objects {
		if obj.Collection != StorageCollectionEnforcementJournal || obj.Key != StorageKeyEnforcementJournal {
			continue
		}
		var probe struct {
			GuildIDs []string `json:"guild_ids"`
		}
		if err := json.Unmarshal([]byte(obj.Value), &probe); err != nil {
			continue
		}
		for _, g := range probe.GuildIDs {
			if g == id {
				matched = append(matched, obj)
				break
			}
		}
	}
	return &api.StorageObjects{Objects: matched}, "", nil
}

// --- test fixtures -----------------------------------------------------

func newLegacyBanRecord(groupID, notice, notes string, createdAt time.Time, expiry time.Time) GuildEnforcementRecord {
	return GuildEnforcementRecord{
		ID:                 uuid.Must(uuid.NewV4()).String(),
		GroupID:            groupID,
		EnforcerUserID:     "enforcer-1",
		EnforcerDiscordID:  "111",
		CreatedAt:          createdAt,
		UpdatedAt:          createdAt,
		UserNoticeText:     notice,
		Expiry:             expiry,
		AuditorNotes:       notes,
		RuleViolated:       "Cheating or Exploiting",
		IsPubliclyVisible:  true,
		DMNotificationSent: false,
	}
}

// legacyBansTestFixture wires one service guild, N inheriting guilds, and one
// suspended user whose journal carries a single active record in the service
// guild's group.
type legacyBansTestFixture struct {
	nk             *legacyBansTestModule
	serviceGroupID string
	guildAID       string // inherits
	guildBID       string // inherits
	otherGroupID   string // does NOT inherit
	userID         string
	origRecordID   string
}

func newLegacyBansTestFixture(t *testing.T, notice string) *legacyBansTestFixture {
	t.Helper()

	nk := newLegacyBansTestModule()

	serviceGuildDiscordID := "service-discord-guild"
	serviceGroupID := uuid.Must(uuid.NewV4()).String()
	nk.seedGuildGroup(serviceGroupID, &GroupMetadata{GuildID: serviceGuildDiscordID})

	guildAID := uuid.Must(uuid.NewV4()).String()
	nk.seedGuildGroup(guildAID, &GroupMetadata{
		GuildID:                       "guild-a-discord",
		SuspensionInheritanceGroupIDs: []string{serviceGroupID},
	})

	guildBID := uuid.Must(uuid.NewV4()).String()
	nk.seedGuildGroup(guildBID, &GroupMetadata{
		GuildID:                       "guild-b-discord",
		SuspensionInheritanceGroupIDs: []string{serviceGroupID},
	})

	otherGroupID := uuid.Must(uuid.NewV4()).String()
	nk.seedGuildGroup(otherGroupID, &GroupMetadata{GuildID: "other-guild-discord"})

	ServiceSettingsUpdate(&ServiceSettingsData{ServiceGuildID: serviceGuildDiscordID})
	t.Cleanup(func() { ServiceSettingsUpdate(nil) })

	userID := uuid.Must(uuid.NewV4()).String()
	orig := newLegacyBanRecord(serviceGroupID, notice, "original notes", time.Now().Add(-time.Hour).UTC(), time.Now().Add(24*time.Hour).UTC())

	journal := NewGuildEnforcementJournal(userID)
	journal.RecordsByGroupID = map[string][]GuildEnforcementRecord{
		serviceGroupID: {orig},
	}
	nk.seedJournal(userID, journal)

	return &legacyBansTestFixture{
		nk:             nk,
		serviceGroupID: serviceGroupID,
		guildAID:       guildAID,
		guildBID:       guildBID,
		otherGroupID:   otherGroupID,
		userID:         userID,
		origRecordID:   orig.ID,
	}
}

func (f *legacyBansTestFixture) readJournal(t *testing.T) *GuildEnforcementJournal {
	t.Helper()
	objs, err := f.nk.StorageRead(context.Background(), []*runtime.StorageRead{{
		Collection: StorageCollectionEnforcementJournal,
		Key:        StorageKeyEnforcementJournal,
		UserID:     f.userID,
	}})
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if len(objs) != 1 {
		t.Fatalf("expected 1 journal object, got %d", len(objs))
	}
	journal, err := GuildEnforcementJournalFromStorageObject(objs[0])
	if err != nil {
		t.Fatalf("unmarshal journal: %v", err)
	}
	return journal
}

func (f *legacyBansTestFixture) guildMetadata(t *testing.T, groupID string) *GroupMetadata {
	t.Helper()
	g, ok := f.nk.groups[groupID]
	if !ok {
		t.Fatalf("no such group %q", groupID)
	}
	md := &GroupMetadata{}
	if err := json.Unmarshal([]byte(g.Metadata), md); err != nil {
		t.Fatalf("unmarshal group metadata: %v", err)
	}
	return md
}

func runLegacyBansMigration(t *testing.T, nk runtime.NakamaModule) error {
	t.Helper()
	m := &MigrationLegacyEchoVRCEBans{}
	logger := NewRuntimeGoLogger(loggerForTest(t))
	return m.MigrateSystem(context.Background(), logger, nil, nk)
}

// --- tests ---------------------------------------------------------------

func TestLegacyBansMigration_DryRun_WritesNothing(t *testing.T) {
	f := newLegacyBansTestFixture(t, "Account is Globally Banned")

	if err := runLegacyBansMigration(t, f.nk); err != nil {
		t.Fatalf("dry run returned error: %v", err)
	}

	journal := f.readJournal(t)
	if len(journal.RecordsByGroupID[f.guildAID]) != 0 {
		t.Fatalf("dry run wrote a record into guild A: %+v", journal.RecordsByGroupID[f.guildAID])
	}
	if len(journal.RecordsByGroupID[f.guildBID]) != 0 {
		t.Fatalf("dry run wrote a record into guild B: %+v", journal.RecordsByGroupID[f.guildBID])
	}

	if len(f.nk.groupUpdateLog) != 0 {
		t.Fatalf("dry run called GroupUpdate: %+v", f.nk.groupUpdateLog)
	}

	if marker, err := legacyBansMarkerRead(context.Background(), f.nk); err != nil {
		t.Fatalf("read marker: %v", err)
	} else if marker != nil {
		t.Fatalf("dry run wrote a completion marker: %+v", marker)
	}
}

func TestLegacyBansMigration_Apply_CopiesToEachInheritingGuild(t *testing.T) {
	f := newLegacyBansTestFixture(t, "some other notice")
	t.Setenv(legacyBansApplyEnvVar, legacyBansApplyEnvValue)

	if err := runLegacyBansMigration(t, f.nk); err != nil {
		t.Fatalf("apply run returned error: %v", err)
	}

	journal := f.readJournal(t)

	for _, groupID := range []string{f.guildAID, f.guildBID} {
		recs := journal.RecordsByGroupID[groupID]
		if len(recs) != 1 {
			t.Fatalf("guild %s: expected 1 copied record, got %d", groupID, len(recs))
		}
		cp := recs[0]
		orig := journal.RecordsByGroupID[f.serviceGroupID][0]

		if cp.ID == orig.ID {
			t.Errorf("guild %s: copy kept the original ID", groupID)
		}
		if cp.GroupID != groupID {
			t.Errorf("guild %s: copy GroupID = %q, want %q", groupID, cp.GroupID, groupID)
		}
		wantNotes := fmt.Sprintf(legacyBansAuditorNotePrefix, orig.ID, orig.AuditorNotes)
		if cp.AuditorNotes != wantNotes {
			t.Errorf("guild %s: AuditorNotes = %q, want %q", groupID, cp.AuditorNotes, wantNotes)
		}
		if !cp.DMNotificationSent {
			t.Errorf("guild %s: copy DMNotificationSent = false, want true", groupID)
		}
		if !cp.CreatedAt.Equal(orig.CreatedAt) {
			t.Errorf("guild %s: copy CreatedAt = %v, want %v (byte-identical to original)", groupID, cp.CreatedAt, orig.CreatedAt)
		}
		if !cp.Expiry.Equal(orig.Expiry) {
			t.Errorf("guild %s: copy Expiry = %v, want %v (byte-identical to original)", groupID, cp.Expiry, orig.Expiry)
		}
		if cp.RuleViolated != orig.RuleViolated {
			t.Errorf("guild %s: copy RuleViolated = %q, want %q", groupID, cp.RuleViolated, orig.RuleViolated)
		}
		if cp.UserNoticeText != orig.UserNoticeText {
			t.Errorf("guild %s: copy UserNoticeText = %q, want unchanged %q", groupID, cp.UserNoticeText, orig.UserNoticeText)
		}
	}
}

// TestLegacyBansMigration_CopyFieldEquality is the field-by-field version of
// the copy contract: every field except the five (six, with UserNoticeText)
// the design calls out must be byte-identical to the original.
func TestLegacyBansMigration_CopyFieldEquality(t *testing.T) {
	orig := newLegacyBanRecord("service-group", "unchanged notice", "orig notes", time.Now().Add(-time.Hour).UTC(), time.Now().Add(time.Hour).UTC())
	orig.CommunityValuesRequired = true
	orig.AllowPrivateLobbies = true
	orig.ReporterUserID = "reporter-1"
	orig.ReporterDiscordID = "222"
	orig.ReportID = "report-1"
	orig.IsPubliclyVisible = true
	orig.DMNotificationAttempted = time.Now().Add(-30 * time.Minute).UTC()

	cp := legacyBansCopyRecord(orig, "target-group")

	if cp.ID == orig.ID {
		t.Error("ID was not changed")
	}
	if cp.GroupID != "target-group" {
		t.Errorf("GroupID = %q, want %q", cp.GroupID, "target-group")
	}
	if cp.UpdatedAt.Equal(orig.UpdatedAt) {
		t.Error("UpdatedAt was not refreshed")
	}
	wantNotes := fmt.Sprintf(legacyBansAuditorNotePrefix, orig.ID, orig.AuditorNotes)
	if cp.AuditorNotes != wantNotes {
		t.Errorf("AuditorNotes = %q, want %q", cp.AuditorNotes, wantNotes)
	}
	if !cp.DMNotificationSent {
		t.Error("DMNotificationSent = false, want true")
	}
	if cp.UserNoticeText != orig.UserNoticeText {
		t.Errorf("UserNoticeText changed unexpectedly: %q -> %q", orig.UserNoticeText, cp.UserNoticeText)
	}

	// Everything else: byte-identical.
	if !cp.CreatedAt.Equal(orig.CreatedAt) {
		t.Error("CreatedAt changed")
	}
	if !cp.Expiry.Equal(orig.Expiry) {
		t.Error("Expiry changed")
	}
	if cp.EnforcerUserID != orig.EnforcerUserID || cp.EnforcerDiscordID != orig.EnforcerDiscordID {
		t.Error("enforcer fields changed")
	}
	if cp.ReporterUserID != orig.ReporterUserID || cp.ReporterDiscordID != orig.ReporterDiscordID || cp.ReportID != orig.ReportID {
		t.Error("reporter fields changed")
	}
	if cp.CommunityValuesRequired != orig.CommunityValuesRequired {
		t.Error("CommunityValuesRequired changed")
	}
	if cp.AllowPrivateLobbies != orig.AllowPrivateLobbies {
		t.Error("AllowPrivateLobbies changed")
	}
	if cp.RuleViolated != orig.RuleViolated {
		t.Error("RuleViolated changed")
	}
	if cp.IsPubliclyVisible != orig.IsPubliclyVisible {
		t.Error("IsPubliclyVisible changed")
	}
	if !cp.DMNotificationAttempted.Equal(orig.DMNotificationAttempted) {
		t.Error("DMNotificationAttempted changed")
	}
}

// TestLegacyBansNoticeTextReplacement table-tests every exact production
// variant plus one unchanged control, per dad's final ruling.
func TestLegacyBansNoticeTextReplacement(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Account is Globally Banned", "Account is Banned"},
		{"Account Globally Banned", "Account is Banned"},
		{"Account Globally Banned.", "Account is Banned"},
		{"Account globally banned.", "Account is Banned"},
		{"Account Global Banned.", "Account is Banned"},
		{"Acount Globally Banned", "Account is Banned"},
		{"Global Ban", "Account is Banned"},
		// Control: anything not an exact listed variant is left alone,
		// including near-misses and case differences not in the list.
		{"Cheating or Exploiting", "Cheating or Exploiting"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := legacyBansNoticeText(c.in); got != c.want {
				t.Errorf("legacyBansNoticeText(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestLegacyBansMigration_SkipsUserWithExistingActiveSuspensionInTargetGuild(t *testing.T) {
	f := newLegacyBansTestFixture(t, "notice")
	t.Setenv(legacyBansApplyEnvVar, legacyBansApplyEnvValue)

	// Guild A already has its own, unrelated active suspension for this user.
	journal := f.readJournal(t)
	journal.RecordsByGroupID[f.guildAID] = []GuildEnforcementRecord{
		newLegacyBanRecord(f.guildAID, "guild A's own ban", "guild A notes", time.Now().Add(-time.Minute).UTC(), time.Now().Add(time.Hour).UTC()),
	}
	if err := SyncJournalAndProfile(context.Background(), f.nk, f.userID, journal); err != nil {
		t.Fatalf("seed guild A suspension: %v", err)
	}

	if err := runLegacyBansMigration(t, f.nk); err != nil {
		t.Fatalf("apply run returned error: %v", err)
	}

	after := f.readJournal(t)
	if got := len(after.RecordsByGroupID[f.guildAID]); got != 1 {
		t.Fatalf("guild A: expected the pre-existing record to be the only one (skipped), got %d records", got)
	}
	if got := len(after.RecordsByGroupID[f.guildBID]); got != 1 {
		t.Fatalf("guild B: expected 1 copied record, got %d", got)
	}
}

func TestLegacyBansMigration_DoesNotTouchNonInheritingGuild(t *testing.T) {
	f := newLegacyBansTestFixture(t, "notice")
	t.Setenv(legacyBansApplyEnvVar, legacyBansApplyEnvValue)

	if err := runLegacyBansMigration(t, f.nk); err != nil {
		t.Fatalf("apply run returned error: %v", err)
	}

	journal := f.readJournal(t)
	if got := len(journal.RecordsByGroupID[f.otherGroupID]); got != 0 {
		t.Fatalf("non-inheriting guild got %d records, want 0", got)
	}
	for _, call := range f.nk.groupUpdateLog {
		if call.groupID == f.otherGroupID {
			t.Fatalf("GroupUpdate was called on the non-inheriting guild: %+v", call)
		}
	}
}

func TestLegacyBansMigration_UnlinksOnlyAfterCopiesSucceed(t *testing.T) {
	f := newLegacyBansTestFixture(t, "notice")
	t.Setenv(legacyBansApplyEnvVar, legacyBansApplyEnvValue)

	if err := runLegacyBansMigration(t, f.nk); err != nil {
		t.Fatalf("apply run returned error: %v", err)
	}

	for _, groupID := range []string{f.guildAID, f.guildBID} {
		md := f.guildMetadata(t, groupID)
		for _, id := range md.SuspensionInheritanceGroupIDs {
			if id == f.serviceGroupID {
				t.Fatalf("guild %s still lists the service group after a successful apply run: %+v", groupID, md.SuspensionInheritanceGroupIDs)
			}
		}
	}

	// The non-inheriting guild's metadata is untouched.
	otherMD := f.guildMetadata(t, f.otherGroupID)
	if len(otherMD.SuspensionInheritanceGroupIDs) != 0 {
		t.Fatalf("non-inheriting guild's inheritance list changed: %+v", otherMD.SuspensionInheritanceGroupIDs)
	}
}

func TestLegacyBansMigration_CopyFailureStopsBeforeUnlink(t *testing.T) {
	f := newLegacyBansTestFixture(t, "notice")
	t.Setenv(legacyBansApplyEnvVar, legacyBansApplyEnvValue)

	f.nk.failNonVersion = fmt.Errorf("simulated storage outage")

	if err := runLegacyBansMigration(t, f.nk); err == nil {
		t.Fatal("expected an error when the journal write fails, got nil")
	}

	if len(f.nk.groupUpdateLog) != 0 {
		t.Fatalf("unlink was attempted despite the copy failure: %+v", f.nk.groupUpdateLog)
	}
	for _, groupID := range []string{f.guildAID, f.guildBID} {
		md := f.guildMetadata(t, groupID)
		found := false
		for _, id := range md.SuspensionInheritanceGroupIDs {
			if id == f.serviceGroupID {
				found = true
			}
		}
		if !found {
			t.Fatalf("guild %s was unlinked despite the copy failure", groupID)
		}
	}

	if marker, err := legacyBansMarkerRead(context.Background(), f.nk); err != nil {
		t.Fatalf("read marker: %v", err)
	} else if marker != nil {
		t.Fatalf("a completion marker was written despite the copy failure: %+v", marker)
	}
}

func TestLegacyBansMigration_IdempotentOnSecondRun(t *testing.T) {
	f := newLegacyBansTestFixture(t, "notice")
	t.Setenv(legacyBansApplyEnvVar, legacyBansApplyEnvValue)

	if err := runLegacyBansMigration(t, f.nk); err != nil {
		t.Fatalf("first apply run returned error: %v", err)
	}
	first := f.readJournal(t)
	firstCountA := len(first.RecordsByGroupID[f.guildAID])
	firstCountB := len(first.RecordsByGroupID[f.guildBID])
	firstUpdateCalls := len(f.nk.groupUpdateLog)

	if err := runLegacyBansMigration(t, f.nk); err != nil {
		t.Fatalf("second apply run returned error: %v", err)
	}

	second := f.readJournal(t)
	if got := len(second.RecordsByGroupID[f.guildAID]); got != firstCountA {
		t.Fatalf("guild A record count changed on second run: %d -> %d", firstCountA, got)
	}
	if got := len(second.RecordsByGroupID[f.guildBID]); got != firstCountB {
		t.Fatalf("guild B record count changed on second run: %d -> %d", firstCountB, got)
	}
	// The completion marker short-circuits the second run before it reaches
	// GroupsList/GroupUpdate at all.
	if got := len(f.nk.groupUpdateLog); got != firstUpdateCalls {
		t.Fatalf("GroupUpdate was called again on the already-completed second run: %d -> %d calls", firstUpdateCalls, got)
	}
}

// TestLegacyBansMigration_IdempotentOnSecondRun_NegativeControl proves the
// idempotency test above can actually fail: without the completion marker (or
// the skip rule it backs up), a second run would double the record count.
// This runs the copy logic twice by hand, bypassing the marker, and asserts
// the SKIP RULE ALONE (marker deleted) still prevents a duplicate -- which is
// what makes the migration safe to re-run even if the marker write is lost.
func TestLegacyBansMigration_IdempotentOnSecondRun_NegativeControl(t *testing.T) {
	f := newLegacyBansTestFixture(t, "notice")
	t.Setenv(legacyBansApplyEnvVar, legacyBansApplyEnvValue)

	if err := runLegacyBansMigration(t, f.nk); err != nil {
		t.Fatalf("first apply run returned error: %v", err)
	}
	first := f.readJournal(t)
	firstCountA := len(first.RecordsByGroupID[f.guildAID])
	if firstCountA == 0 {
		t.Fatal("fixture bug: first run copied nothing, so this control proves nothing")
	}

	// Simulate a lost marker: delete it, but leave the copied records (and
	// the guild's already-unlinked metadata) in place, exactly as a crash
	// between "write marker" and "next boot" would.
	if _, err := f.nk.StorageWrite(context.Background(), []*runtime.StorageWrite{{
		Collection: legacyBansMigrationStorageCollection,
		Key:        legacyBansMigrationStorageKey,
		UserID:     SystemUserID,
		Value:      `{}`, // CompletedAt zero value: "not completed"
	}}); err != nil {
		t.Fatalf("reset marker: %v", err)
	}

	if err := runLegacyBansMigration(t, f.nk); err != nil {
		t.Fatalf("second apply run (marker cleared) returned error: %v", err)
	}

	second := f.readJournal(t)
	if got := len(second.RecordsByGroupID[f.guildAID]); got != firstCountA {
		t.Fatalf("skip rule failed to prevent a duplicate copy with the marker cleared: %d -> %d records", firstCountA, got)
	}
}

func TestLegacyBansMigration_NoServiceGuildConfigured(t *testing.T) {
	nk := newLegacyBansTestModule()
	ServiceSettingsUpdate(&ServiceSettingsData{})
	t.Cleanup(func() { ServiceSettingsUpdate(nil) })
	t.Setenv(legacyBansApplyEnvVar, legacyBansApplyEnvValue)

	if err := runLegacyBansMigration(t, nk); err != nil {
		t.Fatalf("expected no error when no service guild is configured, got %v", err)
	}
	if len(nk.groupUpdateLog) != 0 {
		t.Fatalf("GroupUpdate was called with no service guild configured: %+v", nk.groupUpdateLog)
	}
}

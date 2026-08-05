package server

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
)

// seedJournalAndReadTwice stores an empty journal for userID and returns two
// independently-read handles on it, standing in for two moderators who both
// loaded the journal before either of them wrote.
func seedJournalAndReadTwice(t *testing.T, ctx context.Context, nk *storableRaceNK, userID string) (a, b *GuildEnforcementJournal) {
	t.Helper()
	nk.set(userID, StorageCollectionEnforcementJournal, StorageKeyEnforcementJournal, mustStorableJSON(t, NewGuildEnforcementJournal(userID)))

	a = NewGuildEnforcementJournal(userID)
	if err := StorableRead(ctx, nk, userID, a, false); err != nil {
		t.Fatalf("read journal A: %v", err)
	}
	b = NewGuildEnforcementJournal(userID)
	if err := StorableRead(ctx, nk, userID, b, false); err != nil {
		t.Fatalf("read journal B: %v", err)
	}
	return a, b
}

// TestSyncJournalAndProfileWithRetry_PreservesConcurrentRecord is the headline
// case: moderator A commits a ban, then moderator B commits from a copy that
// predates it. B's write conflicts and is retried — and the retry must NOT
// write B's stale journal over A's record.
func TestSyncJournalAndProfileWithRetry_PreservesConcurrentRecord(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV4()).String()
	nk := newStorableRaceNK()

	journalA, journalB := seedJournalAndReadTwice(t, ctx, nk, userID)

	// Moderator A lands first, advancing the stored version.
	journalA.AddRecord("group-A", "mod-A", "", "A ban", "A notes", false, false, time.Hour)
	if err := StorableWrite(ctx, nk, userID, journalA); err != nil {
		t.Fatalf("write journal A: %v", err)
	}

	// Moderator B now writes from the stale copy: conflict, then retry.
	journalB.AddRecord("group-B", "mod-B", "", "B ban", "B notes", false, false, time.Hour)
	if err := SyncJournalAndProfileWithRetry(ctx, nk, userID, journalB); err != nil {
		t.Fatalf("SyncJournalAndProfileWithRetry: %v", err)
	}

	final := NewGuildEnforcementJournal(userID)
	if err := StorableRead(ctx, nk, userID, final, false); err != nil {
		t.Fatalf("final read: %v", err)
	}
	if len(final.RecordsByGroupID["group-A"]) != 1 {
		t.Errorf("moderator A's record was destroyed by the retry; records = %+v", final.RecordsByGroupID)
	}
	if len(final.RecordsByGroupID["group-B"]) != 1 {
		t.Errorf("moderator B's record was lost; records = %+v", final.RecordsByGroupID)
	}

	// The user-facing suspension profile must list both.
	profile := NewSuspensionProfile(userID)
	if err := StorableRead(ctx, nk, userID, profile, false); err != nil {
		t.Fatalf("read suspension profile: %v", err)
	}
	if len(profile.Suspensions) != 2 {
		t.Errorf("suspension profile out of sync with the merged journal: %d suspensions, want 2", len(profile.Suspensions))
	}
}

// TestSyncJournalAndProfileWithRetry_PreservesConcurrentRecordSameGroup is the
// same race confined to one guild group, where both records land in the same
// append-structured slice.
func TestSyncJournalAndProfileWithRetry_PreservesConcurrentRecordSameGroup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV4()).String()
	nk := newStorableRaceNK()

	journalA, journalB := seedJournalAndReadTwice(t, ctx, nk, userID)

	journalA.AddRecord("group-1", "mod-A", "", "A ban", "A notes", false, false, time.Hour)
	if err := StorableWrite(ctx, nk, userID, journalA); err != nil {
		t.Fatalf("write journal A: %v", err)
	}

	journalB.AddRecord("group-1", "mod-B", "", "B ban", "B notes", false, false, time.Hour)
	if err := SyncJournalAndProfileWithRetry(ctx, nk, userID, journalB); err != nil {
		t.Fatalf("SyncJournalAndProfileWithRetry: %v", err)
	}

	final := NewGuildEnforcementJournal(userID)
	if err := StorableRead(ctx, nk, userID, final, false); err != nil {
		t.Fatalf("final read: %v", err)
	}
	notices := make([]string, 0, 2)
	for _, r := range final.RecordsByGroupID["group-1"] {
		notices = append(notices, r.UserNoticeText)
	}
	if len(notices) != 2 {
		t.Fatalf("expected both records in group-1, got %v", notices)
	}
}

// TestSyncJournalAndProfileWithRetry_PreservesConcurrentVoid covers the other
// append-structured mutation: a void recorded by the concurrent winner must
// survive the retrier's write.
func TestSyncJournalAndProfileWithRetry_PreservesConcurrentVoid(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV4()).String()
	nk := newStorableRaceNK()

	// Start from a journal that already holds a record, so it can be voided.
	seed := NewGuildEnforcementJournal(userID)
	existing := seed.AddRecord("group-1", "mod-0", "", "original", "notes", false, false, time.Hour)
	nk.set(userID, StorageCollectionEnforcementJournal, StorageKeyEnforcementJournal, mustStorableJSON(t, seed))

	journalA := NewGuildEnforcementJournal(userID)
	if err := StorableRead(ctx, nk, userID, journalA, false); err != nil {
		t.Fatalf("read journal A: %v", err)
	}
	journalB := NewGuildEnforcementJournal(userID)
	if err := StorableRead(ctx, nk, userID, journalB, false); err != nil {
		t.Fatalf("read journal B: %v", err)
	}

	// A voids the existing record and lands first.
	journalA.VoidRecord("group-1", existing.ID, "mod-A", "", "voided by A")
	if err := StorableWrite(ctx, nk, userID, journalA); err != nil {
		t.Fatalf("write journal A: %v", err)
	}

	// B adds a new record from the stale copy.
	journalB.AddRecord("group-1", "mod-B", "", "B ban", "B notes", false, false, time.Hour)
	if err := SyncJournalAndProfileWithRetry(ctx, nk, userID, journalB); err != nil {
		t.Fatalf("SyncJournalAndProfileWithRetry: %v", err)
	}

	final := NewGuildEnforcementJournal(userID)
	if err := StorableRead(ctx, nk, userID, final, false); err != nil {
		t.Fatalf("final read: %v", err)
	}
	if !final.IsVoid("group-1", existing.ID) {
		t.Errorf("moderator A's void was destroyed by the retry; voids = %+v", final.VoidsByRecordIDByGroupID)
	}
	if len(final.RecordsByGroupID["group-1"]) != 2 {
		t.Errorf("expected the original plus B's record, got %+v", final.RecordsByGroupID["group-1"])
	}
}

// TestSyncJournalAndProfileWithRetry_KeepsLocalEditOfSharedRecord covers the
// non-append mutation: when both copies contain the same record and only the
// retrier edited it, the retrier's newer edit must survive the merge.
func TestSyncJournalAndProfileWithRetry_KeepsLocalEditOfSharedRecord(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV4()).String()
	nk := newStorableRaceNK()

	seed := NewGuildEnforcementJournal(userID)
	existing := seed.AddRecord("group-1", "mod-0", "", "original", "notes", false, false, time.Hour)
	nk.set(userID, StorageCollectionEnforcementJournal, StorageKeyEnforcementJournal, mustStorableJSON(t, seed))

	journalA := NewGuildEnforcementJournal(userID)
	if err := StorableRead(ctx, nk, userID, journalA, false); err != nil {
		t.Fatalf("read journal A: %v", err)
	}
	journalB := NewGuildEnforcementJournal(userID)
	if err := StorableRead(ctx, nk, userID, journalB, false); err != nil {
		t.Fatalf("read journal B: %v", err)
	}

	// A adds an unrelated record and lands first.
	journalA.AddRecord("group-2", "mod-A", "", "A ban", "A notes", false, false, time.Hour)
	if err := StorableWrite(ctx, nk, userID, journalA); err != nil {
		t.Fatalf("write journal A: %v", err)
	}

	// B edits the shared record from the stale copy.
	if rec := journalB.EditRecord("group-1", existing.ID, "mod-B", "", existing.Expiry, "edited notice", "edited notes", false); rec == nil {
		t.Fatal("EditRecord returned nil")
	}
	if err := SyncJournalAndProfileWithRetry(ctx, nk, userID, journalB); err != nil {
		t.Fatalf("SyncJournalAndProfileWithRetry: %v", err)
	}

	final := NewGuildEnforcementJournal(userID)
	if err := StorableRead(ctx, nk, userID, final, false); err != nil {
		t.Fatalf("final read: %v", err)
	}
	rec := final.GetRecord("group-1", existing.ID)
	if rec == nil {
		t.Fatal("the edited record vanished")
	}
	if rec.UserNoticeText != "edited notice" {
		t.Errorf("the retrier's edit was discarded by the merge: %q", rec.UserNoticeText)
	}
	if len(final.RecordsByGroupID["group-2"]) != 1 {
		t.Errorf("moderator A's record was destroyed by the retry; records = %+v", final.RecordsByGroupID)
	}
}

// TestSyncJournalAndProfileWithRetry_AbortsWhenRereadFails proves that a failed
// post-conflict re-read stops the loop instead of re-writing the unchanged
// stale journal — which can only conflict again and burn every attempt.
func TestSyncJournalAndProfileWithRetry_AbortsWhenRereadFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV4()).String()
	nk := newStorableRaceNK()

	journalA, journalB := seedJournalAndReadTwice(t, ctx, nk, userID)

	journalA.AddRecord("group-A", "mod-A", "", "A ban", "A notes", false, false, time.Hour)
	if err := StorableWrite(ctx, nk, userID, journalA); err != nil {
		t.Fatalf("write journal A: %v", err)
	}
	storedBefore := nk.get(userID, StorageCollectionEnforcementJournal, StorageKeyEnforcementJournal).Value

	// Every read from here on fails.
	readsSoFar, writesSoFar, _ := nk.counts()
	boom := errors.New("storage unavailable")
	nk.readErrFrom = readsSoFar + 1
	nk.readErr = boom

	journalB.AddRecord("group-B", "mod-B", "", "B ban", "B notes", false, false, time.Hour)
	err := SyncJournalAndProfileWithRetry(ctx, nk, userID, journalB)
	if err == nil {
		t.Fatal("expected an error when the post-conflict re-read fails, got nil")
	}
	if !errors.Is(err, boom) {
		t.Errorf("the re-read failure should be reported to the caller; got %v", err)
	}

	_, writesAfter, _ := nk.counts()
	if attempts := writesAfter - writesSoFar; attempts != 1 {
		t.Errorf("stale journal was re-written after the re-read failed: %d write attempts, want 1", attempts)
	}
	storedAfter := nk.get(userID, StorageCollectionEnforcementJournal, StorageKeyEnforcementJournal).Value
	if storedAfter != storedBefore {
		t.Errorf("stored journal changed despite the aborted sync:\n before: %s\n  after: %s", storedBefore, storedAfter)
	}
	if !strings.Contains(storedAfter, "A ban") {
		t.Errorf("moderator A's record was lost; stored = %s", storedAfter)
	}
}

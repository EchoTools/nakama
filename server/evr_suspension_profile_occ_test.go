package server

import (
	"context"
	"errors"
	"slices"
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

// TestSyncJournalAndProfileWithRetry_NotFoundKeepsPendingCommunityValues covers
// the merge base used when the journal object is absent on the post-conflict
// re-read.
//
// A requirement is "pending" when the newest CommunityValuesRequired record was
// created AFTER CommunityValuesCompletedAt; updateFields() then zeroes the
// timestamp at marshal time (evr_enforcement_journal.go updateFields).
//
// NewGuildEnforcementJournal seeds CommunityValuesCompletedAt with time.Now(),
// so using a constructor-fresh journal as the merge base hands mergeStored a
// completion timestamp later than any record the caller just added. mergeStored
// keeps the later one, updateFields no longer sees a record newer than it, and
// the requirement is silently satisfied without the player ever re-accepting.
func TestSyncJournalAndProfileWithRetry_NotFoundKeepsPendingCommunityValues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV4()).String()
	nk := newStorableRaceNK()

	// Seed a journal whose last community-values acceptance is an hour old, so
	// a record added now is unambiguously newer than it.
	seed := NewGuildEnforcementJournal(userID)
	seed.CommunityValuesCompletedAt = time.Now().UTC().Add(-time.Hour)
	nk.set(userID, StorageCollectionEnforcementJournal, StorageKeyEnforcementJournal, mustStorableJSON(t, seed))

	journalA := NewGuildEnforcementJournal(userID)
	if err := StorableRead(ctx, nk, userID, journalA, false); err != nil {
		t.Fatalf("read journal A: %v", err)
	}
	journalB := NewGuildEnforcementJournal(userID)
	if err := StorableRead(ctx, nk, userID, journalB, false); err != nil {
		t.Fatalf("read journal B: %v", err)
	}

	// A lands first so B's write will conflict.
	journalA.AddRecord("group-A", "mod-A", "", "A ban", "A notes", false, false, time.Hour)
	if err := StorableWrite(ctx, nk, userID, journalA); err != nil {
		t.Fatalf("write journal A: %v", err)
	}

	// B records a ban that requires the player to re-accept community values.
	rec := journalB.AddRecord("group-1", "mod-B", "", "B ban", "B notes", true, false, time.Hour)
	if !rec.CreatedAt.After(journalB.CommunityValuesCompletedAt) {
		t.Fatalf("precondition: the new record must post-date the last acceptance (%v vs %v)", rec.CreatedAt, journalB.CommunityValuesCompletedAt)
	}

	// The journal object is removed before B's retry re-reads it, so the retry
	// takes the NotFound branch.
	nk.afterWrite = onceHook(func(m *storableRaceNK) {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.objects, storableRaceKey(userID, StorageCollectionEnforcementJournal, StorageKeyEnforcementJournal))
	})

	if err := SyncJournalAndProfileWithRetry(ctx, nk, userID, journalB); err != nil {
		t.Fatalf("SyncJournalAndProfileWithRetry: %v", err)
	}

	final := NewGuildEnforcementJournal(userID)
	if err := StorableRead(ctx, nk, userID, final, false); err != nil {
		t.Fatalf("final read: %v", err)
	}
	if len(final.RecordsByGroupID["group-1"]) != 1 {
		t.Fatalf("precondition: B's record must have been written; records = %+v", final.RecordsByGroupID)
	}
	if !final.CommunityValuesCompletedAt.IsZero() {
		t.Errorf("the pending community-values requirement was cleared by the merge: CommunityValuesCompletedAt = %v, want the zero time", final.CommunityValuesCompletedAt)
	}
}

// seedPendingCommunityValuesJournal stores a journal for userID that holds one
// CommunityValuesRequired record and a PENDING requirement — the zero
// CommunityValuesCompletedAt that updateFields() writes when the newest
// requiring record post-dates the last acceptance. It returns the record.
func seedPendingCommunityValuesJournal(t *testing.T, nk *storableRaceNK, userID, groupID string) GuildEnforcementRecord {
	t.Helper()
	seed := NewGuildEnforcementJournal(userID)
	seed.CommunityValuesCompletedAt = time.Now().UTC().Add(-time.Hour)
	rec := seed.AddRecord(groupID, "mod-0", "", "accept the rules", "notes", true, false, time.Hour)
	stored := mustStorableJSON(t, seed)
	if !seed.CommunityValuesCompletedAt.IsZero() {
		t.Fatalf("precondition: marshalling must leave the requirement pending, got %v", seed.CommunityValuesCompletedAt)
	}
	nk.set(userID, StorageCollectionEnforcementJournal, StorageKeyEnforcementJournal, stored)
	return rec
}

// TestSyncJournalAndProfileWithRetry_FoundKeepsPendingCommunityValues is the
// found-branch twin of the NotFound case above, and it is the one two production
// callers actually take.
//
// writeGuildBanEnforcement (evr_discord_integrator.go) and
// applyGhostSpamSuspension (evr_pipeline_login.go) both continue with a
// CONSTRUCTOR-FRESH journal when their StorableRead fails transiently — and
// NewGuildEnforcementJournal seeds CommunityValuesCompletedAt with time.Now().
// Resolving that field by "later wins" then lets the fabricated "now" beat the
// stored ZERO that encodes a pending requirement, and updateFields() no longer
// sees a record newer than the completion, so the requirement is silently
// satisfied and the player is never gated (evr_pipeline_login.go IsZero checks).
func TestSyncJournalAndProfileWithRetry_FoundKeepsPendingCommunityValues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV4()).String()
	nk := newStorableRaceNK()

	rec := seedPendingCommunityValuesJournal(t, nk, userID, "group-2")

	// The caller never managed to read the journal, exactly as the two
	// production callers do when StorableRead fails for any non-NotFound reason.
	journal := NewGuildEnforcementJournal(userID)
	if journal.CommunityValuesCompletedAt.IsZero() {
		t.Fatal("precondition: a constructor-fresh journal must carry a non-zero completion")
	}
	journal.AddRecord("group-1", "mod-B", "", "guild ban", "banned", false, false, time.Hour)

	// version "*" against an existing object: the first write conflicts and the
	// retry merges against the stored journal.
	if err := SyncJournalAndProfileWithRetry(ctx, nk, userID, journal); err != nil {
		t.Fatalf("SyncJournalAndProfileWithRetry: %v", err)
	}

	final := NewGuildEnforcementJournal(userID)
	if err := StorableRead(ctx, nk, userID, final, false); err != nil {
		t.Fatalf("final read: %v", err)
	}
	if final.GetRecord("group-2", rec.ID) == nil {
		t.Errorf("the stored community-values record was destroyed by the retry; records = %+v", final.RecordsByGroupID)
	}
	if len(final.RecordsByGroupID["group-1"]) != 1 {
		t.Errorf("the caller's own record was lost; records = %+v", final.RecordsByGroupID)
	}
	if !final.CommunityValuesCompletedAt.IsZero() {
		t.Errorf("a pending community-values requirement was cleared by mergeStored: CommunityValuesCompletedAt = %v, want the zero time", final.CommunityValuesCompletedAt)
	}
}

// TestGuildEnforcementJournal_mergeStored_CommunityValues pins the merge rule
// for CommunityValuesCompletedAt in both directions. stored was re-read AFTER
// the conflict, so it is the newest persisted value of a field this merge path
// never legitimately mutates; the caller's copy is either an older snapshot or a
// constructor default. stored therefore wins outright, and updateFields()
// re-derives the gate from the merged record set at marshal time.
func TestGuildEnforcementJournal_mergeStored_CommunityValues(t *testing.T) {
	t.Parallel()
	userID := uuid.Must(uuid.NewV4()).String()
	accepted := time.Now().UTC().Add(-time.Hour)

	t.Run("stored pending beats a constructor-fresh completion", func(t *testing.T) {
		local := NewGuildEnforcementJournal(userID) // CommunityValuesCompletedAt = now
		stored := NewGuildEnforcementJournal(userID)
		stored.CommunityValuesCompletedAt = time.Time{}

		local.mergeStored(stored)

		if !local.CommunityValuesCompletedAt.IsZero() {
			t.Errorf("pending requirement lost: got %v, want the zero time", local.CommunityValuesCompletedAt)
		}
	})

	t.Run("stored acceptance beats a locally pending snapshot", func(t *testing.T) {
		// The player accepted concurrently, so the caller's older "pending"
		// snapshot must not re-gate them.
		local := NewGuildEnforcementJournal(userID)
		local.CommunityValuesCompletedAt = time.Time{}
		stored := NewGuildEnforcementJournal(userID)
		stored.CommunityValuesCompletedAt = accepted

		local.mergeStored(stored)

		if !local.CommunityValuesCompletedAt.Equal(accepted) {
			t.Errorf("the concurrent acceptance was discarded: got %v, want %v", local.CommunityValuesCompletedAt, accepted)
		}
	})

	t.Run("a locally added requiring record still re-gates after adoption", func(t *testing.T) {
		local := NewGuildEnforcementJournal(userID)
		local.AddRecord("group-1", "mod-B", "", "accept the rules", "notes", true, false, time.Hour)
		stored := NewGuildEnforcementJournal(userID)
		stored.CommunityValuesCompletedAt = accepted

		local.mergeStored(stored)
		if !local.CommunityValuesCompletedAt.Equal(accepted) {
			t.Fatalf("merge should adopt the stored acceptance first: got %v", local.CommunityValuesCompletedAt)
		}
		// updateFields runs from MarshalJSON, which is what StorableWrite calls.
		if _, err := local.MarshalJSON(); err != nil {
			t.Fatalf("MarshalJSON: %v", err)
		}
		if !local.CommunityValuesCompletedAt.IsZero() {
			t.Errorf("the newly added requirement did not re-gate the player: got %v, want the zero time", local.CommunityValuesCompletedAt)
		}
	})
}

// TestGuildEnforcementJournal_mergeStored_StoredEditWins pins the reverse merge
// direction of the same-record case: when the concurrent winner holds the newer
// edit of a record both copies carry, the winner's version survives.
func TestGuildEnforcementJournal_mergeStored_StoredEditWins(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV4()).String()
	base := NewGuildEnforcementJournal(userID)
	rec := base.AddRecord("group-1", "mod-0", "", "original", "notes", false, false, time.Hour)

	local := NewGuildEnforcementJournal(userID)
	local.RecordsByGroupID = map[string][]GuildEnforcementRecord{"group-1": {*base.GetRecord("group-1", rec.ID)}}

	stored := NewGuildEnforcementJournal(userID)
	stored.RecordsByGroupID = map[string][]GuildEnforcementRecord{"group-1": {*base.GetRecord("group-1", rec.ID)}}
	if edited := stored.EditRecord("group-1", rec.ID, "mod-A", "", rec.Expiry, "stored edit", "stored notes", false); edited == nil {
		t.Fatal("EditRecord returned nil")
	}

	local.mergeStored(stored)

	merged := local.GetRecord("group-1", rec.ID)
	if merged == nil {
		t.Fatal("the record vanished from the merge")
	}
	if merged.UserNoticeText != "stored edit" {
		t.Errorf("the concurrent winner's newer edit was discarded: %q", merged.UserNoticeText)
	}
}

// TestGuildEnforcementJournal_mergeStored_UnionsEditLogs covers the audit-trail
// consequence of whole-record last-writer-wins: two moderators editing the same
// record from copies read before either wrote. Only one moderator's field values
// can survive, but the EditLog is append-only history and BOTH entries must
// remain — otherwise the losing moderator's edit leaves no trace anywhere.
func TestGuildEnforcementJournal_mergeStored_UnionsEditLogs(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV4()).String()
	base := NewGuildEnforcementJournal(userID)
	rec := base.AddRecord("group-1", "mod-0", "", "original", "notes", false, false, time.Hour)
	original := *base.GetRecord("group-1", rec.ID)

	// Moderator A's edit lands first and is what the retrier re-reads.
	stored := NewGuildEnforcementJournal(userID)
	stored.RecordsByGroupID = map[string][]GuildEnforcementRecord{"group-1": {original}}
	if edited := stored.EditRecord("group-1", rec.ID, "mod-A", "", rec.Expiry, "A notice", "A notes", false); edited == nil {
		t.Fatal("EditRecord(A) returned nil")
	}

	// Moderator B edits the same record from the pre-A copy, then retries.
	local := NewGuildEnforcementJournal(userID)
	local.RecordsByGroupID = map[string][]GuildEnforcementRecord{"group-1": {original}}
	if edited := local.EditRecord("group-1", rec.ID, "mod-B", "", rec.Expiry, "B notice", "B notes", false); edited == nil {
		t.Fatal("EditRecord(B) returned nil")
	}

	local.mergeStored(stored)

	merged := local.GetRecord("group-1", rec.ID)
	if merged == nil {
		t.Fatal("the record vanished from the merge")
	}
	editors := make([]string, 0, len(merged.EditLog))
	for _, e := range merged.EditLog {
		editors = append(editors, e.EditorUserID)
	}
	if len(merged.EditLog) != 2 {
		t.Fatalf("the losing moderator's audit entry was destroyed: EditLog editors = %v, want both mod-A and mod-B", editors)
	}
	if !slices.Contains(editors, "mod-A") || !slices.Contains(editors, "mod-B") {
		t.Errorf("EditLog editors = %v, want both mod-A and mod-B", editors)
	}
	for i := 1; i < len(merged.EditLog); i++ {
		if merged.EditLog[i].EditedAt.Before(merged.EditLog[i-1].EditedAt) {
			t.Errorf("EditLog is not ordered by EditedAt: %v", merged.EditLog)
		}
	}

	// Re-merging the same pair must not duplicate entries.
	local.mergeStored(stored)
	if got := len(local.GetRecord("group-1", rec.ID).EditLog); got != 2 {
		t.Errorf("re-merging duplicated edit entries: %d, want 2", got)
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

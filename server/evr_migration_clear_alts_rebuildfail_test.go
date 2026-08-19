package server

import (
	"errors"
	"testing"
)

// TestClearAltsMigration_RebuildFailureLeavesStoredLinksIntact is P1-2.
//
// The migration clears AlternateMatches in memory BEFORE calling
// UpdateAlternates, because UpdateAlternates returns early on a zero-match
// search and would otherwise leave the stale map in place. That ordering is
// correct. What is not correct is what happens when the rebuild fails: the
// cleared, now-empty map is marshalled and written anyway, so a transient I/O
// failure is persisted as a successful clear. A genuine disabled-alt link is
// erased and alt-based enforcement goes blind to it.
//
// A failed rebuild and a successful one must not produce the same stored
// result (RULINGS.md:4256-4268, move 4 -- FAIL LOUD).
func TestClearAltsMigration_RebuildFailureLeavesStoredLinksIntact(t *testing.T) {
	nk := newAltClearTestModule()
	nk.indexErr = errors.New("error listing alt index: context deadline exceeded")
	nk.seedLinkedAccount(t, "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222")

	logger := runAltClearMigration(t, nk)

	stored := nk.storedHistory(t, "11111111-1111-1111-1111-111111111111")
	if len(stored.AlternateMatches) == 0 {
		t.Errorf("rebuild failed, yet the cleared empty AlternateMatches was persisted: the genuine link to 2222... is gone. Stored state must survive a rebuild failure intact.")
	}
	if len(stored.SecondDegreeAlternates) == 0 {
		t.Errorf("rebuild failed, yet the cleared empty SecondDegreeAlternates was persisted")
	}

	// FAIL LOUD: the failure must be visible in the migration's own numbers,
	// not only in a warning line that scrolls past.
	if got := completionField(t, logger, "rebuild_failed"); got != 1 {
		t.Errorf("rebuild_failed = %d, want 1: a rebuild failure must be counted, not absorbed", got)
	}
	if got := completionField(t, logger, "rebuilt"); got != 0 {
		t.Errorf("rebuilt = %d, want 0: nothing was rebuilt, the rebuild failed", got)
	}
}

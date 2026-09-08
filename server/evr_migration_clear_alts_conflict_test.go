package server

import (
	"testing"
)

// TestClearAltsMigration_ConflictDoesNotDropInnocentsOrOvercount is P1-3.
//
// One racing login invalidates one row's version. Production rolls the whole
// transaction back, so none of the batch commits, and it does not retry. The
// migration nevertheless adds len(writes) to rebuilt and advances the cursor,
// so it reports work it did not do and never revisits the accounts that were
// never in conflict.
//
// Two things must hold: the count must discriminate "wrote 100" from "wrote 0"
// (RULINGS.md:4256-4268, move 1 -- DISCRIMINATE), and the non-conflicting
// accounts must not be silently skipped.
func TestClearAltsMigration_ConflictDoesNotDropInnocentsOrOvercount(t *testing.T) {
	const (
		racer      = "33333333-3333-3333-3333-333333333333"
		innocentA  = "11111111-1111-1111-1111-111111111111"
		innocentB  = "22222222-2222-2222-2222-222222222222"
		linkTarget = "99999999-9999-9999-9999-999999999999"
	)

	nk := newAltClearTestModule()
	nk.seedLinkedAccount(t, innocentA, linkTarget)
	nk.seedLinkedAccount(t, racer, linkTarget)
	nk.seedLinkedAccount(t, innocentB, linkTarget)
	nk.conflictUserIDs[racer] = true

	logger := runAltClearMigration(t, nk)

	// The 2 accounts that never raced must have been written.
	for _, userID := range []string{innocentA, innocentB} {
		stored := nk.storedHistory(t, userID)
		if len(stored.AlternateMatches) != 0 || len(stored.SecondDegreeAlternates) != 0 {
			t.Errorf("account %s never conflicted, yet its stale links survive (%v / %v): one racing row must not drop the rest of the page",
				userID, stored.AlternateMatches, stored.SecondDegreeAlternates)
		}
	}

	// The racing account must be untouched -- its version moved on, and a
	// later login rebuilds it.
	racerStored := nk.storedHistory(t, racer)
	if len(racerStored.AlternateMatches) == 0 {
		t.Errorf("the conflicting row was overwritten; an OCC rejection must not be forced through")
	}

	// The count must not claim the row that was rejected.
	if got := completionField(t, logger, "rebuilt"); got != 2 {
		t.Errorf("rebuilt = %d, want 2: the migration counted rows a rolled-back transaction never committed", got)
	}
	if got := completionField(t, logger, "conflicted"); got != 1 {
		t.Errorf("conflicted = %d, want 1: the rows left behind must be reported, not silently dropped", got)
	}
}

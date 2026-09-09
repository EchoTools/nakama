package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
)

// listCountingModule counts StorageList calls so "skipped" can be asserted as
// "did not walk storage" rather than as "produced no writes". Those are not the
// same claim: a migration that lists all 45,967 rows and decides to write none
// of them has still paid for the walk, on every boot, forever.
type listCountingModule struct {
	*altClearTestModule
	listCalls int
}

func (m *listCountingModule) StorageList(ctx context.Context, callerID, userID, collection string, limit int, cursor string) ([]*api.StorageObject, string, error) {
	m.listCalls++
	return m.altClearTestModule.StorageList(ctx, callerID, userID, collection, limit, cursor)
}

// storedMarker reads back the completion marker the migration recorded, or nil.
func storedMarker(t *testing.T, m *altClearTestModule) *migrationCompletionMarker {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[occStorageKey(SystemUserID, MigrationStateStorageCollection, MigrationClearAltsStateKey)]
	if !ok {
		return nil
	}
	marker := &migrationCompletionMarker{}
	if err := json.Unmarshal([]byte(obj.Value), marker); err != nil {
		t.Fatalf("stored marker is not valid JSON (%q): %v", obj.Value, err)
	}
	return marker
}

// clearMigrationMarker is the operator action the marker design promises:
// delete the storage object and the migration is owed again. Modelled here as
// a direct delete because that is what an operator does through the storage
// API -- no code change, no redeploy.
func clearMigrationMarker(t *testing.T, m *altClearTestModule) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, occStorageKey(SystemUserID, MigrationStateStorageCollection, MigrationClearAltsStateKey))
}

// TestClearAltsMigration_MarkerAbsentRunsAndRecordsIt covers the first boot.
//
// With no marker the migration must do its work, and on success it must leave
// behind a marker that names where it lives -- that record is the only thing
// standing between this and a full-table walk on every process start.
func TestClearAltsMigration_MarkerAbsentRunsAndRecordsIt(t *testing.T) {
	nk := newAltClearTestModule()
	nk.seedLinkedAccount(t, "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222")

	logger := runAltClearMigration(t, nk)

	if _, ok := logger.find("info", "alt-clear migration: no completion marker found, starting a fresh run"); !ok {
		t.Error("a fresh run was not announced; an operator cannot tell a run from a skip")
	}
	if got := completionField(t, logger, "walked"); got != 1 {
		t.Errorf("walked = %d, want 1: the migration did not run", got)
	}

	marker := storedMarker(t, nk)
	if marker == nil {
		t.Fatal("no completion marker was recorded after a successful run; the next boot will walk the whole table again")
	}
	if marker.CompletedAt.IsZero() {
		t.Error("marker.CompletedAt is zero; the marker must say when the run finished")
	}
	if marker.Migration == "" {
		t.Error("marker.Migration is empty; the marker must name what it is a marker for")
	}
}

// TestClearAltsMigration_MarkerPresentSkipsWithoutWalking covers every boot
// after the first.
//
// The requirement is not "writes nothing", it is "does not walk". A skip that
// still lists 45,967 rows before deciding it has nothing to do has not removed
// the per-boot cost the marker exists to remove.
func TestClearAltsMigration_MarkerPresentSkipsWithoutWalking(t *testing.T) {
	base := newAltClearTestModule()
	base.seedLinkedAccount(t, "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222")
	nk := &listCountingModule{altClearTestModule: base}

	// First boot: runs, and records the marker.
	if _, ok := runAltClearMigration(t, nk).find("info", "alt-clear migration: no completion marker found, starting a fresh run"); !ok {
		t.Fatal("the first run did not announce itself; the rest of this test would be vacuous")
	}
	if base.storedHistory(t, "11111111-1111-1111-1111-111111111111") == nil {
		t.Fatal("first run stored nothing")
	}
	if storedMarker(t, base) == nil {
		t.Fatal("first run left no marker; the skip under test cannot happen")
	}
	firstBootLists := nk.listCalls
	if firstBootLists == 0 {
		t.Fatal("the first run never listed storage; the counter is not wired to the walk")
	}

	// Second boot.
	logger := newCaptureLogger()
	if err := (&MigrationClearAlternateMatches{}).MigrateSystem(context.Background(), logger, nil, nk); err != nil {
		t.Fatalf("second boot returned an error: %v", err)
	}

	if nk.listCalls != firstBootLists {
		t.Errorf("StorageList calls went %d -> %d on the second boot; a completed migration must not walk storage at all",
			firstBootLists, nk.listCalls)
	}
	if _, ok := logger.find("info", "alt-clear migration: already complete, skipping; delete this storage object to force a re-run"); !ok {
		t.Error("the skip was not logged; an operator must be able to see why the migration did nothing")
	}
	if _, ok := logger.find("info", "alt-clear migration complete"); ok {
		t.Error("a skipped boot logged the completion line, which would read as a second full run")
	}
}

// TestClearAltsMigration_ErrorMidRunLeavesNoMarker is the one that matters most.
//
// A marker written after a partial run converts a transient failure into a
// permanent one: the migration is never attempted again, and whatever it left
// half-done stays half-done with nothing recording that fact. The marker is a
// claim of completion and may only be written by a run that completed.
func TestClearAltsMigration_ErrorMidRunLeavesNoMarker(t *testing.T) {
	nk := newAltClearTestModule()
	nk.listErr = errors.New("storage list: context deadline exceeded")
	nk.seedLinkedAccount(t, "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222")

	if _, err := runAltClearMigrationExpectingError(t, nk); err == nil {
		t.Fatal("the injected list failure did not surface")
	}

	if marker := storedMarker(t, nk); marker != nil {
		t.Errorf("a failed run recorded a completion marker (%+v); the migration will never be attempted again", marker)
	}
}

// TestClearAltsMigration_FloorAbortLeavesNoMarker is the same rule for the
// other abort path. The safety floor exists because the run was destroying
// everything it touched; recording that as "complete" would make it final.
func TestClearAltsMigration_FloorAbortLeavesNoMarker(t *testing.T) {
	const linkTarget = "99999999-9999-9999-9999-999999999999"

	nk := newAltClearTestModule()
	for i := 0; i < migrationClearAltsFloorMinLinks+20; i++ {
		nk.seedLinkedAccount(t, migrationTestUserID(i+1), linkTarget)
	}

	if _, err := runAltClearMigrationExpectingError(t, nk); err == nil {
		t.Fatal("the floor did not trip")
	}

	if marker := storedMarker(t, nk); marker != nil {
		t.Errorf("a floor-aborted run recorded a completion marker (%+v); the aborted work would never be retried", marker)
	}
}

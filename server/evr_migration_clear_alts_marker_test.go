package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	markerTestUserA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	markerTestUserB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	markerTestOther = "cccccccc-cccc-cccc-cccc-cccccccccccc"
)

func seedMarker(t *testing.T, nk *altClearTestModule, marker *migrationMarker) {
	t.Helper()
	data, err := json.Marshal(marker)
	if err != nil {
		t.Fatalf("marshal marker: %v", err)
	}
	nk.seedObject(SystemUserID, MigrationStateStorageCollection, MigrationClearAltsStateKey, string(data))
}

func storedMarker(t *testing.T, nk runtime.NakamaModule) *migrationMarker {
	t.Helper()
	marker, err := migrationMarkerRead(context.Background(), nk, MigrationClearAltsStateKey)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if marker == nil {
		t.Fatal("no marker stored")
	}
	return marker
}

func setListedUpdateTime(t *testing.T, nk *altClearTestModule, userID string, at time.Time) {
	t.Helper()
	for _, obj := range nk.listed {
		if obj.UserId == userID {
			obj.UpdateTime = timestamppb.New(at)
			return
		}
	}
	t.Fatalf("no listed row for %s", userID)
}

func loginWriteCount(nk *altClearTestModule) int {
	n := 0
	for _, batch := range nk.writeBatches {
		for _, userID := range batch {
			if userID != SystemUserID {
				n++
			}
		}
	}
	return n
}

func migrateSystem(t *testing.T, nk runtime.NakamaModule) (*captureLogger, error) {
	t.Helper()
	logger := newCaptureLogger()
	err := (&MigrationClearAlternateMatches{}).MigrateSystem(context.Background(), logger, nil, nk)
	return logger, err
}

// A fresh run records all three times and clears the stale link; a second boot
// does nothing.
func TestAltMigrationMarker_FreshRunCompletesAndSecondBootSkips(t *testing.T) {
	nk := newAltClearTestModule()
	nk.seedLinkedAccount(t, markerTestUserA, markerTestOther)

	if _, err := migrateSystem(t, nk); err != nil {
		t.Fatalf("first run: %v", err)
	}

	marker := storedMarker(t, nk)
	if marker.StartedAt.IsZero() || marker.PhaseTwoStartedAt.IsZero() || marker.CompletedAt.IsZero() {
		t.Fatalf("marker times not all recorded: %+v", marker)
	}
	if marker.PhaseTwoStartedAt.Before(marker.StartedAt) || marker.CompletedAt.Before(marker.PhaseTwoStartedAt) {
		t.Errorf("marker times out of order: %+v", marker)
	}
	if got := len(nk.storedHistory(t, markerTestUserA).AlternateMatches); got != 0 {
		t.Errorf("AlternateMatches has %d entries, want the stale link cleared", got)
	}

	writesAfterFirst := loginWriteCount(nk)
	logger, err := migrateSystem(t, nk)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if _, ok := logger.find("info", "alt-clear migration: already completed; delete the marker to run it again"); !ok {
		t.Error("second run did not log that the migration was already completed")
	}
	if got := loginWriteCount(nk); got != writesAfterFirst {
		t.Errorf("second run wrote %d login rows, want 0", got-writesAfterFirst)
	}
}

// A completed marker stops the run before any login row is touched.
func TestAltMigrationMarker_CompletedMarkerLeavesRowsAlone(t *testing.T) {
	nk := newAltClearTestModule()
	nk.seedLinkedAccount(t, markerTestUserA, markerTestOther)
	now := time.Now().UTC()
	seedMarker(t, nk, &migrationMarker{Migration: "MigrationClearAlternateMatches", StartedAt: now.Add(-2 * time.Hour), PhaseTwoStartedAt: now.Add(-time.Hour), CompletedAt: now})

	if _, err := migrateSystem(t, nk); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := loginWriteCount(nk); got != 0 {
		t.Errorf("wrote %d login rows, want 0", got)
	}
	if got := len(nk.storedHistory(t, markerTestUserA).AlternateMatches); got != 1 {
		t.Errorf("AlternateMatches has %d entries, want the stored link untouched", got)
	}
}

// Resuming in phase 2: a row updated after the phase 2 start is done and
// skipped; an older row is still processed; phase 1 does not run again.
func TestAltMigrationMarker_ResumeSkipsRowsUpdatedAfterPhaseTwoStart(t *testing.T) {
	nk := newAltClearTestModule()
	nk.seedLinkedAccount(t, markerTestUserA, markerTestOther)
	nk.seedLinkedAccount(t, markerTestUserB, markerTestOther)

	now := time.Now().UTC()
	phaseTwo := now.Add(-time.Hour)
	seedMarker(t, nk, &migrationMarker{Migration: "MigrationClearAlternateMatches", StartedAt: now.Add(-2 * time.Hour), PhaseTwoStartedAt: phaseTwo})
	setListedUpdateTime(t, nk, markerTestUserA, now)                           // done since phase 2 started
	setListedUpdateTime(t, nk, markerTestUserB, phaseTwo.Add(-30*time.Minute)) // not yet done

	logger, err := migrateSystem(t, nk)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if _, ok := logger.find("info", "alt-cache repair complete"); ok {
		t.Error("phase 1 ran again on a run that had already reached phase 2")
	}
	if got := len(nk.storedHistory(t, markerTestUserA).AlternateMatches); got != 1 {
		t.Errorf("user A: AlternateMatches has %d entries, want untouched (row was already done)", got)
	}
	if got := len(nk.storedHistory(t, markerTestUserB).AlternateMatches); got != 0 {
		t.Errorf("user B: AlternateMatches has %d entries, want the stale link cleared", got)
	}
	if got := completionField(t, logger, "skipped_done"); got != 1 {
		t.Errorf("skipped_done = %d, want 1", got)
	}
	if marker := storedMarker(t, nk); marker.CompletedAt.IsZero() {
		t.Error("resumed run did not record completion")
	}
}

// A row updated just after the phase start, inside the clock margin, is not
// treated as done.
func TestAltMigrationMarker_ClockMarginDoesNotSkip(t *testing.T) {
	nk := newAltClearTestModule()
	nk.seedLinkedAccount(t, markerTestUserA, markerTestOther)

	now := time.Now().UTC()
	phaseTwo := now.Add(-time.Hour)
	seedMarker(t, nk, &migrationMarker{Migration: "MigrationClearAlternateMatches", StartedAt: now.Add(-2 * time.Hour), PhaseTwoStartedAt: phaseTwo})
	setListedUpdateTime(t, nk, markerTestUserA, phaseTwo.Add(migrationResumeClockMargin-time.Minute))

	logger, err := migrateSystem(t, nk)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := completionField(t, logger, "skipped_done"); got != 0 {
		t.Errorf("skipped_done = %d, want 0 inside the clock margin", got)
	}
	if got := len(nk.storedHistory(t, markerTestUserA).AlternateMatches); got != 0 {
		t.Errorf("AlternateMatches has %d entries, want the stale link cleared", got)
	}
}

// Phase 1's own writes move update_time past StartedAt. On a resume that is
// still in phase 1, those rows skip the cache repair but must still get their
// links recomputed in phase 2.
func TestAltMigrationMarker_PhaseOneResumeStillRecomputesLinks(t *testing.T) {
	nk := newAltClearTestModule()
	nk.seedLinkedAccount(t, markerTestUserA, markerTestOther)

	now := time.Now().UTC()
	started := now.Add(-2 * time.Hour)
	seedMarker(t, nk, &migrationMarker{Migration: "MigrationClearAlternateMatches", StartedAt: started})
	setListedUpdateTime(t, nk, markerTestUserA, started.Add(time.Hour)) // written by phase 1 before the crash

	if _, err := migrateSystem(t, nk); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := len(nk.storedHistory(t, markerTestUserA).AlternateMatches); got != 0 {
		t.Errorf("AlternateMatches has %d entries, want links recomputed in phase 2", got)
	}
	marker := storedMarker(t, nk)
	if marker.PhaseTwoStartedAt.IsZero() || marker.CompletedAt.IsZero() {
		t.Errorf("marker not advanced: %+v", marker)
	}
}

type markerReadFailModule struct {
	*altClearTestModule
}

func (m *markerReadFailModule) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	return nil, errors.New("storage unavailable")
}

// If the marker cannot be read, the migration does not run.
func TestAltMigrationMarker_ReadErrorRefusesToRun(t *testing.T) {
	nk := newAltClearTestModule()
	nk.seedLinkedAccount(t, markerTestUserA, markerTestOther)

	if _, err := migrateSystem(t, &markerReadFailModule{altClearTestModule: nk}); err == nil {
		t.Fatal("want an error when the marker cannot be read")
	}
	if len(nk.writeBatches) != 0 {
		t.Errorf("wrote %d batches, want none", len(nk.writeBatches))
	}
}

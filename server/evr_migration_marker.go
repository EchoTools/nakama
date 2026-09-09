package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	// MigrationStateStorageCollection holds one record per one-shot system
	// migration, owned by SystemUserID.
	//
	// This is the shape the rest of the service already uses for system state:
	// ServiceSettingsLoad/Save read and write Global/settings under
	// SystemUserID with both permissions 0 (evr_global_settings.go:293-333),
	// and every other subsystem that persists non-player state gives itself a
	// collection of its own -- UnreachableServers, ServerBlacklist, Matchmaker.
	// Migration state gets the same treatment rather than a new mechanism: no
	// version table, no schema change, no migration-of-the-migration.
	//
	// One KEY per migration, rather than one record listing all of them, is
	// what makes the operator story work. Clearing a single migration's marker
	// is a single-object delete through the storage API -- no code change, no
	// redeploy, and no risk of taking an unrelated migration's state with it.
	MigrationStateStorageCollection = "MigrationState"

	// MigrationClearAltsStateKey is the completion marker for
	// MigrationClearAlternateMatches.
	MigrationClearAltsStateKey = "clear_alternate_matches"
)

// migrationCompletionMarker records that a one-shot system migration ran to
// completion. Its presence is the whole signal; the fields exist so that an
// operator reading the record can tell what happened without correlating logs
// that have long since rotated.
type migrationCompletionMarker struct {
	// Migration is the Go type name of the migration this marks.
	Migration string `json:"migration"`
	// CompletedAt is when the run finished, not when it started.
	CompletedAt time.Time `json:"completed_at"`
	// Summary is the run's own final counters, verbatim.
	Summary map[string]any `json:"summary,omitempty"`
}

// migrationMarkerRead returns the completion marker for key, or nil if the
// migration has not completed.
//
// A read FAILURE is returned as an error and must not be read as "not
// completed". The caller's response to "no marker" is to run a destructive
// full-table migration, so an unavailable storage layer has to stop the run
// rather than authorize one. Fail-closed, deliberately: AGENTS.md names
// fail-open-on-a-fail-closed-control as this repo's dominant anti-pattern, and
// "could not check, so I did it anyway" is exactly that shape.
func migrationMarkerRead(ctx context.Context, nk runtime.NakamaModule, key string) (*migrationCompletionMarker, error) {
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: MigrationStateStorageCollection,
		Key:        key,
		UserID:     SystemUserID,
	}})
	if err != nil {
		return nil, fmt.Errorf("read migration marker %s/%s: %w", MigrationStateStorageCollection, key, err)
	}
	if len(objs) == 0 {
		return nil, nil
	}

	marker := &migrationCompletionMarker{}
	if err := json.Unmarshal([]byte(objs[0].Value), marker); err != nil {
		// A marker that cannot be parsed is still a marker. Something wrote
		// this record, and the only thing that writes it is a completed run --
		// so treating a decode failure as "never ran" would re-run the
		// migration on the strength of a corrupt byte. Report the corruption
		// and let the caller refuse.
		return nil, fmt.Errorf("migration marker %s/%s is present but unreadable: %w", MigrationStateStorageCollection, key, err)
	}
	return marker, nil
}

// migrationMarkerWrite records completion. The write is unconditional (empty
// version): the marker is a fact about this process's run, and there is no
// concurrent writer whose version would be worth losing a race to.
func migrationMarkerWrite(ctx context.Context, nk runtime.NakamaModule, key string, marker *migrationCompletionMarker) error {
	data, err := json.Marshal(marker)
	if err != nil {
		return fmt.Errorf("marshal migration marker %s/%s: %w", MigrationStateStorageCollection, key, err)
	}
	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection: MigrationStateStorageCollection,
		Key:        key,
		UserID:     SystemUserID,
		Value:      string(data),
		// Server-only, like every other system record here. The marker is
		// operator state, not player data.
		PermissionRead:  0,
		PermissionWrite: 0,
	}}); err != nil {
		return fmt.Errorf("write migration marker %s/%s: %w", MigrationStateStorageCollection, key, err)
	}
	return nil
}

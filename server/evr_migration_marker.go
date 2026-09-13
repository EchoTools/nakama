package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	// MigrationStateStorageCollection holds one record per one-shot system
	// migration, owned by SystemUserID, following the same shape as other
	// system state (Global/settings under SystemUserID, permissions 0).
	//
	// One key per migration, so clearing one migration's marker is a single
	// storage delete and cannot touch another migration's state.
	MigrationStateStorageCollection = "MigrationState"

	// MigrationClearAltsStateKey is the marker for MigrationClearAlternateMatches.
	MigrationClearAltsStateKey = "clear_alternate_matches"

	// migrationResumeClockMargin widens the "already done" comparison. The marker
	// times come from the server clock; a storage row's update_time comes from
	// the database. A row only counts as done when it was updated more than this
	// margin after the marker time, so clock skew can cause a harmless re-check
	// but never a wrongly skipped row.
	migrationResumeClockMargin = 5 * time.Minute
)

// migrationMarker records the progress of a one-shot system migration.
//
//   - StartedAt is recorded before the first write of the run.
//   - PhaseTwoStartedAt is recorded when the first phase has finished.
//   - CompletedAt is recorded when the whole run has finished; once set, the
//     migration does not run again.
//
// A row whose update_time is after the relevant phase's start time (plus
// migrationResumeClockMargin) was written by that phase or by a login under
// the current code, and a resumed run skips it.
type migrationMarker struct {
	Migration         string         `json:"migration"`
	StartedAt         time.Time      `json:"started_at"`
	PhaseTwoStartedAt time.Time      `json:"phase_two_started_at,omitzero"`
	CompletedAt       time.Time      `json:"completed_at,omitzero"`
	Summary           map[string]any `json:"summary,omitempty"`
}

// migrationMarkerRead returns the marker for key, or nil if none is stored.
//
// A read failure is an error, never "no marker": the response to "no marker" is
// a full-table migration, so an unavailable storage layer must stop the run.
// A marker that is present but cannot be decoded is also an error.
func migrationMarkerRead(ctx context.Context, nk runtime.NakamaModule, key string) (*migrationMarker, error) {
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

	marker := &migrationMarker{}
	if err := json.Unmarshal([]byte(objs[0].Value), marker); err != nil {
		return nil, fmt.Errorf("migration marker %s/%s is present but unreadable: %w", MigrationStateStorageCollection, key, err)
	}
	return marker, nil
}

// migrationMarkerWrite stores the marker unconditionally (no version check):
// only the migration itself writes it.
func migrationMarkerWrite(ctx context.Context, nk runtime.NakamaModule, key string, marker *migrationMarker) error {
	data, err := json.Marshal(marker)
	if err != nil {
		return fmt.Errorf("marshal migration marker %s/%s: %w", MigrationStateStorageCollection, key, err)
	}
	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      MigrationStateStorageCollection,
		Key:             key,
		UserID:          SystemUserID,
		Value:           string(data),
		PermissionRead:  0,
		PermissionWrite: 0,
	}}); err != nil {
		return fmt.Errorf("write migration marker %s/%s: %w", MigrationStateStorageCollection, key, err)
	}
	return nil
}

// migrationRowDoneSince reports whether a storage row was updated after since
// (plus migrationResumeClockMargin). A zero since, or a row with no
// update_time, is never done.
func migrationRowDoneSince(obj *api.StorageObject, since time.Time) bool {
	if since.IsZero() || obj.GetUpdateTime() == nil {
		return false
	}
	return obj.GetUpdateTime().AsTime().After(since.Add(migrationResumeClockMargin))
}

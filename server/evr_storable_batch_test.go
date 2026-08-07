package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// rejectSecondWriteNakamaModule rejects a batch on a version conflict against
// the SECOND object in it, and reports it the way the real storage layer does:
// a bare runtime.ErrStorageRejectedVersion carrying no object identity at all.
//
// That fidelity is the point of the test. storageWriteObjects (core_storage.go)
// has the offending object in hand when it rejects, but returns the sentinel
// without it; StorageWriteObjects then wraps the sentinel and returns it
// unadorned. So a caller of nk.StorageWrite genuinely cannot learn which object
// of the batch was rejected.
type rejectSecondWriteNakamaModule struct {
	runtime.NakamaModule
	seen [][]*runtime.StorageWrite
}

func (m *rejectSecondWriteNakamaModule) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	m.seen = append(m.seen, writes)
	return nil, runtime.ErrStorageRejectedVersion
}

// MultiUpdate is the entry point StorableWriteMany actually calls; this path
// only ever carries storage writes, so it rejects the same way.
func (m *rejectSecondWriteNakamaModule) MultiUpdate(ctx context.Context, accountUpdates []*runtime.AccountUpdate, storageWrites []*runtime.StorageWrite, storageDeletes []*runtime.StorageDelete, walletUpdates []*runtime.WalletUpdate, updateLedger bool) ([]*api.StorageObjectAck, []*runtime.WalletUpdateResult, error) {
	acks, err := m.StorageWrite(ctx, storageWrites)
	return acks, nil, err
}

// TestStorableWriteMany_BatchErrorDoesNotBlameTheWrongObject pins what a failed
// batch write is allowed to claim.
//
// StorableWriteMany wrapped the failure with metas[0], so the message asserted a
// specific object — "storable error on <user>/EarlyQuit/statistics/..." — that
// the storage layer never named. In the batch the completion path actually
// writes (StorableWriteMany(ctx, nk, userID, eqconfig, history) in
// evr_match.go), metas[0] is always the early-quit state row, so a version
// conflict on the HISTORY row was reported against the state row. Under
// contention that sends whoever reads the log at the wrong record.
//
// The storage layer does not say which object it rejected, so neither may we:
// name every object in the batch. This is a reporting fix only — the error is
// still returned, unretried, for the caller to resolve ("errors are okay,
// retries are picking a winner in a race").
func TestStorableWriteMany_BatchErrorDoesNotBlameTheWrongObject(t *testing.T) {
	t.Parallel()

	nk := &rejectSecondWriteNakamaModule{}
	userID := occTestUserID

	state := NewEarlyQuitPlayerState()
	history := NewEarlyQuitHistory(userID)

	err := StorableWriteMany(context.Background(), nk, userID, state, history)
	if err == nil {
		t.Fatal("StorableWriteMany returned nil on a rejected batch write")
	}

	// Both objects must be identifiable from the message; the conflict could
	// have been on either and the storage layer did not tell us.
	for _, src := range []StorableAdapter{state, history} {
		meta := src.StorageMeta()
		if !strings.Contains(err.Error(), meta.Collection) || !strings.Contains(err.Error(), meta.Key) {
			t.Errorf("batch error does not identify %s/%s, so a conflict on it is reported against another object.\n"+
				"got: %v", meta.Collection, meta.Key, err)
		}
	}

	// The user and the underlying cause must survive the rewording.
	if !strings.Contains(err.Error(), userID) {
		t.Errorf("batch error dropped the user ID %s: %v", userID, err)
	}
	if !strings.Contains(err.Error(), runtime.ErrStorageRejectedVersion.Error()) {
		t.Errorf("batch error dropped the underlying cause %q: %v", runtime.ErrStorageRejectedVersion, err)
	}

	// The sentinel must survive in the CHAIN, not just in the text.
	// isVersionConflictError is errors.Is-based, so formatting the cause away
	// here (a %v where a %w belongs) would make every version conflict on this
	// path read as a hard failure and silently stop the retry loop in
	// SyncJournalAndProfileWithRetry.
	if !errors.Is(err, runtime.ErrStorageRejectedVersion) {
		t.Errorf("batch error broke the errors.Is chain to ErrStorageRejectedVersion, "+
			"so isVersionConflictError can no longer see a version conflict: %v", err)
	}
}

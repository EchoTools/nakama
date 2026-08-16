package server

import (
	"context"
	"errors"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/stretchr/testify/require"
)

// recordedCounter is one observed MetricsCounterAdd call.
type recordedCounter struct {
	name  string
	tags  map[string]string
	delta int64
}

// metricsRecorderNK is the one double in this package that does NOT no-op
// MetricsCounterAdd: it records every call so the tests below can assert that
// the profile write/read counters actually fire, with the tag values claimed,
// on the branch claimed. Every other double in server/ defines a no-op, which
// means a counter could be deleted from evr_account.go and every one of those
// tests would still pass. This file is what makes that deletion fail.
//
// It embeds a nil runtime.NakamaModule like the rest, so it panics on any
// method the code under test calls that is not defined here — a panic is the
// intended outcome, since a silently-stubbed call is how instrumentation goes
// unverified in the first place.
type metricsRecorderNK struct {
	runtime.NakamaModule
	account        *api.Account
	storageObjs    []*api.StorageObject
	multiUpdateErr error
	acks           []*api.StorageObjectAck
	counters       []recordedCounter
	storageWrites  int
}

func (m *metricsRecorderNK) AccountGetId(_ context.Context, userID string) (*api.Account, error) {
	return m.account, nil
}

func (m *metricsRecorderNK) StorageRead(_ context.Context, _ []*runtime.StorageRead) ([]*api.StorageObject, error) {
	return m.storageObjs, nil
}

func (m *metricsRecorderNK) MultiUpdate(_ context.Context, _ []*runtime.AccountUpdate, _ []*runtime.StorageWrite, _ []*runtime.StorageDelete, _ []*runtime.WalletUpdate, _ bool) ([]*api.StorageObjectAck, []*runtime.WalletUpdateResult, error) {
	if m.multiUpdateErr != nil {
		return nil, nil, m.multiUpdateErr
	}
	return m.acks, nil, nil
}

// StorageWrite records the attempt and reports success, so that
// EVRProfileLoad's metadata-fallback repairing write does not panic on this
// double's nil embedded runtime.NakamaModule. Nothing embeds metricsRecorderNK
// and the call arrives through the runtime.NakamaModule interface, so AGENTS.md
// defect class #1 does not apply — the same argument recorded in full at
// mockProfileLoadNakamaModule.StorageWrite in evr_account_test.go.
func (m *metricsRecorderNK) StorageWrite(_ context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	m.storageWrites += len(writes)
	acks := make([]*api.StorageObjectAck, 0, len(writes))
	for range writes {
		acks = append(acks, &api.StorageObjectAck{Version: "v-written"})
	}
	return acks, nil
}

func (m *metricsRecorderNK) MetricsCounterAdd(name string, tags map[string]string, delta int64) {
	copied := make(map[string]string, len(tags))
	for k, v := range tags {
		copied[k] = v
	}
	m.counters = append(m.counters, recordedCounter{name: name, tags: copied, delta: delta})
}

// total sums the deltas of every recorded call matching name and every tag in
// want. Returns the summed delta and the number of matching calls, so a test
// can distinguish "one call of 1" from "two calls of 1 and -1".
func (m *metricsRecorderNK) total(name string, want map[string]string) (int64, int) {
	var sum int64
	var calls int
	for _, c := range m.counters {
		if c.name != name {
			continue
		}
		matched := true
		for k, v := range want {
			if c.tags[k] != v {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		sum += c.delta
		calls++
	}
	return sum, calls
}

func (m *metricsRecorderNK) requireCounter(t *testing.T, name string, tags map[string]string, wantDelta int64, wantCalls int) {
	t.Helper()
	delta, calls := m.total(name, tags)
	require.Equalf(t, wantCalls, calls, "calls to %s%v; recorded: %+v", name, tags, m.counters)
	require.Equalf(t, wantDelta, delta, "summed delta of %s%v; recorded: %+v", name, tags, m.counters)
}

// TestEVRProfileUpdate_CommittedCounterFires pins the happy path: one
// successful write emits exactly one evr_profile_write_count{outcome=committed}
// and neither of the failure outcomes.
func TestEVRProfileUpdate_CommittedCounterFires(t *testing.T) {
	t.Parallel()
	nk := &metricsRecorderNK{acks: []*api.StorageObjectAck{{Version: "v2"}}}

	err := EVRProfileUpdate(context.Background(), nk, uuid.Must(uuid.NewV4()).String(), &EVRProfile{})
	require.NoError(t, err)

	nk.requireCounter(t, profileWriteCounter, map[string]string{"outcome": "committed"}, 1, 1)
	nk.requireCounter(t, profileWriteCounter, map[string]string{"outcome": "conflict"}, 0, 0)
	nk.requireCounter(t, profileWriteCounter, map[string]string{"outcome": "error"}, 0, 0)
}

// TestEVRProfileUpdate_ConflictCounterFires pins the branch the whole stage
// exists for: a version conflict must be counted as a conflict and not folded
// into the generic error bucket, because "writes are losing to OCC" and "writes
// are erroring" call for different responses.
func TestEVRProfileUpdate_ConflictCounterFires(t *testing.T) {
	t.Parallel()
	nk := &metricsRecorderNK{multiUpdateErr: runtime.ErrStorageRejectedVersion}

	err := EVRProfileUpdate(context.Background(), nk, uuid.Must(uuid.NewV4()).String(), &EVRProfile{})
	require.Error(t, err)

	nk.requireCounter(t, profileWriteCounter, map[string]string{"outcome": "conflict"}, 1, 1)
	nk.requireCounter(t, profileWriteCounter, map[string]string{"outcome": "committed"}, 0, 0)
	nk.requireCounter(t, profileWriteCounter, map[string]string{"outcome": "error"}, 0, 0)
}

// TestEVRProfileUpdate_ErrorCounterFires is the negative half of the pair: a
// non-OCC failure must NOT be reported as a conflict.
func TestEVRProfileUpdate_ErrorCounterFires(t *testing.T) {
	t.Parallel()
	nk := &metricsRecorderNK{multiUpdateErr: errors.New("storage unavailable")}

	err := EVRProfileUpdate(context.Background(), nk, uuid.Must(uuid.NewV4()).String(), &EVRProfile{})
	require.Error(t, err)

	nk.requireCounter(t, profileWriteCounter, map[string]string{"outcome": "error"}, 1, 1)
	nk.requireCounter(t, profileWriteCounter, map[string]string{"outcome": "conflict"}, 0, 0)
	nk.requireCounter(t, profileWriteCounter, map[string]string{"outcome": "committed"}, 0, 0)
}

// TestEVRProfileLoad_StorageSourceCounterFires pins the read that found a
// storage row: source=storage, and specifically NOT source=metadata.
func TestEVRProfileLoad_StorageSourceCounterFires(t *testing.T) {
	t.Parallel()
	nk := &metricsRecorderNK{
		account:     &api.Account{User: &api.User{Metadata: `{"active_group_id":"g1"}`}},
		storageObjs: []*api.StorageObject{{Value: `{"active_group_id":"g-from-storage"}`, Version: "v1"}},
	}

	profile, err := EVRProfileLoad(context.Background(), nk, "user-1")
	require.NoError(t, err)
	require.Equal(t, "g-from-storage", profile.ActiveGroupID, "fixture must exercise the storage branch, not the fallback")

	nk.requireCounter(t, profileReadCounter, map[string]string{"source": "storage"}, 1, 1)
	nk.requireCounter(t, profileReadCounter, map[string]string{"source": "metadata"}, 0, 0)
}

// TestEVRProfileLoad_MetadataSourceCounterFires pins the fallback. This is the
// counter that answers, without a production database read, whether any live
// account is in the row-less state whose next write goes out unversioned.
func TestEVRProfileLoad_MetadataSourceCounterFires(t *testing.T) {
	t.Parallel()
	nk := &metricsRecorderNK{
		account: &api.Account{User: &api.User{Metadata: `{"active_group_id":"g-from-metadata"}`}},
		// no storage objects: StorableRead reports NotFound and EVRProfileLoad falls back
	}

	profile, err := EVRProfileLoad(context.Background(), nk, "user-1")
	require.NoError(t, err)
	require.Equal(t, "g-from-metadata", profile.ActiveGroupID, "fixture must exercise the fallback branch, not the storage read")

	nk.requireCounter(t, profileReadCounter, map[string]string{"source": "metadata"}, 1, 1)
	nk.requireCounter(t, profileReadCounter, map[string]string{"source": "storage"}, 0, 0)
}

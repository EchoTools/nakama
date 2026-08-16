package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/stretchr/testify/require"
)

// selfHealNK is a purpose-built double for EVRProfileLoad's metadata-fallback
// repairing write. It is deliberately NOT one of the shared doubles in this
// package: the assertions here are about writes being attempted and about how
// MANY were attempted, and a double that other tests also drive would let an
// unrelated change move those numbers.
//
// It embeds a nil runtime.NakamaModule, so any method production calls that is
// not defined below panics rather than being silently stubbed.
//
// AGENTS.md defect class #1 does not apply: nothing embeds selfHealNK, and
// every call reaches it through the runtime.NakamaModule interface.
type selfHealNK struct {
	runtime.NakamaModule

	mu       sync.Mutex
	account  *api.Account
	objects  map[string]*api.StorageObject // collection/key/userID -> object
	writes   int                           // every StorageWrite operation, successful or not
	reads    int
	writeErr error // if set, every StorageWrite fails with it
	counters []recordedCounter
	nextVer  int

	// beforeRead runs on entry to StorageRead, outside the lock. The
	// concurrency test uses it as a start barrier so the goroutines provably
	// overlap (AGENTS.md defect class #2).
	beforeRead func()
	// duringWrite runs inside StorageWrite while the singleflight leader holds
	// the flight, so followers have a window in which to coalesce.
	duringWrite func()
}

func newSelfHealNK(metadata string) *selfHealNK {
	return &selfHealNK{
		account: &api.Account{User: &api.User{Metadata: metadata}},
		objects: make(map[string]*api.StorageObject),
	}
}

func selfHealKey(collection, key, userID string) string {
	return collection + "/" + key + "/" + userID
}

func (m *selfHealNK) seed(collection, key, userID, value, version string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[selfHealKey(collection, key, userID)] = &api.StorageObject{
		Collection: collection, Key: key, UserId: userID, Value: value, Version: version,
	}
}

func (m *selfHealNK) object(collection, key, userID string) *api.StorageObject {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.objects[selfHealKey(collection, key, userID)]
}

func (m *selfHealNK) writeCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writes
}

func (m *selfHealNK) AccountGetId(_ context.Context, _ string) (*api.Account, error) {
	return m.account, nil
}

func (m *selfHealNK) StorageRead(_ context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	if m.beforeRead != nil {
		m.beforeRead()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	out := make([]*api.StorageObject, 0, len(reads))
	for _, r := range reads {
		if obj, ok := m.objects[selfHealKey(r.Collection, r.Key, r.UserID)]; ok {
			out = append(out, obj)
		}
	}
	return out, nil
}

// StorageWrite honours the create-only version "*" the same way the real
// storage layer does: a write guarded by "*" against an existing row is
// rejected with runtime.ErrStorageRejectedVersion. Without that, a test could
// not tell a create-only repair from a last-write-wins clobber -- which is the
// exact distinction this whole change exists to make.
func (m *selfHealNK) StorageWrite(_ context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	m.mu.Lock()
	m.writes += len(writes)
	writeErr := m.writeErr
	m.mu.Unlock()

	if m.duringWrite != nil {
		m.duringWrite()
	}
	if writeErr != nil {
		return nil, writeErr
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	acks := make([]*api.StorageObjectAck, 0, len(writes))
	for _, w := range writes {
		k := selfHealKey(w.Collection, w.Key, w.UserID)
		existing, present := m.objects[k]
		switch {
		case w.Version == "*" && present:
			return nil, runtime.ErrStorageRejectedVersion
		case w.Version != "" && w.Version != "*" && (!present || existing.Version != w.Version):
			return nil, runtime.ErrStorageRejectedVersion
		}
		m.nextVer++
		version := fmt.Sprintf("v%d", m.nextVer)
		m.objects[k] = &api.StorageObject{
			Collection: w.Collection, Key: w.Key, UserId: w.UserID,
			Value: w.Value, Version: version,
		}
		acks = append(acks, &api.StorageObjectAck{
			Collection: w.Collection, Key: w.Key, UserId: w.UserID, Version: version,
		})
	}
	return acks, nil
}

func (m *selfHealNK) MetricsCounterAdd(name string, tags map[string]string, delta int64) {
	copied := make(map[string]string, len(tags))
	for k, v := range tags {
		copied[k] = v
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counters = append(m.counters, recordedCounter{name: name, tags: copied, delta: delta})
}

func (m *selfHealNK) requireCounter(t *testing.T, name string, tags map[string]string, wantDelta int64, wantCalls int) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	var sum int64
	var calls int
	for _, c := range m.counters {
		if c.name != name {
			continue
		}
		matched := true
		for k, v := range tags {
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
	require.Equalf(t, wantCalls, calls, "calls to %s%v; recorded: %+v", name, tags, m.counters)
	require.Equalf(t, wantDelta, sum, "summed delta of %s%v; recorded: %+v", name, tags, m.counters)
}

// selfHealUserID keeps each test on its own singleflight key. The group is
// package-level state, so two tests sharing a user id could coalesce across
// test boundaries and make a write count look lower than it was.
func selfHealUserID(t *testing.T) string {
	t.Helper()
	return "user-" + t.Name()
}

// --- R1 ----------------------------------------------------------------------

// TestEVRProfileLoad_SelfHeal_CreatesMissingRow is R1: the fallback path must
// leave a storage row behind.
//
// The row is the visible half. The assertion that actually pins the defect is
// the one on the returned profile's storage version: a row-less read used to
// return a profile with version "", and core_storage.go executes an empty
// version as an explicit non-OCC last-write-wins write. Creating the row while
// leaving the caller's profile unversioned would satisfy "the row exists" and
// fix nothing.
func TestEVRProfileLoad_SelfHeal_CreatesMissingRow(t *testing.T) {
	userID := selfHealUserID(t)
	nk := newSelfHealNK(`{"active_group_id":"g-from-metadata"}`)

	profile, err := EVRProfileLoad(context.Background(), nk, userID)
	require.NoError(t, err)
	require.NotNil(t, profile)
	require.Equal(t, "g-from-metadata", profile.ActiveGroupID, "fixture must exercise the fallback branch")

	obj := nk.object(StorageCollectionEVRProfile, StorageKeyEVRProfile, userID)
	require.NotNil(t, obj, "the repairing write must have created the storage row")
	require.NotEmpty(t, obj.Version)

	require.NotEmpty(t, profile.StorageMeta().Version,
		"the returned profile must carry the repaired row's version; an empty version is the last-write-wins window this change closes")

	// Stage 1's read-source counter must still report this as a metadata read:
	// the row did not exist when the read happened, and the self-heal must not
	// disguise that.
	nk.requireCounter(t, profileReadCounter, map[string]string{"source": "metadata"}, 1, 1)
	nk.requireCounter(t, profileReadCounter, map[string]string{"source": "storage"}, 0, 0)
	nk.requireCounter(t, profileSelfHealCounter, map[string]string{"outcome": "repaired"}, 1, 1)
	nk.requireCounter(t, profileSelfHealCounter, map[string]string{"outcome": "error"}, 0, 0)
}

// --- R2 ----------------------------------------------------------------------

// TestEVRProfileLoad_SelfHeal_WriteFailureDoesNotFailRead is R2: the repairing
// write is advisory, so its failure must not fail the read.
//
// This test is worthless without the write-count assertion. "The read still
// succeeded" passes trivially against a build that issues no write at all --
// including the build from before this change. The assertion that makes it a
// falsifier is require.Equal(1, nk.writeCount()): attempted, AND failed, AND
// the read still returned the profile.
func TestEVRProfileLoad_SelfHeal_WriteFailureDoesNotFailRead(t *testing.T) {
	userID := selfHealUserID(t)
	nk := newSelfHealNK(`{"active_group_id":"g-from-metadata"}`)
	nk.writeErr = errors.New("storage unavailable")

	profile, err := EVRProfileLoad(context.Background(), nk, userID)
	require.NoError(t, err, "an advisory write failure must not fail the read")
	require.NotNil(t, profile)
	require.Equal(t, "g-from-metadata", profile.ActiveGroupID, "the rebuilt profile must still be returned in full")

	require.Equal(t, 1, nk.writeCount(),
		"the repairing write must have been ATTEMPTED; without this assertion the test passes against a build that never writes")
	require.Nil(t, nk.object(StorageCollectionEVRProfile, StorageKeyEVRProfile, userID),
		"the write failed, so no row may exist -- otherwise the fixture is not exercising a failure")

	nk.requireCounter(t, profileSelfHealCounter, map[string]string{"outcome": "error"}, 1, 1)
	nk.requireCounter(t, profileSelfHealCounter, map[string]string{"outcome": "repaired"}, 0, 0)
}

// --- R3 ----------------------------------------------------------------------

// selfHealBarrier releases every arriving goroutine only once n of them have
// arrived, and fails the test rather than hanging if they never do.
//
// AGENTS.md defect class #2: a -race test in this repo once passed against a
// deliberately unlocked accessor because the goroutines never overlapped. A
// concurrency test that cannot fail when concurrency is absent proves nothing,
// so the absence of overlap is made an explicit failure here.
type selfHealBarrier struct {
	n     int
	mu    sync.Mutex
	count int
	ch    chan struct{}
}

func newSelfHealBarrier(n int) *selfHealBarrier {
	return &selfHealBarrier{n: n, ch: make(chan struct{})}
}

func (b *selfHealBarrier) arrive(t *testing.T) {
	b.mu.Lock()
	b.count++
	if b.count == b.n {
		close(b.ch)
	}
	b.mu.Unlock()

	select {
	case <-b.ch:
	case <-time.After(10 * time.Second):
		// t.Error is safe from a non-test goroutine; t.Fatal is not.
		t.Error("start barrier timed out: the goroutines never overlapped, so this test proves nothing about concurrency (AGENTS.md defect class #2)")
	}
}

// TestEVRProfileLoad_SelfHeal_ConcurrentLoadsWriteOnce is R3. Run under -race.
//
// The barrier sits in StorageRead, the last call every goroutine makes before
// it reaches the singleflight, so all N are provably in flight together. The
// leader then holds inside StorageWrite while the followers arrive at the
// singleflight and coalesce onto it.
func TestEVRProfileLoad_SelfHeal_ConcurrentLoadsWriteOnce(t *testing.T) {
	const n = 8
	userID := selfHealUserID(t)

	nk := newSelfHealNK(`{"active_group_id":"g-from-metadata"}`)
	barrier := newSelfHealBarrier(n)
	nk.beforeRead = func() { barrier.arrive(t) }
	nk.duringWrite = func() { time.Sleep(50 * time.Millisecond) }

	profiles := make([]*EVRProfile, n)
	errs := make([]error, n)

	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			profiles[i], errs[i] = EVRProfileLoad(context.Background(), nk, userID)
		}()
	}
	wg.Wait()

	for i := range n {
		require.NoErrorf(t, errs[i], "goroutine %d", i)
		require.NotNilf(t, profiles[i], "goroutine %d", i)
		require.NotEmptyf(t, profiles[i].StorageMeta().Version,
			"goroutine %d must observe the repaired version, not just the leader", i)
	}

	require.Equal(t, 1, nk.writeCount(),
		"N concurrent row-less loads must coalesce into exactly ONE repairing write")
	nk.requireCounter(t, profileSelfHealCounter, map[string]string{"outcome": "repaired"}, n, n)
	nk.requireCounter(t, profileReadCounter, map[string]string{"source": "metadata"}, n, n)
}

// --- R4 ----------------------------------------------------------------------

// TestEVRProfileLoad_SelfHeal_StorageHitWritesNothing is R4: a read that found
// its row must issue no write at all.
//
// Positive-controlled: adding any write to the cache-hit branch of
// EVRProfileLoad makes this fail. Recorded in the commit message with the red
// output, because a zero-assertion that has never been shown to be capable of
// being non-zero is not evidence.
func TestEVRProfileLoad_SelfHeal_StorageHitWritesNothing(t *testing.T) {
	userID := selfHealUserID(t)
	nk := newSelfHealNK(`{"active_group_id":"g-from-metadata"}`)
	nk.seed(StorageCollectionEVRProfile, StorageKeyEVRProfile, userID, `{"active_group_id":"g-from-storage"}`, "v1")

	profile, err := EVRProfileLoad(context.Background(), nk, userID)
	require.NoError(t, err)
	require.Equal(t, "g-from-storage", profile.ActiveGroupID, "fixture must exercise the storage branch, not the fallback")

	require.Equal(t, 0, nk.writeCount(), "a cache hit must write nothing")
	nk.requireCounter(t, profileReadCounter, map[string]string{"source": "storage"}, 1, 1)
	nk.requireCounter(t, profileSelfHealCounter, map[string]string{"outcome": "repaired"}, 0, 0)
	nk.requireCounter(t, profileSelfHealCounter, map[string]string{"outcome": "error"}, 0, 0)
}

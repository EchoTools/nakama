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
	// readErrAfter fails StorageRead with readErr once this many reads have
	// already succeeded. The lost-race test needs exactly that shape: read #1
	// (EVRProfileLoad's own) must return "no row" so the fallback branch is
	// entered at all, and read #2 (storableCreate's adopt-the-winner re-read)
	// must fail. A blanket readErr would abort the load before it ever reached
	// the code under test.
	readErrAfter int
	readErr      error
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
	if m.readErr != nil && m.reads >= m.readErrAfter {
		m.reads++
		return nil, m.readErr
	}
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

// MultiUpdate is the path EVRProfileUpdate actually writes through. It exists
// here so the self-heal failure tests can assert the hazard end-to-end -- "the
// caller's NEXT update silently overwrote another writer's row" -- rather than
// asserting that some particular sentinel string was stored on the profile. The
// storage writes are routed through StorageWrite so the create-only ("*") and
// OCC version semantics are identical to every other write in this double.
//
// Account updates and wallet updates are ignored: nothing under test reads them
// back. The storage deletes are applied because EVRProfileUpdate always issues
// one (the ServerProfile cache invalidation) and silently dropping it would let
// a stale object survive a test that later looked for it.
func (m *selfHealNK) MultiUpdate(ctx context.Context, _ []*runtime.AccountUpdate, storageWrites []*runtime.StorageWrite, storageDeletes []*runtime.StorageDelete, _ []*runtime.WalletUpdate, _ bool) ([]*api.StorageObjectAck, []*runtime.WalletUpdateResult, error) {
	acks, err := m.StorageWrite(ctx, storageWrites)
	if err != nil {
		return nil, nil, err
	}
	m.mu.Lock()
	for _, d := range storageDeletes {
		delete(m.objects, selfHealKey(d.Collection, d.Key, d.UserID))
	}
	m.mu.Unlock()
	return acks, nil, nil
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

// --- R5 ----------------------------------------------------------------------
//
// R5 is the requirement R2 left open. R2 pins that a FAILED repair still returns
// a usable profile; it says nothing about what version that profile carries, and
// the answer was "" -- the empty version core_storage.go:738-739 executes as an
// explicit non-OCC last-write-wins upsert. So the repair for a last-write-wins
// hazard reinstated the hazard on its own failure path.
//
// This is RULINGS.md:4256-4268 move 1, DISCRIMINATE: the failed-repair state and
// the never-attempted state produced the same downstream value and were
// therefore indistinguishable to every later writer.
//
// Both tests below assert the property BEHAVIOURALLY -- they drive the caller's
// next EVRProfileUpdate and require that it cannot silently take the row -- so
// they pin the requirement rather than the sentinel chosen to meet it. Asserting
// `version == "*"` would pass against an implementation that stamped "*" and
// then stripped it before the write.

// requireNextUpdateCannotClobber drives profile's next EVRProfileUpdate against
// a row another writer owns, and requires that it is refused as a version
// conflict with the other writer's bytes left intact.
//
// The conflict is the WANTED outcome, not a regression: it is only reachable
// when the repair failed AND someone else owns the row, which is exactly the
// case where this caller's metadata-rebuilt profile is stale and writing it
// would destroy data. evrProfileUpdateWithRetry (evr_account.go:687-711) treats
// a conflict as retryable -- it reloads the profile, which now finds the row and
// adopts its real version, and the retried write lands. Silence, not the
// conflict, was the defect.
func requireNextUpdateCannotClobber(t *testing.T, nk *selfHealNK, userID string, profile *EVRProfile) {
	t.Helper()

	// Disarm every fixture hook first. The lost-race test leaves duringWrite
	// armed, and it would fire again inside THIS update and re-seed the row
	// underneath the assertions below -- making a passing production path look
	// like a clobber.
	nk.writeErr = nil
	nk.readErr = nil
	nk.duringWrite = nil
	nk.beforeRead = nil
	nk.seed(StorageCollectionEVRProfile, StorageKeyEVRProfile, userID,
		`{"active_group_id":"g-from-the-other-writer"}`, "v-other-writer")

	err := EVRProfileUpdate(context.Background(), nk, userID, profile)
	require.Error(t, err,
		"the caller's next update must not silently succeed against a row it never read; "+
			"an empty version makes this write a non-OCC last-write-wins upsert (core_storage.go:738-739)")
	require.True(t, isVersionConflictError(err),
		"the refusal must be a version conflict so evrProfileUpdateWithRetry reloads and retries; got %v", err)

	obj := nk.object(StorageCollectionEVRProfile, StorageKeyEVRProfile, userID)
	require.NotNil(t, obj, "the other writer's row must still exist")
	require.Equal(t, "v-other-writer", obj.Version, "the other writer's row must be untouched")
	require.Contains(t, obj.Value, "g-from-the-other-writer",
		"the other writer's VALUE must survive; this is the data loss the whole change exists to prevent")
}

// TestEVRProfileLoad_SelfHeal_WriteFailureLeavesNoSilentClobber is R5 on the
// plain write-failure path -- the same fixture R2 uses, carried one step
// further to the consequence R2 stops short of.
func TestEVRProfileLoad_SelfHeal_WriteFailureLeavesNoSilentClobber(t *testing.T) {
	userID := selfHealUserID(t)
	nk := newSelfHealNK(`{"active_group_id":"g-from-metadata"}`)
	nk.writeErr = errors.New("storage unavailable")

	profile, err := EVRProfileLoad(context.Background(), nk, userID)
	require.NoError(t, err, "ADVISORY, NOT BLOCKING: a failed repair must never fail the read")
	require.NotNil(t, profile)
	require.Equal(t, "g-from-metadata", profile.ActiveGroupID, "the rebuilt profile must still be returned in full")
	require.Equal(t, 1, nk.writeCount(),
		"the repair must have been ATTEMPTED and failed; without this the test passes against a build that never writes")
	nk.requireCounter(t, profileSelfHealCounter, map[string]string{"outcome": "error"}, 1, 1)

	require.NotEmpty(t, profile.StorageMeta().Version,
		"DISCRIMINATE: a failed repair must not be indistinguishable from a repair that was never attempted")

	requireNextUpdateCannotClobber(t, nk, userID, profile)
}

// TestEVRProfileLoad_SelfHeal_LostRaceThenFailedAdoptLeavesNoSilentClobber is R5
// on the literal lost-race path: the create is rejected because another writer
// created the row in between, and storableCreate's adopt-the-winner re-read then
// fails, so no version is adopted.
//
// This fixture is what makes the test a falsifier rather than a restatement of
// the one above. storableCreate (evr_storable.go:215-238) ALREADY adopts the
// winner on an ordinary lost race, so a lost race alone never reaches
// evrProfileSelfHeal's error branch. Only a lost race whose adopt-read also
// fails does -- and that is the interleaving where the caller was left holding
// "" while another writer's row sat in storage, which is the state the doc
// comment on evrProfileSelfHeal claims cannot happen.
func TestEVRProfileLoad_SelfHeal_LostRaceThenFailedAdoptLeavesNoSilentClobber(t *testing.T) {
	userID := selfHealUserID(t)
	nk := newSelfHealNK(`{"active_group_id":"g-from-metadata"}`)

	// The racing writer lands between this caller's read and its create. The
	// double's StorageWrite rejects a "*" write against a row that is present,
	// exactly as core_storage.go:763-772 does, so the create is genuinely
	// rejected rather than merely made to return an error.
	nk.duringWrite = func() {
		nk.seed(StorageCollectionEVRProfile, StorageKeyEVRProfile, userID,
			`{"active_group_id":"g-from-the-racing-writer"}`, "v-racer")
	}
	// Read #1 is EVRProfileLoad's own and must find nothing; read #2 is the
	// adopt-the-winner re-read and must fail.
	nk.readErrAfter = 1
	nk.readErr = errors.New("storage read unavailable")

	profile, err := EVRProfileLoad(context.Background(), nk, userID)
	require.NoError(t, err, "ADVISORY, NOT BLOCKING: a failed repair must never fail the read")
	require.NotNil(t, profile)
	require.Equal(t, "g-from-metadata", profile.ActiveGroupID, "the rebuilt profile must still be returned in full")
	require.Equal(t, 1, nk.writeCount(), "the repair must have been ATTEMPTED and rejected")
	nk.requireCounter(t, profileSelfHealCounter, map[string]string{"outcome": "error"}, 1, 1)

	obj := nk.object(StorageCollectionEVRProfile, StorageKeyEVRProfile, userID)
	require.NotNil(t, obj, "fixture check: the racing writer's row must exist")
	require.Equal(t, "v-racer", obj.Version,
		"fixture check: the create must have been REJECTED, not applied -- otherwise this is not a lost race")

	require.NotEmpty(t, profile.StorageMeta().Version,
		"DISCRIMINATE: the caller lost the race, so it must not walk away holding the empty version that wins unconditionally")

	requireNextUpdateCannotClobber(t, nk, userID, profile)
}

// TestEVRProfileLoad_SelfHeal_FailedRepairStillCompletesLater is the other half
// of R5, and the one that keeps the two tests above from being satisfiable by a
// poison value.
//
// "Cannot silently win" is trivially met by stamping a version that can never
// match anything -- but that would wedge the profile permanently: the row is
// absent precisely because the repair failed, and a caller that can no longer
// create it has traded a silent clobber for silent data loss of its own. The
// version stamped on failure must still permit the write when, and only when,
// nobody else took the row.
//
// Together with requireNextUpdateCannotClobber this pins both directions:
// refused when a row exists, allowed when one does not.
func TestEVRProfileLoad_SelfHeal_FailedRepairStillCompletesLater(t *testing.T) {
	userID := selfHealUserID(t)
	nk := newSelfHealNK(`{"active_group_id":"g-from-metadata"}`)
	nk.writeErr = errors.New("storage unavailable")

	profile, err := EVRProfileLoad(context.Background(), nk, userID)
	require.NoError(t, err)
	require.Equal(t, 1, nk.writeCount(), "the repair must have been ATTEMPTED and failed")
	require.Nil(t, nk.object(StorageCollectionEVRProfile, StorageKeyEVRProfile, userID),
		"fixture check: the repair failed, so no row may exist")
	nk.requireCounter(t, profileSelfHealCounter, map[string]string{"outcome": "error"}, 1, 1)

	// Storage recovers. No other writer has taken the row.
	nk.writeErr = nil

	require.NoError(t, EVRProfileUpdate(context.Background(), nk, userID, profile),
		"a failed repair must not wedge the profile: with no competing writer the caller's next update must still land")

	obj := nk.object(StorageCollectionEVRProfile, StorageKeyEVRProfile, userID)
	require.NotNil(t, obj, "the update must have created the row the failed repair could not")
	require.Contains(t, obj.Value, "g-from-metadata")
	require.NotEqual(t, selfHealUnguessedVersion, profile.StorageMeta().Version,
		"the caller must have adopted the real acked version, not still be holding the failure marker")
	require.Equal(t, obj.Version, profile.StorageMeta().Version,
		"the profile's version must match the row it just wrote, putting this caller back under OCC")
}

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// storableRaceNK is an in-memory NakamaModule with the same optimistic
// concurrency semantics as real Nakama storage:
//
//	Version == ""  -> unconditional upsert
//	Version == "*" -> create only; rejected if the object already exists
//	otherwise      -> rejected unless it matches the stored version
//
// The afterRead hook runs once a StorageRead has returned, which lets a test
// simulate a concurrent writer winning the race between a read and the write
// that follows it.
type storableRaceNK struct {
	runtime.NakamaModule
	mu      sync.Mutex
	objects map[string]*api.StorageObject
	nextVer int

	reads   int
	writes  int
	deletes int

	// alwaysConflict makes every StorageWrite fail with the version sentinel.
	alwaysConflict bool
	// readErrFrom, when > 0, makes every StorageRead from that 1-based call
	// index onward fail with readErr.
	readErrFrom int
	readErr     error
	// deleteErr, when non-nil, makes every StorageDelete fail with it and
	// remove nothing — a transient storage failure, as distinct from the
	// version-guarded rejection the mock raises on its own.
	deleteErr error

	afterRead func(m *storableRaceNK)
	// afterWrite runs once a StorageWrite has been resolved (whether it was
	// accepted or rejected), which lets a test simulate a concurrent writer
	// acting in the window between a rejected write and the read that follows.
	afterWrite func(m *storableRaceNK)
}

func newStorableRaceNK() *storableRaceNK {
	return &storableRaceNK{objects: make(map[string]*api.StorageObject)}
}

func storableRaceKey(userID, collection, key string) string {
	return userID + "|" + collection + "|" + key
}

func (m *storableRaceNK) nextVersionLocked() string {
	m.nextVer++
	return fmt.Sprintf("v%d", m.nextVer)
}

// set installs a value bypassing OCC and returns the assigned version.
func (m *storableRaceNK) set(userID, collection, key, value string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ver := m.nextVersionLocked()
	m.objects[storableRaceKey(userID, collection, key)] = &api.StorageObject{
		Collection: collection,
		Key:        key,
		UserId:     userID,
		Value:      value,
		Version:    ver,
	}
	return ver
}

func (m *storableRaceNK) get(userID, collection, key string) *api.StorageObject {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.objects[storableRaceKey(userID, collection, key)]
}

func (m *storableRaceNK) counts() (reads, writes, deletes int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reads, m.writes, m.deletes
}

func (m *storableRaceNK) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	m.mu.Lock()
	m.reads++
	n := m.reads
	if m.readErrFrom > 0 && n >= m.readErrFrom {
		err := m.readErr
		m.mu.Unlock()
		return nil, err
	}
	out := make([]*api.StorageObject, 0, len(reads))
	for _, r := range reads {
		if obj, ok := m.objects[storableRaceKey(r.UserID, r.Collection, r.Key)]; ok {
			out = append(out, obj)
		}
	}
	hook := m.afterRead
	m.mu.Unlock()
	if hook != nil {
		hook(m)
	}
	return out, nil
}

func (m *storableRaceNK) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	acks, err := m.storageWriteLocked(writes)
	m.mu.Lock()
	hook := m.afterWrite
	m.mu.Unlock()
	if hook != nil {
		hook(m)
	}
	return acks, err
}

// MultiUpdate models the transactional write path. Real MultiUpdate runs every
// write inside one ExecuteInTxPgx (core_multi.go), so the batch is
// all-or-nothing: storageWriteLocked below therefore validates every version
// guard BEFORE applying any of them, and a single rejection leaves storage
// untouched. A mock that applied writes as it went would let a test pass while
// the code under test half-applied a pair that must not disagree.
//
// It is otherwise indistinguishable from StorageWrite — same write counter,
// same afterWrite hook — so a test cannot accidentally observe stronger or
// weaker guarantees just because the code under test switched entry points.
func (m *storableRaceNK) MultiUpdate(ctx context.Context, accountUpdates []*runtime.AccountUpdate, storageWrites []*runtime.StorageWrite, storageDeletes []*runtime.StorageDelete, walletUpdates []*runtime.WalletUpdate, updateLedger bool) ([]*api.StorageObjectAck, []*runtime.WalletUpdateResult, error) {
	acks, err := m.StorageWrite(ctx, storageWrites)
	return acks, nil, err
}

func (m *storableRaceNK) storageWriteLocked(writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes++
	if m.alwaysConflict {
		return nil, runtime.ErrStorageRejectedVersion
	}
	// Validate the whole batch first: nothing is applied unless every guard
	// passes. See MultiUpdate above.
	for _, w := range writes {
		existing, ok := m.objects[storableRaceKey(w.UserID, w.Collection, w.Key)]
		switch w.Version {
		case "":
			// Unconditional upsert.
		case "*":
			if ok {
				return nil, runtime.ErrStorageRejectedVersion
			}
		default:
			if !ok || existing.Version != w.Version {
				return nil, runtime.ErrStorageRejectedVersion
			}
		}
	}
	acks := make([]*api.StorageObjectAck, 0, len(writes))
	for _, w := range writes {
		k := storableRaceKey(w.UserID, w.Collection, w.Key)
		ver := m.nextVersionLocked()
		m.objects[k] = &api.StorageObject{
			Collection: w.Collection,
			Key:        w.Key,
			UserId:     w.UserID,
			Value:      w.Value,
			Version:    ver,
		}
		acks = append(acks, &api.StorageObjectAck{
			Collection: w.Collection,
			Key:        w.Key,
			UserId:     w.UserID,
			Version:    ver,
		})
	}
	return acks, nil
}

func (m *storableRaceNK) StorageDelete(ctx context.Context, deletes []*runtime.StorageDelete) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletes++
	if m.deleteErr != nil {
		return m.deleteErr
	}
	for _, d := range deletes {
		k := storableRaceKey(d.UserID, d.Collection, d.Key)
		existing, ok := m.objects[k]
		if d.Version != "" {
			// Version-guarded delete. Real storage rejects whenever the guarded
			// DELETE matches no row (core_storage.go storageDeleteObjects checks
			// rowsAffected == 0), which covers BOTH "the object was replaced"
			// and "the object is already gone" — the latter is the interleaving
			// a lenient mock would silently report as success.
			if !ok || existing.Version != d.Version {
				return StatusError(codes.InvalidArgument, "Storage delete rejected.", errors.New("Storage delete rejected - not found, version check failed, or permission denied."))
			}
		} else if !ok {
			// Unversioned authoritative delete of a missing object is a no-op
			// in real storage (the `continue` before the rowsAffected check).
			continue
		}
		delete(m.objects, k)
	}
	return nil
}

func mustStorableJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// onceHook returns an afterRead hook that runs fn only on its first invocation.
func onceHook(fn func(m *storableRaceNK)) func(m *storableRaceNK) {
	var done bool
	return func(m *storableRaceNK) {
		if done {
			return
		}
		done = true
		fn(m)
	}
}

// winnerLatencyJSON builds a stored LatencyHistory holding a single entry for
// the given IP, as a concurrent writer would have left it.
func winnerLatencyJSON(t *testing.T, ip string) string {
	t.Helper()
	winner := NewLatencyHistory()
	winner.GameServerLatencies[ip] = []LatencyHistoryItem{{Timestamp: time.Now().UTC(), RTT: 42 * time.Millisecond}}
	return mustStorableJSON(t, winner)
}

// TestStorableRead_CreateDoesNotClobberConcurrentCreate proves that
// StorableRead(create=true) really is create-only: if another writer creates the
// object between the read that misses and the write that follows, the winner's
// object survives and is adopted into dst, rather than being overwritten with
// this caller's defaults.
//
// LatencyHistory is used because its zero value carries an empty storage
// version — the case where the write goes out unconditional.
func TestStorableRead_CreateDoesNotClobberConcurrentCreate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV4()).String()
	nk := newStorableRaceNK()

	winnerJSON := winnerLatencyJSON(t, "10.0.0.1")
	nk.afterRead = onceHook(func(m *storableRaceNK) {
		m.set(userID, LatencyHistoryStorageCollection, LatencyHistoryStorageKey, winnerJSON)
	})

	dst := NewLatencyHistory()
	if err := StorableRead(ctx, nk, userID, dst, true); err != nil {
		t.Fatalf("StorableRead(create=true): %v", err)
	}

	stored := nk.get(userID, LatencyHistoryStorageCollection, LatencyHistoryStorageKey)
	if stored == nil {
		t.Fatal("no object stored")
	}
	if !strings.Contains(stored.Value, "10.0.0.1") {
		t.Errorf("create clobbered the concurrently-created object; stored value = %s", stored.Value)
	}
	if _, ok := dst.GameServerLatencies["10.0.0.1"]; !ok {
		t.Errorf("dst did not adopt the winner's object, got %+v", dst.GameServerLatencies)
	}
}

// TestStorableRead_CorruptRecoveryDoesNotClobberConcurrentRecreate proves that
// the corrupt-record recovery path honours its own contract ("disallow
// overwriting any concurrently-recreated object"): if another writer replaces
// the corrupt object before our versioned delete lands, that good object must
// not be overwritten with defaults.
func TestStorableRead_CorruptRecoveryDoesNotClobberConcurrentRecreate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV4()).String()
	nk := newStorableRaceNK()

	// Stored object is unparseable for this type.
	nk.set(userID, LatencyHistoryStorageCollection, LatencyHistoryStorageKey, `{"game_server_latencies":"corrupt"}`)

	winnerJSON := winnerLatencyJSON(t, "10.0.0.1")
	// After our read of the corrupt object, a concurrent writer replaces it with
	// a valid object under a new version, so our versioned delete matches
	// nothing.
	nk.afterRead = onceHook(func(m *storableRaceNK) {
		m.set(userID, LatencyHistoryStorageCollection, LatencyHistoryStorageKey, winnerJSON)
	})

	dst := NewLatencyHistory()
	if err := StorableRead(ctx, nk, userID, dst, true); err != nil {
		t.Fatalf("StorableRead(create=true): %v", err)
	}

	stored := nk.get(userID, LatencyHistoryStorageCollection, LatencyHistoryStorageKey)
	if stored == nil {
		t.Fatal("no object stored")
	}
	if !strings.Contains(stored.Value, "10.0.0.1") {
		t.Errorf("corrupt-record recovery clobbered the concurrently-recreated object; stored value = %s", stored.Value)
	}
	if _, ok := dst.GameServerLatencies["10.0.0.1"]; !ok {
		t.Errorf("dst did not adopt the recreated object, got %+v", dst.GameServerLatencies)
	}
}

// TestStorableRead_CorruptRecoverySelfHealsWhenDeleteFails is the other half of
// the corrupt-record contract: recovery must still recover.
//
// The delete is version-guarded and its error is deliberately not fatal, so a
// transient delete failure leaves the corrupt record in place. If the follow-up
// write is create-only ("*") it is then rejected, the fallback re-read hits the
// same unparseable bytes, and StorableRead(create=true) returns a hard "failed
// to unmarshal" for every future call — a permanent, per-user failure on an API
// whose whole job is get-or-create.
func TestStorableRead_CorruptRecoverySelfHealsWhenDeleteFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV4()).String()
	nk := newStorableRaceNK()

	nk.set(userID, LatencyHistoryStorageCollection, LatencyHistoryStorageKey, `{"game_server_latencies":"corrupt"}`)
	nk.deleteErr = errors.New("storage delete failed transiently")

	dst := NewLatencyHistory()
	if err := StorableRead(ctx, nk, userID, dst, true); err != nil {
		t.Fatalf("corrupt-record recovery must self-heal when the delete does not land, got: %v", err)
	}

	stored := nk.get(userID, LatencyHistoryStorageCollection, LatencyHistoryStorageKey)
	if stored == nil {
		t.Fatal("no object stored")
	}
	if strings.Contains(stored.Value, "corrupt") {
		t.Errorf("the corrupt object was left in place; stored value = %s", stored.Value)
	}
	if v := dst.StorageMeta().Version; v == "" || v == "*" {
		t.Errorf("dst version not updated from the write ack, got %q", v)
	}
}

// TestStorableRead_CorruptRecoveryWhenObjectConcurrentlyDeleted covers the last
// interleaving of the recovery decision tree: the corrupt record is deleted by
// somebody else between our read and our version-guarded delete.
//
// Our delete is then rejected (rowsAffected == 0, indistinguishable from "the
// record was replaced"), so the follow-up write is guarded on the corrupt
// record's own version — which now matches nothing and is rejected too. The path
// must fall through to the create-only write and leave a healthy object, not
// surface the rejection to the caller.
func TestStorableRead_CorruptRecoveryWhenObjectConcurrentlyDeleted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV4()).String()
	nk := newStorableRaceNK()

	nk.set(userID, LatencyHistoryStorageCollection, LatencyHistoryStorageKey, `{"game_server_latencies":"corrupt"}`)
	// Between our read of the corrupt object and our delete, it is removed.
	nk.afterRead = onceHook(func(m *storableRaceNK) {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.objects, storableRaceKey(userID, LatencyHistoryStorageCollection, LatencyHistoryStorageKey))
	})

	dst := NewLatencyHistory()
	if err := StorableRead(ctx, nk, userID, dst, true); err != nil {
		t.Fatalf("corrupt-record recovery must still create when the object vanished: %v", err)
	}

	stored := nk.get(userID, LatencyHistoryStorageCollection, LatencyHistoryStorageKey)
	if stored == nil {
		t.Fatal("get-or-create returned success without creating the object")
	}
	if strings.Contains(stored.Value, "corrupt") {
		t.Errorf("the corrupt object was resurrected; stored value = %s", stored.Value)
	}
	if v := dst.StorageMeta().Version; v == "" || v == "*" {
		t.Errorf("dst version not updated from the write ack, got %q", v)
	}
	// Two writes prove the delete really was rejected and the guarded
	// corrupt-version write ran before the create-only fallback. A mock that
	// reported the delete as a success would reach the same end state in one
	// write, leaving this branch untested.
	if _, writes, _ := nk.counts(); writes != 2 {
		t.Errorf("expected the guarded corrupt-version write then the create: %d writes, want 2", writes)
	}
}

// TestStorableRead_CreateRetriesWhenWinnerVanishes pins the get-or-create
// contract across the narrow window in which the caller loses the create race
// and then the winner's object is deleted before the fallback re-read reaches
// it. Surfacing NotFound there would be a get-or-create call returning
// "not found" with nothing stored — several callers treat any error from
// StorableRead(create=true) as fatal.
func TestStorableRead_CreateRetriesWhenWinnerVanishes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV4()).String()
	nk := newStorableRaceNK()

	// A concurrent writer creates the object between our read (which misses)
	// and our create-only write, so the write is rejected...
	nk.afterRead = onceHook(func(m *storableRaceNK) {
		m.set(userID, LatencyHistoryStorageCollection, LatencyHistoryStorageKey, winnerLatencyJSON(t, "10.0.0.1"))
	})
	// ...and then deletes it again before we can read it back.
	nk.afterWrite = onceHook(func(m *storableRaceNK) {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.objects, storableRaceKey(userID, LatencyHistoryStorageCollection, LatencyHistoryStorageKey))
	})

	dst := NewLatencyHistory()
	if err := StorableRead(ctx, nk, userID, dst, true); err != nil {
		t.Fatalf("get-or-create must not surface NotFound when nothing is stored: %v", err)
	}
	if nk.get(userID, LatencyHistoryStorageCollection, LatencyHistoryStorageKey) == nil {
		t.Fatal("create=true returned success without creating the object")
	}
	if v := dst.StorageMeta().Version; v == "" || v == "*" {
		t.Errorf("dst version not updated from the write ack, got %q", v)
	}
}

// TestStorableRead_CreateFailsHonestlyWhenTheRaceNeverSettles pins the terminal
// behaviour of storableCreate when its attempts run out, and pins the reason it
// is left alone.
//
// The interleaving: a concurrent writer recreates the object after every read
// (so every create-only write is rejected) and deletes it after every write (so
// every adoption re-read misses). storableCreate spends both attempts and
// returns the last error it observed, which is the NotFound from the final
// adoption read. That NotFound is not a lie — it is exactly what the last read
// saw — but it does leave a get-or-create reporting absence.
//
// It is deliberately NOT converted into a further write attempt. The invariant
// that matters is that a conflict never resolves to a false success, and it
// holds here: the call reports failure and nothing of dst was persisted. Adding
// one more create-only write does not remove the failure — in this exact
// interleaving it is rejected too, and the call returns
// "failed to write: Storage write rejected - version check failed" instead of
// "no ... found". That is a relabelled error bought with an extra round-trip,
// and it pushes retry down to a layer that has no way to know whether retrying
// is the right answer for this data. Retry belongs to the caller.
//
// Reachability is effectively nil regardless: no path in this package deletes a
// create=true object's collection+key except StorableRead's own corrupt-record
// recovery, which cannot fire on an object a writer of the same type just
// created.
func TestStorableRead_CreateFailsHonestlyWhenTheRaceNeverSettles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV4()).String()
	nk := newStorableRaceNK()

	nk.afterRead = func(m *storableRaceNK) {
		m.set(userID, LatencyHistoryStorageCollection, LatencyHistoryStorageKey, winnerLatencyJSON(t, "10.0.0.1"))
	}
	nk.afterWrite = func(m *storableRaceNK) {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.objects, storableRaceKey(userID, LatencyHistoryStorageCollection, LatencyHistoryStorageKey))
	}

	dst := NewLatencyHistory()
	err := StorableRead(ctx, nk, userID, dst, true)
	if err == nil {
		t.Fatal("a create that never won must not report success")
	}
	if got := status.Code(err); got != codes.NotFound {
		t.Errorf("terminal error code = %v, want %v (the last observed read); err = %v", got, codes.NotFound, err)
	}
	// The load-bearing half: failure is reported, and no mutation was silently
	// discarded behind a success.
	if _, writes, _ := nk.counts(); writes != storableCreateMaxAttempts {
		t.Errorf("write attempts = %d, want %d — the terminal error must not be bought with an extra blind write",
			writes, storableCreateMaxAttempts)
	}
	if v := dst.StorageMeta().Version; v != "stale-version" && v != "" {
		t.Errorf("dst must not carry a version it never got an ack for, got %q", v)
	}
}

// TestStorableRead_CreateStillCreatesWhenAbsent is the companion boundary case:
// with no concurrent writer, create=true must still create the object and leave
// dst holding a usable storage version. Both a type whose zero storage version
// is empty (LatencyHistory) and one that starts at "*" (the journal) are
// covered.
func TestStorableRead_CreateStillCreatesWhenAbsent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("empty initial version", func(t *testing.T) {
		userID := uuid.Must(uuid.NewV4()).String()
		nk := newStorableRaceNK()
		dst := NewLatencyHistory()
		if err := StorableRead(ctx, nk, userID, dst, true); err != nil {
			t.Fatalf("StorableRead(create=true): %v", err)
		}
		if nk.get(userID, LatencyHistoryStorageCollection, LatencyHistoryStorageKey) == nil {
			t.Fatal("create=true did not create the object")
		}
		if v := dst.StorageMeta().Version; v == "" || v == "*" {
			t.Errorf("dst version not updated from the write ack, got %q", v)
		}
	})

	t.Run("star initial version", func(t *testing.T) {
		userID := uuid.Must(uuid.NewV4()).String()
		nk := newStorableRaceNK()
		dst := NewGuildEnforcementJournal(userID)
		if err := StorableRead(ctx, nk, userID, dst, true); err != nil {
			t.Fatalf("StorableRead(create=true): %v", err)
		}
		if nk.get(userID, StorageCollectionEnforcementJournal, StorageKeyEnforcementJournal) == nil {
			t.Fatal("create=true did not create the object")
		}
		if v := dst.GetStorageVersion(); v == "" || v == "*" {
			t.Errorf("dst version not updated from the write ack, got %q", v)
		}
	})
}

// TestStorableWrite_VersionConflictPreservesSentinel proves the error returned
// by a rejected write still satisfies errors.Is against
// runtime.ErrStorageRejectedVersion, and keeps its gRPC code.
func TestStorableWrite_VersionConflictPreservesSentinel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV4()).String()
	nk := newStorableRaceNK()
	nk.set(userID, StorageCollectionEnforcementJournal, StorageKeyEnforcementJournal, "{}")

	// A brand new journal writes with version "*", which must be rejected
	// because the object already exists.
	j := NewGuildEnforcementJournal(userID)
	err := StorableWrite(ctx, nk, userID, j)
	if err == nil {
		t.Fatal("expected a version conflict, got nil")
	}
	if !errors.Is(err, runtime.ErrStorageRejectedVersion) {
		t.Errorf("StorableWrite must keep runtime.ErrStorageRejectedVersion in the chain; got %v", err)
	}
	if code := status.Code(err); code != codes.Internal {
		t.Errorf("gRPC status code lost: got %v, want %v", code, codes.Internal)
	}
	if !isVersionConflictError(err) {
		t.Errorf("isVersionConflictError should classify %v as a version conflict", err)
	}
}

// TestStorableRead_ReadFailurePreservesCause proves read failures also keep the
// underlying cause in the chain.
func TestStorableRead_ReadFailurePreservesCause(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV4()).String()
	nk := newStorableRaceNK()
	sentinel := errors.New("storage unavailable")
	nk.readErrFrom = 1
	nk.readErr = sentinel

	dst := NewGuildEnforcementJournal(userID)
	err := StorableRead(ctx, nk, userID, dst, false)
	if err == nil {
		t.Fatal("expected a read error, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("StorableRead must keep the underlying cause in the chain; got %v", err)
	}
}

// TestIsVersionConflictError_MatchesSentinelNotSubstring pins which errors are
// classified retryable, and records the ONE deliberate reclassification.
//
// A rejected storage delete is raised inside the transaction as
// StatusError(codes.InvalidArgument, "Storage delete rejected.", cause), but
// StorageDeleteObjects unwraps it and hands the runtime e.Cause()
// (core_storage.go, `return e.Code(), e.Cause()`). That cause's text DOES
// contain "version check failed", so the old substring predicate classified a
// delete rejection as a version conflict; the sentinel predicate does not.
//
// The reclassification is unreachable in production: all six call sites receive
// a WRITE error (EVRProfileUpdate, ServerProfileStore, ServerProfileStoreJSON,
// StorableWrite x3), and the only delete on any of those paths is
// EVRProfileUpdate's unversioned MultiUpdate delete, which storageDeleteObjects
// short-circuits with `continue` before it can reject. Documented, not hidden.
func TestIsVersionConflictError_MatchesSentinelNotSubstring(t *testing.T) {
	t.Parallel()

	if isVersionConflictError(nil) {
		t.Error("nil must not be a version conflict")
	}
	if !isVersionConflictError(runtime.ErrStorageRejectedVersion) {
		t.Error("the sentinel itself must be a version conflict")
	}
	if !isVersionConflictError(fmt.Errorf("wrapped: %w", runtime.ErrStorageRejectedVersion)) {
		t.Error("a wrapped sentinel must be a version conflict")
	}
	if isVersionConflictError(errors.New("some unrelated failure")) {
		t.Error("unrelated errors must not be version conflicts")
	}

	// The delete rejection exactly as core_storage.go builds it...
	deleteRejected := StatusError(codes.InvalidArgument, "Storage delete rejected.", errors.New("Storage delete rejected - not found, version check failed, or permission denied."))
	// ...and what a runtime caller would actually receive.
	causer, ok := deleteRejected.(ErrorCauser)
	if !ok {
		t.Fatalf("StatusError no longer implements ErrorCauser: %T", deleteRejected)
	}
	cause := causer.Cause()
	if !strings.Contains(cause.Error(), "version check failed") {
		t.Fatalf("precondition: the delete rejection reaching callers must still carry the legacy substring, got %q", cause.Error())
	}
	if isVersionConflictError(cause) {
		t.Errorf("a delete rejection must not be classified as a write version conflict: %v", cause)
	}
}

// multiUpdateRejectNK reports the storage optimistic-concurrency rejection the
// way real storage does on the MultiUpdate path: storageWriteObjects returns the
// bare runtime.ErrStorageRejectedVersion, which is not a *statusError, so
// core_multi.go MultiUpdate falls through its own errors.Is check on that same
// sentinel and returns it unwrapped.
type multiUpdateRejectNK struct{ runtime.NakamaModule }

func (multiUpdateRejectNK) MultiUpdate(ctx context.Context, accountUpdates []*runtime.AccountUpdate, storageWrites []*runtime.StorageWrite, storageDeletes []*runtime.StorageDelete, walletUpdates []*runtime.WalletUpdate, updateLedger bool) ([]*api.StorageObjectAck, []*runtime.WalletUpdateResult, error) {
	return nil, nil, runtime.ErrStorageRejectedVersion
}

// TestEVRProfileUpdate_VersionConflictStaysRetryable guards the one caller that
// would fail SILENTLY if the substring-to-sentinel switch missed a path: the
// display-name update loop (evr_discord_integrator.go) stops retrying and
// reports a hard failure the moment isVersionConflictError says "not a
// conflict". EVRProfileUpdate's %w must therefore keep the sentinel reachable.
func TestEVRProfileUpdate_VersionConflictStaysRetryable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV4()).String()

	err := EVRProfileUpdate(ctx, multiUpdateRejectNK{}, userID, &EVRProfile{})
	if err == nil {
		t.Fatal("expected the rejection to be reported")
	}
	if !isVersionConflictError(err) {
		t.Errorf("a MultiUpdate version rejection must stay retryable through EVRProfileUpdate; got %v", err)
	}
}

// legacyStorableErrorf reproduces storableErrorf exactly as it was at
// d3e7e5549, so the message contract can be asserted rather than described.
func legacyStorableErrorf(m StorableMetadata, c codes.Code, format string, a ...any) error {
	return fmt.Errorf("storable error on %s/%s/%s/%s: %v", m.UserID, m.Collection, m.Key, m.Version, status.Errorf(c, format, a...))
}

// TestStorableErrorf_MessageMatchesLegacyFormat pins the operator- and
// player-visible text of every storage error this package produces. The message
// reaches players through LobbySessionFailureFromError and operators through
// nakama.log, so preserving the cause in the error chain must not perturb it.
func TestStorableErrorf_MessageMatchesLegacyFormat(t *testing.T) {
	t.Parallel()
	m := StorableMetadata{UserID: "u", Collection: "c", Key: "k", Version: "v"}

	cases := []struct {
		name   string
		build  func(format string, a ...any) error
		format string
		args   []any
	}{
		{"wrapped cause", nil, "failed to write: %w", []any{runtime.ErrStorageRejectedVersion}},
		{"no verb", nil, "multiple objects returned", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := storableErrorf(m, codes.Internal, tc.format, tc.args...).Error()
			// The legacy formatter used %v where the new one uses %w; both
			// render an error operand identically.
			legacyFormat := strings.ReplaceAll(tc.format, "%w", "%v")
			want := legacyStorableErrorf(m, codes.Internal, legacyFormat, tc.args...).Error()
			if got != want {
				t.Errorf("storage error text changed:\n got: %q\nwant: %q", got, want)
			}
		})
	}
}

// TestStorableErrorf_LobbyAndLoginMessagesUnchanged proves that giving
// storableError a gRPC status does not change what a player sees when a storage
// failure aborts a lobby join, nor what a failed login reports — for a bare
// storable error and for one wrapped by an outer %w (the shape
// evr_lobby_parameters.go produces via "failed to load join directive: %w").
func TestStorableErrorf_LobbyAndLoginMessagesUnchanged(t *testing.T) {
	t.Parallel()
	m := StorableMetadata{UserID: "u", Collection: "c", Key: "k", Version: "v"}
	mode := evr.ModeArenaPublic
	groupID := uuid.Must(uuid.NewV4())

	for _, tc := range []struct {
		name string
		wrap func(error) error
	}{
		{"bare", func(err error) error { return err }},
		{"wrapped", func(err error) error { return fmt.Errorf("failed to load join directive: %w", err) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := tc.wrap(storableErrorf(m, codes.Internal, "failed to read: %w", errors.New("storage unavailable")))
			legacy := tc.wrap(legacyStorableErrorf(m, codes.Internal, "failed to read: %v", errors.New("storage unavailable")))

			gotMsg := LobbySessionFailureFromError(mode, groupID, current).(*evr.LobbySessionFailurev4)
			wantMsg := LobbySessionFailureFromError(mode, groupID, legacy).(*evr.LobbySessionFailurev4)
			if gotMsg.ErrorCode != wantMsg.ErrorCode {
				t.Errorf("lobby failure code changed: got %v, want %v", gotMsg.ErrorCode, wantMsg.ErrorCode)
			}
			if gotMsg.Message != wantMsg.Message {
				t.Errorf("lobby failure message changed:\n got: %q\nwant: %q", gotMsg.Message, wantMsg.Message)
			}

			xpID := evr.EvrId{PlatformCode: evr.OVR_ORG, AccountId: 1234}
			if got, want := formatLoginErrorMessage(xpID, "", current), formatLoginErrorMessage(xpID, "", legacy); got != want {
				t.Errorf("login failure message changed:\n got: %q\nwant: %q", got, want)
			}
		})
	}
}

// TestStorableErrorf_PreservesChainThroughMultiWrap guards the footgun in a
// helper whose entire purpose is chain preservation: Go's multi-%w wrapper
// implements Unwrap() []error, which errors.Unwrap reports as nil.
func TestStorableErrorf_PreservesChainThroughMultiWrap(t *testing.T) {
	t.Parallel()
	m := StorableMetadata{UserID: "u", Collection: "c", Key: "k", Version: "v"}
	other := errors.New("some other failure")

	err := storableErrorf(m, codes.Internal, "failed to write: %w (while %w)", runtime.ErrStorageRejectedVersion, other)
	if !errors.Is(err, runtime.ErrStorageRejectedVersion) {
		t.Errorf("a two-%%w format dropped the version sentinel from the chain: %v", err)
	}
	if !errors.Is(err, other) {
		t.Errorf("a two-%%w format dropped the second cause from the chain: %v", err)
	}
}

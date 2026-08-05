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

func (m *storableRaceNK) storageWriteLocked(writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes++
	if m.alwaysConflict {
		return nil, runtime.ErrStorageRejectedVersion
	}
	acks := make([]*api.StorageObjectAck, 0, len(writes))
	for _, w := range writes {
		k := storableRaceKey(w.UserID, w.Collection, w.Key)
		existing, ok := m.objects[k]
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
		if !ok {
			continue
		}
		if d.Version != "" && existing.Version != d.Version {
			// Versioned delete matched nothing: the object was replaced.
			return StatusError(codes.InvalidArgument, "Storage delete rejected.", errors.New("Storage delete rejected - not found, version check failed, or permission denied."))
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
// classified retryable. Note that a storage DELETE rejection (core_storage.go
// storageDeleteObjects) names "version check failed" only in its *cause*, which
// statusError.Error() does not render, so it was never matched by the old
// substring predicate either — it must stay unmatched.
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
	deleteRejected := StatusError(codes.InvalidArgument, "Storage delete rejected.", errors.New("Storage delete rejected - not found, version check failed, or permission denied."))
	if isVersionConflictError(deleteRejected) {
		t.Errorf("a delete rejection must not be classified as a write version conflict: %v", deleteRejected)
	}
	if isVersionConflictError(errors.New("some unrelated failure")) {
		t.Error("unrelated errors must not be version conflicts")
	}
}

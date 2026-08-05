package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

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

	afterRead func(m *storableRaceNK)
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

package server

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// occTestNakamaModule is an in-memory NakamaModule mock with optimistic
// concurrency control (OCC) semantics that mirror real Nakama: a write whose
// supplied Version does not match the stored object's current version is
// rejected with runtime.ErrStorageRejectedVersion. A Version of "" means
// "create only" and a Version of "*" means "must not already exist".
type occTestNakamaModule struct {
	runtime.NakamaModule
	mu      sync.Mutex
	objects map[string]*api.StorageObject
	nextVer int

	// writeAttempts counts every StorageWrite call (used to assert retry/no-retry).
	writeAttempts int
	// failNonVersion, when set, makes StorageWrite return a non-version error
	// (simulating an unrelated Internal failure) instead of an OCC conflict.
	failNonVersion error
}

func newOCCTestNakamaModule() *occTestNakamaModule {
	return &occTestNakamaModule{objects: make(map[string]*api.StorageObject)}
}

func occStorageKey(userID, collection, key string) string {
	return userID + ":" + collection + ":" + key
}

func (m *occTestNakamaModule) versionLocked() string {
	m.nextVer++
	return "v" + string(rune('0'+m.nextVer))
}

func (m *occTestNakamaModule) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writeAttempts++

	if m.failNonVersion != nil {
		return nil, m.failNonVersion
	}

	acks := make([]*api.StorageObjectAck, 0, len(writes))
	for _, w := range writes {
		k := occStorageKey(w.UserID, w.Collection, w.Key)
		existing, ok := m.objects[k]

		switch w.Version {
		case "":
			// Unconditional write: allowed regardless of current state.
		case "*":
			if ok {
				return nil, runtime.ErrStorageRejectedVersion
			}
		default:
			if !ok || existing.Version != w.Version {
				return nil, runtime.ErrStorageRejectedVersion
			}
		}

		ver := m.versionLocked()
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

func (m *occTestNakamaModule) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	objects := make([]*api.StorageObject, 0, len(reads))
	for _, r := range reads {
		if obj, ok := m.objects[occStorageKey(r.UserID, r.Collection, r.Key)]; ok {
			objects = append(objects, obj)
		}
	}
	return objects, nil
}

// seedObject writes raw value bytes for a key, bypassing OCC, and returns the
// assigned version. Used to set up a pre-existing stored object in tests.
func (m *occTestNakamaModule) seedObject(userID, collection, key, value string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ver := m.versionLocked()
	m.objects[occStorageKey(userID, collection, key)] = &api.StorageObject{
		Collection: collection,
		Key:        key,
		UserId:     userID,
		Value:      value,
		Version:    ver,
	}
	return ver
}

// failOnceNakamaModule wraps occTestNakamaModule to inject a version conflict on
// the first StorageWrite only, then defers to the real OCC mock. This simulates
// a concurrent winner having advanced the version between read and write.
type failOnceNakamaModule struct {
	*occTestNakamaModule
	conflicted bool
}

func (m *failOnceNakamaModule) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	m.occTestNakamaModule.mu.Lock()
	first := !m.conflicted
	if first {
		m.conflicted = true
	}
	m.occTestNakamaModule.mu.Unlock()
	if first {
		m.occTestNakamaModule.mu.Lock()
		m.occTestNakamaModule.writeAttempts++
		m.occTestNakamaModule.mu.Unlock()
		return nil, runtime.ErrStorageRejectedVersion
	}
	return m.occTestNakamaModule.StorageWrite(ctx, writes)
}

const occTestUserID = "00000000-0000-0000-0000-000000000001"

// TestStorableWriteWithRetry_ReReadsAndReAppliesLossless proves that when the
// first write hits a version conflict, the helper re-reads the fresh stored
// object (picking up a concurrent winner's entries) and re-applies the caller's
// pending mutation, so BOTH the winner's and this caller's new entries survive.
func TestStorableWriteWithRetry_ReReadsAndReAppliesLossless(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	base := newOCCTestNakamaModule()

	// Session B (the concurrent winner) has already written a LatencyHistory
	// containing one RTT sample for serverA. Seed it as the current stored state.
	winner := NewLatencyHistory()
	winner.GameServerLatencies = map[string][]LatencyHistoryItem{
		"10.0.0.1": {{Timestamp: time.Now(), RTT: 42 * time.Millisecond}},
	}
	winnerVer := base.seedObject(occTestUserID, LatencyHistoryStorageCollection, LatencyHistoryStorageKey, winner.String())
	_ = winnerVer

	nk := &failOnceNakamaModule{occTestNakamaModule: base}

	// Session A holds a stale in-memory LatencyHistory (empty version, so its
	// first write would conflict). It just received an RTT for serverB.
	sessionA := NewLatencyHistory()
	expiry := time.Now().Add(-14 * 24 * time.Hour)
	reapply := func() error {
		sessionA.Add(net.ParseIP("10.0.0.2"), 99, 25, expiry)
		return nil
	}
	// Apply A's pending mutation once before the first write attempt.
	if err := reapply(); err != nil {
		t.Fatalf("reapply: %v", err)
	}

	if err := StorableWriteWithRetry(ctx, nk, occTestUserID, sessionA, reapply); err != nil {
		t.Fatalf("StorableWriteWithRetry: %v", err)
	}

	// Read back the final stored object and assert lossless merge.
	final := NewLatencyHistory()
	if err := StorableRead(ctx, nk, occTestUserID, final, false); err != nil {
		t.Fatalf("final read: %v", err)
	}
	if _, ok := final.GameServerLatencies["10.0.0.1"]; !ok {
		t.Errorf("winner's entry for 10.0.0.1 was clobbered: %v", final.GameServerLatencies)
	}
	if _, ok := final.GameServerLatencies["10.0.0.2"]; !ok {
		t.Errorf("session A's entry for 10.0.0.2 was lost: %v", final.GameServerLatencies)
	}
}

// TestStorableWriteWithRetry_NoRetryOnNonVersionError proves that a non-version
// storage error is returned immediately without re-reading or retrying.
func TestStorableWriteWithRetry_NoRetryOnNonVersionError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	nk := newOCCTestNakamaModule()
	nk.failNonVersion = context.DeadlineExceeded

	h := NewLatencyHistory()
	reapplyCount := 0
	reapply := func() error {
		reapplyCount++
		return nil
	}

	err := StorableWriteWithRetry(ctx, nk, occTestUserID, h, reapply)
	if err == nil {
		t.Fatal("expected error for non-version failure, got nil")
	}
	if nk.writeAttempts != 1 {
		t.Errorf("non-version error must NOT be retried: got %d write attempts, want 1", nk.writeAttempts)
	}
	if reapplyCount != 0 {
		t.Errorf("reapply must NOT be invoked for non-version error: got %d calls", reapplyCount)
	}
}

// TestStorableWriteWithRetry_BoundedAttempts proves the helper gives up after a
// bounded number of attempts when conflicts persist indefinitely.
func TestStorableWriteWithRetry_BoundedAttempts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	nk := newOCCTestNakamaModule()
	// A stored object already exists so re-reads after each conflict succeed,
	// letting the helper loop until it exhausts its bounded attempts.
	nk.seedObject(occTestUserID, LatencyHistoryStorageCollection, LatencyHistoryStorageKey, NewLatencyHistory().String())
	nk.failNonVersion = runtime.ErrStorageRejectedVersion // always conflict

	h := NewLatencyHistory()
	reapply := func() error { return nil }

	err := StorableWriteWithRetry(ctx, nk, occTestUserID, h, reapply)
	if err == nil {
		t.Fatal("expected error after exhausting retries, got nil")
	}
	if nk.writeAttempts < 2 {
		t.Errorf("expected multiple bounded attempts, got %d", nk.writeAttempts)
	}
	if nk.writeAttempts > storableMaxWriteAttempts {
		t.Errorf("attempts %d exceeded bound %d", nk.writeAttempts, storableMaxWriteAttempts)
	}
}

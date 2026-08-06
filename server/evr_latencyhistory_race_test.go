package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// A *LatencyHistory is shared across a session's goroutines: it lives in
// sessionParameters.latencyHistory (*atomic.Pointer[LatencyHistory]) and is
// loaded by lobbyPingResponse (inbound pipeline goroutine) while the
// lobbySessionRequest goroutine reads it via CheckServerPing ->
// HasRecentEntry / sortPingCandidatesByLatencyHistory and via
// NewLobbyParametersFromRequest -> AverageRTTs.
//
// Every accessor on the type takes the embedded RWMutex, but the storage path
// did not: StorableWrite json.Marshal'd the live object and StorableRead
// json.Unmarshal'd into it with no lock held. The unmarshal is a map WRITE, so
// it races with the locked readers on the other goroutine — Go reports that as
// "fatal error: concurrent map read and map write", a runtime throw that takes
// the whole process down rather than a recoverable panic.
//
// These tests pin the storage path to the same locking discipline as the rest
// of the type. They only fail under -race (or, nondeterministically, with the
// map fatal error).

// conflictAlternatingModule rejects every other StorageWrite with a version
// conflict so that each writeWithRetry call exercises BOTH the marshal path
// (StorableWrite) and the re-read/unmarshal path (StorableRead) before it
// finally succeeds.
type conflictAlternatingModule struct {
	*occTestNakamaModule
	callMu sync.Mutex
	calls  int
}

func (m *conflictAlternatingModule) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	m.callMu.Lock()
	m.calls++
	conflict := m.calls%2 == 1
	m.callMu.Unlock()
	if conflict {
		return nil, runtime.ErrStorageRejectedVersion
	}
	return m.occTestNakamaModule.StorageWrite(ctx, writes)
}

// seedLatencyHistory returns a history populated with n game server entries.
func seedLatencyHistory(n int) *LatencyHistory {
	h := NewLatencyHistory()
	for i := 0; i < n; i++ {
		h.Add(net.ParseIP(fmt.Sprintf("10.0.0.%d", i+1)), 20+i, 25, time.Time{})
	}
	return h
}

// hammerReaders runs the locked read accessors that the lobby-find goroutine
// uses, until stop is closed.
func hammerReaders(h *LatencyHistory, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	cutoff := time.Now().Add(-time.Hour)
	for {
		select {
		case <-stop:
			return
		default:
		}
		_ = h.AverageRTTs(false)
		_ = h.LatestRTTs()
		_ = h.HasRecentEntry("10.0.0.1", cutoff)
		_ = h.AverageRTT("10.0.0.2", true)
		_, _, _ = h.BestAddress("10.0.0.1", "10.0.0.2")
		ips := []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}
		sortPingCandidatesByLatencyHistory(ips, h)
	}
}

// TestLatencyHistory_StorableRead_UnmarshalIsLocked proves the re-read after a
// version conflict (StorableRead -> json.Unmarshal into the live object) does
// not race with the locked readers on another goroutine.
func TestLatencyHistory_StorableRead_UnmarshalIsLocked(t *testing.T) {
	ctx := context.Background()

	base := newOCCTestNakamaModule()
	base.seedObject(occTestUserID, LatencyHistoryStorageCollection, LatencyHistoryStorageKey, seedLatencyHistory(16).String())
	nk := &conflictAlternatingModule{occTestNakamaModule: base}

	// The session-shared history, as loaded from params.latencyHistory.
	h := seedLatencyHistory(4)

	stop := make(chan struct{})
	readersDone := make(chan struct{})
	go hammerReaders(h, stop, readersDone)

	expiry := time.Now().Add(-14 * 24 * time.Hour)
	reapply := func() error {
		h.Add(net.ParseIP("10.0.0.99"), 42, 25, expiry)
		return nil
	}
	for i := 0; i < 20; i++ {
		if err := reapply(); err != nil {
			t.Fatalf("reapply: %v", err)
		}
		if err := h.writeWithRetry(ctx, nk, occTestUserID, reapply); err != nil {
			t.Fatalf("writeWithRetry iteration %d: %v", i, err)
		}
	}

	close(stop)
	<-readersDone
}

// TestLatencyHistory_StorageMeta_IsLocked pins the locking contract on the
// version field: SetStorageMeta writes h.version under the write lock (it is
// called by both StorableRead and StorableWrite), so StorageMeta must read it
// under the read lock.
//
// Unlike the marshal/unmarshal races above, no confirmed production
// interleaving reaches this today: the two StorableRead call sites that touch a
// LatencyHistory (evr_pipeline_login.go:721 and the appbot handlers) all read
// into an object that is not yet published to params.latencyHistory. This test
// pins the type's contract rather than a reproduced production failure — every
// other accessor on LatencyHistory locks, and an unlocked version read is a
// trap for the next caller that does a storage op on the shared object.
func TestLatencyHistory_StorageMeta_IsLocked(t *testing.T) {
	h := seedLatencyHistory(2)

	stop := make(chan struct{})
	started := make(chan struct{})
	writersDone := make(chan struct{})
	go func() {
		defer close(writersDone)
		h.SetStorageMeta(StorableMetadata{Version: "v0"})
		close(started) // Guarantee the two loops actually overlap.
		for i := 1; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			h.SetStorageMeta(StorableMetadata{Version: fmt.Sprintf("v%d", i)})
		}
	}()

	<-started
	for i := 0; i < 200000; i++ {
		_ = h.StorageMeta().Version
	}

	close(stop)
	<-writersDone
}

// TestLatencyHistory_StorableWrite_MarshalIsLocked proves the marshal inside
// StorableWrite does not race with a concurrent locked mutation (Add) of the
// same session-shared history.
func TestLatencyHistory_StorableWrite_MarshalIsLocked(t *testing.T) {
	ctx := context.Background()

	nk := newOCCTestNakamaModule()
	h := seedLatencyHistory(8)

	stop := make(chan struct{})
	addersDone := make(chan struct{})
	go func() {
		defer close(addersDone)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			h.Add(net.ParseIP(fmt.Sprintf("10.1.0.%d", i%32)), 10+i%50, 25, time.Time{})
		}
	}()

	for i := 0; i < 200; i++ {
		if err := StorableWrite(ctx, nk, occTestUserID, h); err != nil {
			t.Fatalf("StorableWrite iteration %d: %v", i, err)
		}
	}

	close(stop)
	<-addersDone
}

// TestLatencyHistory_String_ConcurrentWithAdd proves String() (which marshals)
// is safe against a concurrent locked mutation, and that adding a locking
// MarshalJSON did not reintroduce a recursive read-lock deadlock in String().
func TestLatencyHistory_String_ConcurrentWithAdd(t *testing.T) {
	h := seedLatencyHistory(4)

	stop := make(chan struct{})
	addersDone := make(chan struct{})
	go func() {
		defer close(addersDone)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			h.Add(net.ParseIP(fmt.Sprintf("10.2.0.%d", i%16)), 10+i%40, 25, time.Time{})
		}
	}()

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for i := 0; i < 500; i++ {
			if s := h.String(); s == "" {
				// t.Fatal is illegal off the test goroutine, and panicking
				// here would abort the whole ./server/ test binary.
				t.Errorf("String() returned empty; marshal failed")
				return
			}
		}
	}()

	select {
	case <-finished:
	case <-time.After(30 * time.Second):
		close(stop)
		t.Fatal("String() deadlocked (recursive read lock?)")
	}

	close(stop)
	<-addersDone
}

// TestLatencyHistory_Get_DoesNotEscapeLiveSlice proves Get hands back memory the
// caller can safely read after the lock is released.
//
// sortPingCandidatesByLatencyHistory (the lobby-find goroutine) calls Get and
// then reads history[len(history)-1] with no lock held. Add compacts the stored
// slice in place via slices.Delete, which both shifts and zeroes elements the
// escaped slice still spans. A stored record containing a zero-RTT entry is
// enough to reach that path: Add strips zeroes it appends, but a record decoded
// from storage (written by another session, or legacy data) can carry an
// interior zero entry that the next Add then deletes in place.
func TestLatencyHistory_Get_DoesNotEscapeLiveSlice(t *testing.T) {
	const extIP = "10.5.0.1"

	// decodedRecord builds a record carrying an interior zero-RTT entry, as
	// StorableRead can produce. Generous capacity so Add compacts the slice in
	// place instead of reallocating.
	decodedRecord := func() []LatencyHistoryItem {
		items := make([]LatencyHistoryItem, 0, 64)
		for i := 0; i < 12; i++ {
			rtt := time.Duration(20+i) * time.Millisecond
			if i == 4 {
				rtt = 0
			}
			items = append(items, LatencyHistoryItem{Timestamp: time.Now(), RTT: rtt})
		}
		return items
	}

	h := NewLatencyHistory()
	h.GameServerLatencies[extIP] = decodedRecord()

	stop := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			// Exactly what sortPingCandidatesByLatencyHistory does: read the
			// returned entries after Get's lock has been released.
			if history, ok := h.Get(extIP); ok {
				for i := range history {
					_ = history[i].RTT
					_ = history[i].Timestamp
				}
			}
		}
	}()

	for i := 0; i < 2000; i++ {
		// Install a freshly decoded record (its own backing array, never touched
		// by this goroutine again) so the next Add hits the in-place delete path.
		// All in-place mutation of reader-visible memory is therefore done by
		// production code, not by the test.
		h.Lock()
		h.GameServerLatencies[extIP] = decodedRecord()
		h.Unlock()
		h.Add(net.ParseIP(extIP), 30, 25, time.Time{})
	}

	close(stop)
	<-readerDone
}

// TestLatencyHistory_JSONRoundTrip pins the wire format so that locking the
// marshal/unmarshal path cannot silently change what is persisted. A regression
// here would orphan every stored LatencyHistory record.
func TestLatencyHistory_JSONRoundTrip(t *testing.T) {
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	h := NewLatencyHistory()
	h.GameServerLatencies = map[string][]LatencyHistoryItem{
		"10.0.0.1": {{Timestamp: ts, RTT: 42 * time.Millisecond}},
	}

	data, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal into raw: %v", err)
	}
	if _, ok := raw["game_server_latencies"]; !ok {
		t.Fatalf("marshalled form lost the game_server_latencies key: %s", data)
	}
	if len(raw) != 1 {
		t.Fatalf("marshalled form has unexpected keys: %s", data)
	}

	got := NewLatencyHistory()
	if err := json.Unmarshal(data, got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	items, ok := got.GameServerLatencies["10.0.0.1"]
	if !ok || len(items) != 1 {
		t.Fatalf("round trip lost entries: %#v", got.GameServerLatencies)
	}
	if !items[0].Timestamp.Equal(ts) || items[0].RTT != 42*time.Millisecond {
		t.Fatalf("round trip corrupted entry: %#v", items[0])
	}
}

// TestLatencyHistory_UnmarshalMergesIntoExisting pins the pre-existing
// encoding/json map semantics that writeWithRetry's re-read relies on: keys
// present only in the in-memory object survive the re-read, and keys present in
// the stored object are adopted.
func TestLatencyHistory_UnmarshalMergesIntoExisting(t *testing.T) {
	local := NewLatencyHistory()
	local.Add(net.ParseIP("10.0.0.2"), 99, 25, time.Time{})

	stored := NewLatencyHistory()
	stored.Add(net.ParseIP("10.0.0.1"), 42, 25, time.Time{})

	if err := json.Unmarshal([]byte(stored.String()), local); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if _, ok := local.GameServerLatencies["10.0.0.1"]; !ok {
		t.Errorf("stored key was not adopted: %#v", local.GameServerLatencies)
	}
	if _, ok := local.GameServerLatencies["10.0.0.2"]; !ok {
		t.Errorf("local-only key was dropped: %#v", local.GameServerLatencies)
	}
}

// TestLatencyHistory_UnmarshalNullMapPreservesExisting pins the ONE case where
// the explicit UnmarshalJSON diverges from the reflection decode it replaced.
//
// A `&LatencyHistory{}` with a nil map (evr_pipeline_login.go:720,
// evr_runtime_rpc.go:1950, evr_runtime_rpc_match.go:168) marshals to
// `{"game_server_latencies":null}`, so this document is genuinely stored.
// Reflection-decoding it set the receiver's map to nil, discarding the caller's
// pending samples during writeWithRetry's re-read; the merge keeps them. The
// divergence is deliberate and strictly the safer direction.
func TestLatencyHistory_UnmarshalNullMapPreservesExisting(t *testing.T) {
	// The document really is what a nil-map history serializes to.
	if got := (&LatencyHistory{}).String(); got != `{"game_server_latencies":null}` {
		t.Fatalf("nil-map history serialized to %s; the null case may no longer be reachable", got)
	}

	h := NewLatencyHistory()
	h.Add(net.ParseIP("10.0.0.2"), 99, 25, time.Time{})

	if err := json.Unmarshal([]byte(`{"game_server_latencies":null}`), h); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if h.GameServerLatencies == nil {
		t.Fatal("null map nil'd the receiver's map; pending samples would be lost on the OCC re-read")
	}
	if _, ok := h.GameServerLatencies["10.0.0.2"]; !ok {
		t.Errorf("null map dropped the local-only key: %#v", h.GameServerLatencies)
	}
}

// TestLatencyHistory_UnmarshalIntoNilMap covers the fresh-object path used by
// every StorableRead into a zero-valued LatencyHistory.
func TestLatencyHistory_UnmarshalIntoNilMap(t *testing.T) {
	h := &LatencyHistory{}
	if err := json.Unmarshal([]byte(seedLatencyHistory(3).String()), h); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(h.GameServerLatencies) != 3 {
		t.Fatalf("expected 3 entries, got %#v", h.GameServerLatencies)
	}
}

// TestLatencyHistory_UnmarshalRejectsGarbage ensures a decode failure is still
// reported (StorableRead depends on the error to trigger its corrupt-record
// recovery path).
func TestLatencyHistory_UnmarshalRejectsGarbage(t *testing.T) {
	h := NewLatencyHistory()
	if err := json.Unmarshal([]byte(`{"game_server_latencies":"not-a-map"}`), h); err == nil {
		t.Fatal("expected an error unmarshalling a malformed record, got nil")
	}
}

// TestLatencyHistory_UnmarshalErrorLeavesReceiverUntouched pins the second
// deliberate divergence from the reflection decode: decoding is all-or-nothing.
//
// Reflection decoded into the receiver key by key and kept whatever it had
// parsed before hitting the malformed value, so a partially-corrupt record
// contributed its valid keys AND wiped nothing. This implementation decodes
// into a scratch value and returns before taking the lock, so a failed decode
// contributes nothing and — critically — cannot leave the live, session-shared
// object holding a half-applied merge of a record that never existed.
func TestLatencyHistory_UnmarshalErrorLeavesReceiverUntouched(t *testing.T) {
	h := NewLatencyHistory()
	h.Add(net.ParseIP("10.0.0.9"), 7, 25, time.Time{})

	// One valid entry, one malformed: the reflection decode salvaged 1.1.1.1.
	const partial = `{"game_server_latencies":{"1.1.1.1":[{"timestamp":"2024-01-01T00:00:00Z","rtt":1000000}],"2.2.2.2":"bad"}}`
	if err := json.Unmarshal([]byte(partial), h); err == nil {
		t.Fatal("expected an error unmarshalling a partially malformed record, got nil")
	}

	if _, ok := h.GameServerLatencies["1.1.1.1"]; ok {
		t.Error("a failed decode contributed a key to the receiver; decoding must be all-or-nothing")
	}
	if _, ok := h.GameServerLatencies["2.2.2.2"]; ok {
		t.Error("a failed decode contributed the malformed key to the receiver")
	}
	if _, ok := h.GameServerLatencies["10.0.0.9"]; !ok {
		t.Errorf("a failed decode dropped the receiver's own entries: %#v", h.GameServerLatencies)
	}
}

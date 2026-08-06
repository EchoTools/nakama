package server

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
)

// staleLatencyHistory builds an in-memory history holding one entry for staleIP
// and pinned to a storage version that no longer matches storage, so its first
// write is guaranteed to hit an optimistic-concurrency conflict.
func staleLatencyHistory(staleIP string) *LatencyHistory {
	h := NewLatencyHistory()
	h.GameServerLatencies[staleIP] = []LatencyHistoryItem{{Timestamp: time.Now().UTC(), RTT: 7 * time.Millisecond}}
	h.SetStorageMeta(StorableMetadata{Version: "stale-version"})
	return h
}

// TestLatencyHistory_writeWithRetry_AdoptsWinnerRatherThanUnion proves that the
// post-conflict re-read really adopts the concurrent winner's contents. Entries
// that exist only in this caller's stale copy — entries the winner pruned, or
// IPs that the caller's game-server allowlist would now reject — must NOT be
// resurrected by the retry.
func TestLatencyHistory_writeWithRetry_AdoptsWinnerRatherThanUnion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV4()).String()
	nk := newStorableRaceNK()

	nk.set(userID, LatencyHistoryStorageCollection, LatencyHistoryStorageKey, winnerLatencyJSON(t, "10.0.0.1"))

	h := staleLatencyHistory("10.9.9.9")
	reapply := func() error {
		h.Add(net.ParseIP("10.0.0.2"), 99, 25, time.Time{})
		return nil
	}
	if err := reapply(); err != nil {
		t.Fatalf("reapply: %v", err)
	}
	if err := h.writeWithRetry(ctx, nk, userID, reapply); err != nil {
		t.Fatalf("writeWithRetry: %v", err)
	}

	final := NewLatencyHistory()
	if err := StorableRead(ctx, nk, userID, final, false); err != nil {
		t.Fatalf("final read: %v", err)
	}
	if _, ok := final.GameServerLatencies["10.0.0.1"]; !ok {
		t.Errorf("the concurrent winner's entry was clobbered: %v", final.GameServerLatencies)
	}
	if _, ok := final.GameServerLatencies["10.0.0.2"]; !ok {
		t.Errorf("the caller's re-applied sample was lost: %v", final.GameServerLatencies)
	}
	if _, ok := final.GameServerLatencies["10.9.9.9"]; ok {
		t.Errorf("a stale local-only entry was resurrected by the re-read: %v", final.GameServerLatencies)
	}
}

// TestLatencyHistory_writeWithRetry_AdoptionDropsUnpersistedEarlierSamples pins
// the cost of adopting the winner wholesale instead of unioning, so the trade is
// a tested contract rather than an accident.
//
// h is the session-shared history. If one ping round exhausts its attempts, its
// samples exist only in h; the next round's first conflict replaces h's map with
// storage's and those samples are gone for good. A union would eventually have
// written them. Adoption is still the right default — see the writeWithRetry doc
// comment — but callers must not treat h as a durable accumulator.
func TestLatencyHistory_writeWithRetry_AdoptionDropsUnpersistedEarlierSamples(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV4()).String()
	nk := newStorableRaceNK()
	nk.set(userID, LatencyHistoryStorageCollection, LatencyHistoryStorageKey, winnerLatencyJSON(t, "10.0.0.1"))

	h := staleLatencyHistory("10.9.9.9")

	// Round 1 never persists: every write conflicts and the attempts run out.
	nk.alwaysConflict = true
	round1 := func() error {
		h.Add(net.ParseIP("10.1.1.1"), 99, 25, time.Time{})
		return nil
	}
	if err := round1(); err != nil {
		t.Fatalf("round 1 apply: %v", err)
	}
	if err := h.writeWithRetry(ctx, nk, userID, round1); err == nil {
		t.Fatal("precondition: round 1 must exhaust its attempts")
	}
	if _, ok := h.GameServerLatencies["10.1.1.1"]; !ok {
		t.Fatalf("precondition: round 1's sample should still be in h, got %v", h.GameServerLatencies)
	}

	// A concurrent writer advances storage, so round 2's first write conflicts.
	nk.alwaysConflict = false
	nk.set(userID, LatencyHistoryStorageCollection, LatencyHistoryStorageKey, winnerLatencyJSON(t, "10.0.0.1"))

	round2 := func() error {
		h.Add(net.ParseIP("10.2.2.2"), 99, 25, time.Time{})
		return nil
	}
	if err := round2(); err != nil {
		t.Fatalf("round 2 apply: %v", err)
	}
	if err := h.writeWithRetry(ctx, nk, userID, round2); err != nil {
		t.Fatalf("round 2 writeWithRetry: %v", err)
	}

	final := NewLatencyHistory()
	if err := StorableRead(ctx, nk, userID, final, false); err != nil {
		t.Fatalf("final read: %v", err)
	}
	if _, ok := final.GameServerLatencies["10.2.2.2"]; !ok {
		t.Errorf("round 2's sample was lost: %v", final.GameServerLatencies)
	}
	if _, ok := final.GameServerLatencies["10.0.0.1"]; !ok {
		t.Errorf("the concurrent winner's entry was clobbered: %v", final.GameServerLatencies)
	}
	if _, ok := final.GameServerLatencies["10.1.1.1"]; ok {
		t.Errorf("round 1's unpersisted sample was expected to be dropped by adoption, but it survived: %v", final.GameServerLatencies)
	}
}

// TestLatencyHistory_writeWithRetry_NoRereadOnFinalAttempt proves the loop does
// not spend a read + re-apply round-trip whose result it will never write. On
// permanent contention there must be exactly maxAttempts writes but only
// maxAttempts-1 re-reads and re-applies.
func TestLatencyHistory_writeWithRetry_NoRereadOnFinalAttempt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV4()).String()
	nk := newStorableRaceNK()
	nk.set(userID, LatencyHistoryStorageCollection, LatencyHistoryStorageKey, winnerLatencyJSON(t, "10.0.0.1"))
	readsBefore, _, _ := nk.counts()
	nk.alwaysConflict = true

	h := staleLatencyHistory("10.9.9.9")
	reapplies := 0
	reapply := func() error {
		reapplies++
		return nil
	}

	if err := h.writeWithRetry(ctx, nk, userID, reapply); err == nil {
		t.Fatal("expected an error after exhausting attempts, got nil")
	}

	readsAfter, writes, _ := nk.counts()
	if writes != latencyRetryMaxAttempts {
		t.Errorf("write attempts = %d, want %d", writes, latencyRetryMaxAttempts)
	}
	if reads := readsAfter - readsBefore; reads != latencyRetryMaxAttempts-1 {
		t.Errorf("the final attempt must not re-read: reads = %d, want %d", reads, latencyRetryMaxAttempts-1)
	}
	if reapplies != latencyRetryMaxAttempts-1 {
		t.Errorf("the final attempt must not re-apply: re-applies = %d, want %d", reapplies, latencyRetryMaxAttempts-1)
	}
}

// TestLatencyHistory_writeWithRetry_ExhaustionLeavesHistoryStale pins what the
// early break on the final attempt does and does NOT achieve.
//
// It saves a round-trip; it does not leave h consistent with storage. Earlier
// attempts already adopted the stored object and re-applied onto it, so on
// exhaustion h holds merged state that was never persisted. h is typically the
// session-shared history (evr_pipeline_login.go), so a caller that ignores the
// error keeps serving that unpersisted state for the rest of the session. This
// test documents the contract rather than endorsing it.
func TestLatencyHistory_writeWithRetry_ExhaustionLeavesHistoryStale(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV4()).String()
	nk := newStorableRaceNK()
	nk.set(userID, LatencyHistoryStorageCollection, LatencyHistoryStorageKey, winnerLatencyJSON(t, "10.0.0.1"))
	nk.alwaysConflict = true

	h := staleLatencyHistory("10.9.9.9")
	reapply := func() error {
		h.Add(net.ParseIP("10.0.0.2"), 99, 25, time.Time{})
		return nil
	}
	if err := reapply(); err != nil {
		t.Fatalf("reapply: %v", err)
	}
	if err := h.writeWithRetry(ctx, nk, userID, reapply); err == nil {
		t.Fatal("expected an error after exhausting attempts, got nil")
	}

	// h now reflects the last adopted-and-re-applied attempt, none of which
	// reached storage.
	if _, ok := h.GameServerLatencies["10.0.0.2"]; !ok {
		t.Errorf("expected the caller's re-applied sample to still be in h, got %v", h.GameServerLatencies)
	}
	stored := NewLatencyHistory()
	if err := StorableRead(ctx, nk, userID, stored, false); err != nil {
		t.Fatalf("read stored: %v", err)
	}
	if _, ok := stored.GameServerLatencies["10.0.0.2"]; ok {
		t.Fatalf("precondition: nothing should have been persisted, got %v", stored.GameServerLatencies)
	}
}

// wideLatencyJSON builds a stored history with n distinct game servers. Width
// matters for TestLatencyHistory_writeWithRetry_AdoptionIsLocked: the readers it
// races hold the read lock for the length of a full map iteration, so a wide map
// keeps their critical sections open across the adoption instead of closing them
// in the nanoseconds between two of writeWithRetry's own lock acquisitions.
func wideLatencyJSON(t *testing.T, n int) string {
	t.Helper()
	winner := NewLatencyHistory()
	for i := 0; i < n; i++ {
		winner.GameServerLatencies[fmt.Sprintf("10.0.%d.%d", i/256, i%256)] = []LatencyHistoryItem{
			{Timestamp: time.Now().UTC(), RTT: time.Duration(i+1) * time.Millisecond},
		}
	}
	return mustStorableJSON(t, winner)
}

// TestLatencyHistory_writeWithRetry_AdoptionIsLocked pins that the post-conflict
// adoption of the winner's object happens under h's write lock.
//
// h is the session-shared history (sessionParameters.latencyHistory), read
// concurrently by the lobby-find and matchmaker goroutines through LatestRTTs /
// AverageRTTs / HasRecentEntry, all of which take h.RLock. Replacing
// h.GameServerLatencies with a bare field assignment is therefore an
// unsynchronized write against every one of those locked readers — a wider race
// than the unmarshal-into-h it replaced, because the map header itself is the
// thing being swapped.
//
// The map pointer and the storage version must also move together: a reader that
// observes the winner's entries paired with this caller's stale version has seen
// a state that no stored record ever had.
//
// This test needs -race. Without the detector a torn map-header swap is very
// unlikely to be observed, which is exactly why the detector is the instrument.
func TestLatencyHistory_writeWithRetry_AdoptionIsLocked(t *testing.T) {
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV4()).String()
	nk := newStorableRaceNK()
	nk.set(userID, LatencyHistoryStorageCollection, LatencyHistoryStorageKey, wideLatencyJSON(t, 512))
	// Permanent contention: every write is rejected, so the loop performs
	// latencyRetryMaxAttempts-1 adoptions with jittered backoff between them.
	nk.alwaysConflict = true

	h := staleLatencyHistory("10.9.9.9")
	for i := 0; i < 512; i++ {
		h.Add(net.ParseIP(fmt.Sprintf("10.1.%d.%d", i/256, i%256)), i+1, 25, time.Time{})
	}

	// reapply is a no-op so that the ONLY write to h from the writeWithRetry
	// goroutine is the adoption itself. An Add here would muddy the finding.
	reapply := func() error { return nil }

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = h.LatestRTTs()
				_ = h.AverageRTTs(false)
				_ = h.HasRecentEntry("10.0.0.1", time.Time{})
			}
		}()
	}

	err := h.writeWithRetry(ctx, nk, userID, reapply)
	close(stop)
	wg.Wait()

	if err == nil {
		t.Fatal("precondition: permanent contention must exhaust the attempts")
	}
	if _, writes, _ := nk.counts(); writes != latencyRetryMaxAttempts {
		t.Fatalf("precondition: want %d write attempts (so %d adoptions ran), got %d",
			latencyRetryMaxAttempts, latencyRetryMaxAttempts-1, writes)
	}
}

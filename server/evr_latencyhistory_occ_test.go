package server

import (
	"context"
	"net"
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

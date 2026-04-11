package server

import (
	"testing"
	"time"
)

func TestUnreachableServers_AddAndIsUnreachable(t *testing.T) {
	u := NewUnreachableServers()

	if u.IsUnreachable("1.2.3.4") {
		t.Fatal("Expected server to be reachable before any records")
	}

	u.Add("1.2.3.4", "invalid_peer_id")

	if !u.IsUnreachable("1.2.3.4") {
		t.Fatal("Expected server to be unreachable after Add")
	}

	if u.IsUnreachable("5.6.7.8") {
		t.Fatal("Expected different server to still be reachable")
	}
}

func TestUnreachableServers_UnreachableIPs(t *testing.T) {
	u := NewUnreachableServers()
	u.Add("1.2.3.4", "connection_failed")
	u.Add("10.0.0.1", "disconnected_timeout")

	ips := u.UnreachableIPs()
	if len(ips) != 2 {
		t.Fatalf("Expected 2 unreachable IPs, got %d", len(ips))
	}
	if _, ok := ips["1.2.3.4"]; !ok {
		t.Fatal("Expected 1.2.3.4 in unreachable set")
	}
	if _, ok := ips["10.0.0.1"]; !ok {
		t.Fatal("Expected 10.0.0.1 in unreachable set")
	}
}

func TestUnreachableServers_PruneExpired(t *testing.T) {
	u := NewUnreachableServers()

	// Inject an expired record directly.
	u.Servers["1.2.3.4"] = []UnreachableRecord{
		{Timestamp: time.Now().Add(-UnreachableServerTTL - time.Hour), Reason: "old"},
	}
	// Add a fresh record for a different IP.
	u.Add("5.6.7.8", "connection_failed")

	u.PruneExpired()

	if u.IsUnreachable("1.2.3.4") {
		t.Fatal("Expected expired record to be pruned")
	}
	if !u.IsUnreachable("5.6.7.8") {
		t.Fatal("Expected fresh record to survive pruning")
	}
	if _, exists := u.Servers["1.2.3.4"]; exists {
		t.Fatal("Expected empty IP entry to be removed from map")
	}
}

func TestUnreachableServers_MaxRecordsCap(t *testing.T) {
	u := NewUnreachableServers()

	// Add more than MaxUnreachableRecords entries.
	for i := 0; i < MaxUnreachableRecords+20; i++ {
		ip := "10.0.0.1"
		u.Add(ip, "flood")
	}

	total := 0
	for _, records := range u.Servers {
		total += len(records)
	}
	if total > MaxUnreachableRecords {
		t.Fatalf("Expected at most %d records, got %d", MaxUnreachableRecords, total)
	}
}

func TestUnreachableServers_StorableMeta(t *testing.T) {
	u := NewUnreachableServers()
	meta := u.StorageMeta()

	if meta.Collection != UnreachableServersStorageCollection {
		t.Fatalf("Expected collection %q, got %q", UnreachableServersStorageCollection, meta.Collection)
	}
	if meta.Key != UnreachableServersStorageKey {
		t.Fatalf("Expected key %q, got %q", UnreachableServersStorageKey, meta.Key)
	}
}

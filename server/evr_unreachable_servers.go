package server

import (
	"context"
	"sync"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	UnreachableServersStorageCollection = "UnreachableServers"
	UnreachableServersStorageKey        = "store"

	// UnreachableServerTTL is the duration after which an unreachable server
	// record expires. 24 hours is long enough to avoid repeatedly sending
	// players to servers they can't reach, but short enough that transient
	// network/routing issues resolve naturally.
	UnreachableServerTTL = 24 * time.Hour

	// MaxUnreachableRecords caps the number of tracked servers per player to
	// prevent unbounded growth.
	MaxUnreachableRecords = 100
)

// UnreachableRecord tracks a single per-player unreachability event for a
// game server, identified by external IP.
type UnreachableRecord struct {
	Timestamp time.Time `json:"timestamp"`
	Reason    string    `json:"reason"`
}

// UnreachableServers stores per-player records of game servers that the
// player failed to connect to. Records expire after UnreachableServerTTL.
// This is NOT a global ban — it only affects the player who experienced the
// failure (network/routing issues are player-specific).
type UnreachableServers struct {
	sync.RWMutex
	// Servers maps external IP -> list of failure records.
	Servers map[string][]UnreachableRecord `json:"servers"`
	version string
}

func NewUnreachableServers() *UnreachableServers {
	return &UnreachableServers{
		Servers: make(map[string][]UnreachableRecord),
	}
}

func (u *UnreachableServers) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection:      UnreachableServersStorageCollection,
		Key:             UnreachableServersStorageKey,
		PermissionRead:  0,
		PermissionWrite: 0,
		Version:         u.version,
	}
}

func (u *UnreachableServers) SetStorageMeta(meta StorableMetadata) {
	u.Lock()
	defer u.Unlock()
	u.version = meta.Version
}

// Add records an unreachable server for this player, pruning expired entries.
func (u *UnreachableServers) Add(extIP string, reason string) {
	u.Lock()
	defer u.Unlock()

	if u.Servers == nil {
		u.Servers = make(map[string][]UnreachableRecord)
	}

	now := time.Now()
	cutoff := now.Add(-UnreachableServerTTL)

	// Prune expired records for this IP.
	records := u.Servers[extIP]
	pruned := make([]UnreachableRecord, 0, len(records)+1)
	for _, r := range records {
		if r.Timestamp.After(cutoff) {
			pruned = append(pruned, r)
		}
	}

	pruned = append(pruned, UnreachableRecord{
		Timestamp: now,
		Reason:    reason,
	})

	u.Servers[extIP] = pruned

	// Global prune if over capacity.
	u.pruneExpiredLocked(cutoff)
}

// IsUnreachable returns true if the server at extIP has a non-expired
// unreachable record for this player.
func (u *UnreachableServers) IsUnreachable(extIP string) bool {
	u.RLock()
	defer u.RUnlock()

	records, ok := u.Servers[extIP]
	if !ok || len(records) == 0 {
		return false
	}

	cutoff := time.Now().Add(-UnreachableServerTTL)
	for _, r := range records {
		if r.Timestamp.After(cutoff) {
			return true
		}
	}
	return false
}

// UnreachableIPs returns the set of external IPs that are currently marked
// unreachable (have at least one non-expired record).
func (u *UnreachableServers) UnreachableIPs() map[string]struct{} {
	u.RLock()
	defer u.RUnlock()

	result := make(map[string]struct{})
	cutoff := time.Now().Add(-UnreachableServerTTL)

	for ip, records := range u.Servers {
		for _, r := range records {
			if r.Timestamp.After(cutoff) {
				result[ip] = struct{}{}
				break
			}
		}
	}
	return result
}

// PruneExpired removes all expired records across all servers.
func (u *UnreachableServers) PruneExpired() {
	u.Lock()
	defer u.Unlock()
	u.pruneExpiredLocked(time.Now().Add(-UnreachableServerTTL))
}

func (u *UnreachableServers) pruneExpiredLocked(cutoff time.Time) {
	for ip, records := range u.Servers {
		pruned := make([]UnreachableRecord, 0, len(records))
		for _, r := range records {
			if r.Timestamp.After(cutoff) {
				pruned = append(pruned, r)
			}
		}
		if len(pruned) == 0 {
			delete(u.Servers, ip)
		} else {
			u.Servers[ip] = pruned
		}
	}

	// If still over capacity, drop the oldest entries globally.
	totalRecords := 0
	for _, records := range u.Servers {
		totalRecords += len(records)
	}
	if totalRecords > MaxUnreachableRecords {
		// Find and remove the oldest records across all IPs until under limit.
		for totalRecords > MaxUnreachableRecords {
			var oldestIP string
			var oldestTime time.Time
			for ip, records := range u.Servers {
				if len(records) > 0 {
					if oldestIP == "" || records[0].Timestamp.Before(oldestTime) {
						oldestIP = ip
						oldestTime = records[0].Timestamp
					}
				}
			}
			if oldestIP == "" {
				break
			}
			u.Servers[oldestIP] = u.Servers[oldestIP][1:]
			if len(u.Servers[oldestIP]) == 0 {
				delete(u.Servers, oldestIP)
			}
			totalRecords--
		}
	}
}

// LoadUnreachableServers loads the per-player unreachable servers from storage.
func LoadUnreachableServers(ctx context.Context, nk runtime.NakamaModule, userID string) (*UnreachableServers, error) {
	u := NewUnreachableServers()
	if err := StorableRead(ctx, nk, userID, u, true); err != nil {
		return nil, err
	}
	u.PruneExpired()
	return u, nil
}

// StoreUnreachableServers persists the per-player unreachable servers to storage.
func StoreUnreachableServers(ctx context.Context, nk runtime.NakamaModule, userID string, u *UnreachableServers) error {
	u.PruneExpired()
	return StorableWrite(ctx, nk, userID, u)
}

// RecordUnreachableServer adds an unreachable server record for the player,
// updates the in-memory session state, and persists to storage. Safe to call
// from event handlers; logs but does not propagate storage write errors.
func RecordUnreachableServer(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, params *SessionParameters, extIP string, reason string) {
	if params == nil || params.unreachableServers == nil {
		return
	}

	u := params.unreachableServers.Load()
	u.Add(extIP, reason)

	logger.WithFields(map[string]any{
		"user_id": userID,
		"ext_ip":  extIP,
		"reason":  reason,
	}).Info("Recorded unreachable server for player")

	if err := StoreUnreachableServers(ctx, nk, userID, u); err != nil {
		logger.WithFields(map[string]any{
			"error":   err.Error(),
			"user_id": userID,
			"ext_ip":  extIP,
		}).Warn("Failed to persist unreachable server record")
	}
}

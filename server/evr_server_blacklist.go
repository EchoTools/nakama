package server

import (
	"context"

	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	ServerBlacklistStorageCollection = "ServerBlacklist"
	ServerBlacklistStorageKey        = "store"
)

// unionBlacklistedIPs reads each user's server blacklist and returns the union
// of their blacklisted external IPs. Reads fail open: a missing record (NotFound)
// contributes nothing, and any other read error is debug-logged then skipped so a
// transient storage hiccup never blocks matchmaking. The returned set is never nil.
func unionBlacklistedIPs(ctx context.Context, nk runtime.NakamaModule, userIDs []string) map[string]struct{} {
	union := make(map[string]struct{})
	for _, userID := range userIDs {
		if userID == "" {
			continue
		}
		bl := NewServerBlacklist()
		if err := StorableRead(ctx, nk, userID, bl, false); err != nil {
			if status.Code(err) != codes.NotFound {
				blacklistDebugLog("unionBlacklistedIPs: failed to read blacklist", userID, err)
			}
			continue
		}
		for ip := range bl.Servers {
			union[ip] = struct{}{}
		}
	}
	return union
}

// loadUserBlacklist reads a single user's server blacklist. It always returns a
// usable (non-nil, nil-map-safe) *ServerBlacklist: on NotFound it returns an empty
// blacklist, and on any other read error it debug-logs and returns empty. Reads are
// fail-open — a storage error must never block a player from matchmaking.
func loadUserBlacklist(ctx context.Context, nk runtime.NakamaModule, userID string) *ServerBlacklist {
	bl := NewServerBlacklist()
	if err := StorableRead(ctx, nk, userID, bl, false); err != nil {
		if status.Code(err) != codes.NotFound {
			blacklistDebugLog("loadUserBlacklist: failed to read blacklist", userID, err)
		}
	}
	return bl
}

// blacklistDebugLog records a non-fatal blacklist read error at debug level.
// Blacklist reads are fail-open, so these are diagnostics, not warnings.
var blacklistDebugLog = func(msg, userID string, err error) {
	zap.L().Debug(msg, zap.String("user_id", userID), zap.Error(err))
}

// ServerBlacklist stores a player's blacklisted game server IPs so they are
// never allocated to those servers during matchmaking
type ServerBlacklist struct {
	Servers map[string]string `json:"servers"` // map[extIP]displayLabel
	version string
}

func NewServerBlacklist() *ServerBlacklist {
	return &ServerBlacklist{Servers: make(map[string]string)}
}

func (b *ServerBlacklist) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection:      ServerBlacklistStorageCollection,
		Key:             ServerBlacklistStorageKey,
		PermissionRead:  0,
		PermissionWrite: 0,
		Version:         b.version,
	}
}

func (b *ServerBlacklist) SetStorageMeta(meta StorableMetadata) {
	b.version = meta.Version
	// A stored {"servers":null} record unmarshals to a nil map. Re-init it so the
	// add path (assignment to a map key) does not panic on the next write.
	if b.Servers == nil {
		b.Servers = make(map[string]string)
	}
}

// IPs returns the list of blacklisted external IPs as a slice
func (b *ServerBlacklist) IPs() []string {
	ips := make([]string, 0, len(b.Servers))
	for ip := range b.Servers {
		ips = append(ips, ip)
	}
	return ips
}

// IPSet returns the blacklisted IPs as a set for lookup (O(1) I think)
func (b *ServerBlacklist) IPSet() map[string]struct{} {
	m := make(map[string]struct{}, len(b.Servers))
	for ip := range b.Servers {
		m[ip] = struct{}{}
	}
	return m
}

package server

const (
	ServerBlacklistStorageCollection = "ServerBlacklist"
	ServerBlacklistStorageKey        = "store"
)

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

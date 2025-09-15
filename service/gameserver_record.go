package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	StorageCollectionGameServers = "game_servers"
	StorageIndexGameServer       = "game_server_idx"
)

var _ = StorableAdapter(&GameServerRecord{})

type GameServerRecord struct {
	ServerID      string            `json:"server_id"`
	ExternalIP    string            `json:"external_ip"`
	InternalIP    string            `json:"internal_ip"`
	Port          uint16            `json:"port"`
	Region        string            `json:"region"`
	Version       string            `json:"version"`
	IsNative      bool              `json:"is_native"`
	GroupIDs      []string          `json:"group_ids"`
	Tags          []string          `json:"tags"`
	Features      []string          `json:"supported_features"`
	LastSeen      time.Time         `json:"last_seen"`
	RegisteredAt  time.Time         `json:"registered_at"`
	OperatorID    string            `json:"user_id"`
	SessionID     string            `json:"session_id"`
	TimeStepUsecs uint32            `json:"time_step_usecs"`
	Metadata      map[string]string `json:"metadata"`
	version       string
}

func (g *GameServerRecord) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection:      "game_servers",
		Key:             g.ServerID,
		UserID:          SystemUserID,
		PermissionRead:  runtime.STORAGE_PERMISSION_OWNER_READ,
		PermissionWrite: runtime.STORAGE_PERMISSION_OWNER_WRITE,
		Version:         g.version,
	}
}

func (g *GameServerRecord) SetStorageMeta(meta StorableMetadata) {
	g.version = meta.Version
}

// GuildGameServerList retrieves a list of game servers associated with the provided group IDs and query.
func GuildGameServerList(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, callerID string, query string) (json.RawMessage, error) {
	if query == "" {
		query = "*"
	}

	order := []string{}

	var (
		cursor  = ""
		records []string
	)
	for {
		objs, nextCursor, err := nk.StorageIndexList(ctx, callerID, StorageIndexGameServer, query, 100, order, cursor)
		if err != nil {
			return nil, fmt.Errorf("failed to read storage objects: %w", err)
		}
		for _, obj := range objs.GetObjects() {
			records = append(records, obj.GetValue())
		}
		if nextCursor == "" || nextCursor == cursor {
			break
		}
		cursor = nextCursor
	}

	return json.Marshal(records)
}

func JoinJsonSlice(elems []string) string {
	result := "["
	for i, elem := range elems {
		result += fmt.Sprintf(`"%s"`, elem)
		if i < len(elems)-1 {
			result += ","
		}
	}
	result += "]"
	return result
}

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	StorageCollectionGuildGroupState = "GuildGroupState"
	StorageIndexGuildGroupState      = "guild_group_state_index"
)

// This allows the system to operate correctly even when discord is down
type GuildGroupState struct {
	sync.RWMutex                       // for storage operations
	GroupID        string              `json:"group_id"`
	RoleCache      RoleCacheMap        `json:"role_cache"`      // map[RoleID]map[UserID]struct{}
	SuspendedXPIDs map[evr.XPID]string `json:"suspended_xpids"` // map[XPID]UserID
	RulesText      string              `json:"rules_text"`      // The rules text displayed on the main menu

	updated bool
	version string
}

func (s *GuildGroupState) hasRole(userID, role string) bool {
	if s.RoleCache == nil {
		return false
	}
	if userIDs, ok := s.RoleCache[role]; ok {
		_, found := userIDs[userID]
		return found
	}
	return false
}

// CreateStorableAdapter creates a StorableAdapter for GuildGroupState
func (s *GuildGroupState) StorageMeta() StorableMetadata {
	s.RLock()
	defer s.RUnlock()
	return StorableMetadata{
		Collection:      StorageCollectionGuildGroupState,
		Key:             s.GroupID,
		PermissionRead:  0,
		PermissionWrite: 0,
		Version:         s.version,
	}
}

func (s *GuildGroupState) SetStorageMeta(meta StorableMetadata) {
	s.Lock()
	defer s.Unlock()
	s.version = meta.Version
	s.updated = false
}

func (s *GuildGroupState) StorageIndexes() []StorableIndexMeta {
	return []StorableIndexMeta{
		{
			Name:           StorageIndexGuildGroupState,
			Collection:     StorageCollectionGuildGroupState,
			Key:            "", // Empty key means index applies to all keys in collection
			Fields:         []string{"suspended_xpids", "role_cache"},
			SortableFields: []string{"_key"},
			MaxEntries:     10000,
			IndexOnly:      false,
		},
	}
}

func GuildGroupStateLoad(ctx context.Context, nk runtime.NakamaModule, botUserID, groupID string) (*GuildGroupState, error) {
	var (
		err   error
		state = &GuildGroupState{GroupID: groupID}
	)
	if err = StorableReadNk(ctx, nk, botUserID, state, true); err != nil {
		return nil, err
	}
	state.GroupID = groupID
	return state, nil
}

func GuildGroupStateSave(ctx context.Context, nk runtime.NakamaModule, botUserID string, state *GuildGroupState) error {
	// Store the State
	err := StorableWriteNk(ctx, nk, ServiceSettings().DiscordBotUserID, state)
	if err != nil {
		return fmt.Errorf("failed to write guild group state: %v", err)
	}

	return nil
}

type RoleCacheMap map[string]map[string]struct{} // map[RoleID]map[UserID]struct{}

func (m *RoleCacheMap) Add(userID string, roles ...string) (updated bool) {
	if *m == nil {
		*m = make(RoleCacheMap)
		updated = true
	}
	for _, role := range roles {
		if (*m)[role] == nil {
			(*m)[role] = make(map[string]struct{})
			updated = true
		}
		if _, exists := (*m)[role][userID]; !exists {
			(*m)[role][userID] = struct{}{}
			updated = true
		}
	}
	return updated
}

func (m *RoleCacheMap) Remove(userID string, roles ...string) (updated bool) {
	if *m == nil {
		return false
	}
	for _, role := range roles {
		if (*m)[role] != nil {
			if _, exists := (*m)[role][userID]; exists {
				delete((*m)[role], userID)
				updated = true
			}
			if len((*m)[role]) == 0 {
				delete(*m, role)
				updated = true
			}
		}
	}
	return updated
}

func GuildGroupGetIDs(ctx context.Context, nk runtime.NakamaModule, botUserID string, groupIDs []string) (map[string]*GuildGroupState, error) {
	query := fmt.Sprintf("+_key:/(%s)/", strings.Join(groupIDs, "|"))

	var (
		limit   = min(100, len(groupIDs))
		cursor  = ""
		allObjs = make([]*api.StorageObject, 0, len(groupIDs))
		order   = []string{"_key"}
		err     error
		objs    *api.StorageObjects
	)
	for {
		objs, cursor, err = nk.StorageIndexList(ctx, botUserID, StorageIndexGuildGroupState, query, limit, order, cursor)
		if err != nil {
			return nil, fmt.Errorf("failed to list guild group states: %v", err)
		}
		allObjs = append(allObjs, objs.GetObjects()...)
		if cursor == "" {
			break
		}
	}

	result := make(map[string]*GuildGroupState, len(allObjs))
	for _, obj := range allObjs {
		var state GuildGroupState
		if err := json.Unmarshal([]byte(obj.GetValue()), &state); err != nil {
			return nil, fmt.Errorf("failed to unmarshal guild group state: %v", err)
		}
		state.GroupID = obj.Key
		state.version = obj.Version
		result[obj.Key] = &state
	}
	return result, nil
}

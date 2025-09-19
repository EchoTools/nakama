package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

const (
	StorageCollectionState = "GuildGroupState"
)

// This allows the system to operate correctly even when discord is down
type GuildGroupState struct {
	sync.RWMutex                              // for storage operations
	GroupID        string                     `json:"group_id"`
	RoleCache      map[string]map[string]bool `json:"role_cache"`        // map[RoleID]map[UserID]struct{}
	SuspendedXPIDs map[evr.EvrId]string       `json:"suspended_devices"` // map[XPID]UserID
	RulesText      string                     `json:"rules_text"`        // The rules text displayed on the main menu

	updated bool
	version string
}

func (s *GuildGroupState) UnmarshalJSON(data []byte) error {
	// Handle RoleCache migration from map[string]map[string]struct{} to map[string]map[string]bool
	type Alias GuildGroupState
	aux := &struct {
		RoleCache map[string]map[string]any `json:"role_cache"`
		*Alias
	}{
		Alias: (*Alias)(s),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if aux.RoleCache != nil {
		s.RoleCache = make(map[string]map[string]bool, len(aux.RoleCache))
		for roleID, userMap := range aux.RoleCache {
			boolMap := make(map[string]bool, len(userMap))
			for userID := range userMap {
				boolMap[userID] = true
			}
			s.RoleCache[roleID] = boolMap
		}
	}
	return nil
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
		Collection:      StorageCollectionState,
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

func GuildGroupStatesLoad(ctx context.Context, nk runtime.NakamaModule, botUserID string, groupIDs []string) ([]*GuildGroupState, error) {

	reads := make([]*runtime.StorageRead, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		if groupID == "" {
			continue
		}
		reads = append(reads, &runtime.StorageRead{
			Collection: StorageCollectionState,
			Key:        groupID,
			UserID:     botUserID,
		})
	}
	// Read all of the states at once
	objs, err := nk.StorageRead(ctx, reads)
	if err != nil {
		return nil, fmt.Errorf("failed to read guild group states: %v", err)
	}
	states := make([]*GuildGroupState, 0, len(groupIDs))
	for _, obj := range objs {
		state := &GuildGroupState{}
		if err := json.Unmarshal([]byte(obj.Value), state); err != nil {
			return nil, fmt.Errorf("failed to unmarshal guild group state: %v", err)
		}
		state.version = obj.Version
		states = append(states, state)
	}
	return states, nil
}

func GuildGroupStateLoad(ctx context.Context, nk runtime.NakamaModule, botUserID, groupID string) (*GuildGroupState, error) {
	var (
		err   error
		state = &GuildGroupState{GroupID: groupID}
	)
	if err = StorableRead(ctx, nk, botUserID, state, true); err != nil {
		return nil, err
	}
	state.GroupID = groupID
	return state, nil
}

func GuildGroupStateSave(ctx context.Context, nk runtime.NakamaModule, botUserID string, state *GuildGroupState) error {
	// Store the State
	err := StorableWrite(ctx, nk, ServiceSettings().DiscordBotUserID, state)
	if err != nil {
		return fmt.Errorf("failed to write guild group state: %v", err)
	}

	return nil
}

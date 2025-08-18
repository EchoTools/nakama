package server

import (
	"context"
	"fmt"
	"sync"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	StorageCollectionState = "GuildGroupState"
)

// This allows the system to operate correctly even when discord is down
type GuildGroupState struct {
	sync.RWMutex                                  // for storage operations
	GroupID        string                         `json:"group_id"`
	RoleCache      map[string]map[string]struct{} `json:"role_cache"`        // map[RoleID]map[UserID]struct{}
	SuspendedXPIDs map[evr.EvrId]string           `json:"suspended_devices"` // map[XPID]UserID
	RulesText      string                         `json:"rules_text"`        // The rules text displayed on the main menu

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

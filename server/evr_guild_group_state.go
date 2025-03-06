package server

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

const (
	StorageCollectionState = "GuildGroupState"
)

// This allows the system to operate correctly even when discord is down
type GuildGroupState struct {
	sync.RWMutex                                           // for storage operations
	GroupID                 string                         `json:"group_id"`
	TimedOutUserIDs         map[string]time.Time           `json:"timed_out_user_ids"`         // UserIDs that are required to go to community values when the first join the social lobby
	CommunityValuesUserIDs  map[string]time.Time           `json:"community_values_user_ids"`  // UserIDs that are required to go to community values when the first join the social lobby
	RoleCache               map[string]map[string]struct{} `json:"role_cache"`                 // map[RoleID]map[UserID]struct{}
	SuspendedXPIDs          map[evr.EvrId]string           `json:"suspended_devices"`          // map[XPID]UserID
	NegatedModeratorUserIDs []string                       `json:"negated_moderator_user_ids"` // UserIDs that are negated from moderator roles
	RulesText               string                         `json:"rules_text"`                 // The rules text displayed on the main menu

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

func (s *GuildGroupState) StorageID() StorageID {
	return StorageID{Collection: StorageCollectionState, Key: s.GroupID}
}

func (s *GuildGroupState) StorageIndex() *StorageIndexMeta {
	return nil
}

func GuildGroupStateLoad(ctx context.Context, nk runtime.NakamaModule, botUserID, groupID string) (*GuildGroupState, error) {
	state := &GuildGroupState{GroupID: groupID}
	version, err := StorageRead(ctx, nk, botUserID, state, true)
	if err != nil {
		return nil, err
	}
	state.GroupID = groupID
	state.version = version
	return state, nil
}

func GuildGroupStateSave(ctx context.Context, nk runtime.NakamaModule, botUserID string, state *GuildGroupState) error {
	// Store the State
	version, err := StorageWrite(ctx, nk, ServiceSettings().DiscordBotUserID, state)
	if err != nil {
		return fmt.Errorf("failed to write guild group state: %v", err)
	}

	state.version = version

	return nil
}

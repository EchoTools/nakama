package server

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

type ctxGuildGroupMembershipsKey struct{}

type GuildGroupRoles struct {
	Member           string `json:"member"`
	Moderator        string `json:"moderator"`
	ServerHost       string `json:"server_host"`
	Allocator        string `json:"allocator"`
	Suspended        string `json:"suspended"`
	APIAccess        string `json:"api_access"`
	AccountAgeBypass string `json:"account_age_bypass"`
	AccountLinked    string `json:"headset_linked"`
}

// Roles returns a slice of role IDs
func (r *GuildGroupRoles) Slice() []string {
	return []string{
		r.Member,
		r.Moderator,
		r.ServerHost,
		r.Allocator,
		r.Suspended,
		r.APIAccess,
		r.AccountAgeBypass,
		r.AccountLinked,
	}
}

func MigrateGroupRoles(nk runtime.NakamaModule, group *api.Group) error {
	metamap := make(map[string]interface{})
	if err := json.Unmarshal([]byte(group.Metadata), &metamap); err != nil {
		return err
	}

	if metamap["roles"] != nil {
		return nil
	}

	metadata := &GroupMetadata{}
	if err := json.Unmarshal([]byte(group.Metadata), metadata); err != nil {
		return err
	}

	memberRole, ok := metamap["member_role"].(string)
	if !ok {
		memberRole = ""
	}
	moderatorRole, ok := metamap["moderator_role"].(string)
	if !ok {
		moderatorRole = ""
	}
	serverHostRole, ok := metamap["serverhost_role"].(string)
	if !ok {
		serverHostRole = ""
	}
	allocatorRole, ok := metamap["allocator_role"].(string)
	if !ok {
		allocatorRole = ""
	}
	suspendedRole, ok := metamap["suspension_role"].(string)
	if !ok {
		suspendedRole = ""
	}
	apiAccessRole, ok := metamap["api_access_role"].(string)
	if !ok {
		apiAccessRole = ""
	}
	accountAgeBypassRole, ok := metamap["minimum_account_age_bypass_role"].(string)
	if !ok {
		accountAgeBypassRole = ""
	}
	accountLinkedRole, ok := metamap["account_linked_role"].(string)
	if !ok {
		accountLinkedRole = ""
	}

	metadata.Roles = &GuildGroupRoles{
		Member:           memberRole,
		Moderator:        moderatorRole,
		ServerHost:       serverHostRole,
		Allocator:        allocatorRole,
		Suspended:        suspendedRole,
		APIAccess:        apiAccessRole,
		AccountAgeBypass: accountAgeBypassRole,
		AccountLinked:    accountLinkedRole,
	}

	err := nk.GroupUpdate(context.Background(), group.Id, "", group.Name, group.CreatorId, group.LangTag, group.Description, group.AvatarUrl, false, metadata.MarshalMap(), int(group.MaxCount))
	if err != nil {
		return err
	}
	return nil
}

type GuildGroupMemberships []GuildGroupMembership

func (g GuildGroupMemberships) IsMember(groupID string) bool {
	for _, m := range g {
		if m.GuildGroup.ID().String() == groupID {
			return m.isMember
		}
	}
	return false
}

type GroupMetadata struct {
	GuildID                      string                 `json:"guild_id"`                        // The guild ID
	RulesText                    string                 `json:"rules_text"`                      // The rules text displayed on the main menu
	MinimumAccountAgeDays        int                    `json:"minimum_account_age_days"`        // The minimum account age in days to be able to play echo on this guild's sessions
	MembersOnlyMatchmaking       bool                   `json:"members_only_matchmaking"`        // Restrict matchmaking to members only (when this group is the active one)
	DisablePublicAllocateCommand bool                   `json:"disable_public_allocate_command"` // Disable the public allocate command
	Roles                        *GuildGroupRoles       `json:"roles"`                           // The roles text displayed on the main menu
	RoleCache                    map[string][]string    `json:"role_cache"`                      // The role cache
	MatchmakingChannelIDs        map[evr.Symbol]string  `json:"matchmaking_channel_ids"`         // The matchmaking channel IDs
	DebugChannel                 string                 `json:"debug_channel_id"`                // The debug channel
	Unhandled                    map[string]interface{} `json:"-"`
}

func NewGuildGroupMetadata(guildID string) *GroupMetadata {
	return &GroupMetadata{
		GuildID:               guildID,
		RoleCache:             make(map[string][]string),
		Roles:                 &GuildGroupRoles{},
		MatchmakingChannelIDs: make(map[evr.Symbol]string),
	}
}

func (g *GroupMetadata) MarshalMap() map[string]any {
	m := make(map[string]any)
	data, _ := json.Marshal(g)
	_ = json.Unmarshal(data, &m)
	return m
}

type GuildGroup struct {
	Metadata *GroupMetadata
	Group    *api.Group
}

func (g *GuildGroup) GuildID() string {
	return g.Metadata.GuildID
}

func (g *GuildGroup) Name() string {
	return g.Group.Name
}

func (g *GuildGroup) Description() string {
	return g.Group.Description
}

func (g *GuildGroup) ID() uuid.UUID {
	return uuid.FromStringOrNil(g.Group.Id)
}

func (g *GuildGroup) Size() int {
	return int(g.Group.EdgeCount)
}

func (g *GuildGroup) ServerHostUserIDs() []string {
	return g.Metadata.RoleCache[g.Metadata.Roles.ServerHost]
}

func (g *GuildGroup) AllocatorUserIDs() []string {
	return g.Metadata.RoleCache[g.Metadata.Roles.Allocator]
}

func (g *GuildGroup) ModeratorUserIDs() []string {
	return g.Metadata.RoleCache[g.Metadata.Roles.Moderator]
}

func (g *GuildGroup) SuspendedUserIDs() []string {
	return g.Metadata.RoleCache[g.Metadata.Roles.Suspended]
}

func (g *GuildGroup) IsServerHost(userID string) bool {
	return slices.Contains(g.ServerHostUserIDs(), userID)
}

func (g *GuildGroup) IsAllocator(userID string) bool {
	return slices.Contains(g.AllocatorUserIDs(), userID)
}

func (g *GuildGroup) IsModerator(userID string) bool {
	return slices.Contains(g.ModeratorUserIDs(), userID)
}

func (g *GuildGroup) IsSuspended(userID string) bool {
	return slices.Contains(g.SuspendedUserIDs(), userID)
}

func (g *GuildGroup) IsAPIAccess(userID string) bool {
	return slices.Contains(g.Metadata.RoleCache[g.Metadata.Roles.APIAccess], userID)
}

func (g *GuildGroup) IsAccountAgeBypass(userID string) bool {
	return slices.Contains(g.Metadata.RoleCache[g.Metadata.Roles.AccountAgeBypass], userID)
}
func (g *GuildGroup) IsAccountLinked(userID string) bool {
	return slices.Contains(g.Metadata.RoleCache[g.Metadata.Roles.AccountLinked], userID)
}

func (g *GuildGroup) UpdateRoleCache(userID string, roles []string) bool {
	roleCacheUpdated := false
	roleCache := g.Metadata.RoleCache
	for role, userIDs := range roleCache {
		if slices.Contains(roles, role) {
			if !slices.Contains(userIDs, userID) {
				g.Metadata.RoleCache[role] = append(userIDs, userID)
				roleCacheUpdated = true
			}
		} else {
			if slices.Contains(userIDs, userID) {
				g.Metadata.RoleCache[role] = slices.DeleteFunc(userIDs, func(s string) bool {
					return s == userID
				})
				roleCacheUpdated = true
			}
		}
	}
	if roleCacheUpdated {
		g.Metadata.RoleCache = roleCache
	}
	return roleCacheUpdated
}

func NewGuildGroup(group *api.Group) *GuildGroup {

	md := &GroupMetadata{}
	if err := json.Unmarshal([]byte(group.Metadata), md); err != nil {
		return nil
	}

	// Ensure the matchmaking channel IDs have been initialized
	if md.MatchmakingChannelIDs == nil {
		md.MatchmakingChannelIDs = make(map[evr.Symbol]string)
	}

	return &GuildGroup{
		Metadata: md,
		Group:    group,
	}
}

type GuildGroupMembership struct {
	GuildGroup   GuildGroup
	isMember     bool
	isModerator  bool // Admin
	isServerHost bool // Broadcaster Host
	isAllocator  bool // Can allocate servers with slash command
	isSuspended  bool
}

func NewGuildGroupMembership(group *api.Group, userID uuid.UUID, state api.UserGroupList_UserGroup_State) GuildGroupMembership {
	gg := NewGuildGroup(group)

	return GuildGroupMembership{
		GuildGroup:   *gg,
		isMember:     state <= api.UserGroupList_UserGroup_MEMBER,
		isModerator:  state <= api.UserGroupList_UserGroup_ADMIN,
		isServerHost: slices.Contains(gg.ServerHostUserIDs(), userID.String()),
		isAllocator:  slices.Contains(gg.AllocatorUserIDs(), userID.String()),
		isSuspended:  slices.Contains(gg.SuspendedUserIDs(), userID.String()),
	}
}

package server

import (
	"encoding/json"
	"slices"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
)

type GuildGroupMemberships []GuildGroupMembership

type GroupMetadata struct {
	GuildID                      string                 `json:"guild_id"`                        // The guild ID
	RulesText                    string                 `json:"rules_text"`                      // The rules text displayed on the main menu
	MemberRole                   string                 `json:"member_role"`                     // The role that has access to create lobbies/matches and join social lobbies
	ModeratorRole                string                 `json:"moderator_role"`                  // The rules that have access to moderation tools
	ServerHostRole               string                 `json:"serverhost_role"`                 // The rules that have access to serverdb
	AccountAgeBypassRole         string                 `json:"minimum_account_age_bypass_role"` // The role that bypasses the minimum account age
	AllocatorRole                string                 `json:"allocator_role"`                  // The rules that have access to reserve servers
	SuspensionRole               string                 `json:"suspension_role"`                 // The roles that have users suspended
	IsLinkedRole                 string                 `json:"is_linked_role"`                  // The role that denotes if an account currently has a linked headset
	MinimumAccountAgeDays        int                    `json:"minimum_account_age_days"`        // The minimum account age in days to be able to play echo on this guild's sessions
	ServerHostUserIDs            []string               `json:"serverhost_user_ids"`             // The broadcaster hosts
	AllocatorUserIDs             []string               `json:"allocator_user_ids"`              // The allocator hosts
	ModeratorUserIDs             []string               `json:"moderator_user_ids"`              // The moderators
	SuspendedUserIDs             []string               `json:"suspended_user_ids"`              // The suspended users
	AccountAgeBypassUserIDs      []string               `json:"account_age_bypass_user_ids"`     // The users that bypass the minimum account age
	ArenaMatchmakingChannelID    string                 `json:"arena_matchmaking_channel_id"`    // The matchmaking channel
	CombatMatchmakingChannelID   string                 `json:"combat_matchmaking_channel_id"`   // The matchmaking channel
	DebugChannel                 string                 `json:"debug_channel_id"`                // The debug channel
	MembersOnlyMatchmaking       bool                   `json:"members_only_matchmaking"`        // Restrict matchmaking to members only (when this group is the active one)
	DisablePublicAllocateCommand bool                   `json:"disable_public_allocate_command"` // Disable the public allocate command
	Unhandled                    map[string]interface{} `json:"-"`
}

type GuildGroup struct {
	Metadata GroupMetadata
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
	return g.Metadata.ServerHostUserIDs
}

func (g *GuildGroup) AllocatorUserIDs() []string {
	return g.Metadata.AllocatorUserIDs
}

func (g *GuildGroup) ModeratorUserIDs() []string {
	return g.Metadata.ModeratorUserIDs
}

func (g *GuildGroup) SuspendedUserIDs() []string {
	return g.Metadata.SuspendedUserIDs
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

func NewGuildGroup(group *api.Group) *GuildGroup {

	md := &GroupMetadata{}
	if err := json.Unmarshal([]byte(group.Metadata), md); err != nil {
		return nil
	}

	return &GuildGroup{
		Metadata: *md,
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

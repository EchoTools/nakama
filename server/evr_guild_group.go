package server

import (
	"encoding/json"
	"slices"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
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
	VPNBypass        string `json:"vpn_bypass"`
	AccountLinked    string `json:"headset_linked"`
}

// Roles returns a slice of role IDs
func (r *GuildGroupRoles) Slice() []string {
	roles := make([]string, 0)
	for _, r := range []string{
		r.Member,
		r.Moderator,
		r.ServerHost,
		r.Allocator,
		r.Suspended,
		r.APIAccess,
		r.AccountAgeBypass,
		r.VPNBypass,
		r.AccountLinked,
	} {
		if r != "" {
			roles = append(roles, r)
		}
	}
	slices.Sort(roles)
	return slices.Compact(roles)
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
	GuildID                string              `json:"guild_id"`                 // The guild ID
	RulesText              string              `json:"rules_text"`               // The rules text displayed on the main menu
	MinimumAccountAgeDays  int                 `json:"minimum_account_age_days"` // The minimum account age in days to be able to play echo on this guild's sessions
	MembersOnlyMatchmaking bool                `json:"members_only_matchmaking"` // Restrict matchmaking to members only (when this group is the active one)
	DisableCreateCommand   bool                `json:"disable_create_command"`   // Disable the public allocate command
	Roles                  *GuildGroupRoles    `json:"roles"`                    // The roles text displayed on the main menu
	RoleCache              map[string][]string `json:"role_cache"`               // The role cache
	MatchmakingChannelIDs  map[string]string   `json:"matchmaking_channel_ids"`  // The matchmaking channel IDs
	DebugChannelID         string              `json:"debug_channel_id"`         // The debug channel
	AuditChannelID         string              `json:"audit_channel_id"`         // The audit channel
	BlockVPNUsers          bool                `json:"block_vpn_users"`          // Block VPN users
	FraudScoreThreshold    int                 `json:"fraud_score_threshold"`    // The fraud score threshold
	AllowedFeatures        []string            `json:"allowed_features"`         // Allowed features

	// UserIDs that are required to go to community values when the first join the social lobby
	CommunityValuesUserIDs []string `json:"community_values_user_ids"`

	Unhandled map[string]interface{} `json:"-"`
}

func NewGuildGroupMetadata(guildID string) *GroupMetadata {
	return &GroupMetadata{
		GuildID:               guildID,
		RoleCache:             make(map[string][]string),
		Roles:                 &GuildGroupRoles{},
		MatchmakingChannelIDs: make(map[string]string),
	}
}

func (g *GroupMetadata) MarshalMap() map[string]any {
	m := make(map[string]any)
	data, _ := json.Marshal(g)
	_ = json.Unmarshal(data, &m)
	return m
}

func (g *GroupMetadata) IsServerHost(userID string) bool {
	if userIDs, ok := g.RoleCache[g.Roles.ServerHost]; ok {
		return slices.Contains(userIDs, userID)
	}
	return false
}

func (g *GroupMetadata) IsAllocator(userID string) bool {
	if userIDs, ok := g.RoleCache[g.Roles.Allocator]; ok {
		return slices.Contains(userIDs, userID)
	}
	return false
}

func (g *GroupMetadata) IsModerator(userID string) bool {
	if userIDs, ok := g.RoleCache[g.Roles.Moderator]; ok {
		return slices.Contains(userIDs, userID)
	}
	return false
}

func (g *GroupMetadata) IsSuspended(userID string) bool {
	if userIDs, ok := g.RoleCache[g.Roles.Suspended]; ok {
		return slices.Contains(userIDs, userID)
	}
	return false
}

func (m *GroupMetadata) IsAPIAccess(userID string) bool {
	if userIDs, ok := m.RoleCache[m.Roles.APIAccess]; ok {
		return slices.Contains(userIDs, userID)
	}
	return false
}

func (m *GroupMetadata) IsAccountAgeBypass(userID string) bool {
	if userIDs, ok := m.RoleCache[m.Roles.AccountAgeBypass]; ok {
		return slices.Contains(userIDs, userID)
	}
	return false
}

func (m *GroupMetadata) IsVPNBypass(userID string) bool {
	if userIDs, ok := m.RoleCache[m.Roles.VPNBypass]; ok {
		return slices.Contains(userIDs, userID)
	}
	return false
}

func (m *GroupMetadata) IsAccountLinked(userID string) bool {
	if userIDs, ok := m.RoleCache[m.Roles.AccountLinked]; ok {
		return slices.Contains(userIDs, userID)
	}
	return false
}

func (m *GroupMetadata) IsAllowedFeature(feature string) bool {
	return slices.Contains(m.AllowedFeatures, feature)
}

func (m *GroupMetadata) hasCompletedCommunityValues(userID string) bool {
	return !slices.Contains(m.CommunityValuesUserIDs, userID)
}

func (m *GroupMetadata) CommunityValuesUserIDsAdd(userID string) {
	if m.CommunityValuesUserIDs == nil {
		m.CommunityValuesUserIDs = make([]string, 0)
	}
	if slices.Contains(m.CommunityValuesUserIDs, userID) {
		return
	}
	m.CommunityValuesUserIDs = append(m.CommunityValuesUserIDs, userID)
}

func (m *GroupMetadata) CommunityValuesUserIDsRemove(userID string) bool {
	if !slices.Contains(m.CommunityValuesUserIDs, userID) {
		return false
	}
	for i, id := range m.CommunityValuesUserIDs {
		if id == userID {
			m.CommunityValuesUserIDs = append(m.CommunityValuesUserIDs[:i], m.CommunityValuesUserIDs[i+1:]...)
			return true
		}
	}
	return false
}

func (g *GroupMetadata) UpdateRoleCache(userID string, userRoles []string) bool {
	roleCacheUpdated := false

	if g.RoleCache == nil {
		g.RoleCache = make(map[string][]string)
		roleCacheUpdated = true
	}

	roles := g.Roles.Slice()

	for role, _ := range g.RoleCache {
		if !slices.Contains(roles, role) {
			delete(g.RoleCache, role)
			roleCacheUpdated = true
		}
	}

	for _, role := range roles {
		if _, ok := g.RoleCache[role]; !ok {
			g.RoleCache[role] = make([]string, 0)
			roleCacheUpdated = true
		}
	}

	for role, userIDs := range g.RoleCache {
		if slices.Contains(userRoles, role) {
			if !slices.Contains(userIDs, userID) {
				g.RoleCache[role] = append(userIDs, userID)
				roleCacheUpdated = true
			}
		} else {
			if slices.Contains(userIDs, userID) {
				g.RoleCache[role] = slices.DeleteFunc(userIDs, func(s string) bool {
					return s == userID
				})
				roleCacheUpdated = true
			}
		}
	}

	return roleCacheUpdated
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

func NewGuildGroup(group *api.Group) (*GuildGroup, error) {

	md := &GroupMetadata{}
	if err := json.Unmarshal([]byte(group.Metadata), md); err != nil {
		return nil, err
	}

	// Ensure the matchmaking channel IDs have been initialized
	if md.MatchmakingChannelIDs == nil {
		md.MatchmakingChannelIDs = make(map[string]string)
	}

	return &GuildGroup{
		Metadata: md,
		Group:    group,
	}, nil
}

type GuildGroupMembership struct {
	GuildGroup   GuildGroup
	isMember     bool
	isModerator  bool // Admin
	isServerHost bool // Broadcaster Host
	isAllocator  bool // Can allocate servers with slash command
	isSuspended  bool
}

func NewGuildGroupMembership(group *api.Group, userID uuid.UUID, state api.UserGroupList_UserGroup_State) (*GuildGroupMembership, error) {
	gg, err := NewGuildGroup(group)
	if err != nil {
		return nil, err
	}
	userIDStr := userID.String()
	return &GuildGroupMembership{
		GuildGroup:   *gg,
		isMember:     state <= api.UserGroupList_UserGroup_MEMBER,
		isModerator:  gg.Metadata.IsModerator(userIDStr),
		isServerHost: gg.Metadata.IsServerHost(userIDStr),
		isAllocator:  gg.Metadata.IsAllocator(userIDStr),
		isSuspended:  gg.Metadata.IsSuspended(userIDStr),
	}, nil
}

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/bits-and-blooms/bitset"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/samber/lo"
)

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
func (r *GuildGroupRoles) AsSlice() []string {
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

type GuildGroupRoleCache struct {
	Member           []string `json:"member"`
	Moderator        []string `json:"moderator"`
	ServerHost       []string `json:"server_host"`
	Allocator        []string `json:"allocator"`
	Suspended        []string `json:"suspended"`
	APIAccess        []string `json:"api_access"`
	AccountAgeBypass []string `json:"account_age_bypass"`
	VPNBypass        []string `json:"vpn_bypass"`
}

type GuildGroupMemberships map[string]GuildGroupMembership

func (g GuildGroupMemberships) IsMember(groupID string) bool {
	_, ok := g[groupID]
	return ok
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
	ErrorChannelID         string              `json:"error_channel_id"`         // The error channel
	BlockVPNUsers          bool                `json:"block_vpn_users"`          // Block VPN users
	FraudScoreThreshold    int                 `json:"fraud_score_threshold"`    // The fraud score threshold
	AllowedFeatures        []string            `json:"allowed_features"`         // Allowed features
	LogAlternateAccounts   bool                `json:"log_alternate_accounts"`   // Log alternate accounts

	// UserIDs that are required to go to community values when the first join the social lobby
	CommunityValuesUserIDs []string `json:"community_values_user_ids"`
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

func (m *GroupMetadata) IsAllowedMatchmaking(userID string) bool {
	if !m.MembersOnlyMatchmaking {
		return true
	}

	if userIDs, ok := m.RoleCache[m.Roles.Member]; ok {
		return slices.Contains(userIDs, userID)
	}

	return false
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

func (g *GroupMetadata) MarshalToMap() (map[string]interface{}, error) {
	guildGroupBytes, err := json.Marshal(g)
	if err != nil {
		return nil, err
	}

	var guildGroupMap map[string]interface{}
	err = json.Unmarshal(guildGroupBytes, &guildGroupMap)
	if err != nil {
		return nil, err
	}

	return guildGroupMap, nil
}

func UnmarshalGuildGroupMetadataFromMap(guildGroupMap map[string]interface{}) (*GroupMetadata, error) {
	guildGroupBytes, err := json.Marshal(guildGroupMap)
	if err != nil {
		return nil, err
	}

	var g GroupMetadata
	err = json.Unmarshal(guildGroupBytes, &g)
	if err != nil {
		return nil, err
	}

	return &g, nil
}

type GuildGroup struct {
	GroupMetadata
	Group *api.Group
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
		GroupMetadata: *md,
		Group:         group,
	}, nil
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

// Roles returns a slice of role IDs
func (g *GuildGroup) RolesUserList(userID string) []string {
	roles := make([]string, 0, len(g.RoleCache))
	for role, userIDs := range g.RoleCache {
		if slices.Contains(userIDs, userID) {
			roles = append(roles, role)
		}
	}
	return roles
}

func (g *GuildGroup) PermissionsUser(userID string) *GuildGroupMembership {
	return &GuildGroupMembership{
		IsAllowedMatchmaking: g.IsAllowedMatchmaking(userID),
		IsModerator:          g.IsModerator(userID),
		IsServerHost:         g.IsServerHost(userID),
		IsAllocator:          g.IsAllocator(userID),
		IsSuspended:          g.IsSuspended(userID),
		IsAPIAccess:          g.IsAPIAccess(userID),
		IsAccountAgeBypass:   g.IsAccountAgeBypass(userID),
		IsVPNBypass:          g.IsVPNBypass(userID),
		IsHeadsetLinked:      g.IsAccountLinked(userID),
	}
}

func (g *GuildGroup) RolesCacheUpdate(userID string, current []string) bool {

	// Only cache relavent roles.
	current = lo.Intersect(current, g.Roles.AsSlice())

	removals, additions := lo.Difference(g.RolesUserList(userID), current)

	if len(removals) == 0 && len(additions) == 0 {
		return false
	}

	for _, role := range removals {
		i := slices.Index(g.RoleCache[role], userID)
		if i == -1 {
			continue
		}
		g.RoleCache[role] = append(g.RoleCache[role][:i], g.RoleCache[role][i+1:]...)
	}

	for _, role := range additions {
		g.RoleCache[role] = append(g.RoleCache[role], userID)
	}

	return true
}

type GuildGroupMembership struct {
	IsAllowedMatchmaking bool
	IsModerator          bool // Admin
	IsServerHost         bool // Broadcaster Host
	IsAllocator          bool // Can allocate servers with slash command
	IsSuspended          bool
	IsAPIAccess          bool
	IsAccountAgeBypass   bool
	IsVPNBypass          bool
	IsHeadsetLinked      bool
}

func (m *GuildGroupMembership) ToUint64() uint64 {
	var i uint64 = 0
	b := m.asBitSet()
	if len(b.Bytes()) == 0 {
		return i
	}
	return uint64(b.Bytes()[0])
}

func (m *GuildGroupMembership) FromUint64(v uint64) {
	b := bitset.From([]uint64{v})
	m.FromBitSet(b)
}

func (m *GuildGroupMembership) FromBitSet(b *bitset.BitSet) {
	m.IsAllowedMatchmaking = b.Test(0)
	m.IsModerator = b.Test(1)
	m.IsServerHost = b.Test(2)
	m.IsAllocator = b.Test(3)
	m.IsSuspended = b.Test(4)
	m.IsAPIAccess = b.Test(5)
	m.IsAccountAgeBypass = b.Test(6)
	m.IsVPNBypass = b.Test(7)
	m.IsHeadsetLinked = b.Test(8)
}

func (m *GuildGroupMembership) asBitSet() *bitset.BitSet {
	b := bitset.New(9)
	if m.IsAllowedMatchmaking {
		b.Set(0)
	}
	if m.IsModerator {
		b.Set(1)
	}
	if m.IsServerHost {
		b.Set(2)
	}
	if m.IsAllocator {
		b.Set(3)
	}
	if m.IsSuspended {
		b.Set(4)
	}
	if m.IsAPIAccess {
		b.Set(5)
	}
	if m.IsAccountAgeBypass {
		b.Set(6)
	}
	if m.IsVPNBypass {
		b.Set(7)
	}
	if m.IsHeadsetLinked {
		b.Set(8)
	}
	return b
}

func GuildGroupGetID(ctx context.Context, nk runtime.NakamaModule, groupID string) (*GuildGroup, error) {
	g, err := nk.GroupsGetId(ctx, []string{groupID})
	if err != nil {
		return nil, fmt.Errorf("error getting group: %w", err)
	}
	if g == nil {
		return nil, fmt.Errorf("group not found")
	}
	return NewGuildGroup(g[0])
}

func GuildUserGroupsList(ctx context.Context, nk runtime.NakamaModule, userID string) (map[string]*GuildGroup, error) {
	guildGroups := make(map[string]*GuildGroup, 0)
	cursor := ""
	for {
		// Fetch the groups using the provided userId
		groups, _, err := nk.UserGroupsList(ctx, userID, 100, nil, cursor)
		if err != nil {
			return nil, fmt.Errorf("error getting user groups: %w", err)
		}
		for _, ug := range groups {
			if ug.State.Value > int32(api.UserGroupList_UserGroup_MEMBER) {
				continue
			}
			switch ug.Group.GetLangTag() {
			case "guild":
				gg, err := NewGuildGroup(ug.Group)
				if err != nil {
					return nil, fmt.Errorf("error creating guild group: %w", err)
				}
				guildGroups[ug.Group.GetId()] = gg

			}
		}
		if cursor == "" {
			break
		}
	}
	return guildGroups, nil
}

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/bits-and-blooms/bitset"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

type GuildGroupRoles struct {
	Member           string `json:"member"`
	Moderator        string `json:"moderator"`
	ServerHost       string `json:"server_host"`
	Allocator        string `json:"allocator"`
	Suspended        string `json:"suspended"`
	LimitedAccess    string `json:"limited_access"` // Can Access Private matches only
	APIAccess        string `json:"api_access"`
	AccountAgeBypass string `json:"account_age_bypass"`
	VPNBypass        string `json:"vpn_bypass"`
	AccountLinked    string `json:"headset_linked"`
	UsernameOnly     string `json:"username_only"`
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
		r.LimitedAccess,
		r.UsernameOnly,
	} {
		if r != "" {
			roles = append(roles, r)
		}
	}
	slices.Sort(roles)
	return slices.Compact(roles)
}

type GuildGroupMemberships map[string]guildGroupPermissions

func (g GuildGroupMemberships) IsMember(groupID string) bool {
	_, ok := g[groupID]
	return ok
}

type GroupMetadata struct {
	GuildID                            string                         `json:"guild_id"`                   // The guild ID
	RulesText                          string                         `json:"rules_text"`                 // The rules text displayed on the main menu
	MinimumAccountAgeDays              int                            `json:"minimum_account_age_days"`   // The minimum account age in days to be able to play echo on this guild's sessions
	MembersOnlyMatchmaking             bool                           `json:"members_only_matchmaking"`   // Restrict matchmaking to members only (when this group is the active one)
	DisableCreateCommand               bool                           `json:"disable_create_command"`     // Disable the public allocate command
	ModeratorsHaveGoldNames            bool                           `json:"moderators_have_gold_names"` // Moderators have gold display names
	Roles                              *GuildGroupRoles               `json:"roles"`                      // The roles text displayed on the main menu
	RoleCache                          map[string]map[string]struct{} `json:"role_cache"`                 // The role cache
	MatchmakingChannelIDs              map[string]string              `json:"matchmaking_channel_ids"`    // The matchmaking channel IDs
	DebugChannelID                     string                         `json:"debug_channel_id"`           // The debug channel
	AuditChannelID                     string                         `json:"audit_channel_id"`           // The audit channel
	ErrorChannelID                     string                         `json:"error_channel_id"`           // The error channel
	BlockVPNUsers                      bool                           `json:"block_vpn_users"`            // Block VPN users
	FraudScoreThreshold                int                            `json:"fraud_score_threshold"`      // The fraud score threshold
	AllowedFeatures                    []string                       `json:"allowed_features"`           // Allowed features
	LogAlternateAccounts               bool                           `json:"log_alternate_accounts"`     // Log alternate accounts
	AlternateAccountNotificationExpiry time.Time                      `json:"alt_notification_threshold"` // Show alternate notifications newer than this time.
	CommunityValuesUserIDs             map[string]time.Time           `json:"community_values_user_ids"`  // UserIDs that are required to go to community values when the first join the social lobby
}

func NewGuildGroupMetadata(guildID string) *GroupMetadata {
	return &GroupMetadata{
		GuildID:               guildID,
		RoleCache:             make(map[string]map[string]struct{}),
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

func (g *GroupMetadata) HasRole(userID string, role string) bool {
	if userIDs, ok := g.RoleCache[role]; ok {
		if _, ok := userIDs[userID]; ok {
			return true
		}
	}
	return false
}

func (g *GroupMetadata) IsServerHost(userID string) bool {
	return g.HasRole(userID, g.Roles.ServerHost)
}

func (g *GroupMetadata) IsAllocator(userID string) bool {
	return g.HasRole(userID, g.Roles.Allocator)
}

func (g *GroupMetadata) IsModerator(userID string) bool {
	return g.HasRole(userID, g.Roles.Moderator)
}

func (g *GroupMetadata) IsMember(userID string) bool {
	return g.HasRole(userID, g.Roles.Member)
}

func (g *GroupMetadata) IsSuspended(userID string) bool {
	return g.HasRole(userID, g.Roles.Suspended)
}

func (g *GroupMetadata) IsLimitedAccess(userID string) bool {
	return g.HasRole(userID, g.Roles.LimitedAccess)
}

func (g *GroupMetadata) IsAPIAccess(userID string) bool {
	return g.HasRole(userID, g.Roles.APIAccess)
}

func (g *GroupMetadata) IsAccountAgeBypass(userID string) bool {
	return g.HasRole(userID, g.Roles.AccountAgeBypass)
}

func (g *GroupMetadata) IsVPNBypass(userID string) bool {
	return g.HasRole(userID, g.Roles.VPNBypass)
}

func (m *GroupMetadata) IsAllowedFeature(feature string) bool {
	return slices.Contains(m.AllowedFeatures, feature)
}

func (m *GroupMetadata) IsAllowedMatchmaking(userID string) bool {
	if !m.MembersOnlyMatchmaking {
		return true
	}

	if userIDs, ok := m.RoleCache[m.Roles.Member]; ok {
		if _, ok := userIDs[userID]; ok {
			return true
		}
	}

	return false
}

func (m *GroupMetadata) hasCompletedCommunityValues(userID string) bool {
	_, found := m.CommunityValuesUserIDs[userID]
	return !found
}

func (m *GroupMetadata) CommunityValuesUserIDsAdd(userID string, delay time.Duration) {
	if m.CommunityValuesUserIDs == nil {
		m.CommunityValuesUserIDs = make(map[string]time.Time)
	}
	m.CommunityValuesUserIDs[userID] = time.Now().UTC().Add(delay)
}

func (m *GroupMetadata) CommunityValuesUserIDsRemove(userID string) bool {
	if _, ok := m.CommunityValuesUserIDs[userID]; !ok {
		return false
	}
	delete(m.CommunityValuesUserIDs, userID)
	return true
}

func (m *GroupMetadata) IsCommunityValuesTimedOut(userID string) (bool, time.Time) {
	if m.CommunityValuesUserIDs == nil {
		return false, time.Time{}
	}
	if expiry, ok := m.CommunityValuesUserIDs[userID]; ok && time.Now().UTC().Before(expiry) {
		return true, expiry
	}
	return false, time.Time{}
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
		if _, ok := userIDs[userID]; ok {
			roles = append(roles, role)
		}
	}
	return roles
}

func (g GuildGroup) MembershipBitSet(userID string) uint64 {
	return guildGroupPermissions{
		IsAllowedMatchmaking: g.IsAllowedMatchmaking(userID),
		IsModerator:          g.IsModerator(userID),
		IsServerHost:         g.IsServerHost(userID),
		IsAllocator:          g.IsAllocator(userID),
		IsSuspended:          g.IsSuspended(userID),
		IsLimitedAccess:      g.IsLimitedAccess(userID),
		IsAPIAccess:          g.IsAPIAccess(userID),
		IsAccountAgeBypass:   g.IsAccountAgeBypass(userID),
		IsVPNBypass:          g.IsVPNBypass(userID),
	}.ToUint64()
}

func (g *GuildGroup) RolesCacheUpdate(userID string, current []string) bool {

	// Ensure the role cache has been initialized
	if g.RoleCache == nil {
		g.RoleCache = make(map[string]map[string]struct{})
	}
	relevantRoles := make(map[string]struct{}, len(g.Roles.AsSlice()))
	for _, r := range g.Roles.AsSlice() {
		relevantRoles[r] = struct{}{}
		if _, ok := g.RoleCache[r]; !ok {
			g.RoleCache[r] = make(map[string]struct{})
		}
	}

	updated := false

	// Remove any roles that are not relevant
	for role := range g.RoleCache {
		if _, ok := relevantRoles[role]; !ok {
			delete(g.RoleCache, role)
			updated = true
		}
	}

	// Add the user to the roles
	userRoles := make(map[string]struct{}, len(current))
	for _, r := range current {
		userRoles[r] = struct{}{}
	}

	// Update the roles
	for role, userIDs := range g.RoleCache {
		_, hasRole := userRoles[role]
		_, hasUser := userIDs[userID]
		if hasRole && !hasUser {
			userIDs[userID] = struct{}{}
			updated = true
		} else if !hasRole && hasUser {
			delete(userIDs, userID)
			updated = true
		}
	}

	return updated
}

type guildGroupPermissions struct {
	IsAllowedMatchmaking bool
	IsModerator          bool // Admin
	IsServerHost         bool // Broadcaster Host
	IsAllocator          bool // Can allocate servers with slash command
	IsSuspended          bool
	IsAPIAccess          bool
	IsAccountAgeBypass   bool
	IsVPNBypass          bool
	IsLimitedAccess      bool
}

func (m guildGroupPermissions) ToUint64() uint64 {
	var i uint64 = 0
	b := m.asBitSet()
	if len(b.Bytes()) == 0 {
		return i
	}
	return uint64(b.Bytes()[0])
}

func (m *guildGroupPermissions) FromUint64(v uint64) {
	b := bitset.From([]uint64{v})
	m.FromBitSet(b)
}

func (m *guildGroupPermissions) FromBitSet(b *bitset.BitSet) {

	value := reflect.ValueOf(m).Elem()
	for i := 0; i < value.NumField(); i++ {
		field := value.Field(i)
		if field.Kind() != reflect.Bool {
			continue
		}
		field.SetBool(b.Test(uint(i)))
	}
}

func (m guildGroupPermissions) asBitSet() *bitset.BitSet {
	value := reflect.ValueOf(m).Elem()

	b := bitset.New(uint(value.NumField()))
	for i := 0; i < value.NumField(); i++ {
		field := value.Field(i)
		if field.Kind() != reflect.Bool {
			continue
		}
		if field.Bool() {
			b.Set(uint(i))
		}
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

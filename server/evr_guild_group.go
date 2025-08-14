package server

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

type GuildGroup struct {
	GroupMetadata
	State *GuildGroupState
	Group *api.Group
}

func NewGuildGroup(group *api.Group, state *GuildGroupState) (*GuildGroup, error) {

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
		State:         state,
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

func (g *GuildGroup) IDStr() string {
	return g.Group.Id
}

func (g *GuildGroup) Size() int {
	return int(g.Group.EdgeCount)
}

func (g GuildGroup) MembershipBitSet(userID string) uint64 {
	return guildGroupPermissions{
		IsAllowedMatchmaking: g.IsAllowedMatchmaking(userID),
		IsEnforcer:           g.IsEnforcer(userID),
		IsAuditor:            g.IsAuditor(userID),
		IsServerHost:         g.IsServerHost(userID),
		IsAllocator:          g.IsAllocator(userID),
		IsSuspended:          g.IsSuspended(userID, nil),
		IsAPIAccess:          g.IsAPIAccess(userID),
		IsAccountAgeBypass:   g.IsAccountAgeBypass(userID),
		IsVPNBypass:          g.IsVPNBypass(userID),
	}.ToUint64()
}

func (g GuildGroup) HasRole(userID, role string) bool {
	g.State.RLock()
	defer g.State.RUnlock()
	return g.State.hasRole(userID, role)
}

func (g *GuildGroup) RoleCacheUpdate(account *EVRProfile, roles []string) bool {
	g.State.Lock()
	defer g.State.Unlock()
	// Ensure the role cache has been initialized
	if g.State.RoleCache == nil {
		g.State.RoleCache = make(map[string]map[string]struct{})
	}

	g.State.updated = false

	roleSet := g.RoleMap.AsSet()
	// Ignore irrelevant roles
	for i := 0; i < len(roles); i++ {
		if _, ok := roleSet[roles[i]]; !ok {
			roles = slices.Delete(roles, i, i+1)
			i--
		}
	}

	// Add the user to the roles
	updatedRoles := make(map[string]struct{}, len(roleSet))
	for _, r := range roles {
		updatedRoles[r] = struct{}{}
	}

	// Update the roles
	for _, rID := range g.RoleMap.AsSlice() {
		if _, ok := updatedRoles[rID]; ok {
			if userIDs, ok := g.State.RoleCache[rID]; !ok {
				g.State.RoleCache[rID] = map[string]struct{}{account.ID(): {}}
				g.State.updated = true
			} else {
				if _, ok := userIDs[account.ID()]; !ok {
					userIDs[account.ID()] = struct{}{}
					g.State.updated = true
				}
			}
		} else if userIDs, ok := g.State.RoleCache[rID]; ok {
			if _, ok := userIDs[account.ID()]; ok {
				delete(userIDs, account.ID())
				g.State.updated = true
			}
		}
	}

	// If this user is suspended, add their devices to the suspension list
	if g.State.hasRole(account.ID(), g.RoleMap.Suspended) {
		if g.State.SuspendedXPIDs == nil {
			g.State.SuspendedXPIDs = make(map[evr.EvrId]string)
		}
		for _, xpid := range account.XPIDs() {
			g.State.SuspendedXPIDs[xpid] = account.ID()
			g.State.updated = true
		}
	} else {

		// If this user is no longer suspended, remove their devices from the suspension list
		if g.State.SuspendedXPIDs != nil {
			for _, xpid := range account.XPIDs() {
				if _, ok := g.State.SuspendedXPIDs[xpid]; ok {
					delete(g.State.SuspendedXPIDs, xpid)
					g.State.updated = true
				}
			}
		}
	}

	return g.State.updated
}

func (g *GuildGroup) IsOwner(userID string) bool {
	return g.OwnerID == userID
}

func (g *GuildGroup) IsServerHost(userID string) bool {
	return g.HasRole(userID, g.RoleMap.ServerHost)
}

func (g *GuildGroup) IsAllocator(userID string) bool {
	return g.HasRole(userID, g.RoleMap.Allocator)
}

func (g *GuildGroup) IsAuditor(userID string) bool {
	if slices.Contains(g.NegatedEnforcerIDs, userID) {
		return false
	}
	return g.HasRole(userID, g.RoleMap.Auditor)
}

func (g *GuildGroup) IsEnforcer(userID string) bool {
	if slices.Contains(g.NegatedEnforcerIDs, userID) {
		return false
	}
	return g.HasRole(userID, g.RoleMap.Enforcer)
}

func (g *GuildGroup) IsMember(userID string) bool {
	return g.HasRole(userID, g.RoleMap.Member)
}

func (g *GuildGroup) IsSuspended(userID string, xpid *evr.EvrId) bool {
	g.State.RLock()
	defer g.State.RUnlock()

	if g.State.hasRole(userID, g.RoleMap.Suspended) {
		return true
	}
	if xpid == nil || g.State.SuspendedXPIDs == nil {
		return false
	}

	if _, ok := g.State.SuspendedXPIDs[*xpid]; ok {
		// Check if the user is (still) suspended
		if g.State.hasRole(userID, g.RoleMap.Suspended) {
			return true
		}
	}

	return false
}

func (g *GuildGroup) IsAPIAccess(userID string) bool {
	return g.HasRole(userID, g.RoleMap.APIAccess)
}

func (g *GuildGroup) IsAccountAgeBypass(userID string) bool {
	return g.HasRole(userID, g.RoleMap.AccountAgeBypass)
}

func (g *GuildGroup) IsVPNBypass(userID string) bool {
	return g.HasRole(userID, g.RoleMap.VPNBypass)
}

func (g *GuildGroup) IsAllowedFeature(feature string) bool {
	return slices.Contains(g.AllowedFeatures, feature)
}

func (g *GuildGroup) IsAllowedMatchmaking(userID string) bool {
	if !g.EnableMembersOnlyMatchmaking {
		return true
	}
	g.State.RLock()
	defer g.State.RUnlock()

	if g.State.RoleCache == nil {
		return false
	}

	if userIDs, ok := g.State.RoleCache[g.RoleMap.Member]; ok {
		if _, ok := userIDs[userID]; ok {
			return true
		}
	}

	return false
}

func GuildGroupLoad(ctx context.Context, nk runtime.NakamaModule, groupID string) (*GuildGroup, error) {
	groups, err := nk.GroupsGetId(ctx, []string{groupID})
	if err != nil {
		return nil, fmt.Errorf("failed to get group: %v", err)
	}
	if len(groups) == 0 {
		return nil, fmt.Errorf("group not found")
	}

	state, err := GuildGroupStateLoad(ctx, nk, ServiceSettings().DiscordBotUserID, groupID)
	if err != nil {
		return nil, fmt.Errorf("failed to load guild group state: %v", err)
	}

	return NewGuildGroup(groups[0], state)
}

func GuildGroupStore(ctx context.Context, nk runtime.NakamaModule, guildGroupRegistry *GuildGroupRegistry, group *GuildGroup) error {
	_nk, ok := nk.(*RuntimeGoNakamaModule)
	if !ok {
		return fmt.Errorf("failed to cast nakama module")
	}

	// Store the State
	err := StorageWrite(ctx, nk, ServiceSettings().DiscordBotUserID, group.State)
	if err != nil {
		return fmt.Errorf("failed to write guild group state: %v", err)
	}

	// Store the metadata
	if err := GroupMetadataSave(ctx, _nk.db, group.Group.Id, &group.GroupMetadata); err != nil {
		return fmt.Errorf("failed to save guild group metadata: %v", err)
	}
	if guildGroupRegistry != nil {
		guildGroupRegistry.Add(group)
	}
	return nil
}

func GuildUserGroupsList(ctx context.Context, nk runtime.NakamaModule, guildGroupRegistry *GuildGroupRegistry, userID string) (map[string]*GuildGroup, error) {
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
			case GuildGroupLangTag:

				if guildGroupRegistry != nil {
					if gg := guildGroupRegistry.Get(ug.Group.Id); gg != nil {
						guildGroups[ug.Group.Id] = gg
					}
				} else {
					group, err := GuildGroupLoad(ctx, nk, ug.Group.Id)
					if err != nil {
						return nil, fmt.Errorf("error loading guild group: %w", err)
					}
					guildGroups[ug.Group.Id] = group
				}
			}
		}
		if cursor == "" {
			break
		}
	}
	return guildGroups, nil
}

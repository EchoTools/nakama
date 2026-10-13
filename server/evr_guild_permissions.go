package server

import (
	"context"
	"slices"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/atomic"
)

// guildPermissionSource supplies the Discord data canManageGuild needs.
// *DiscordIntegrator implements it; GuildMember is served from its TTL cache.
type guildPermissionSource interface {
	GuildMemberFresh(guildID, discordID string) (*discordgo.Member, error)
	GuildRoles(guildID string) ([]*discordgo.Role, error)
	// GuildOwnerID returns the Discord ID of the guild's owner.
	GuildOwnerID(guildID string) (string, error)
}

type guildPermissionSourceBox struct{ src guildPermissionSource }

// globalGuildPermissionSource is set once the Discord integrator exists. The
// RPC handlers are registered before it does, so they read it at call time.
// While it is unset, canManageGuild grants owner/operator only.
var globalGuildPermissionSource = atomic.NewPointer[guildPermissionSourceBox](nil)

// canManageGuild reports whether userID may administer the guild: its owner, a
// global operator, or a member holding Administrator or Manage Server in it.
//
// Discord-derived grants lag a revocation by at most memberCacheTTL (5 minutes):
// the member lookup may be served from cache within that window, but never from
// an expired entry (see GuildMemberFresh).
//
// It fails closed. Any error looking up the Discord side falls back to the
// owner/operator answer, is logged, and never grants.
func canManageGuild(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, src guildPermissionSource, gg *GuildGroup, userID string) bool {
	if gg.IsOwner(userID) {
		return true
	}
	if isOp, err := isGlobalOperator(ctx, nk, userID); err == nil && isOp {
		return true
	}
	if src == nil || gg.GuildID == "" {
		return false
	}

	account, err := nk.AccountGetId(ctx, userID)
	if err != nil || account == nil || account.CustomId == "" {
		logger.Warn("canManageGuild: no linked discord ID for user %s; owner/operator only (err=%v)", userID, err)
		return false
	}
	// The Discord guild owner has every permission implicitly, whatever their roles.
	if ownerID, err := src.GuildOwnerID(gg.GuildID); err != nil {
		logger.Warn("canManageGuild: discord guild owner lookup failed for guild %s; not granting as owner (err=%v)", gg.GuildID, err)
	} else if ownerID != "" && ownerID == account.CustomId {
		return true
	}
	member, err := src.GuildMemberFresh(gg.GuildID, account.CustomId)
	if err != nil || member == nil {
		logger.Warn("canManageGuild: discord member lookup failed for user %s in guild %s; owner/operator only (err=%v)", userID, gg.GuildID, err)
		return false
	}
	roles, err := src.GuildRoles(gg.GuildID)
	if err != nil {
		logger.Warn("canManageGuild: discord roles lookup failed for guild %s, user %s; owner/operator only (err=%v)", gg.GuildID, userID, err)
		return false
	}

	perms := memberGuildPermissions(gg.GuildID, member.Roles, roles)
	return perms&(discordgo.PermissionAdministrator|discordgo.PermissionManageGuild) != 0
}

// memberGuildPermissions ORs the @everyone role (whose ID is the guild ID) with
// the member's roles, as Discord does for base guild permissions. Channel
// overwrites are irrelevant to guild-level permissions.
func memberGuildPermissions(guildID string, memberRoleIDs []string, guildRoles []*discordgo.Role) int64 {
	var perms int64
	for _, r := range guildRoles {
		if r == nil {
			continue
		}
		if r.ID == guildID || slices.Contains(memberRoleIDs, r.ID) {
			perms |= r.Permissions
		}
	}
	if perms&discordgo.PermissionAdministrator != 0 {
		return ^int64(0)
	}
	return perms
}

// guildPermissionSourceOrNil returns the registered Discord source, or nil
// before the Discord integrator has started.
func guildPermissionSourceOrNil() guildPermissionSource {
	if box := globalGuildPermissionSource.Load(); box != nil {
		return box.src
	}
	return nil
}

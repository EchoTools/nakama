package server

import (
	"context"
	"fmt"
	goruntime "runtime"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

// DefaultGuildGroupID is the Echo VR Lounge guild group. A player who logs in
// with no guild membership at all (a new /device account that is in no
// EchoVRCE-enabled Discord server) is added to it instead of being refused
// with "user is not in any groups" (#749).
const DefaultGuildGroupID = "147afc9d-2819-4197-926d-5b3f92790edc"

// isSyncableGuildID reports whether guildID can name a Discord guild. The empty
// string and the all-zero UUID are what a profile with no active group resolves
// to (GetActiveGroupID is uuid.Nil, and GuildGroupMemberRemove stores uuid.Nil
// back into ActiveGroupID); neither is a guild, and syncing either can only fail
// with "guild group not found".
func isSyncableGuildID(guildID string) bool {
	return guildID != "" && guildID != uuid.Nil.String()
}

// ensureDefaultGuildMembership joins userID to the default guild group and
// returns the user's guild groups afterwards. It is a no-op returning groups
// unchanged when the user is already in at least one guild group.
//
// The join is made by the system user, so it does not depend on the group's
// join policy.
func ensureDefaultGuildMembership(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, registry *GuildGroupRegistry, userID string, groups map[string]*GuildGroup) (map[string]*GuildGroup, error) {
	if len(groups) > 0 {
		return groups, nil
	}
	if err := nk.GroupUsersAdd(ctx, SystemUserID, DefaultGuildGroupID, []string{userID}); err != nil {
		return groups, fmt.Errorf("failed to add user to the default guild group: %w", err)
	}
	groups, err := GuildUserGroupsList(ctx, nk, registry, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list groups after joining the default guild group: %w", err)
	}
	logger.Info("User had no guild groups; added to the default guild group",
		zap.String("uid", userID),
		zap.String("gid", DefaultGuildGroupID),
		zap.Int("guild_group_count", len(groups)))
	return groups, nil
}

// isDefaultGuildGroup reports whether groupID is the default guild group.
// Membership there is not derived from Discord, so a Discord-driven sync must
// not revoke it.
func isDefaultGuildGroup(groupID string) bool {
	return groupID == DefaultGuildGroupID
}

// callerName returns the function name skip frames above its caller, for log
// lines that must say who queued work.
func callerName(skip int) string {
	pc, _, _, ok := goruntime.Caller(skip)
	if !ok {
		return "unknown"
	}
	if fn := goruntime.FuncForPC(pc); fn != nil {
		return fn.Name()
	}
	return "unknown"
}

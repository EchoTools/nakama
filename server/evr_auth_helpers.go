package server

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/heroiclabs/nakama-common/runtime"
)

// RequireEnforcerOrOperator checks if a user is either a global operator or a guild enforcer.
// Returns permission flags, the loaded guild group, and an error if permission is denied.
// This consolidates the "check global ops, load guild group, check enforcer, deny if neither" pattern
// that appears in 8+ places across enforcement RPCs and Discord bot handlers.
func RequireEnforcerOrOperator(
	ctx context.Context,
	db *sql.DB,
	nk runtime.NakamaModule,
	userID, groupID string,
) (isOperator, isEnforcer bool, gg *GuildGroup, err error) {
	// Try cached permissions first
	perms := PermissionsFromContext(ctx)
	if perms != nil {
		isOperator = perms.IsGlobalOperator
	} else {
		// Fallback to DB query for backward compatibility
		isOperator, err = CheckSystemGroupMembership(ctx, db, userID, GroupGlobalOperators)
		if err != nil {
			return false, false, nil, fmt.Errorf("failed to check operator status: %w", err)
		}
	}

	// Load guild group to check enforcer status
	gg, err = GuildGroupLoad(ctx, nk, groupID)
	if err != nil {
		return false, false, nil, fmt.Errorf("failed to load guild group: %w", err)
	}

	isEnforcer = gg.IsEnforcer(userID)

	// Deny if neither operator nor enforcer
	if !isOperator && !isEnforcer {
		return false, false, gg, runtime.NewError(
			"You must be a guild enforcer or global operator",
			StatusPermissionDenied,
		)
	}

	return isOperator, isEnforcer, gg, nil
}

// RequireEnforcerOperatorOrBot checks if a user is a global operator, global bot, or guild enforcer.
// Similar to RequireEnforcerOrOperator but also allows global bots to pass the check.
func RequireEnforcerOperatorOrBot(
	ctx context.Context,
	db *sql.DB,
	nk runtime.NakamaModule,
	userID, groupID string,
) (isOperator, isBot, isEnforcer bool, gg *GuildGroup, err error) {
	// Try cached permissions first
	perms := PermissionsFromContext(ctx)
	if perms != nil {
		isOperator = perms.IsGlobalOperator
		isBot = perms.IsGlobalBot
	} else {
		// Fallback to DB query for backward compatibility
		isOperator, err = CheckSystemGroupMembership(ctx, db, userID, GroupGlobalOperators)
		if err != nil {
			return false, false, false, nil, fmt.Errorf("failed to check operator status: %w", err)
		}
		isBot, err = CheckSystemGroupMembership(ctx, db, userID, GroupGlobalBots)
		if err != nil {
			return false, false, false, nil, fmt.Errorf("failed to check bot status: %w", err)
		}
	}

	// Load guild group to check enforcer status
	gg, err = GuildGroupLoad(ctx, nk, groupID)
	if err != nil {
		return false, false, false, nil, fmt.Errorf("failed to load guild group: %w", err)
	}

	isEnforcer = gg.IsEnforcer(userID)

	// Deny if neither operator nor bot nor enforcer
	if !isOperator && !isBot && !isEnforcer {
		return false, false, false, gg, runtime.NewError(
			"You must be a guild enforcer, global operator, or global bot",
			StatusPermissionDenied,
		)
	}

	return isOperator, isBot, isEnforcer, gg, nil
}

// CallerGuildAccess represents a user's access level within a guild,
// with cascading permissions (global ops → auditor → enforcer).
type CallerGuildAccess struct {
	IsGlobalOperator bool
	IsAuditor        bool // guild auditor OR global ops
	IsEnforcer       bool // guild enforcer OR auditor (cascade)
	IsAllocator      bool
	IsSuspended      bool
	IsServerHost     bool
	IsAPIAccess      bool
}

// ResolveCallerGuildAccess resolves a user's guild access with cascading permissions.
// This consolidates the "auditor OR enforcer OR global ops" cascade pattern
// that appears in /lookup, /whoami, /search, and IGN override handlers.
func ResolveCallerGuildAccess(
	ctx context.Context,
	db *sql.DB,
	callerID, groupID string,
	callerGuildGroups map[string]*GuildGroup,
) CallerGuildAccess {
	access := CallerGuildAccess{}

	// Try cached permissions first
	perms := PermissionsFromContext(ctx)
	if perms != nil {
		access.IsGlobalOperator = perms.IsGlobalOperator
	} else {
		// Fallback to DB query for backward compatibility
		access.IsGlobalOperator, _ = CheckSystemGroupMembership(ctx, db, callerID, GroupGlobalOperators)
	}

	// Get guild-specific roles
	if gg, ok := callerGuildGroups[groupID]; ok {
		access.IsAuditor = gg.IsAuditor(callerID)
		access.IsEnforcer = gg.IsEnforcer(callerID)
		access.IsAllocator = gg.IsAllocator(callerID)
		access.IsSuspended = gg.IsSuspended(callerID, nil)
		access.IsServerHost = gg.IsServerHost(callerID)
		access.IsAPIAccess = gg.IsAPIAccess(callerID)
	}

	// Cascade: global ops → auditor → enforcer
	access.IsAuditor = access.IsAuditor || access.IsGlobalOperator
	access.IsEnforcer = access.IsEnforcer || access.IsAuditor

	return access
}

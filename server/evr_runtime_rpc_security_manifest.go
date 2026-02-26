package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"

	"github.com/heroiclabs/nakama-common/runtime"
)

type SecurityManifestRequest struct {
	GuildID string `json:"guild_id,omitempty"`
}

type SecurityManifestResponse struct {
	Capabilities map[string]bool `json:"capabilities"`
	Roles        []string        `json:"roles"`
	GuildID      string          `json:"guild_id,omitempty"`
}

func defaultSecurityCapabilities() map[string]bool {
	return map[string]bool{
		"match.list":               false,
		"match.allocate":           false,
		"match.prepare":            false,
		"match.terminate":          false,
		"enforcement.kick":         false,
		"enforcement.journals":     false,
		"enforcement.edit":         false,
		"player.kick":              false,
		"player.setnextmatch":      false,
		"player.lookup":            false,
		"player.profile":           false,
		"player.matchlock":         false,
		"guild.manage":             false,
		"guild.roles.update":       false,
		"guild.transfer":           false,
		"account.search":           false,
		"account.lookup":           false,
		"account.break_alternates": false,
		"server.manage":            false,
		"tot.access":               false,
		"operator":                 false,
		"developer":                false,
	}
}

func SecurityManifestRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	resp := SecurityManifestResponse{
		Capabilities: defaultSecurityCapabilities(),
		Roles:        make([]string, 0, 6),
	}

	userID, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if userID == "" {
		b, err := json.Marshal(resp)
		if err != nil {
			return "", runtime.NewError("failed to marshal response", StatusInternalError)
		}
		return string(b), nil
	}

	var req SecurityManifestRequest
	if payload != "" {
		if err := json.Unmarshal([]byte(payload), &req); err != nil {
			return "", runtime.NewError("invalid request payload", StatusInvalidArgument)
		}
	}

	perms := PermissionsFromContext(ctx)
	if perms == nil {
		resolved, err := ResolveUserPermissions(ctx, db, userID)
		if err != nil {
			logger.WithField("user_id", userID).WithField("error", err).Warn("failed to resolve user permissions for security manifest")
			resolved = &UserPermissions{}
		}
		perms = resolved
	}

	guildGroupMap, err := GuildUserGroupsList(ctx, nk, nil, userID)
	if err != nil {
		logger.WithField("user_id", userID).WithField("error", err).Warn("failed to load user guild groups for security manifest")
		guildGroupMap = map[string]*GuildGroup{}
	}

	selectedGuildID := req.GuildID
	if selectedGuildID == "" {
		groupIDs := make([]string, 0, len(guildGroupMap))
		for groupID := range guildGroupMap {
			groupIDs = append(groupIDs, groupID)
		}
		slices.Sort(groupIDs)
		for _, groupID := range groupIDs {
			gg := guildGroupMap[groupID]
			if gg == nil {
				continue
			}
			if gg.IsOwner(userID) || gg.IsEnforcer(userID) || gg.IsAllocator(userID) || gg.IsAuditor(userID) {
				selectedGuildID = groupID
				break
			}
		}
		if selectedGuildID == "" && len(groupIDs) > 0 {
			selectedGuildID = groupIDs[0]
		}
	}

	resp.GuildID = selectedGuildID
	selectedGuild := guildGroupMap[selectedGuildID]

	isOwner := selectedGuild != nil && selectedGuild.IsOwner(userID)
	isAuditor := selectedGuild != nil && selectedGuild.IsAuditor(userID)
	isEnforcer := selectedGuild != nil && selectedGuild.IsEnforcer(userID)
	isAllocator := selectedGuild != nil && selectedGuild.IsAllocator(userID)

	isOperator := perms.IsGlobalOperator
	isDeveloper := perms.IsGlobalDeveloper
	isBot := perms.IsGlobalBot
	isTester := perms.IsGlobalTester

	if isOperator {
		resp.Roles = append(resp.Roles, "operator")
	}
	if isDeveloper {
		resp.Roles = append(resp.Roles, "developer")
	}
	if isOwner {
		resp.Roles = append(resp.Roles, "owner")
	}
	if isAuditor {
		resp.Roles = append(resp.Roles, "auditor")
	}
	if isEnforcer {
		resp.Roles = append(resp.Roles, "enforcer")
	}
	if isAllocator {
		resp.Roles = append(resp.Roles, "allocator")
	}

	resp.Capabilities["match.list"] = true
	resp.Capabilities["match.allocate"] = isOperator || isBot || isAllocator
	resp.Capabilities["match.prepare"] = isOperator || isAllocator
	resp.Capabilities["match.terminate"] = isOperator || isEnforcer

	resp.Capabilities["enforcement.kick"] = isOperator || isEnforcer
	resp.Capabilities["enforcement.journals"] = isOperator || isOwner || isAuditor || isEnforcer
	resp.Capabilities["enforcement.edit"] = isOperator || isEnforcer

	resp.Capabilities["player.kick"] = isOperator || isEnforcer
	resp.Capabilities["player.setnextmatch"] = isOperator || isAuditor || isEnforcer
	resp.Capabilities["player.lookup"] = isOperator || isAuditor || isEnforcer
	resp.Capabilities["player.profile"] = true
	resp.Capabilities["player.matchlock"] = isOperator || isEnforcer

	resp.Capabilities["guild.manage"] = isOperator || isOwner || isEnforcer
	resp.Capabilities["guild.roles.update"] = isOperator || isOwner
	resp.Capabilities["guild.transfer"] = isOperator || isOwner

	resp.Capabilities["account.search"] = true
	resp.Capabilities["account.lookup"] = true
	resp.Capabilities["account.break_alternates"] = isOperator

	resp.Capabilities["server.manage"] = isOperator || isAllocator
	resp.Capabilities["tot.access"] = isTester
	resp.Capabilities["operator"] = isOperator
	resp.Capabilities["developer"] = isDeveloper

	b, err := json.Marshal(resp)
	if err != nil {
		return "", runtime.NewError("failed to marshal response", StatusInternalError)
	}
	return string(b), nil
}

package server

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/heroiclabs/nakama-common/runtime"
)

type GuildGroupUpdateRequest struct {
	GroupID     string         `json:"group_id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	AvatarURL   string         `json:"avatar_url"`
	Open        bool           `json:"open"`
	Metadata    *GroupMetadata `json:"metadata"`
}

type GuildGroupRolesUpdateRequest struct {
	GroupID string          `json:"group_id"`
	Roles   GuildGroupRoles `json:"roles"`
}

type GuildGroupTransferRequest struct {
	GroupID    string `json:"group_id"`
	NewOwnerID string `json:"new_owner_id"`
}

type GuildGroupLeaveRequest struct {
	GroupID string `json:"group_id"`
}

type GuildGroupDeleteRequest struct {
	GroupID string `json:"group_id"`
}

type DiscordGuildRolesRequest struct {
	GuildID string `json:"guild_id"`
}

type DiscordRoleInfo struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color int    `json:"color"`
}

type DiscordGuildRolesResponse struct {
	Roles []*DiscordRoleInfo `json:"roles"`
}

func GuildGroupUpdateRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("unauthenticated", 16)
	}

	var req GuildGroupUpdateRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError("invalid request payload", 3)
	}
	if req.GroupID == "" {
		return "", runtime.NewError("group_id is required", 3)
	}

	gg, err := GuildGroupLoad(ctx, nk, req.GroupID)
	if err != nil {
		return "", runtime.NewError("guild group not found", 5)
	}

	if !gg.IsOwner(userID) && !gg.IsEnforcer(userID) {
		isGlobalOp, checkErr := isGlobalOperator(ctx, nk, userID)
		if checkErr != nil || !isGlobalOp {
			return "", runtime.NewError("permission denied", 7)
		}
	}

	if req.Name != "" {
		if err := nk.GroupUpdate(ctx, req.GroupID, userID, req.Name, "", GuildGroupLangTag, req.Description, req.AvatarURL, req.Open, nil, int(gg.Group.MaxCount)); err != nil {
			return "", runtime.NewError("failed to update group", 13)
		}
	}

	if req.Metadata != nil {
		req.Metadata.GuildID = gg.GuildID
		req.Metadata.OwnerID = gg.OwnerID
		_nk, ok := nk.(*RuntimeGoNakamaModule)
		if !ok {
			return "", runtime.NewError("internal error", 13)
		}
		if err := GroupMetadataSave(ctx, _nk.db, req.GroupID, req.Metadata); err != nil {
			return "", runtime.NewError("failed to save metadata", 13)
		}
	}

	return "{}", nil
}

func GuildGroupRolesUpdateRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("unauthenticated", 16)
	}

	var req GuildGroupRolesUpdateRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError("invalid request payload", 3)
	}
	if req.GroupID == "" {
		return "", runtime.NewError("group_id is required", 3)
	}

	gg, err := GuildGroupLoad(ctx, nk, req.GroupID)
	if err != nil {
		return "", runtime.NewError("guild group not found", 5)
	}

	if !gg.IsOwner(userID) {
		isGlobalOp, checkErr := isGlobalOperator(ctx, nk, userID)
		if checkErr != nil || !isGlobalOp {
			return "", runtime.NewError("permission denied: only guild owner can update roles", 7)
		}
	}

	gg.GroupMetadata.RoleMap = req.Roles

	_nk, ok := nk.(*RuntimeGoNakamaModule)
	if !ok {
		return "", runtime.NewError("internal error", 13)
	}
	if err := GroupMetadataSave(ctx, _nk.db, req.GroupID, &gg.GroupMetadata); err != nil {
		return "", runtime.NewError("failed to save role map", 13)
	}

	return "{}", nil
}

func GuildGroupTransferOwnershipRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("unauthenticated", 16)
	}

	var req GuildGroupTransferRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError("invalid request payload", 3)
	}
	if req.GroupID == "" || req.NewOwnerID == "" {
		return "", runtime.NewError("group_id and new_owner_id are required", 3)
	}

	gg, err := GuildGroupLoad(ctx, nk, req.GroupID)
	if err != nil {
		return "", runtime.NewError("guild group not found", 5)
	}

	if !gg.IsOwner(userID) {
		isGlobalOp, checkErr := isGlobalOperator(ctx, nk, userID)
		if checkErr != nil || !isGlobalOp {
			return "", runtime.NewError("permission denied: only guild owner can transfer ownership", 7)
		}
	}

	if err := nk.GroupUsersPromote(ctx, SystemUserID, req.GroupID, []string{req.NewOwnerID}); err != nil {
		return "", runtime.NewError("failed to promote new owner", 13)
	}

	gg.GroupMetadata.OwnerID = req.NewOwnerID
	_nk, ok := nk.(*RuntimeGoNakamaModule)
	if !ok {
		return "", runtime.NewError("internal error", 13)
	}
	if err := GroupMetadataSave(ctx, _nk.db, req.GroupID, &gg.GroupMetadata); err != nil {
		return "", runtime.NewError("failed to update owner in metadata", 13)
	}

	return "{}", nil
}

func GuildGroupLeaveRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("unauthenticated", 16)
	}

	var req GuildGroupLeaveRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError("invalid request payload", 3)
	}
	if req.GroupID == "" {
		return "", runtime.NewError("group_id is required", 3)
	}

	gg, err := GuildGroupLoad(ctx, nk, req.GroupID)
	if err != nil {
		return "", runtime.NewError("guild group not found", 5)
	}

	if gg.IsOwner(userID) {
		return "", runtime.NewError("guild owner cannot leave; transfer ownership first", 9)
	}

	account, err := nk.AccountGetId(ctx, userID)
	if err != nil {
		return "", runtime.NewError("failed to get account", 13)
	}

	if err := nk.GroupUserLeave(ctx, req.GroupID, userID, account.User.Username); err != nil {
		return "", runtime.NewError("failed to leave group", 13)
	}

	return "{}", nil
}

func GuildGroupDeleteRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("unauthenticated", 16)
	}

	var req GuildGroupDeleteRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError("invalid request payload", 3)
	}
	if req.GroupID == "" {
		return "", runtime.NewError("group_id is required", 3)
	}

	gg, err := GuildGroupLoad(ctx, nk, req.GroupID)
	if err != nil {
		return "", runtime.NewError("guild group not found", 5)
	}

	if !gg.IsOwner(userID) {
		isGlobalOp, checkErr := isGlobalOperator(ctx, nk, userID)
		if checkErr != nil || !isGlobalOp {
			return "", runtime.NewError("permission denied: only guild owner can delete the guild", 7)
		}
	}

	if err := nk.GroupDelete(ctx, req.GroupID); err != nil {
		return "", runtime.NewError("failed to delete group", 13)
	}

	return "{}", nil
}

func (h *RPCHandler) GuildDiscordRolesRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("unauthenticated", 16)
	}

	var req DiscordGuildRolesRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError("invalid request payload", 3)
	}
	if req.GuildID == "" {
		return "", runtime.NewError("guild_id is required", 3)
	}

	groupID := h.GuildIDToGroupID(req.GuildID)
	if groupID == "" {
		return "", runtime.NewError("guild not found", 5)
	}

	gg, err := GuildGroupLoad(ctx, nk, groupID)
	if err != nil {
		return "", runtime.NewError("guild group not found", 5)
	}

	if !gg.IsOwner(userID) && !gg.IsEnforcer(userID) {
		isGlobalOp, checkErr := isGlobalOperator(ctx, nk, userID)
		if checkErr != nil || !isGlobalOp {
			return "", runtime.NewError("permission denied", 7)
		}
	}

	if h.dg == nil {
		return "", runtime.NewError("discord session unavailable", 13)
	}

	dgRoles, err := h.dg.GuildRoles(req.GuildID)
	if err != nil {
		return "", runtime.NewError("failed to fetch discord roles", 13)
	}

	roles := make([]*DiscordRoleInfo, 0, len(dgRoles))
	for _, r := range dgRoles {
		roles = append(roles, &DiscordRoleInfo{
			ID:    r.ID,
			Name:  r.Name,
			Color: r.Color,
		})
	}

	resp := DiscordGuildRolesResponse{Roles: roles}
	b, err := json.Marshal(resp)
	if err != nil {
		return "", runtime.NewError("failed to marshal response", 13)
	}
	return string(b), nil
}

func isGlobalOperator(ctx context.Context, nk runtime.NakamaModule, userID string) (bool, error) {
	groups, _, err := nk.UserGroupsList(ctx, userID, 100, nil, "")
	if err != nil {
		return false, err
	}
	for _, ug := range groups {
		if ug.Group.Name == GroupGlobalOperators {
			return true, nil
		}
	}
	return false, nil
}

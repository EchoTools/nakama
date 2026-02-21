package server

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/heroiclabs/nakama-common/runtime"
)

type GuildInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}

type ClientRoleResponse struct {
	SystemGroups    []string    `json:"system_groups"`
	ModeratorGuilds []GuildInfo `json:"moderator_guilds"`
	AllocatorGuilds []GuildInfo `json:"allocator_guilds"`
	IsTesterAdmin   bool        `json:"is_tester_admin"`
}

func GetClientRoleRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("unauthenticated", 16)
	}

	perms, err := ResolveUserPermissions(ctx, db, userID)
	if err != nil {
		return "", runtime.NewError("failed to resolve permissions", 13)
	}

	systemGroups := make([]string, 0, 8)
	if perms.IsGlobalOperator {
		systemGroups = append(systemGroups, GroupGlobalOperators)
	}
	if perms.IsGlobalDeveloper {
		systemGroups = append(systemGroups, GroupGlobalDevelopers)
	}
	if perms.IsGlobalTester {
		systemGroups = append(systemGroups, GroupGlobalTesters)
	}
	if perms.IsGlobalBadgeAdmin {
		systemGroups = append(systemGroups, GroupGlobalBadgeAdmins)
	}
	if perms.IsGlobalPrivateDataAccess {
		systemGroups = append(systemGroups, GroupGlobalPrivateDataAccess)
	}
	if perms.IsGlobalBot {
		systemGroups = append(systemGroups, GroupGlobalBots)
	}
	if perms.IsGlobalRequire2FA {
		systemGroups = append(systemGroups, GroupGlobalRequire2FA)
	}

	guildGroupMap, err := GuildUserGroupsList(ctx, nk, nil, userID)
	if err != nil {
		logger.WithField("user_id", userID).Warn("Failed to load guild groups for client role")
		guildGroupMap = nil
	}

	moderatorGuilds := make([]GuildInfo, 0)
	allocatorGuilds := make([]GuildInfo, 0)
	for _, gg := range guildGroupMap {
		if gg.Group == nil {
			continue
		}
		info := GuildInfo{
			ID:        gg.Group.Id,
			Name:      gg.Group.Name,
			AvatarURL: gg.Group.AvatarUrl,
		}
		if gg.IsOwner(userID) || gg.IsEnforcer(userID) {
			moderatorGuilds = append(moderatorGuilds, info)
		}
		if gg.IsAllocator(userID) {
			allocatorGuilds = append(allocatorGuilds, info)
		}
	}

	resp := ClientRoleResponse{
		SystemGroups:    systemGroups,
		ModeratorGuilds: moderatorGuilds,
		AllocatorGuilds: allocatorGuilds,
		IsTesterAdmin:   perms.IsGlobalTesterAdmin,
	}

	b, err := json.Marshal(resp)
	if err != nil {
		return "", runtime.NewError("failed to marshal response", 13)
	}
	return string(b), nil
}

func TotTestsListRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("unauthenticated", 16)
	}

	var cursor string
	allObjs := make([]map[string]any, 0, 64)
	for {
		objs, nextCursor, err := nk.StorageList(ctx, "", "", "tot_tests", 100, cursor)
		if err != nil {
			logger.WithField("user_id", userID).Error("Failed to list TOT tests from storage")
			return "", runtime.NewError("failed to list tests", 13)
		}
		for _, obj := range objs {
			var test map[string]any
			if err := json.Unmarshal([]byte(obj.Value), &test); err == nil {
				allObjs = append(allObjs, test)
			}
		}
		if nextCursor == "" {
			break
		}
		cursor = nextCursor
	}

	b, err := json.Marshal(allObjs)
	if err != nil {
		return "", runtime.NewError("failed to marshal tests", 13)
	}
	return string(b), nil
}

type TotTestCreateRequest struct {
	Test json.RawMessage `json:"test"`
}

func TotTestsCreateRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("unauthenticated", 16)
	}

	perms, err := ResolveUserPermissions(ctx, db, userID)
	if err != nil {
		return "", runtime.NewError("failed to resolve permissions", 13)
	}
	if !perms.IsGlobalTesterAdmin {
		return "", runtime.NewError("permission denied: Global Tester admin required", 7)
	}

	var req TotTestCreateRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError("invalid request payload", 3)
	}

	var obj map[string]any
	if err := json.Unmarshal(req.Test, &obj); err != nil {
		return "", runtime.NewError("invalid test object", 3)
	}
	id, _ := obj["id"].(string)
	if id == "" {
		return "", runtime.NewError("test must have a non-empty id field", 3)
	}

	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:      "tot_tests",
			Key:             id,
			UserID:          "",
			Value:           string(req.Test),
			PermissionRead:  2,
			PermissionWrite: 0,
		},
	})
	if err != nil {
		logger.WithField("user_id", userID).Error("Failed to create TOT test in storage")
		return "", runtime.NewError("failed to create test", 13)
	}

	return string(req.Test), nil
}

type TotTestUpdateRequest struct {
	ID   string          `json:"id"`
	Test json.RawMessage `json:"test"`
}

func TotTestsUpdateRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("unauthenticated", 16)
	}

	perms, err := ResolveUserPermissions(ctx, db, userID)
	if err != nil {
		return "", runtime.NewError("failed to resolve permissions", 13)
	}
	if !perms.IsGlobalTesterAdmin {
		return "", runtime.NewError("permission denied: Global Tester admin required", 7)
	}

	var req TotTestUpdateRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError("invalid request payload", 3)
	}
	if req.ID == "" {
		return "", runtime.NewError("id field is required", 3)
	}
	if len(req.Test) == 0 {
		return "", runtime.NewError("test field is required", 3)
	}

	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:      "tot_tests",
			Key:             req.ID,
			UserID:          "",
			Value:           string(req.Test),
			PermissionRead:  2,
			PermissionWrite: 0,
		},
	})
	if err != nil {
		logger.WithField("user_id", userID).Error("Failed to update TOT test in storage")
		return "", runtime.NewError("failed to update test", 13)
	}

	return string(req.Test), nil
}

type TotTestDeleteRequest struct {
	ID string `json:"id"`
}

func TotTestsDeleteRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("unauthenticated", 16)
	}

	perms, err := ResolveUserPermissions(ctx, db, userID)
	if err != nil {
		return "", runtime.NewError("failed to resolve permissions", 13)
	}
	if !perms.IsGlobalTesterAdmin {
		return "", runtime.NewError("permission denied: Global Tester admin required", 7)
	}

	var req TotTestDeleteRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError("invalid request payload", 3)
	}
	if req.ID == "" {
		return "", runtime.NewError("id field is required", 3)
	}

	if err := nk.StorageDelete(ctx, []*runtime.StorageDelete{
		{
			Collection: "tot_tests",
			Key:        req.ID,
			UserID:     "",
		},
	}); err != nil {
		logger.WithField("user_id", userID).Error("Failed to delete TOT test from storage")
		return "", runtime.NewError("failed to delete test", 13)
	}

	b, _ := json.Marshal(map[string]any{"deleted": req.ID})
	return string(b), nil
}

type TotTestUploadRequest struct {
	Tests []json.RawMessage `json:"tests"`
}

func TotTestsUploadRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("unauthenticated", 16)
	}

	perms, err := ResolveUserPermissions(ctx, db, userID)
	if err != nil {
		return "", runtime.NewError("failed to resolve permissions", 13)
	}
	if !perms.IsGlobalTesterAdmin {
		return "", runtime.NewError("permission denied: Global Tester admin required", 7)
	}

	var req TotTestUploadRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError("invalid request payload", 3)
	}
	if len(req.Tests) == 0 {
		return "", runtime.NewError("tests array is required and must not be empty", 3)
	}

	writes := make([]*runtime.StorageWrite, 0, len(req.Tests))
	for _, raw := range req.Tests {
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err != nil {
			return "", runtime.NewError("invalid test object in array", 3)
		}
		id, _ := obj["id"].(string)
		if id == "" {
			return "", runtime.NewError("each test must have a non-empty id field", 3)
		}
		writes = append(writes, &runtime.StorageWrite{
			Collection:      "tot_tests",
			Key:             id,
			UserID:          "",
			Value:           string(raw),
			PermissionRead:  2,
			PermissionWrite: 0,
		})
	}

	if _, err := nk.StorageWrite(ctx, writes); err != nil {
		logger.WithField("user_id", userID).Error("Failed to write TOT tests to storage")
		return "", runtime.NewError("failed to store tests", 13)
	}

	b, _ := json.Marshal(map[string]any{"uploaded": len(writes)})
	return string(b), nil
}

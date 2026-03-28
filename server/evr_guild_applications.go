package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	StorageCollectionGuildAppInstalls = "GuildApplicationInstalls"
)

// GuildAppInstallation represents an application installed in a guild.
type GuildAppInstallation struct {
	GuildID       string   `json:"guild_id"`
	AppID         string   `json:"app_id"`
	Name          string   `json:"name"`
	IconURL       string   `json:"icon_url,omitempty"`
	OwnerID       string   `json:"owner_id,omitempty"`
	OwnerUsername string   `json:"owner_username,omitempty"`
	Scopes        []string `json:"scopes,omitempty"`
	InstalledAt   string   `json:"installed_at,omitempty"`
	InstalledBy   string   `json:"installed_by,omitempty"`
}

type GuildAppListRequest struct {
	GuildID string `json:"guild_id"`
}

type GuildAppListResponse struct {
	Applications []*GuildAppInstallation `json:"applications"`
}

type GuildAppRevokeRequest struct {
	GuildID string `json:"guild_id"`
	AppID   string `json:"app_id"`
}

// GuildApplicationListRPC lists applications installed in a guild.
func GuildApplicationListRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("unauthenticated", 16)
	}

	var req GuildAppListRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError("invalid request payload", 3)
	}
	if req.GuildID == "" {
		return "", runtime.NewError("guild_id is required", 3)
	}

	gg, err := GuildGroupLoad(ctx, nk, req.GuildID)
	if err != nil {
		return "", runtime.NewError("guild group not found", 5)
	}

	if !gg.IsOwner(userID) {
		isGlobalOp, checkErr := isGlobalOperator(ctx, nk, userID)
		if checkErr != nil || !isGlobalOp {
			return "", runtime.NewError("permission denied: only guild owner can list guild applications", 7)
		}
	}

	// List all app installations for this guild from storage.
	prefix := req.GuildID + ":"
	objects, _, err := nk.StorageList(ctx, SystemUserID, StorageCollectionGuildAppInstalls, "", 100, "")
	if err != nil {
		return "", runtime.NewError("failed to list guild applications", 13)
	}

	apps := make([]*GuildAppInstallation, 0)
	for _, obj := range objects {
		if !strings.HasPrefix(obj.Key, prefix) {
			continue
		}
		var app GuildAppInstallation
		if err := json.Unmarshal([]byte(obj.Value), &app); err != nil {
			continue
		}
		apps = append(apps, &app)
	}

	resp := GuildAppListResponse{Applications: apps}
	b, err := json.Marshal(resp)
	if err != nil {
		return "", runtime.NewError("failed to marshal response", 13)
	}
	return string(b), nil
}

// GuildApplicationRevokeRPC revokes (uninstalls) an application from a guild.
func GuildApplicationRevokeRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("unauthenticated", 16)
	}

	var req GuildAppRevokeRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError("invalid request payload", 3)
	}
	if req.GuildID == "" || req.AppID == "" {
		return "", runtime.NewError("guild_id and app_id are required", 3)
	}

	gg, err := GuildGroupLoad(ctx, nk, req.GuildID)
	if err != nil {
		return "", runtime.NewError("guild group not found", 5)
	}

	if !gg.IsOwner(userID) {
		isGlobalOp, checkErr := isGlobalOperator(ctx, nk, userID)
		if checkErr != nil || !isGlobalOp {
			return "", runtime.NewError("permission denied: only guild owner can revoke guild applications", 7)
		}
	}

	// Delete the installation record from storage.
	storageKey := req.GuildID + ":" + req.AppID
	if err := nk.StorageDelete(ctx, []*runtime.StorageDelete{
		{
			Collection: StorageCollectionGuildAppInstalls,
			Key:        storageKey,
			UserID:     SystemUserID,
		},
	}); err != nil {
		return "", runtime.NewError("failed to revoke application", 13)
	}

	return "{}", nil
}

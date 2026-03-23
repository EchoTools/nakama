package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

// ImpersonateRequest is the request payload for the admin/impersonate RPC.
type ImpersonateRequest struct {
	UserID string `json:"user_id"` // Nakama user ID to impersonate (required)
}

// ImpersonateResponse is the response from the admin/impersonate RPC.
type ImpersonateResponse struct {
	Token        string              `json:"token"`         // Session JWT
	RefreshToken string              `json:"refresh_token"` // Refresh JWT
	UserID       string              `json:"user_id"`
	Username     string              `json:"username"`
	DisplayName  string              `json:"display_name"`
	AvatarURL    string              `json:"avatar_url"`
	DiscordID    string              `json:"discord_id"`
	Guilds       []ImpersonateGuild  `json:"guilds"` // Guild memberships
}

// ImpersonateGuild is a guild membership entry in the impersonate response.
type ImpersonateGuild struct {
	GroupID   string `json:"group_id"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}

// ImpersonateRPC generates session and refresh tokens for a target user,
// allowing a global developer to impersonate them for debugging.
// Permission: Global Developers only (enforced via RPC registration).
func ImpersonateRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	callerID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || callerID == "" {
		return "", runtime.NewError("User ID not found in context", StatusUnauthenticated)
	}

	var request ImpersonateRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", runtime.NewError("Invalid request payload", StatusInvalidArgument)
	}

	if request.UserID == "" {
		return "", runtime.NewError("user_id is required", StatusInvalidArgument)
	}

	targetUUID, err := uuid.FromString(request.UserID)
	if err != nil {
		return "", runtime.NewError("Invalid user_id format", StatusInvalidArgument)
	}
	targetUserID := targetUUID.String()

	// Prevent impersonating yourself
	if targetUserID == callerID {
		return "", runtime.NewError("Cannot impersonate yourself", StatusInvalidArgument)
	}

	// Load target account
	account, err := nk.AccountGetId(ctx, targetUserID)
	if err != nil || account == nil {
		return "", runtime.NewError("User not found", StatusNotFound)
	}

	username := account.User.Username
	displayName := account.User.DisplayName
	avatarURL := account.User.AvatarUrl

	// Generate session token (1 hour)
	tokenExpiry := time.Now().Add(1 * time.Hour).Unix()
	token, _, err := nk.AuthenticateTokenGenerate(targetUserID, username, tokenExpiry, map[string]string{
		"impersonated_by": callerID,
	})
	if err != nil {
		logger.Error("Failed to generate impersonate token", zap.Error(err))
		return "", runtime.NewError("Failed to generate token", StatusInternalError)
	}

	// Generate refresh token (8 hours — shorter than normal for safety)
	refreshExpiry := time.Now().Add(8 * time.Hour).Unix()
	refreshToken, _, err := nk.AuthenticateTokenGenerate(targetUserID, username, refreshExpiry, map[string]string{
		"refresh":          "true",
		"impersonated_by":  callerID,
	})
	if err != nil {
		logger.Error("Failed to generate impersonate refresh token", zap.Error(err))
		return "", runtime.NewError("Failed to generate refresh token", StatusInternalError)
	}

	// Get discord ID from custom_id
	discordID := account.CustomId

	// Load guild memberships
	userGroups, _, err := nk.UserGroupsList(ctx, targetUserID, 100, nil, "")
	if err != nil {
		logger.Warn("Failed to list user groups for impersonate", zap.Error(err))
	}

	var guilds []ImpersonateGuild
	for _, ug := range userGroups {
		if ug.State == nil || ug.State.Value > 2 {
			continue // Only include members (state 0=superadmin, 1=admin, 2=member)
		}
		g := ImpersonateGuild{
			GroupID: ug.Group.Id,
			Name:    ug.Group.Name,
		}
		if ug.Group.AvatarUrl != "" {
			g.AvatarURL = ug.Group.AvatarUrl
		}
		guilds = append(guilds, g)
	}

	// Audit log
	callerAccount, _ := nk.AccountGetId(ctx, callerID)
	callerName := callerID
	if callerAccount != nil {
		callerName = callerAccount.User.Username
	}
	logger.Warn("IMPERSONATION: developer initiated impersonation session",
		zap.String("caller_id", callerID),
		zap.String("caller_name", callerName),
		zap.String("target_id", targetUserID),
		zap.String("target_name", username),
	)

	// Send audit to service audit channel
	auditMsg := fmt.Sprintf("**IMPERSONATION** <@%s> (`%s`) is impersonating <@%s> (`%s`)",
		func() string {
			if callerAccount != nil && callerAccount.CustomId != "" {
				return callerAccount.CustomId
			}
			return callerID
		}(),
		callerName,
		discordID,
		username,
	)
	if settings := ServiceSettings(); settings != nil && settings.ServiceAuditChannelID != "" {
		if err := AuditLogSend(dg, settings.ServiceAuditChannelID, auditMsg); err != nil {
			logger.Warn("Failed to send impersonation audit log", zap.Error(err))
		}
	}

	response := ImpersonateResponse{
		Token:        token,
		RefreshToken: refreshToken,
		UserID:       targetUserID,
		Username:     username,
		DisplayName:  displayName,
		AvatarURL:    avatarURL,
		DiscordID:    discordID,
		Guilds:       guilds,
	}

	data, err := json.Marshal(response)
	if err != nil {
		return "", runtime.NewError("Failed to marshal response", StatusInternalError)
	}

	return string(data), nil
}

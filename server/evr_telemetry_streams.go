package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	StreamModeLobbyTelemetry = 32
)

type JoinMatchStreamRequest struct {
	MatchUUID string `json:"match_id"`
}

type LeaveMatchStreamRequest struct {
	MatchUUID string `json:"match_id"`
}

type StreamResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

func JoinTelemetryStreamRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var req JoinMatchStreamRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", fmt.Errorf("invalid request payload: %w", err)
	}

	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", fmt.Errorf("user not authenticated")
	}

	node := ctx.Value(runtime.RUNTIME_CTX_NODE).(string)

	// get the label of the match
	matchID := MatchID{
		UUID: uuid.FromStringOrNil(req.MatchUUID),
		Node: node,
	}

	label, err := MatchLabelByID(ctx, nk, matchID)
	if err != nil {
		return "", fmt.Errorf("failed to get match label: %w", err)
	}
	if label == nil {
		return "", fmt.Errorf("match not found")
	}

	hasRole := false
	guildGroupMap, err := GuildUserGroupsList(ctx, nk, nil, userID)
	if err != nil {
		return "", fmt.Errorf("failed to get user guild groups: %w", err)
	}

	groupID := label.GroupID.String()
	for _, gg := range guildGroupMap {
		if gg.GuildID == groupID && gg.RoleMap.APIAccess != "" {
			if gg.HasRole(userID, gg.RoleMap.APIAccess) {
				hasRole = true
				break
			}
		}
	}

	if !hasRole {
		return "", fmt.Errorf("user does not have API role in guild %s", groupID)
	}

	sessionID := ctx.Value(runtime.RUNTIME_CTX_SESSION_ID).(string)

	// Join the match stream
	if success, err := nk.StreamUserJoin(StreamModeLobbyTelemetry, req.MatchUUID, "", "", userID, sessionID, true, false, ""); err != nil {
		return "", fmt.Errorf("failed to join match stream: %w", err)
	} else if !success {
		return "", fmt.Errorf("failed to join match stream: unknown error")
	}

	response := StreamResponse{
		Success: true,
		Message: "Successfully joined match stream",
	}

	responseBytes, _ := json.Marshal(response)
	return string(responseBytes), nil
}

func LeaveTelemetryStreamRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var req LeaveMatchStreamRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", fmt.Errorf("invalid request payload: %w", err)
	}

	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", fmt.Errorf("user not authenticated")
	}

	sessionID, ok := ctx.Value(runtime.RUNTIME_CTX_SESSION_ID).(string)
	if !ok {
		return "", fmt.Errorf("session not found")
	}
	// Leave the match stream
	if err := nk.StreamUserLeave(StreamModeLobbyTelemetry, req.MatchUUID, "", "", userID, sessionID); err != nil {
		return "", fmt.Errorf("failed to leave match stream: %w", err)
	}

	response := StreamResponse{
		Success: true,
		Message: "Successfully left match stream",
	}

	responseBytes, _ := json.Marshal(response)
	return string(responseBytes), nil
}

func RegisterTelemetryStreamRPCs(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) error {
	if err := initializer.RegisterRpc("/telemetry/stream/join", JoinTelemetryStreamRPC); err != nil {
		return err
	}

	if err := initializer.RegisterRpc("/telemetry/stream/leave", LeaveTelemetryStreamRPC); err != nil {
		return err
	}

	return nil
}

package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

type SetNextMatchRPCRequest struct {
	TargetDiscordID string  `json:"discord_id"`
	TargetUserID    string  `json:"user_id"`
	MatchID         MatchID `json:"match_id"`
	Role            string  `json:"role"`
}

type SetNextMatchRPCResponse struct {
	UserID  string  `json:"user_id"`
	MatchID MatchID `json:"match_id"`
}

func (r SetNextMatchRPCResponse) String() string {
	data, _ := json.Marshal(r)
	return string(data)
}

func SetNextMatchRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {

	request := SetNextMatchRPCRequest{}
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", runtime.NewError(fmt.Sprintf("Error unmarshalling request: %s", err.Error()), StatusInvalidArgument)
	}
	callerUserID := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)

	if request.TargetDiscordID != "" {
		request.TargetUserID, _ = GetUserIDByDiscordID(ctx, db, request.TargetDiscordID)
	}

	if request.TargetUserID == "" {
		request.TargetUserID = callerUserID
	}

	if request.TargetUserID != callerUserID {
		// require them to be a global bot or global developer
		isGlobalDeveloper, _ := CheckSystemGroupMembership(ctx, db, callerUserID, GroupGlobalDevelopers)
		isGlobalBot, _ := CheckSystemGroupMembership(ctx, db, callerUserID, GroupGlobalBots)
		if !isGlobalDeveloper && !isGlobalBot {
			return "", runtime.NewError("You do not have permission to set the next match for another user", StatusPermissionDenied)
		}
	}

	if !request.MatchID.IsNil() {
		// Check if the match exists
		label, err := MatchLabelByID(ctx, nk, request.MatchID)
		if err != nil {
			return "", runtime.NewError(fmt.Sprintf("Error getting match label: %s", err.Error()), StatusInternalError)
		} else if label == nil {
			return "", runtime.NewError("Match not found", StatusNotFound)
		}

		// Check if the match is an active lobby
		if label.LobbyType == UnassignedLobby {
			return "", runtime.NewError("Match is not an active lobby", StatusInvalidArgument)
		}

		// Validate the role
		if request.Role != "" && request.Role != "any" {
			switch label.Mode {

			case evr.ModeArenaPublic, evr.ModeCombatPublic:
				return "", runtime.NewError("Match is a public match, but role is set", StatusInvalidArgument)
			case evr.ModeArenaPrivate, evr.ModeCombatPrivate:
				if request.Role != "orange" && request.Role != "blue" && request.Role != "spectator" {
					return "", runtime.NewError("Match is a private match, but role is not set to 'orange', 'blue', or 'spectator'.", StatusInvalidArgument)
				}
			default:
				return "", runtime.NewError(fmt.Sprintf("Role may not be set for %s matches", label.Mode.String()), StatusInvalidArgument)
			}
		}
	}

	settings, err := LoadMatchmakingSettings(ctx, nk, request.TargetUserID)
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("Error loading matchmaking settings: %s", err.Error()), StatusInternalError)
	}

	settings.NextMatchID = request.MatchID
	settings.NextMatchRole = request.Role
	settings.NextMatchDiscordID = request.TargetDiscordID

	if err = StoreMatchmakingSettings(ctx, nk, request.TargetUserID, settings); err != nil {
		return "", runtime.NewError(fmt.Sprintf("Error saving matchmaking settings: %s", err.Error()), StatusInternalError)
	}

	logger.WithFields(map[string]interface{}{
		"caller_user_id": callerUserID,
		"target_user_id": request.TargetUserID,
		"match_id":       settings.NextMatchID.String(),
	}).Info("Set next match")

	response := SetNextMatchRPCResponse{
		UserID:  request.TargetUserID,
		MatchID: settings.NextMatchID,
	}

	return response.String(), nil
}

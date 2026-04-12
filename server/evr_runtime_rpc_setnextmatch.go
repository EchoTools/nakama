package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/types"
	"go.uber.org/zap"
)

type SetNextMatchRPCRequest struct {
	TargetDiscordID string  `json:"discord_id"`
	TargetUserID    string  `json:"user_id"`
	MatchID         MatchID `json:"match_id"`
	HostDiscordID   string  `json:"host_discord_id"`
	Role            string  `json:"role"`
	JoinImmediately bool    `json:"join_immediately"`
}

type SetNextMatchRPCResponse struct {
	UserID            string  `json:"user_id"`
	MatchID           MatchID `json:"match_id"`
	JoinedImmediately bool    `json:"joined_immediately"`
	Message           string  `json:"message,omitempty"`
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

	// Safely extract caller user ID from context
	callerUserID := ""
	if uid := ctx.Value(runtime.RUNTIME_CTX_USER_ID); uid != nil {
		callerUserID, _ = uid.(string)
	}

	if callerUserID == "" {
		return "", runtime.NewError("Authentication required", StatusUnauthenticated)
	}

	if request.TargetDiscordID != "" {
		request.TargetUserID, _ = GetUserIDByDiscordID(ctx, db, request.TargetDiscordID)
	}

	if request.TargetUserID == "" {
		request.TargetUserID = callerUserID
	}

	if request.TargetUserID != callerUserID {
		isGlobalOperator := false
		if perms := PermissionsFromContext(ctx); perms != nil {
			isGlobalOperator = perms.IsGlobalOperator
		} else {
			var err error
			isGlobalOperator, err = CheckSystemGroupMembership(ctx, db, callerUserID, GroupGlobalOperators)
			if err != nil {
				return "", runtime.NewError("Failed to check operator permissions", StatusInternalError)
			}
		}

		if !isGlobalOperator {
			return "", runtime.NewError("You do not have permission to set the next match for another user", StatusPermissionDenied)
		}
	}

	auditGroupID := ""
	var label *MatchLabel

	if !request.MatchID.IsNil() {
		var err error
		// Check if the match exists
		label, err = MatchLabelByID(ctx, nk, request.MatchID)
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
		if gid := label.GetGroupID(); !gid.IsNil() {
			auditGroupID = gid.String()
		}
	}

	directive := &JoinDirective{
		MatchID:       request.MatchID,
		Role:          request.Role,
		HostDiscordID: request.HostDiscordID,
	}
	if err := StoreJoinDirective(ctx, nk, request.TargetUserID, directive); err != nil {
		return "", runtime.NewError(fmt.Sprintf("Error saving join directive: %s", err.Error()), StatusInternalError)
	}

	response := SetNextMatchRPCResponse{
		UserID:  request.TargetUserID,
		MatchID: request.MatchID,
	}

	// Attempt immediate join if requested and the match is valid
	if request.JoinImmediately && label != nil {
		response.Message = tryImmediateJoin(ctx, logger, nk, request, label, &response)
	}

	logger.WithFields(map[string]interface{}{
		"caller_user_id":    callerUserID,
		"target_user_id":    request.TargetUserID,
		"match_id":          request.MatchID.String(),
		"join_immediately":  request.JoinImmediately,
		"joined_immediately": response.JoinedImmediately,
	}).Info("Set next match")

	sendRPCAuditMessage(
		ctx,
		logger,
		nk,
		"player/setnextmatch",
		auditGroupID,
		callerUserID,
		fmt.Sprintf("target_user_id=%s match_id=%s role=%s host_discord_id=%s join_immediately=%t joined_immediately=%t", request.TargetUserID, request.MatchID.String(), request.Role, request.HostDiscordID, request.JoinImmediately, response.JoinedImmediately),
	)

	return response.String(), nil
}

// tryImmediateJoin attempts to join the target user into the match right now.
// Returns an informational message (empty on success). Sets response.JoinedImmediately on success.
func tryImmediateJoin(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, request SetNextMatchRPCRequest, label *MatchLabel, response *SetNextMatchRPCResponse) string {
	_nk := nk.(*RuntimeGoNakamaModule)

	// Find the user's active EVR session
	presences, err := nk.StreamUserList(StreamModeService, request.TargetUserID, "", StreamLabelMatchService, false, true)
	if err != nil {
		return fmt.Sprintf("failed to look up user sessions: %s", err.Error())
	}
	if len(presences) == 0 {
		return "user not online; directive stored for next session"
	}

	// Get the session object
	sessionID := uuid.FromStringOrNil(presences[0].GetSessionId())
	session := _nk.sessionRegistry.Get(sessionID)
	if session == nil {
		return "user session not found; directive stored for next session"
	}

	// Get the game server session
	serverSession := _nk.sessionRegistry.Get(label.GameServer.SessionID)
	if serverSession == nil {
		return "game server session not found; directive stored for next session"
	}

	// Resolve role string to int
	role := evr.TeamUnassigned
	switch request.Role {
	case "orange":
		role = evr.TeamOrange
	case "blue":
		role = evr.TeamBlue
	case "spectator":
		role = evr.TeamSpectator
	case "moderator":
		role = evr.TeamModerator
	}

	// Build entrant presence from the active session
	presence, err := EntrantPresenceFromSession(session, uuid.Nil, role, types.Rating{}, label.GetGroupID().String(), 0, "")
	if err != nil {
		return fmt.Sprintf("failed to create entrant presence: %s", err.Error())
	}

	// Join the user into the match
	zapLogger := RuntimeLoggerToZapLogger(logger)
	if err := LobbyJoinEntrants(zapLogger, _nk.matchRegistry, _nk.tracker, session, serverSession, label, presence); err != nil {
		return fmt.Sprintf("immediate join failed: %s; directive stored for next session", err.Error())
	}

	// Join succeeded — clean up the stored directive
	if err := DeleteJoinDirective(ctx, nk, request.TargetUserID); err != nil {
		zapLogger.Warn("failed to delete join directive after immediate join", zap.String("uid", request.TargetUserID), zap.Error(err))
	}

	response.JoinedImmediately = true
	return ""
}

package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// PrepareMatchRequest is a local type for the match preparation RPC request.
// This was previously defined in nevr-common/v3/api, but has been removed in v4.
type PrepareMatchRequest struct {
	GroupId          string                 `json:"group_id,omitempty"`
	OwnerId          string                 `json:"owner_id,omitempty"`
	Mode             string                 `json:"mode,omitempty"`
	Level            string                 `json:"level,omitempty"`
	TeamSize         *Int32Wrapper          `json:"team_size,omitempty"`
	Region           string                 `json:"region,omitempty"`
	RequiredFeatures []string               `json:"required_features,omitempty"`
	TeamAlignments   map[string]string      `json:"team_alignments,omitempty"`
	ExpiryTime       *timestamppb.Timestamp `json:"expiry_time,omitempty"`
}

// Int32Wrapper is a simple wrapper for int32 values (mimics protobuf wrapper)
type Int32Wrapper struct {
	Value int32 `json:"value,omitempty"`
}

func (w *Int32Wrapper) GetValue() int32 {
	if w == nil {
		return 0
	}
	return w.Value
}

// AllocateMatchRPC is an RPC handler that allocates a match for a group/guild.
func AllocateMatchRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var err error

	// Ensure the request is authenticated
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", runtime.NewError("authentication required", StatusUnauthenticated)
	}
	callerAccount, err := nk.AccountGetId(ctx, userID)
	if err != nil {
		return "", runtime.NewError("Failed to get caller account: "+err.Error(), StatusNotFound)
	}

	// Unmarshal the request payload into a PrepareMatchRequest
	request := &PrepareMatchRequest{}
	if err := json.Unmarshal([]byte(payload), request); err != nil {
		return "", runtime.NewError("failed to unmarshal request: "+err.Error(), StatusInvalidArgument)
	}
	if request.GroupId == "" {
		return "", runtime.NewError("group/guild ID must be specified", StatusInvalidArgument)
	}

	if uuid.FromStringOrNil(request.GroupId) == uuid.Nil {
		// Assume the groupId is a guild ID and convert it to a group ID
		request.GroupId, err = GetGroupIDByGuildID(ctx, db, request.GroupId)
		if err != nil {
			return "", runtime.NewError(err.Error(), StatusInternalError)
		} else if request.GroupId == "" {
			return "", runtime.NewError("guild group not found", StatusNotFound)
		}
	}

	if request.OwnerId == "" {
		// If no owner ID is specified, use the user ID of the caller
		request.OwnerId = userID
	} else if uuid.FromStringOrNil(request.OwnerId) == uuid.Nil {
		// Assume the owner ID is a discord ID and convert it to a user ID
		if request.OwnerId, err = GetUserIDByDiscordID(ctx, db, request.OwnerId); err != nil {
			return "", runtime.NewError("Failed to get userID by discord ID: "+err.Error(), StatusNotFound)
		}
	}

	// Validate that this userID has permission to signal this match
	gg, err := GuildGroupLoad(ctx, nk, request.GroupId)
	if err != nil {
		return "", runtime.NewError(err.Error(), StatusInternalError)
	}

	isGlobalOperator := false
	isGlobalBot := false
	if perms := PermissionsFromContext(ctx); perms != nil {
		isGlobalOperator = perms.IsGlobalOperator
		isGlobalBot = perms.IsGlobalBot
	} else {
		isGlobalOperator, err = CheckSystemGroupMembership(ctx, db, userID, GroupGlobalOperators)
		if err != nil {
			return "", runtime.NewError("failed to check operator permissions", StatusInternalError)
		}
		isGlobalBot, err = CheckSystemGroupMembership(ctx, db, userID, GroupGlobalBots)
		if err != nil {
			return "", runtime.NewError("failed to check bot permissions", StatusInternalError)
		}
	}

	if !isGlobalOperator && !isGlobalBot && !gg.IsAllocator(userID) {
		return "", runtime.NewError("user must have the `allocator` in the guild.", StatusPermissionDenied)
	}

	if level := evr.ToSymbol(request.Level); !gg.AllowsLevel(level) {
		return "", runtime.NewError(fmt.Sprintf("guild does not allow level '%s'", level.String()), StatusPermissionDenied)
	}

	// Increase the expiry time to a minimum of 1 minute from now
	if request.ExpiryTime.AsTime().Before(time.Now().Add(1 * time.Minute)) {
		request.ExpiryTime = timestamppb.New(time.Now().Add(1 * time.Minute))
	}

	// Build the team alignments map from the request
	var teamAlignments = make(map[string]int, len(request.TeamAlignments))
	for id, roleName := range request.TeamAlignments {

		// Set the roleID based on the roleName. The switch below covers every
		// role (default returns), so no initializer is needed.
		var roleID int
		switch roleName {
		case "blue":
			roleID = 0
		case "orange":
			roleID = 1
		case "spectator":
			roleID = 2
		case "social":
			roleID = 3
		default:
			return "", runtime.NewError("invalid team alignment role: "+roleName, StatusInvalidArgument)
		}
		if uuid.FromStringOrNil(id) == uuid.Nil {
			// Assume the id is a discord ID and convert it to a user ID
			resolvedID, err := GetUserIDByDiscordID(ctx, db, id)
			if err != nil {
				return "", runtime.NewError("Failed to get userID by discord ID: "+err.Error(), StatusNotFound)
			}
			if resolvedID == "" {
				return "", runtime.NewError("discord user not found: "+id, StatusNotFound)
			}
			teamAlignments[resolvedID] = roleID
		} else {
			teamAlignments[id] = roleID
		}

	}

	// Otherwise, find an open server in the given region
	settings := &MatchSettings{
		Mode:             evr.ToSymbol(request.Mode),
		Level:            evr.ToSymbol(request.Level),
		TeamSize:         int(request.TeamSize.GetValue()),
		StartTime:        request.ExpiryTime.AsTime().UTC(),
		SpawnedBy:        userID,             // The actual user making the request
		Owner:            request.OwnerId,    // The designated owner (can be different from spawner)
		Classification:   ClassificationNone, // Default classification, can be overridden
		GroupID:          uuid.FromStringOrNil(request.GroupId),
		RequiredFeatures: request.RequiredFeatures,
		TeamAlignments:   teamAlignments,
	}

	// Load the latency history for the match owner
	latencyHistory := &LatencyHistory{}
	if err := StorableRead(ctx, nk, request.OwnerId, latencyHistory, true); err != nil {
		return "", runtime.NewError("Failed to read latency history: "+err.Error(), StatusInternalError)
	}

	// Allocate a server by region or match ID
	var label *MatchLabel
	if matchID := MatchIDFromStringOrNil(request.Region); !matchID.IsNil() {
		// If the region is a match ID, get the match label by ID
		match, err := nk.MatchGet(ctx, matchID.String())
		if err != nil {
			return "", runtime.NewError("Failed to get match: "+err.Error(), StatusInternalError)
		} else if match == nil {
			return "", runtime.NewError("Match not found: "+matchID.String(), StatusNotFound)
		}
		// If the region is a match ID, get the match label by ID
		label, err = MatchLabelByID(ctx, nk, matchID)
		if err != nil || label == nil {
			return "", runtime.NewError("Failed to get match label", StatusNotFound)
		}

		// Refuse before the match is touched: LobbyPrepareSession rewrites the
		// match's group, so a refusal after it would leave the match changed.
		if err := requireAllocatorForServer(ctx, logger, nk, userID, request.GroupId, label, isGlobalOperator || isGlobalBot); err != nil {
			return "", err
		}

		// Prepare the session with the given match ID
		label, err = LobbyPrepareSession(ctx, nk, label.ID, settings)
		if err != nil {
			return "", runtime.NewError(err.Error(), StatusInvalidArgument)
		}
		if label == nil {
			return "", runtime.NewError("match preparation returned nil label", StatusInternalError)
		}
	} else {
		// Allocate a game server for the given group ID and region.
		// Default to the "default" region if no region is specified
		if request.Region == "" {
			request.Region = "default"
		}
		label, err = LobbyGameServerAllocate(ctx, logger, nk, []string{request.GroupId}, latencyHistory.LatestRTTs(), settings, []string{request.Region}, false, true, ServiceSettings().Matchmaking.QueryAddons.RPCAllocate, nil)
		if err != nil || label == nil {
			// Check if this is a region fallback error - for RPC, auto-select closest
			var regionErr ErrMatchmakingNoServersInRegion
			if errors.As(err, &regionErr) && regionErr.FallbackInfo != nil {
				logger.WithFields(map[string]any{
					"requested_region": request.Region,
					"selected_region":  regionErr.FallbackInfo.ClosestRegion,
					"latency_ms":       regionErr.FallbackInfo.ClosestLatencyMs,
				}).Info("Auto-selecting closest server for RPC allocation (no servers in requested region)")

				// Allocate without region requirement to get the closest server
				label, err = LobbyGameServerAllocate(ctx, logger, nk, []string{request.GroupId}, latencyHistory.LatestRTTs(), settings, nil, false, false, ServiceSettings().Matchmaking.QueryAddons.RPCAllocate, nil)
			}

			if err != nil || label == nil {
				if err == nil {
					err = fmt.Errorf("no server allocated")
				}
				if strings.Contains(err.Error(), "bad request: feature not supported") {
					err = runtime.NewError("required features not supported", StatusInvalidArgument)
				}
				logger.WithField("error", err).Error("Failed to allocate game server")
				return "", runtime.NewError(err.Error(), StatusInvalidArgument)
			}
		}
	}

	// The fresh-allocation branch only offers servers that host for the target
	// guild, so this holds there by construction; it is kept as the final guard
	// on the label that is about to be returned. The match-ID branch has already
	// been checked, before it was prepared.
	if err := requireAllocatorForServer(ctx, logger, nk, userID, request.GroupId, label, isGlobalOperator || isGlobalBot); err != nil {
		return "", err
	}

	// Log the match preparation as an audit log message
	if appBot := globalAppBot.Load(); appBot != nil && appBot.dg != nil {
		if settingsJSON, err := json.MarshalIndent(settings, "", "  "); err != nil {
			logger.WithField("error", err.Error()).Error("Failed to marshal match settings")
		} else {
			guid := strings.ToUpper(label.ID.UUID.String())
			auditMessage := fmt.Sprintf("<@%s> allocated https://echo.taxi/spark://j/%s via RPC:\n\n```json\n%s\n```", callerAccount.GetCustomId(), guid, string(settingsJSON))
			if _, err := AuditLogSendGuild(appBot.dg, gg, auditMessage); err != nil {
				logger.WithField("err", err).Error("Failed to send audit log message")
			}
		}
	}

	logger.WithFields(map[string]any{
		"mid":       label.ID.String(),
		"caller_id": userID,
		"gid":       request.GroupId,
		"owner_id":  request.OwnerId,
		"mode":      label.Mode,
		"state":     string(label.GetLabel()),
	}).Info("Match prepared")

	// The label carries the players of a match that already exists. Only Global
	// Private Data Access sees where they are; the server endpoint stays.
	privileged, err := callerHasPrivateDataAccess(ctx, db, userID)
	if err != nil {
		return "", runtime.NewError("failed to check private data access", StatusInternalError)
	}
	if !privileged {
		label = label.RedactPlayerLocation()
	}

	// Return the match label as a JSON string
	return string(label.GetLabelIndented()), nil
}

// requireAllocatorForServer applies the allocation rule shared with the /allocate
// command: a caller may allocate a server for target guild T when they are
// allocator in T (checked by the caller of this function) and allocator in at
// least one guild the server hosts for (GameServer.GroupIDs). Global operators
// and bots, and the server's own operator, are exempt from the second condition.
// A caller with no allocator role in any of the server's guilds is refused.
func requireAllocatorForServer(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID, targetGroupID string, label *MatchLabel, isGlobalPrivileged bool) error {
	if label == nil || label.GameServer == nil {
		return runtime.NewError("match has no game server", StatusInternalError)
	}
	if isGlobalPrivileged || label.GameServer.OperatorID.String() == userID {
		return nil
	}
	for _, gid := range label.GameServer.GroupIDs {
		hostGuild, err := GuildGroupLoad(ctx, nk, gid.String())
		if err != nil {
			// Fail closed: a guild that cannot be loaded confers no allocator role.
			logger.WithFields(map[string]any{
				"caller_id":  userID,
				"server_gid": gid.String(),
				"error":      err.Error(),
			}).Warn("Could not load a guild the game server hosts for; it grants no allocator access")
			continue
		}
		if hostGuild.IsAllocator(userID) {
			return nil
		}
	}
	logger.WithFields(map[string]any{
		"caller_id":   userID,
		"target_gid":  targetGroupID,
		"server_gids": label.GameServer.GroupIDs,
		"mid":         label.ID.String(),
	}).Warn("Allocation refused: caller is not allocator in any guild the game server hosts for")
	return runtime.NewError("user must be an allocator in a guild the game server hosts for.", StatusPermissionDenied)
}

// callerHasPrivateDataAccess reports whether the caller is in the Global Private
// Data Access system group, the gate for every player location field.
func callerHasPrivateDataAccess(ctx context.Context, db *sql.DB, userID string) (bool, error) {
	if perms := PermissionsFromContext(ctx); perms != nil {
		return perms.IsGlobalPrivateDataAccess, nil
	}
	return CheckSystemGroupMembership(ctx, db, userID, GroupGlobalPrivateDataAccess)
}

type shutdownMatchRequest struct {
	MatchID      MatchID `json:"match_id"`
	GraceSeconds int     `json:"grace_seconds,omitempty"`
}

type shutdownMatchResponse struct {
	Success  bool   `json:"success"`
	Response string `json:"response"`
}

func shutdownMatchRpc(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	// Authorization semantics for shutdownMatchRpc:
	// - Global operators may shutdown any match.
	// - The server host operator (the operator who owns the game server running the match)
	//   is allowed to shutdown the match for servers they operate.
	// - Guild enforcers (enforcer role in the guild the match runs in) are allowed to
	//   shutdown matches for their guild via RequireEnforcerOrOperator.
	// These rules mirror the Discord /shutdown-match command semantics implemented
	// in the Discord app-bot helper so behavior is consistent across entry points.
	r := NewRuntimeContext(ctx)
	request := &shutdownMatchRequest{}
	if err := json.Unmarshal([]byte(payload), request); err != nil {
		return "", runtime.NewError("invalid request payload: "+err.Error(), StatusInvalidArgument)
	}

	if !request.MatchID.IsValid() {
		return "", runtime.NewError("match_id is required", StatusInvalidArgument)
	}

	label, err := MatchLabelByID(ctx, nk, request.MatchID)
	if err != nil {
		return "", err
	}

	isGlobalOperator := false
	if perms := PermissionsFromContext(ctx); perms != nil {
		isGlobalOperator = perms.IsGlobalOperator
	} else if db != nil {
		isGlobalOperator, err = CheckSystemGroupMembership(ctx, db, r.UserID, GroupGlobalOperators)
		if err != nil {
			return "", runtime.NewError("failed to check operator permissions", StatusInternalError)
		}
	}

	isServerHostOperator := label.GameServer != nil && label.GameServer.OperatorID.String() == r.UserID

	if !isGlobalOperator && !isServerHostOperator {
		if label.GetGroupID().IsNil() {
			return "", runtime.NewError("permission denied", StatusPermissionDenied)
		}
		if _, _, _, err := RequireEnforcerOrOperator(ctx, db, nk, r.UserID, label.GetGroupID().String()); err != nil {
			return "", err
		}
	}

	if request.GraceSeconds <= 0 {
		request.GraceSeconds = 10
	}
	env := NewSignalEnvelope(r.UserID, SignalShutdown, SignalShutdownPayload{
		GraceSeconds:         request.GraceSeconds,
		DisconnectGameServer: false,
	})
	signalResponse, err := nk.MatchSignal(ctx, request.MatchID.String(), env.String())
	if err != nil {
		return "", err
	}

	groupID := ""
	if gid := label.GetGroupID(); !gid.IsNil() {
		groupID = gid.String()
	}
	sendRPCAuditMessage(
		ctx,
		logger,
		nk,
		"match/terminate",
		groupID,
		r.UserID,
		fmt.Sprintf("match_id=%s mode=%s grace_seconds=%d", request.MatchID.String(), label.Mode.String(), request.GraceSeconds),
	)

	response := &shutdownMatchResponse{
		Success:  true,
		Response: signalResponse,
	}
	jsonData, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(jsonData), nil
}

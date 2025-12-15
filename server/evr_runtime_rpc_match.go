package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	nevrapi "github.com/echotools/nevr-common/v4/gen/go/api"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

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
	request := &nevrapi.LobbySessionCreateRequest{}
	if err := protojson.Unmarshal([]byte(payload), request); err != nil {
		return "", runtime.NewError("failed to unmarshal request: "+err.Error(), StatusInvalidArgument)
	}
	if request.GuildGroupId == "" {
		return "", runtime.NewError("group/guild ID must be specified", StatusInvalidArgument)
	}

	if uuid.FromStringOrNil(request.GuildGroupId) == uuid.Nil {
		// Assume the groupId is a guild ID and convert it to a group ID
		request.GuildGroupId, err = GetGroupIDByGuildID(ctx, db, request.GuildGroupId)
		if err != nil {
			return "", runtime.NewError(err.Error(), StatusInternalError)
		} else if request.GuildGroupId == "" {
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
	gg, err := GuildGroupLoad(ctx, nk, request.GuildGroupId)
	if err != nil {
		return "", runtime.NewError(err.Error(), StatusInternalError)
	}

	// Validate that the user has permissions to allocate to the target guild
	if !gg.HasRole(userID, gg.RoleMap.Allocator) {
		return "", runtime.NewError("user must have the `allocator` in the guild.", StatusPermissionDenied)
	}

	// Increase the expiry time to a minimum of 1 minute from now
	if request.MatchExpiry.AsTime().Before(time.Now().Add(1 * time.Minute)) {
		request.MatchExpiry = timestamppb.New(time.Now().Add(1 * time.Minute))
	}

	// Build the team alignments map from the request
	var teamAlignments = make(map[string]int, len(request.Reservations))
	for _, entrant := range request.Reservations {
		id := entrant.GetId()
		roleAlignment := nevrapi.LobbySessionCreateRequest_EntrantReservation_RoleAlignment(entrant.GetRoleAlignment())
		// Set the teamIndex based on the roleName
		teamIndex := AnyTeam
		switch roleAlignment {
		case nevrapi.LobbySessionCreateRequest_EntrantReservation_ROLE_ALIGNMENT_BLUE:
			teamIndex = BlueTeam
		case nevrapi.LobbySessionCreateRequest_EntrantReservation_ROLE_ALIGNMENT_ORANGE:
			teamIndex = OrangeTeam
		case nevrapi.LobbySessionCreateRequest_EntrantReservation_ROLE_ALIGNMENT_SPECTATOR:
			teamIndex = Spectator
		case nevrapi.LobbySessionCreateRequest_EntrantReservation_ROLE_ALIGNMENT_SOCIAL:
			teamIndex = SocialLobbyParticipant
		default:
			return "", runtime.NewError("invalid team alignment role", StatusInvalidArgument)
		}

		roleID := int(teamIndex)

		if uuid.FromStringOrNil(id) == uuid.Nil {
			// Assume the id is a discord ID and convert it to a user ID
			userID, err := GetUserIDByDiscordID(ctx, db, id)
			if err != nil {
				return "", runtime.NewError("Failed to get userID by discord ID: "+err.Error(), StatusNotFound)
			}
			if userID == "" {
				return "", runtime.NewError(fmt.Sprintf("discord user not found: %s", id), StatusNotFound)
			}
			teamAlignments[userID] = roleID // Assuming roleName is an int32 representing the team index
		}

		teamAlignments[id] = roleID // Assuming id is a user ID

	}

	// Otherwise, find an open server in the given region
	settings := &MatchSettings{
		Mode:             evr.ToSymbol(request.Mode),
		Level:            evr.ToSymbol(request.Level),
		TeamSize:         int(request.GetTeamSize()),
		StartTime:        request.GetMatchExpiry().AsTime().UTC(),
		SpawnedBy:        request.OwnerId,
		GroupID:          uuid.FromStringOrNil(request.GetGuildGroupId()),
		RequiredFeatures: request.RequiredFeatures,
		TeamAlignments:   teamAlignments,
	}

	// Load the latency history for the match owner
	latencyHistory := &LatencyHistory{}
	if err := StorableRead(ctx, nk, request.OwnerId, latencyHistory, true); err != nil {
		return "", runtime.NewError("Failed to read latency history: "+err.Error(), StatusInternalError)
	}

	// Allocate a server by region code
	var label *MatchLabel
	if matchID := MatchIDFromStringOrNil(request.GetRegionCode()); !matchID.IsNil() {
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

		// Prepare the session with the given match ID
		label, err = LobbyPrepareSession(ctx, nk, label.ID, settings)
		if err != nil || label == nil {
			return err.Error(), runtime.NewError(err.Error(), StatusInvalidArgument)
		}
	} else {
		// Allocate a game server for the given group ID and region.
		// Default to the "default" region if no region is specified
		if request.GetRegionCode() == "" {
			request.RegionCode = "default"
		}
		label, err = LobbyGameServerAllocate(ctx, logger, nk, []string{request.GetGuildGroupId()}, latencyHistory.LatestRTTs(), settings, []string{request.GetRegionCode()}, false, true, ServiceSettings().Matchmaking.QueryAddons.RPCAllocate)
		if err != nil || label == nil {
			if strings.Contains(err.Error(), "bad request:") {
				err = runtime.NewError("required features not supported", StatusInvalidArgument)
			}
			logger.WithField("error", err).Error("Failed to allocate game server")
			return "", runtime.NewError(err.Error(), StatusInvalidArgument)
		}
	}

	// Guild-specified server-hosts and the server's operators may signal their own game servers.
	// Otherwise, the game server must host for the guild.
	if label.GameServer.OperatorID.String() != userID && !slices.Contains(label.GameServer.GroupIDs, uuid.FromStringOrNil(request.GetGuildGroupId())) {
		return "", runtime.NewError("game server does not host for that guild.", StatusPermissionDenied)
	}

	// Log the match preparation as an audit log message
	if appBot := globalAppBot.Load(); appBot != nil && appBot.dg != nil {
		if settingsJSON, err := json.MarshalIndent(settings, "", "  "); err != nil {
			logger.Error("Failed to marshal match settings: %s", err.Error())
		} else {
			guid := strings.ToUpper(label.ID.UUID.String())
			auditMessage := fmt.Sprintf("<@%s> allocated https://echo.taxi/spark://j/%s via RPC:\n\n```json\n%s\n```", callerAccount.GetCustomId(), guid, string(settingsJSON))
			if _, err := AuditLogSendGuild(globalAppBot.Load().dg, gg, auditMessage); err != nil {
				logger.WithField("err", err).Error("Failed to send audit log message")
			}
		}
	}

	logger.WithFields(map[string]any{
		"mid":       label.ID.String(),
		"caller_id": userID,
		"gid":       request.GetGuildGroupId(),
		"owner_id":  request.GetOwnerId(),
		"mode":      label.Mode,
		"state":     string(label.GetLabel()),
	}).Info("Match prepared")

	// Return the match label as a JSON string
	return string(label.GetLabelIndented()), nil
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

	r := NewRuntimeContext(ctx)

	request := &shutdownMatchRequest{}
	if err := json.Unmarshal([]byte(payload), request); err != nil {
		return "", err
	}

	label, err := MatchLabelByID(ctx, nk, request.MatchID)
	if err != nil {
		return "", err
	}

	if label.LobbyType == UnassignedLobby {
		return "", fmt.Errorf("match %s is not in a lobby", request.MatchID)
	}
	if request.GraceSeconds <= 0 {
		request.GraceSeconds = 10
	}

	env := NewSignalEnvelope(r.UserID, SignalShutdown, SignalShutdownPayload{
		GraceSeconds:         request.GraceSeconds,
		DisconnectGameServer: false,
		DisconnectUsers:      false,
	})

	signalResponse, err := nk.MatchSignal(ctx, request.MatchID.String(), env.String())
	if err != nil {
		return "", err
	}

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

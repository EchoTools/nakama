package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	ErrorEntrantNotFound       = errors.New("entrant not found")
	ErrorMultipleEntrantsFound = errors.New("multiple entrants found")
	ErrorMatchNotFound         = errors.New("match not found")
)

// lobbyMatchmakerStatusRequest is a message requesting the status of the matchmaker.
func (p *EvrPipeline) lobbyMatchmakerStatusRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	_ = in.(*evr.LobbyMatchmakerStatusRequest)

	// TODO Check if the matchmaking ticket is still open
	err := session.SendEvr(evr.NewLobbyMatchmakerStatusResponse())
	if err != nil {
		return fmt.Errorf("LobbyMatchmakerStatus: %v", err)
	}
	return nil
}

// authorizeMatchmaking checks if the user is allowed to join a public match or spawn a new match
func (p *EvrPipeline) authorizeMatchmaking(ctx context.Context, logger *zap.Logger, session *sessionWS, loginSessionID uuid.UUID, groupID uuid.UUID, requireMembership bool) error {

	if groupID == uuid.Nil {
		var ok bool
		groupID, ok = ctx.Value(ctxGroupIDKey{}).(uuid.UUID)
		if !ok {
			return status.Errorf(codes.InvalidArgument, "Failed to get group ID from context")
		}
	}

	// Get the EvrID from the context
	evrID, ok := ctx.Value(ctxEvrIDKey{}).(evr.EvrId)
	if !ok {
		return status.Errorf(codes.InvalidArgument, "Failed to get EVR ID")
	}

	// Send a match leave if this user is in another match
	if session.userID == uuid.Nil {
		return status.Errorf(codes.Unauthenticated, "User not authenticated")
	}

	// Only bots may join multiple matches
	singleMatch := true
	if flags, ok := ctx.Value(ctxFlagsKey{}).(int); ok {
		if flags&FlagGlobalBots != 0 || flags&FlagGlobalDevelopers != 0 {
			singleMatch = false
		}
	}

	if singleMatch {
		// Disconnect this EVRID from other matches
		sessionIDs := session.tracker.ListLocalSessionIDByStream(PresenceStream{Mode: StreamModeService, Subject: evrID.UUID(), Label: StreamLabelMatchService})
		for _, foundSessionID := range sessionIDs {
			if foundSessionID == session.id {
				// Allow the current session, only disconnect any older ones.
				continue
			}

			// Disconnect the older session.
			logger.Debug("Disconnecting older session from matchmaking", zap.String("other_sid", foundSessionID.String()))
			fs := p.sessionRegistry.Get(foundSessionID)
			if fs == nil {
				logger.Warn("Failed to find older session to disconnect", zap.String("other_sid", foundSessionID.String()))
				continue
			}
			// Send an error
			fs.Close("New session started", runtime.PresenceReasonDisconnect)
		}
	}
	// Track this session as a matchmaking session. (This is a track and not an update, because if user is in a match, that data should remain)
	s := session
	s.tracker.TrackMulti(s.ctx, s.id, []*TrackerOp{
		{
			Stream: PresenceStream{Mode: StreamModeService, Subject: session.userID, Subcontext: StreamContextMatch},
			Meta:   PresenceMeta{Format: s.format, Hidden: true},
		},
		// By login sessionID and match service ID
		{
			Stream: PresenceStream{Mode: StreamModeService, Subject: loginSessionID, Subcontext: StreamContextMatch},
			Meta:   PresenceMeta{Format: s.format, Hidden: true},
		},
		// By EVRID and match service ID
		{
			Stream: PresenceStream{Mode: StreamModeService, Subject: evrID.UUID(), Subcontext: StreamContextMatch},
			Meta:   PresenceMeta{Format: s.format, Hidden: true},
		},
		// EVR packet data stream for the match session by Session ID and service ID
	}, s.userID)

	// Check if th user is a member of this guild
	guildID, err := GetGuildIDByGroupID(ctx, p.db, groupID.String())
	if err != nil {
		return status.Errorf(codes.Internal, "Failed to get guild ID: %v", err)
	}
	discordID, err := GetDiscordIDByUserID(ctx, p.db, session.userID.String())
	if err != nil {
		return status.Errorf(codes.Internal, "Failed to get discord id: %v", err)
	}

	// Check if the user is a member of this guild
	member, err := p.discordRegistry.GetGuildMember(ctx, guildID, discordID)
	if err != nil || member == nil || member.User == nil {
		if requireMembership {
			return status.Errorf(codes.PermissionDenied, "User is not a member of this guild")
		} else {
			return status.Errorf(codes.NotFound, "User is not a member of this guild")
		}
	}

	// Check if the guild has a membership role defined in the metadata
	if requireMembership {
		metadata, err := p.discordRegistry.GetGuildGroupMetadata(ctx, groupID.String())
		if err != nil {
			return status.Errorf(codes.Internal, "Failed to get guild metadata: %v", err)
		}
		if metadata.MemberRole != "" {
			// Check if the user has the membership role
			if !lo.Contains(member.Roles, metadata.MemberRole) {
				return status.Errorf(codes.PermissionDenied, "User does not have the required role to join this guild")
			}
		}
	}

	// Check for suspensions on this groupID.
	suspensions, err := p.checkSuspensionStatus(ctx, logger, session.UserID().String(), groupID)
	if err != nil {
		return status.Errorf(codes.Internal, "Failed to check suspension status: %v", err)
	}
	if len(suspensions) != 0 {
		msg := suspensions[0].Reason

		return status.Errorf(codes.PermissionDenied, msg)
	}
	return nil
}

func (p *EvrPipeline) matchmakingLabelFromFindRequest(ctx context.Context, session *sessionWS, request *evr.LobbyFindSessionRequest) (*MatchLabel, error) {

	// If the channel is nil, use the players profile channel

	groupID := request.GroupID
	if groupID == uuid.Nil {
		var ok bool
		groupID, ok = ctx.Value(ctxGroupIDKey{}).(uuid.UUID)
		if !ok {
			return nil, status.Errorf(codes.InvalidArgument, "Failed to get group ID from context")
		}
	}

	_, guildPriority, err := p.GetGuildPriorityList(ctx, session.userID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to get guild priority list: %v", err)
	}

	if request.Mode == evr.ModeArenaPublicAI {
		request.Mode = evr.ModeCombatPublic
		request.Level = evr.LevelUnspecified
		request.Entrants[0].Role = int8(AnyTeam)
		request.SessionSettings = evr.LobbySessionSettings{
			AppID: request.SessionSettings.AppID,
			Mode:  int64(evr.ModeCombatPublic),
		}
	}

	features := ctx.Value(ctxFeaturesKey{}).([]string)

	ml := &MatchLabel{
		GroupID: &groupID,

		Mode:  request.Mode,
		Level: request.Level,
		Open:  true,

		SessionSettings: &request.SessionSettings,
		TeamIndex:       TeamIndex(request.Entrants[0].Role),

		Broadcaster: MatchBroadcaster{
			VersionLock: uint64(request.VersionLock),
			GroupIDs:    guildPriority,
			Features:    features,
		},
	}
	if !request.CurrentLobbySessionID.IsNil() {
		ml.ID = MatchID{request.CurrentLobbySessionID, p.node} // The existing lobby/match that the player is in (if any)
	}

	// Check if the team index is valid for the mode
	if !slices.Contains(evr.AlignmentsByMode[ml.Mode], int(ml.TeamIndex)) {
		ml.TeamIndex = AnyTeam
	}

	return ml, nil

}

// lobbyFindSessionRequest is a message requesting to find a public session to join.
func (p *EvrPipeline) lobbyFindSessionRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) (err error) {
	// Instantly return to avoid any hangs
	go p.findSession(ctx, logger, session, in)
	return nil
}
func (p *EvrPipeline) findSession(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) (err error) {
	request := in.(*evr.LobbyFindSessionRequest)
	if len(request.Entrants) == 0 {
		return fmt.Errorf("request is missing entrants")
	}
	// Prepare the response message
	response := NewMatchmakingResult(logger, request.Mode, request.GroupID)

	// Build the matchmaking label using the request parameters
	ml, err := p.matchmakingLabelFromFindRequest(ctx, session, request)
	if err != nil {
		return response.SendErrorToSession(session, err)
	}

	_, groupMetadata, err := GetGuildGroupMetadata(ctx, p.runtimeModule, ml.GetGroupID().String())
	if err != nil {
		return nil
	}
	if groupMetadata.MembersOnlyMatchmaking {
		// Exclude labels that are not in the user's group
		ml.Broadcaster.GroupIDs = []uuid.UUID{ml.GetGroupID()}
	}

	metricsTags := map[string]string{
		"mode":     request.Mode.String(),
		"group_id": ml.GroupID.String(),
		"level":    request.Level.String(),
		"role":     strconv.FormatInt(int64(request.GetAlignment()), 10),
	}
	p.metrics.CustomCounter("lobbyfindsession_active_count", metricsTags, 1)

	// Check for suspensions on this channel, if this is a request for a public match.
	if err := p.authorizeMatchmaking(ctx, logger, session, request.LoginSessionID, *ml.GroupID, true); err != nil {
		switch status.Code(err) {
		case codes.Internal:
			logger.Warn("Failed to authorize matchmaking, allowing player to continue. ", zap.Error(err))
		default:
			return response.SendErrorToSession(session, err)
		}
	}
	go p.PrepareLobbyProfile(ctx, logger, session, request.GetEvrID(), session.userID.String(), ml.GroupID.String())

	ml.Broadcaster.Regions = []evr.Symbol{evr.DefaultRegion}

	// Wait for a graceperiod Unless this is a social lobby, wait for a grace period before starting the matchmaker

	if ml.Mode != evr.ModeSocialPublic {
		select {
		case <-time.After(MatchmakingStartGracePeriod):
		case <-ctx.Done():
			return
		}
	}

	// Determine the display name

	// Create the matchmaking session
	err = p.MatchFind(ctx, logger, session, ml)
	if err != nil {
		response.SendErrorToSession(session, err)
	}

	return nil
}

// lobbyPingResponse is a message responding to a ping request.
func (p *EvrPipeline) lobbyPingResponse(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	response := in.(*evr.LobbyPingResponse)

	// Look up the matching session.
	msession, ok := p.matchmakingRegistry.GetMatchingBySessionId(session.id)
	if !ok {
		logger.Debug("Matching session not found")
		return nil
	}
	select {
	case <-msession.Ctx.Done():
		return nil
	case msession.PingResultsCh <- response.Results:
	case <-time.After(time.Second * 2):
		logger.Debug("Failed to send ping results")
	}

	return nil
}

// lobbyCreateSessionRequest is a request to create a new session.
func (p *EvrPipeline) lobbyCreateSessionRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.LobbyCreateSessionRequest)
	response := NewMatchmakingResult(logger, request.Mode, request.GroupID)

	features := ctx.Value(ctxFeaturesKey{}).([]string)
	requiredFeatures := ctx.Value(ctxRequiredFeaturesKey{}).([]string)

	// Get the GroupID from the context
	groupID := request.GroupID
	if groupID != uuid.Nil {
		groups, _, err := p.runtimeModule.UserGroupsList(ctx, session.userID.String(), 200, nil, "")
		if err != nil {
			return status.Errorf(codes.Internal, "Failed to get user groups: %v", err)
		}

		groupStr := groupID.String()
		isMember := lo.ContainsBy(groups, func(g *api.UserGroupList_UserGroup) bool {
			return g.Group.Id == groupStr && g.State.GetValue() <= int32(api.UserGroupList_UserGroup_MEMBER)
		})
		if !isMember {
			groupID = uuid.Nil
		}
	}

	if groupID == uuid.Nil {
		var ok bool
		groupID, ok = ctx.Value(ctxGroupIDKey{}).(uuid.UUID)
		if !ok {
			return status.Errorf(codes.InvalidArgument, "Failed to get group ID from context")
		}
	}

	metricsTags := map[string]string{
		"type":     strconv.FormatInt(int64(request.LobbyType), 10),
		"mode":     request.Mode.String(),
		"group_id": request.GroupID.String(),
		"level":    request.Level.String(),
		"role":     strconv.FormatInt(int64(request.Entrants[0].Role), 10),
	}

	// Add the features to teh metrics tags as feature_<featurename>
	for _, feature := range features {
		metricsTags[fmt.Sprintf("feature_%s", feature)] = "1"
	}

	p.metrics.CustomCounter("lobbycreatesession_active_count", metricsTags, 1)
	loginSessionID := request.LoginSessionID

	// Check for membership and suspensions on this channel. The user will not be allowed to create lobby's
	if err := p.authorizeMatchmaking(ctx, logger, session, loginSessionID, groupID, true); err != nil {
		switch status.Code(err) {
		case codes.Internal:
			logger.Warn("Failed to authorize matchmaking, allowing player to continue. ", zap.Error(err))
		default:
			return response.SendErrorToSession(session, err)
		}
	}

	isDeveloper, err := CheckSystemGroupMembership(ctx, p.db, session.userID.String(), GroupGlobalDevelopers)
	logger.Info("User is developer", zap.Bool("isDeveloper", isDeveloper))
	if err != nil {
		return response.SendErrorToSession(session, status.Errorf(codes.Internal, "Failed to check if user is a developer: %v", err))
	}
	if !isDeveloper {
		if levels, ok := evr.LevelsByMode[request.Mode]; ok {
			if request.Level != evr.LevelUnspecified && !lo.Contains(levels, request.Level) {
				return response.SendErrorToSession(session, status.Errorf(codes.InvalidArgument, "Invalid level %v for game mode %v", request.Level, request.Mode))
			}
		} else {
			return response.SendErrorToSession(session, status.Errorf(codes.InvalidArgument, "Failed to create matchmaking session: Tried to create a match with an unknown level or gamemode: %v", request.Mode))
		}
	}

	regions := make([]evr.Symbol, 0)
	if request.Region == evr.UnspecifiedRegion {
		request.Region = evr.DefaultRegion
	} else if request.Region != evr.DefaultRegion {
		regions = append(regions, request.Region)
	}
	regions = append(regions, evr.DefaultRegion)

	// Make the regions unique without resorting it
	uniqueRegions := make([]evr.Symbol, 0, len(regions))
	seen := make(map[evr.Symbol]struct{}, len(regions))
	for _, region := range regions {
		if _, ok := seen[region]; !ok {
			uniqueRegions = append(uniqueRegions, region)
			seen[region] = struct{}{}
		}
	}

	_, priorities, err := p.GetGuildPriorityList(ctx, session.userID)
	if err != nil {
		logger.Warn("Failed to get guild priority list", zap.Error(err))
	}

	ml := &MatchLabel{
		Level:            request.Level,
		LobbyType:        LobbyType(request.LobbyType),
		Mode:             request.Mode,
		Open:             true,
		SessionSettings:  &request.SessionSettings,
		TeamIndex:        TeamIndex(request.GetAlignment()),
		GroupID:          &request.GroupID,
		RequiredFeatures: requiredFeatures,
		Broadcaster: MatchBroadcaster{
			VersionLock: uint64(request.VersionLock),
			Regions:     uniqueRegions,
			GroupIDs:    priorities,
			Features:    features,
		},
	}
	p.PrepareLobbyProfile(ctx, logger, session, request.GetEvrID(), session.userID.String(), ml.GroupID.String())

	// Start the search in a goroutine.
	go func() error {
		// Set some defaults

		// Create a matching session
		timeout := 15 * time.Minute
		msession, err := p.matchmakingRegistry.Create(ctx, logger, session, ml, timeout)
		if err != nil {
			return response.SendErrorToSession(session, status.Errorf(codes.Internal, "Failed to create matchmaking session: %v", err))
		}

		// Leave any existing party group
		msession.LeavePartyGroup()

		p.metrics.CustomCounter("create_active_count", map[string]string{}, 1)
		err = p.MatchCreateLoop(session, msession, 5*time.Minute)
		if err != nil {
			return response.SendErrorToSession(session, err)
		}
		return nil
	}()

	return nil
}

// lobbyJoinSessionRequest is a request to join a specific existing session.
func (p *EvrPipeline) lobbyJoinSessionRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.LobbyJoinSessionRequest)
	response := NewMatchmakingResult(logger, 0xFFFFFFFFFFFFFFFF, request.MatchID)
	loginSessionID := request.LoginSessionID

	roleAlignment := int(request.GetAlignment())

	// Make sure the match exists
	matchToken, err := NewMatchID(request.MatchID, p.node)
	if err != nil {
		return response.SendErrorToSession(session, status.Errorf(codes.InvalidArgument, "Invalid match ID"))
	}
	match, _, err := p.matchRegistry.GetMatch(ctx, matchToken.String())
	if err != nil || match == nil {
		return response.SendErrorToSession(session, status.Errorf(codes.NotFound, "Match not found"))
	}

	// Extract the label
	ml := &MatchLabel{}
	if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), ml); err != nil {
		return response.SendErrorToSession(session, status.Errorf(codes.NotFound, err.Error()))
	}
	if ml.GroupID == nil {
		ml.GroupID = &uuid.Nil
	}

	p.PrepareLobbyProfile(ctx, logger, session, request.GetEvrID(), session.userID.String(), ml.GroupID.String())

	metricsTags := map[string]string{
		"role":     fmt.Sprintf("%d", roleAlignment),
		"mode":     ml.Mode.String(),
		"group_id": ml.GroupID.String(),
		"level":    ml.Level.String(),
	}
	p.metrics.CustomCounter("lobbyjoinsession_active_count", metricsTags, 1)

	switch {

	case ml.LobbyType == UnassignedLobby:
		err = status.Errorf(codes.NotFound, "Match is not a lobby")
	case !ml.Open:
		err = status.Errorf(codes.InvalidArgument, "Match is not open")
	case int(ml.Size) >= int(ml.MaxSize):
		err = status.Errorf(codes.ResourceExhausted, "Match is full")
	case ml.LobbyType == PublicLobby:
		// Check if this player is a global moderator or developer
		isModerator, _ := CheckSystemGroupMembership(ctx, p.db, session.userID.String(), GroupGlobalModerators)
		isDeveloper, _ := CheckSystemGroupMembership(ctx, p.db, session.userID.String(), GroupGlobalDevelopers)

		if !isDeveloper {
			// Let developers join public matches however they want

			switch request.GetAlignment() {
			case int8(Spectator):
				if ml.Mode == evr.ModeSocialPublic || ml.Mode == evr.ModeSocialPrivate {
					err = status.Errorf(codes.PermissionDenied, "social lobbies can only be joined by moderators and social participants.")
				}
				// Allow spectators to join public matches
			case int8(Moderator):
				// Allow moderators to join social lobbies
				if !isModerator || (ml.Mode != evr.ModeSocialPublic && ml.Mode != evr.ModeSocialPrivate) {
					request.SetAlignment(int(AnyTeam))
				}
			case int8(SocialLobbyParticipant):
				if ml.Mode != evr.ModeSocialPublic && ml.Mode != evr.ModeSocialPrivate {
					request.SetAlignment(int(AnyTeam))
				}
			default:
				if time.Since(ml.StartTime) < time.Second*10 {
					// Allow if the match is over 15 seconds old, to allow matchmaking to properly populate the match
					err = status.Errorf(codes.InvalidArgument, "Match is a newly started public match")
				}
				request.SetAlignment(int(AnyTeam))
			}
		}
	}

	if err != nil {
		return response.SendErrorToSession(session, err)
	}

	// Ensure the client has the required features
	if len(ml.RequiredFeatures) > 0 {
		features, ok := ctx.Value(ctxFeaturesKey{}).([]string)
		if !ok {
			features = make([]string, 0)
		}
		for _, f := range ml.RequiredFeatures {
			if !lo.Contains(features, f) {
				return response.SendErrorToSession(session, status.Errorf(codes.FailedPrecondition, "Missing required feature: %v", f))
			}
		}
	}
	if err := p.authorizeMatchmaking(ctx, logger, session, loginSessionID, *ml.GroupID, false); err != nil {
		switch status.Code(err) {
		case codes.Internal:
			logger.Warn("Failed to authorize matchmaking, allowing player to continue. ", zap.Error(err))
		case codes.NotFound:
			// Allow the player to join the match even though they are not a member.
		default:
			return response.SendErrorToSession(session, err)
		}
	}
	msession, err := p.matchmakingRegistry.Create(ctx, logger, session, ml, 1*time.Minute)
	if err != nil {
		return response.SendErrorToSession(session, status.Errorf(codes.Internal, "Failed to create matchmaking session: %v", err))
	}
	logger.Info("Joining match", zap.String("mid", matchToken.String()), zap.Int("role", int(request.GetAlignment())), zap.String("session_id", session.id.String()))
	if err = p.LobbyJoin(ctx, logger, matchToken, int(request.GetAlignment()), "", msession); err != nil {
		return response.SendErrorToSession(session, status.Errorf(codes.NotFound, err.Error()))
	}

	msession.LeavePartyGroup()
	p.metrics.CustomCounter("match_join_direct_count", metricsTags, 1)
	return nil
}

// LobbyPendingSessionCancel is a message from the server to the client, indicating that the user wishes to cancel matchmaking.
func (p *EvrPipeline) lobbyPendingSessionCancel(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	// Look up the matching session.
	if matchingSession, ok := p.matchmakingRegistry.GetMatchingBySessionId(session.id); ok {
		matchingSession.Cancel(ErrMatchmakingCanceledByPlayer)
	}
	return nil
}

// lobbyPlayerSessionsRequest is called when a client requests the player sessions for a list of EchoVR IDs.
// Player Sessions are UUIDv5 of the MatchID and EVR-ID
func (p *EvrPipeline) lobbyPlayerSessionsRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	message := in.(*evr.LobbyPlayerSessionsRequest)

	matchID, err := NewMatchID(message.LobbyID, p.node)
	if err != nil {
		return fmt.Errorf("failed to create match ID: %w", err)
	}
	entrantID := uuid.NewV5(message.LobbyID, message.GetEvrID().String())

	presence, err := PresenceByEntrantID(p.runtimeModule, matchID, entrantID)
	if err != nil {
		return fmt.Errorf("failed to get lobby presence for entrant `%s`: %w", entrantID.String(), err)
	}

	entrantIDs := make([]uuid.UUID, len(message.PlayerEvrIDs))
	for i, e := range message.PlayerEvrIDs {
		entrantIDs[i] = uuid.NewV5(message.LobbyID, e.String())
	}

	entrant := evr.NewLobbyEntrant(message.EvrId, message.LobbyID, entrantID, entrantIDs, int16(presence.RoleAlignment))

	return session.SendEvr(entrant.Version3())
}

func (p *EvrPipeline) PrepareLobbyProfile(ctx context.Context, logger *zap.Logger, session *sessionWS, evrID evr.EvrId, userID, groupID string) {
	// prepare the profile ahead of time
	var err error
	displayName, ok := ctx.Value(ctxDisplayNameOverrideKey{}).(string)
	if !ok {
		displayName, err = GetDisplayNameByGroupID(ctx, p.runtimeModule, userID, groupID)
		if err != nil {
			logger.Warn("Failed to set display name.", zap.Error(err))
		}

	}

	if err := p.profileRegistry.SetLobbyProfile(ctx, session.userID, evrID, displayName); err != nil {
		logger.Warn("Failed to set lobby profile", zap.Error(err))
		return
	}
}

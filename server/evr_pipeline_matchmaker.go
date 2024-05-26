package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
func (p *EvrPipeline) authorizeMatchmaking(ctx context.Context, logger *zap.Logger, session *sessionWS, loginSessionID uuid.UUID, channel uuid.UUID) (bool, error) {

	// Get the EvrID from the context
	evrID, ok := ctx.Value(ctxEvrIDKey{}).(evr.EvrId)
	if !ok {
		return false, fmt.Errorf("failed to get evrID from context")
	}
	if !evrID.Valid() {
		return false, fmt.Errorf("evrID is invalid")
	}

	// Send a match leave if this user is in another match
	if session.userID == uuid.Nil {
		return false, status.Errorf(codes.PermissionDenied, "User not authenticated")
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
		sessionIDs := session.tracker.ListLocalSessionIDByStream(PresenceStream{Mode: StreamModeEvr, Subject: evrID.UUID(), Subcontext: svcMatchID})
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
	// Track this session as a matchmaking session.
	s := session
	s.tracker.TrackMulti(s.ctx, s.id, []*TrackerOp{
		{
			Stream: PresenceStream{Mode: StreamModeEvr, Subject: session.id, Subcontext: svcMatchID},
			Meta:   PresenceMeta{Format: s.format, Hidden: true},
		},
		// By login sessionID and match service ID
		{
			Stream: PresenceStream{Mode: StreamModeEvr, Subject: loginSessionID, Subcontext: svcMatchID},
			Meta:   PresenceMeta{Format: s.format, Hidden: true},
		},
		// By EVRID and match service ID
		{
			Stream: PresenceStream{Mode: StreamModeEvr, Subject: evrID.UUID(), Subcontext: svcMatchID},
			Meta:   PresenceMeta{Format: s.format, Hidden: true},
		},
		// EVR packet data stream for the match session by Session ID and service ID
	}, s.userID)

	if channel == uuid.Nil {
		logger.Warn("Channel is nil")
		return true, nil
	}

	// Check for suspensions on this channel.
	suspensions, err := p.checkSuspensionStatus(ctx, logger, session.UserID().String(), channel)
	if err != nil {
		return true, status.Errorf(codes.Internal, "Failed to check suspension status: %v", err)
	}
	if len(suspensions) != 0 {
		msg := suspensions[0].Reason

		return false, status.Errorf(codes.PermissionDenied, msg)
	}
	return true, nil
}
func (p *EvrPipeline) matchmakingLabelFromFindRequest(ctx context.Context, session *sessionWS, request *evr.LobbyFindSessionRequest) (*EvrMatchState, error) {
	// Get the EvrID from the context (ignoring the request)
	evrID, ok := ctx.Value(ctxEvrIDKey{}).(evr.EvrId)
	if !ok {
		return nil, fmt.Errorf("failed to get evrID from context")
	}

	// If the channel is nil, use the players profile channel
	channel := request.Channel
	if channel == uuid.Nil {
		profile, ok := p.profileRegistry.Load(session.userID, evrID)
		if !ok {
			return nil, status.Errorf(codes.Internal, "Failed to get players profile")
		}
		channel = profile.GetChannel()
	}

	// Set the channels this player is allowed to matchmake/create a match on.
	groups, err := p.discordRegistry.GetGuildGroups(ctx, session.UserID())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to get guild groups: %v", err)
	}

	allowedChannels := make([]uuid.UUID, 0, len(groups))
	for _, group := range groups {
		allowedChannels = append(allowedChannels, uuid.FromStringOrNil(group.Id))
	}

	return &EvrMatchState{
		Channel: &channel,

		MatchID: request.CurrentMatch, // The existing lobby/match that the player is in (if any)
		Mode:    request.Mode,
		Level:   request.Level,
		Open:    true,

		SessionSettings: &request.SessionSettings,
		TeamIndex:       TeamIndex(request.TeamIndex),

		Broadcaster: MatchBroadcaster{
			VersionLock: request.VersionLock,
			Channels:    allowedChannels,
		},
	}, nil
}

// lobbyFindSessionRequest is a message requesting to find a public session to join.
func (p *EvrPipeline) lobbyFindSessionRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) (err error) {
	request := in.(*evr.LobbyFindSessionRequest)
	metricsTags := map[string]string{
		"mode":     request.Mode.String(),
		"channel":  request.Channel.String(),
		"level":    request.Level.String(),
		"team_idx": strconv.FormatInt(int64(request.TeamIndex), 10),
	}
	p.metrics.CustomCounter("lobbyfindsession_active_count", metricsTags, 1)
	loginSessionID := request.LoginSessionID
	groups, priorities, err := p.GetGuildPriorityList(ctx, session.userID)
	if err != nil {
		logger.Warn("Failed to get guild priority list", zap.Error(err))
	}

	// Validate that the channel is in the user's guilds
	if request.Channel == uuid.Nil || !lo.Contains(groups, request.Channel) {
		if len(priorities) > 0 {
			// If the channel is nil, use the players primary channel
			request.Channel = priorities[0]
		} else if len(groups) > 0 {
			// If the player has no guilds, use the first guild
			request.Channel = groups[0]
		} else {
			// If the player has no guilds
			return NewMatchmakingResult(logger, request.Mode, uuid.Nil).SendErrorToSession(session, status.Errorf(codes.PermissionDenied, "No guilds available"))
		}
	}

	// Prepare the response message
	response := NewMatchmakingResult(logger, request.Mode, request.Channel)

	// Build the matchmaking label using the request parameters
	ml, err := p.matchmakingLabelFromFindRequest(ctx, session, request)
	if err != nil {
		return response.SendErrorToSession(session, err)
	}

	// Check for suspensions on this channel, if this is a request for a public match.
	if authorized, err := p.authorizeMatchmaking(ctx, logger, session, loginSessionID, *ml.Channel); !authorized {
		return response.SendErrorToSession(session, err)
	} else if err != nil {
		logger.Warn("Failed to authorize matchmaking, allowing player to continue. ", zap.Error(err))
	}

	// Wait for a graceperiod Unless this is a social lobby, wait for a grace period before starting the matchmaker
	matchmakingDelay := MatchJoinGracePeriod
	if ml.Mode == evr.ModeSocialPublic {
		matchmakingDelay = 0
	}
	select {
	case <-time.After(matchmakingDelay):
	case <-ctx.Done():
		return nil
	}

	go func() {
		// Create the matchmaking session
		err = p.MatchFind(ctx, logger, session, ml)
		if err != nil {
			response.SendErrorToSession(session, err)
		}
	}()

	return nil
}

func (p *EvrPipeline) MatchSpectateStreamLoop(session *sessionWS, msession *MatchmakingSession, skipDelay bool, create bool) error {
	logger := msession.Logger
	ctx := msession.Context()
	p.metrics.CustomCounter("spectatestream_active_count", msession.metricsTags(), 1)
	// Create a ticker to spectate
	spectateInterval := time.Duration(10) * time.Second

	limit := 100
	minSize := 3
	maxSize := MatchMaxSize - 1
	query := fmt.Sprintf("+label.open:T +label.lobby_type:public +label.mode:%s +label.size:>=2", msession.Label.Mode.Token())
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		// list existing matches
		matches, err := listMatches(ctx, p, limit, minSize, maxSize, query)
		if err != nil {
			return msession.Cancel(fmt.Errorf("failed to find spectate match: %w", err))
		}

		if len(matches) != 0 {
			// sort matches by population
			sort.SliceStable(matches, func(i, j int) bool {
				// Sort by newer matches
				return matches[i].Size > matches[j].Size
			})

			// Found a backfill match
			foundMatch := FoundMatch{
				MatchID:   matches[0].GetMatchId(),
				Query:     query,
				TeamIndex: TeamIndex(evr.TeamSpectator),
			}
			select {
			case <-ctx.Done():
				return nil
			case msession.MatchJoinCh <- foundMatch:
				p.metrics.CustomCounter("spectatestream_found_count", msession.metricsTags(), 1)
				logger.Debug("Spectating match", zap.String("mid", foundMatch.MatchID))
			case <-time.After(3 * time.Second):
				p.metrics.CustomCounter("spectatestream_join_timeout_count", msession.metricsTags(), 1)
				logger.Warn("Failed to spectate match", zap.String("mid", foundMatch.MatchID))
			}
		}
		<-time.After(spectateInterval)
	}
}

func (p *EvrPipeline) MatchBackfillLoop(session *sessionWS, msession *MatchmakingSession, skipDelay bool, create bool, minCount int) error {
	interval := p.config.GetMatchmaker().IntervalSec
	idealMatchIntervals := p.config.GetMatchmaker().RevThreshold
	logger := msession.Logger
	ctx := msession.Context()
	// Wait for at least 1 interval before starting to look for a backfill.
	// This gives the matchmaker a chance to find a full ideal match
	backfillDelay := time.Duration(interval*idealMatchIntervals) * time.Second
	if skipDelay {
		backfillDelay = 0 * time.Second
	} else if msession.Party != nil && !msession.Party.ID.IsNil() {
		// Add extra delay for backfill when a user is in a party
		backfillDelay += 45 * time.Second
	}

	// Check for a backfill match on a regular basis
	backfilInterval := time.Duration(10) * time.Second

	// Create a ticker to check for backfill matches
	backfillTicker := time.NewTimer(backfilInterval)
	backfillDelayTimer := time.NewTimer(backfillDelay)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-backfillDelayTimer.C:
		case <-backfillTicker.C:
		}
		if msession.Label.Mode != evr.ModeSocialPublic && msession.Party != nil && msession.Party.members.Size() > 1 {
			// Don't backfill party members, let the matchmaker handle it.
			continue
		}

		foundMatch := FoundMatch{}
		// Backfill any existing matches
		label, query, err := p.Backfill(ctx, session, msession, minCount)
		if err != nil {
			return msession.Cancel(fmt.Errorf("failed to find backfill match: %w", err))
		}

		if label != nil {
			// Found a backfill match
			foundMatch = FoundMatch{
				MatchID:   label.MatchID.String(),
				Query:     query,
				TeamIndex: TeamIndex(evr.TeamUnassigned),
			}
		} else if create {
			create = false
			// Start the create loop too
			go p.MatchCreateLoop(session, msession, 5*time.Minute)
		}

		if foundMatch.MatchID == "" {
			// No match found
			continue
		}

		select {
		case <-ctx.Done():
			return nil
		default:
		}
		logger.Debug("Attempting to backfill match", zap.String("mid", foundMatch.MatchID))
		p.metrics.CustomCounter("match_backfill_found_count", msession.metricsTags(), 1)
		msession.MatchJoinCh <- foundMatch

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(3 * time.Second):
			p.metrics.CustomCounter("match_backfill_join_timeout_count", msession.metricsTags(), 1)
			logger.Warn("Failed to backfill match", zap.String("mid", foundMatch.MatchID))
		}
		// Continue to loop until the context is done

	}
}

func (p *EvrPipeline) MatchCreateLoop(session *sessionWS, msession *MatchmakingSession, pruneDelay time.Duration) error {
	ctx := msession.Context()
	logger := msession.Logger
	// set a timeout
	//stageTimer := time.NewTimer(pruneDelay)
	p.metrics.CustomCounter("match_create_active_count", msession.metricsTags(), 1)
	for {

		select {
		case <-ctx.Done():
			return nil
		default:
		}
		// Stage 1: Check if there is an available broadcaster
		matchID, err := p.MatchCreate(ctx, session, msession, msession.Label)

		switch status.Code(err) {

		case codes.OK:
			if matchID == "" {
				return msession.Cancel(fmt.Errorf("match is nil"))
			}
			foundMatch := FoundMatch{
				MatchID:   matchID,
				Query:     "",
				TeamIndex: TeamIndex(evr.TeamUnassigned),
			}
			select {
			case <-ctx.Done():
				return nil
			case msession.MatchJoinCh <- foundMatch:
				p.metrics.CustomCounter("match_create_join_active_count", msession.metricsTags(), 1)
				logger.Debug("Joining match", zap.String("mid", foundMatch.MatchID))
			case <-time.After(3 * time.Second):
				p.metrics.CustomCounter("make_create_create_join_timeout_count", msession.metricsTags(), 1)
				msession.Cancel(fmt.Errorf("failed to join match"))
			}
			// Keep trying until the context is done
		case codes.NotFound, codes.ResourceExhausted, codes.Unavailable:
			p.metrics.CustomCounter("create_unavailable_count", msession.metricsTags(), 1)
		default:
			return msession.Cancel(err)
		}
		<-time.After(10 * time.Second)
	}
}

func (p *EvrPipeline) MatchFind(parentCtx context.Context, logger *zap.Logger, session *sessionWS, ml *EvrMatchState) error {
	if s, found := p.matchmakingRegistry.GetMatchingBySessionId(session.id); found {
		// Replace the session
		logger.Warn("Matchmaking session already exists", zap.Any("tickets", s.Tickets))
	}
	joinFn := func(matchID string, query string) error {
		err := p.JoinEvrMatch(parentCtx, logger, session, query, matchID, int(ml.TeamIndex))
		if err != nil {
			return NewMatchmakingResult(logger, ml.Mode, *ml.Channel).SendErrorToSession(session, err)
		}
		return nil
	}
	errorFn := func(err error) error {
		return NewMatchmakingResult(logger, ml.Mode, *ml.Channel).SendErrorToSession(session, err)
	}

	// Create a new matching session
	logger.Debug("Creating a new matchmaking session")
	partySize := 1
	timeout := 60 * time.Minute

	if ml.TeamIndex == TeamIndex(evr.TeamSpectator) {
		timeout = 12 * time.Hour
	}

	msession, err := p.matchmakingRegistry.Create(parentCtx, logger, session, ml, partySize, timeout, errorFn, joinFn)
	if err != nil {
		logger.Error("Failed to create matchmaking session", zap.Error(err))
		return err
	}
	p.metrics.CustomCounter("match_find_active_count", msession.metricsTags(), 1)
	// Load the user's matchmaking config

	config, err := p.matchmakingRegistry.LoadMatchmakingSettings(msession.Ctx, session.logger, session.userID.String())
	if err != nil {
		logger.Error("Failed to load matchmaking config", zap.Error(err))
	}

	matchToken := ""
	// Check for a direct match first
	if config.NextMatchToken != "" {
		matchToken = config.NextMatchToken.String()
	}

	config.NextMatchToken = ""
	err = p.matchmakingRegistry.StoreMatchmakingSettings(msession.Ctx, session.logger, config, session.userID.String())
	if err != nil {
		logger.Error("Failed to save matchmaking config", zap.Error(err))
	}

	if matchToken != "" {
		logger.Debug("Attempting to join match from settings", zap.String("mid", matchToken))
		match, _, err := p.matchRegistry.GetMatch(msession.Ctx, matchToken)
		if err != nil {
			logger.Error("Failed to get match", zap.Error(err))
		} else {
			if match == nil {
				logger.Warn("Match not found", zap.String("mid", matchToken))
			} else {
				p.metrics.CustomCounter("match_next_join_count", map[string]string{}, 1)
				// Join the match
				msession.MatchJoinCh <- FoundMatch{
					MatchID:   matchToken,
					Query:     "",
					TeamIndex: TeamIndex(evr.TeamUnassigned),
				}
				<-time.After(3 * time.Second)
				select {
				case <-msession.Ctx.Done():
					return nil
				default:
				}
			}
		}
	}
	logger = msession.Logger

	skipBackfillDelay := false
	if ml.TeamIndex == TeamIndex(evr.TeamModerator) {
		skipBackfillDelay = true
		// Check that the user is a moderator for this channel, or globally
		guild, err := p.discordRegistry.GetGuildByGroupId(parentCtx, ml.Channel.String())
		if err != nil || guild == nil {
			logger.Warn("failed to get guild: %v", zap.Error(err))
			ml.TeamIndex = TeamIndex(evr.TeamSpectator)
		} else {
			discordID, err := p.discordRegistry.GetDiscordIdByUserId(parentCtx, session.userID)
			if err != nil {
				return fmt.Errorf("failed to get discord id: %v", err)
			}
			if ok, _, err := p.discordRegistry.isModerator(parentCtx, guild.ID, discordID); err != nil || !ok {
				ml.TeamIndex = TeamIndex(evr.TeamSpectator)
			}
		}
	}
	if ml.TeamIndex == TeamIndex(evr.TeamSpectator) {
		skipBackfillDelay = true
		if ml.Mode != evr.ModeArenaPublic && ml.Mode != evr.ModeCombatPublic {
			return fmt.Errorf("spectators are only allowed in arena and combat matches")
		}
		// Spectators don't matchmake, and they don't have a delay for backfill.
		// Spectators also don't time out.
		go p.MatchSpectateStreamLoop(session, msession, skipBackfillDelay, false)
		return nil
	}

	switch ml.Mode {
	// For public matches, backfill or matchmake
	// If it's a social match, backfill or create immediately
	case evr.ModeSocialPublic:
		// Continue to try to backfill
		skipBackfillDelay = true
		go p.MatchBackfillLoop(session, msession, skipBackfillDelay, true, 1)

		// For public arena/combat matches, backfill while matchmaking
	case evr.ModeCombatPublic:
		// Join any on-going combat match without delay
		skipBackfillDelay = false
		go p.MatchBackfillLoop(session, msession, skipBackfillDelay, false, 1)
		// For Arena and combat matches try to backfill while matchmaking

		// Put a ticket in for matching
		_, err := p.MatchMake(session, msession)
		if err != nil {
			return err
		}

	case evr.ModeArenaPublic:

		// Start the backfill loop
		go p.MatchBackfillLoop(session, msession, skipBackfillDelay, false, 1)

		// Put a ticket in for matching
		_, err := p.MatchMake(session, msession)
		if err != nil {
			return err
		}
	default:
		return status.Errorf(codes.InvalidArgument, "invalid mode")
	}

	return nil
}

// lobbyPingResponse is a message responding to a ping request.
func (p *EvrPipeline) lobbyPingResponse(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	response := in.(*evr.LobbyPingResponse)

	userID := session.userID
	// Validate the connection.
	if userID == uuid.Nil {
		return fmt.Errorf("session not authenticated")
	}

	// Look up the matching session.
	msession, ok := p.matchmakingRegistry.GetMatchingBySessionId(session.id)
	if !ok {
		return fmt.Errorf("matching session not found")
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

func (p *EvrPipeline) GetGuildPriorityList(ctx context.Context, userID uuid.UUID) (all []uuid.UUID, selected []uuid.UUID, err error) {
	evrID, ok := ctx.Value(ctxEvrIDKey{}).(evr.EvrId)
	if !ok {
		return nil, nil, fmt.Errorf("failed to get evrID from context")
	}

	profile, ok := p.profileRegistry.Load(userID, evrID)
	if !ok {
		return nil, nil, status.Errorf(codes.Internal, "Failed to get players profile")
	}

	currentChannel := profile.GetChannel()

	// Get the guild priority from the context
	groups, err := p.discordRegistry.GetGuildGroups(ctx, userID)
	if err != nil {
		return nil, nil, status.Errorf(codes.Internal, "Failed to get guilds: %v", err)
	}

	groupIDs := make([]uuid.UUID, 0)
	for _, group := range groups {
		groupIDs = append(groupIDs, uuid.FromStringOrNil(group.Id))
	}

	guildPriority := make([]uuid.UUID, 0)
	params, ok := ctx.Value(ctxUrlParamsKey{}).(map[string][]string)
	if ok {
		// If the params are set, use them
		for _, gid := range params["guilds"] {
			for _, guildId := range strings.Split(gid, ",") {
				s := strings.Trim(guildId, " ")
				if s != "" {
					// Get the groupId for the guild
					groupIDstr, found := p.discordRegistry.Get(s)
					if !found {
						continue
					}
					groupID := uuid.FromStringOrNil(groupIDstr)
					if groupID != uuid.Nil && lo.Contains(groupIDs, groupID) {
						guildPriority = append(guildPriority, groupID)
					}
				}
			}
		}
	} else {
		// If the params are not set, use the user's guilds
		guildPriority = []uuid.UUID{currentChannel}
		for _, groupID := range groupIDs {
			if groupID != currentChannel {
				guildPriority = append(guildPriority, groupID)
			}
		}
	}
	p.logger.Debug("guild priorites", zap.Any("prioritized", guildPriority), zap.Any("all", groupIDs))
	return groupIDs, selected, nil
}

// lobbyCreateSessionRequest is a request to create a new session.
func (p *EvrPipeline) lobbyCreateSessionRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.LobbyCreateSessionRequest)
	metricsTags := map[string]string{
		"type":     strconv.FormatInt(int64(request.LobbyType), 10),
		"mode":     request.Mode.String(),
		"channel":  request.Channel.String(),
		"level":    request.Level.String(),
		"team_idx": strconv.FormatInt(int64(request.TeamIndex), 10),
	}
	p.metrics.CustomCounter("lobbycreatesession_active_count", metricsTags, 1)
	loginSessionID := request.LoginSessionID
	groups, priorities, err := p.GetGuildPriorityList(ctx, session.userID)
	if err != nil {
		logger.Warn("Failed to get guild priority list", zap.Error(err))
	}

	result := NewMatchmakingResult(logger, request.Mode, request.Channel)

	// Validate that the channel is in the user's guilds
	if request.Channel == uuid.Nil || !lo.Contains(groups, request.Channel) {
		if len(priorities) > 0 {
			// If the channel is nil, use the players primary channel
			request.Channel = priorities[0]
		} else if len(groups) > 0 {
			// If the player has no guilds, use the first guild
			request.Channel = groups[0]
		} else {
			// If the player has no guilds,
			return NewMatchmakingResult(logger, request.Mode, uuid.Nil).SendErrorToSession(session, status.Errorf(codes.PermissionDenied, "No guilds available"))
		}

	}

	// Check for suspensions on this channel. The user will not be allowed to create lobby's
	if authorized, err := p.authorizeMatchmaking(ctx, logger, session, loginSessionID, request.Channel); !authorized {
		return result.SendErrorToSession(session, err)
	} else if err != nil {
		logger.Warn("Failed to authorize matchmaking, allowing player to continue. ", zap.Error(err))
	}
	// Validating the level against the game mode
	validLevels := map[evr.Symbol][]evr.Symbol{
		evr.ModeArenaPublic:          {evr.LevelArena},
		evr.ModeArenaPrivate:         {evr.LevelArena},
		evr.ModeArenaTournment:       {evr.LevelArena},
		evr.ModeArenaPublicAI:        {evr.LevelArena},
		evr.ModeArenaTutorial:        {evr.LevelArena},
		evr.ModeSocialPublic:         {evr.LevelSocial},
		evr.ModeSocialPrivate:        {evr.LevelSocial},
		evr.ModeSocialNPE:            {evr.LevelSocial},
		evr.ModeCombatPublic:         {evr.LevelCombustion, evr.LevelDyson, evr.LevelFission, evr.LevelGauss},
		evr.ModeCombatPrivate:        {evr.LevelCombustion, evr.LevelDyson, evr.LevelFission, evr.LevelGauss},
		evr.ModeEchoCombatTournament: {evr.LevelCombustion, evr.LevelDyson, evr.LevelFission, evr.LevelGauss},
	}
	isDeveloper, err := checkIfGlobalDeveloper(ctx, p.runtimeModule, session.userID)
	logger.Info("User is developer", zap.Bool("isDeveloper", isDeveloper))
	if err != nil {
		return result.SendErrorToSession(session, status.Errorf(codes.Internal, "Failed to check if user is a developer: %v", err))
	}
	if !isDeveloper {
		if levels, ok := validLevels[request.Mode]; ok {
			if request.Level != evr.LevelUnspecified && !lo.Contains(levels, request.Level) {
				return result.SendErrorToSession(session, status.Errorf(codes.InvalidArgument, "Invalid level %v for game mode %v", request.Level, request.Mode))
			}
		} else {
			return result.SendErrorToSession(session, status.Errorf(codes.InvalidArgument, "Failed to create matchmaking session: Tried to create a match with an unknown level or gamemode: %v", request.Mode))
		}
	}

	ml := &EvrMatchState{

		Level:           request.Level,
		LobbyType:       LobbyType(request.LobbyType),
		Mode:            request.Mode,
		Open:            true,
		SessionSettings: &request.SessionSettings,
		TeamIndex:       TeamIndex(request.TeamIndex),
		Channel:         &request.Channel,
		Broadcaster: MatchBroadcaster{
			VersionLock: uint64(request.VersionLock),
			Region:      request.Region,
			Channels:    priorities,
		},
	}

	// Start the search in a goroutine.
	go func() error {
		// Set some defaults
		partySize := 1 // TODO FIXME this should include the party size

		joinFn := func(matchID string, query string) error {
			logger := logger.With(zap.String("mid", matchID))

			err := p.JoinEvrMatch(ctx, logger, session, query, matchID, int(ml.TeamIndex))
			switch status.Code(err) {
			case codes.ResourceExhausted:
				logger.Warn("Failed to join match (retrying): ", zap.Error(err))
				return nil
			default:
				return err
			}
		}

		errorFn := func(err error) error {
			return NewMatchmakingResult(logger, ml.Mode, *ml.Channel).SendErrorToSession(session, err)
		}

		// Create a matching session
		timeout := 15 * time.Minute
		msession, err := p.matchmakingRegistry.Create(ctx, logger, session, ml, partySize, timeout, errorFn, joinFn)
		if err != nil {
			return result.SendErrorToSession(session, status.Errorf(codes.Internal, "Failed to create matchmaking session: %v", err))
		}
		p.metrics.CustomCounter("create_active_count", map[string]string{}, 1)
		err = p.MatchCreateLoop(session, msession, 5*time.Minute)
		if err != nil {
			return result.SendErrorToSession(session, err)
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
	// Make sure the match exists
	matchToken, err := NewMatchToken(request.MatchID, p.node)
	if err != nil {
		return response.SendErrorToSession(session, status.Errorf(codes.InvalidArgument, "Invalid match ID"))
	}
	match, _, err := p.matchRegistry.GetMatch(ctx, matchToken.String())
	if err != nil || match == nil {
		return response.SendErrorToSession(session, status.Errorf(codes.NotFound, "Match not found"))
	}

	// Extract the label
	ml := &EvrMatchState{}
	if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), ml); err != nil {
		return response.SendErrorToSession(session, status.Errorf(codes.NotFound, err.Error()))
	}
	if ml.Channel == nil {
		ml.Channel = &uuid.Nil
	}

	metricsTags := map[string]string{
		"team":    TeamIndex(request.TeamIndex).String(),
		"mode":    ml.Mode.String(),
		"channel": ml.Channel.String(),
		"level":   ml.Level.String(),
	}
	p.metrics.CustomCounter("lobbyjoinsession_active_count", metricsTags, 1)

	switch {

	case ml.LobbyType == UnassignedLobby:
		err = status.Errorf(codes.NotFound, "Match is not a lobby")
	case !ml.Open:
		err = status.Errorf(codes.InvalidArgument, "Match is not open")
	case int(match.GetSize()) >= MatchMaxSize:
		err = status.Errorf(codes.ResourceExhausted, "Match is full")
	case ml.LobbyType == PublicLobby:

		// Check if this player is a global moderator or developer
		isModerator, _ := checkIfGlobalModerator(ctx, p.runtimeModule, session.userID)
		isDeveloper, _ := checkIfGlobalDeveloper(ctx, p.runtimeModule, session.userID)

		// Let developers and moderators join public matches
		if request.TeamIndex != int16(Spectator) && !isDeveloper && !isModerator && time.Since(ml.StartedAt) < time.Second*15 {
			// Allow if the match is over 15 seconds old, to allow matchmaking to properly populate the match
			err = status.Errorf(codes.InvalidArgument, "Match is a newly started public match")
		}
	}

	if err != nil {
		return response.SendErrorToSession(session, err)
	}

	authorized, err := p.authorizeMatchmaking(ctx, logger, session, loginSessionID, *ml.Channel)
	if err != nil {
		if authorized {
			logger.Warn("Failed to authorize matchmaking, allowing player to continue. ", zap.Error(err))
		} else {
			err = status.Errorf(codes.PermissionDenied, err.Error())
			return response.SendErrorToSession(session, err)
		}
	}
	// Join the match

	if err = p.JoinEvrMatch(ctx, logger, session, "", matchToken.String(), int(request.TeamIndex)); err != nil {
		return response.SendErrorToSession(session, status.Errorf(codes.NotFound, err.Error()))
	}
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

// pruneMatches prunes matches that are underutilized
func (p *EvrPipeline) pruneMatches(ctx context.Context, session *sessionWS) error {
	session.logger.Warn("Pruning matches")
	matches, err := p.matchmakingRegistry.listMatches(ctx, 1000, 2, 3, "*")
	if err != nil {
		return err
	}

	for _, match := range matches {
		_, err := SignalMatch(ctx, p.matchRegistry, match.MatchId, SignalPruneUnderutilized, nil)
		if err != nil {
			return err

		}
	}
	return nil
}

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
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
	if !evrID.Valid() {
		return status.Errorf(codes.InvalidArgument, "Invalid EVR ID")
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
		sessionIDs := session.tracker.ListLocalSessionIDByStream(PresenceStream{Mode: StreamModeService, Subject: evrID.UUID(), Subcontext: StreamContextMatch})
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
			Stream: PresenceStream{Mode: StreamModeService, Subject: session.id, Subcontext: StreamContextMatch},
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

func (p *EvrPipeline) matchmakingLabelFromFindRequest(ctx context.Context, session *sessionWS, request *evr.LobbyFindSessionRequest) (*EvrMatchState, error) {

	// If the channel is nil, use the players profile channel
	groupID := request.Channel
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
		request.TeamIndex = int16(AnyTeam)
		request.SessionSettings = evr.SessionSettings{
			AppID: request.SessionSettings.AppID,
			Mode:  int64(evr.ModeCombatPublic),
		}
	}

	features := ctx.Value(ctxFeaturesKey{}).([]string)

	ml := &EvrMatchState{
		GroupID: &groupID,

		Mode:  request.Mode,
		Level: request.Level,
		Open:  true,

		SessionSettings: &request.SessionSettings,
		TeamIndex:       TeamIndex(request.TeamIndex),

		Broadcaster: MatchBroadcaster{
			VersionLock: request.VersionLock,
			GroupIDs:    guildPriority,
			Features:    features,
		},
	}
	if !request.CurrentMatch.IsNil() {
		ml.ID = MatchID{request.CurrentMatch, p.node} // The existing lobby/match that the player is in (if any)
	}

	// Check if the team index is valid for the mode
	if !slices.Contains(evr.AlignmentsByMode[ml.Mode], int(ml.TeamIndex)) {
		ml.TeamIndex = AnyTeam
	}

	return ml, nil

}

// lobbyFindSessionRequest is a message requesting to find a public session to join.
func (p *EvrPipeline) lobbyFindSessionRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) (err error) {
	request := in.(*evr.LobbyFindSessionRequest)

	// Prepare the response message
	response := NewMatchmakingResult(logger, request.Mode, request.Channel)

	// Build the matchmaking label using the request parameters
	ml, err := p.matchmakingLabelFromFindRequest(ctx, session, request)
	if err != nil {
		return response.SendErrorToSession(session, err)
	}

	metricsTags := map[string]string{
		"mode":     request.Mode.String(),
		"channel":  ml.GroupID.String(),
		"level":    request.Level.String(),
		"team_idx": strconv.FormatInt(int64(request.TeamIndex), 10),
	}
	p.metrics.CustomCounter("lobbyfindsession_active_count", metricsTags, 1)
	loginSessionID := request.LoginSessionID

	// Check for suspensions on this channel, if this is a request for a public match.
	if err := p.authorizeMatchmaking(ctx, logger, session, loginSessionID, *ml.GroupID, true); err != nil {
		switch status.Code(err) {
		case codes.Internal:
			logger.Warn("Failed to authorize matchmaking, allowing player to continue. ", zap.Error(err))
		default:
			return response.SendErrorToSession(session, err)
		}
	}

	ml.Broadcaster.Regions = []evr.Symbol{evr.DefaultRegion}

	// Wait for a graceperiod Unless this is a social lobby, wait for a grace period before starting the matchmaker
	matchmakingDelay := MatchmakingStartGracePeriod
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

	limit := 100
	minSize := 1
	maxSize := MatchMaxSize - 1
	query := fmt.Sprintf("+label.open:T +label.lobby_type:public +label.mode:%s +label.size:>=%d +label.size:<=%d", msession.Label.Mode.Token(), minSize, maxSize)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		// list existing matches
		matches, err := listMatches(ctx, p, limit, minSize+1, maxSize+1, query)
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
				MatchID:   MatchIDFromStringOrNil(matches[0].GetMatchId()),
				Query:     query,
				TeamIndex: TeamIndex(evr.TeamSpectator),
			}
			logger = logger.With(zap.String("mid", foundMatch.MatchID.String()))

			select {
			case <-ctx.Done():
				return nil
			case msession.MatchJoinCh <- foundMatch:
				p.metrics.CustomCounter("spectatestream_found_count", msession.metricsTags(), 1)
				logger.Debug("Spectating match")
			case <-time.After(3 * time.Second):
				p.metrics.CustomCounter("spectatestream_join_timeout_count", msession.metricsTags(), 1)
				logger.Warn("Failed to spectate match")
			}
		}
		<-time.After(10 * time.Second)
	}
}

func (p *EvrPipeline) MatchBackfillLoop(session *sessionWS, msession *MatchmakingSession, skipDelay bool, create bool, minCount int) error {
	logger := msession.Logger
	ctx := msession.Context()

	// Wait for at least 1 interval before starting to look for a backfill.
	// This gives the matchmaker a chance to find a full ideal match
	interval := p.config.GetMatchmaker().IntervalSec
	idealMatchIntervals := p.config.GetMatchmaker().RevThreshold
	backfillDelay := time.Duration(interval*idealMatchIntervals) * time.Second

	if !skipDelay {
		<-time.After(backfillDelay)
	}
	limit := 5
	delayStartTimer := time.NewTimer(100 * time.Millisecond)
	retryTicker := time.NewTicker(3 * time.Second)
	createTicker := time.NewTicker(5 * time.Second)

OuterLoop:
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-delayStartTimer.C:
		case <-retryTicker.C:
		}
		// Backfill any existing matches
		labels, query, err := p.Backfill(ctx, session, msession, minCount, limit)
		if err != nil {
			return msession.Cancel(fmt.Errorf("failed to find backfill match: %w", err))
		}

		for _, label := range labels {
			p.metrics.CustomCounter("match_backfill_found_count", msession.metricsTags(), 1)
			logger.Debug("Attempting to backfill match", zap.String("mid", label.ID.UUID().String()))
			if err := p.JoinEvrMatch(ctx, logger, session, query, label.ID, evr.TeamUnassigned); err != nil {
				logger.Warn("Failed to backfill match", zap.Error(err))
				continue
			}
			continue OuterLoop
		}
		// After trying to backfill, try to create a match on an interval
		select {
		case <-createTicker.C:
			if create && p.createLobbyMu.TryLock() {
				if _, err := p.MatchCreate(ctx, session, msession, msession.Label); err != nil {
					logger.Warn("Failed to create match", zap.Error(err))
				}
				p.createLobbyMu.Unlock()
			}
		default:
		}

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
			if matchID.IsNil() {
				return msession.Cancel(fmt.Errorf("match is nil"))
			}
			foundMatch := FoundMatch{
				MatchID:   matchID,
				Query:     "",
				TeamIndex: msession.Label.TeamIndex,
			}
			select {
			case <-ctx.Done():
				return nil
			case msession.MatchJoinCh <- foundMatch:
				p.metrics.CustomCounter("match_create_join_active_count", msession.metricsTags(), 1)
				logger.Debug("Joining match", zap.String("mid", foundMatch.MatchID.String()))
			case <-time.After(3 * time.Second):
				p.metrics.CustomCounter("make_create_create_join_timeout_count", msession.metricsTags(), 1)
				msession.Cancel(fmt.Errorf("failed to join match"))
			}
			// Keep trying until the context is done
		case codes.NotFound, codes.ResourceExhausted, codes.Unavailable:
			p.metrics.CustomCounter("create_unavailable_count", msession.metricsTags(), 1)
		case codes.Unknown, codes.Internal:
			logger.Warn("Failed to create match", zap.Error(err))
		default:
			return msession.Cancel(err)
		}
		<-time.After(10 * time.Second)
	}
}

func (p *EvrPipeline) MatchFind(parentCtx context.Context, logger *zap.Logger, session *sessionWS, ml *EvrMatchState) error {
	if s, found := p.matchmakingRegistry.GetMatchingBySessionId(session.id); found {
		// Replace the session
		logger.Debug("Matchmaking session already exists", zap.Any("tickets", s.Tickets))
	}
	joinFn := func(matchID MatchID, query string) error {
		err := p.JoinEvrMatch(parentCtx, logger, session, query, matchID, int(ml.TeamIndex))
		if err != nil {
			return NewMatchmakingResult(logger, ml.Mode, *ml.GroupID).SendErrorToSession(session, err)
		}
		return nil
	}
	errorFn := func(err error) error {
		return NewMatchmakingResult(logger, ml.Mode, *ml.GroupID).SendErrorToSession(session, err)
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

	config, err := LoadMatchmakingSettings(msession.Ctx, p.runtimeModule, session.userID.String())
	if err != nil {
		logger.Error("Failed to load matchmaking config", zap.Error(err))
	}

	// Join party
	if config.GroupID != "" {
		msession.Party, err = msession.JoinPartyGroup(config.GroupID)
		if err != nil {
			logger.Warn("Failed to join party group", zap.String("group_id", config.GroupID), zap.Error(err))
		} else {

			logger.Debug("Joined party", zap.String("party_id", msession.Party.ID().String()), zap.Any("members", msession.Party.GetMembers()))

			if leader := msession.Party.GetLeader(); leader != nil && leader.SessionId != session.id.String() {
				// Delay to allow the leader to end up in a social lobby
				<-time.After(3 * time.Second)
			}
		}
	}

	var matchID MatchID
	// Check for a direct match first
	if !config.NextMatchID.IsNil() {
		matchID = config.NextMatchID
	}

	config.NextMatchID = MatchID{}
	err = StoreMatchmakingSettings(msession.Ctx, p.runtimeModule, session.userID.String(), config)
	if err != nil {
		logger.Error("Failed to save matchmaking config", zap.Error(err))
	}

	if !matchID.IsNil() {
		logger.Debug("Attempting to join match from settings", zap.String("mid", matchID.String()))
		match, _, err := p.matchRegistry.GetMatch(msession.Ctx, matchID.String())
		if err != nil {
			logger.Error("Failed to get match", zap.Error(err))
		} else {
			if match == nil {
				logger.Warn("Match not found", zap.String("mid", matchID.String()))
			} else {
				p.metrics.CustomCounter("match_next_join_count", map[string]string{}, 1)
				// Join the match

				msession.MatchJoinCh <- FoundMatch{
					MatchID:   matchID,
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
		guild, err := p.discordRegistry.GetGuildByGroupId(parentCtx, ml.GroupID.String())
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

		env := p.config.GetRuntime().Environment
		channelID, ok := env["COMBAT_MATCHMAKING_CHANNEL_ID"]
		if ok {

			if bot := p.discordRegistry.GetBot(); bot != nil && ml.TeamIndex != TeamIndex(evr.TeamSpectator) {
				// Count how many players are matchmaking for this mode right now
				sessionsByMode := p.matchmakingRegistry.SessionsByMode()

				userIDs := make([]uuid.UUID, 0)
				for _, s := range sessionsByMode[ml.Mode] {

					if s.Session.userID == session.userID || s.Label.TeamIndex == TeamIndex(evr.TeamSpectator) {
						continue
					}

					userIDs = append(userIDs, s.Session.userID)
				}

				matchID, _, err := GetMatchBySessionID(p.runtimeModule, session.id)
				if err != nil {
					logger.Warn("Failed to get match by session ID", zap.Error(err))
				}

				// Translate the userID's to discord ID's
				discordIDs := make([]string, 0, len(userIDs))
				for _, userID := range userIDs {

					did, err := p.discordRegistry.GetDiscordIdByUserId(parentCtx, userID)
					if err != nil {
						logger.Warn("Failed to get discord ID", zap.Error(err))
						continue
					}
					discordIDs = append(discordIDs, fmt.Sprintf("<@%s>", did))
				}

				discordID, err := p.discordRegistry.GetDiscordIdByUserId(parentCtx, session.userID)
				if err != nil {
					logger.Warn("Failed to get discord ID", zap.Error(err))
				}

				msg := fmt.Sprintf("<@%s> is matchmaking...", discordID)
				if len(discordIDs) > 0 {
					msg = fmt.Sprintf("%s along with %s...", msg, strings.Join(discordIDs, ", "))
				}

				embed := discordgo.MessageEmbed{
					Title:       "Matchmaking",
					Description: msg,
					Color:       0x00cc00,
				}

				if !matchID.IsNil() {
					embed.Footer = &discordgo.MessageEmbedFooter{
						Text: fmt.Sprintf("https://echo.taxi/spark://j/%s", strings.ToUpper(matchID.UUID().String())),
					}
				}

				// Notify the channel that this person started queuing
				message, err := bot.ChannelMessageSendEmbed(channelID, &embed)
				if err != nil {
					logger.Warn("Failed to send message", zap.Error(err))
				}
				go func() {
					// Delete the message when the player stops matchmaking
					select {
					case <-msession.Ctx.Done():
						if message != nil {
							err := bot.ChannelMessageDelete(channelID, message.ID)
							if err != nil {
								logger.Warn("Failed to delete message", zap.Error(err))
							}
						}
					case <-time.After(15 * time.Minute):
						if message != nil {
							err := bot.ChannelMessageDelete(channelID, message.ID)
							if err != nil {
								logger.Warn("Failed to delete message", zap.Error(err))
							}
						}
					}
				}()
			}
		}

		// Join any on-going combat match without delay
		skipBackfillDelay = false
		// Start the backfill loop, if the player is not in a party.
		if msession.Party == nil || len(msession.Party.GetMembers()) == 1 {
			go p.MatchBackfillLoop(session, msession, skipBackfillDelay, false, 1)
		}
		// Put a ticket in for matching
		_, err = p.MatchMake(session, msession)
		if err != nil {
			return err
		}

	case evr.ModeArenaPublic:

		// Start the backfill loop, if the player is not in a party.
		if msession.Party == nil || len(msession.Party.GetMembers()) == 1 {
			// Don't backfill party members, let the matchmaker handle it.
			go p.MatchBackfillLoop(session, msession, skipBackfillDelay, false, 1)
		}

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

func (p *EvrPipeline) GetGuildPriorityList(ctx context.Context, userID uuid.UUID) (all []uuid.UUID, selected []uuid.UUID, err error) {

	currentGroupID, ok := ctx.Value(ctxGroupIDKey{}).(uuid.UUID)
	if !ok {
		return nil, nil, status.Errorf(codes.InvalidArgument, "Failed to get group ID from context")
	}

	// Get the guild priority from the context
	memberships, err := p.discordRegistry.GetGuildGroupMemberships(ctx, userID, nil)
	if err != nil {
		return nil, nil, status.Errorf(codes.Internal, "Failed to get guilds: %v", err)
	}

	// Sort the groups by size descending
	sort.Slice(memberships, func(i, j int) bool {
		return memberships[i].GuildGroup.Size() > memberships[j].GuildGroup.Size()
	})

	groupIDs := make([]uuid.UUID, 0)
	for _, group := range memberships {
		groupIDs = append(groupIDs, group.GuildGroup.ID())
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
	}

	if len(guildPriority) == 0 {
		// If the params are not set, use the user's guilds
		guildPriority = []uuid.UUID{currentGroupID}
		for _, groupID := range groupIDs {
			if groupID != currentGroupID {
				guildPriority = append(guildPriority, groupID)
			}
		}
	}

	p.logger.Debug("guild priorites", zap.Any("prioritized", guildPriority), zap.Any("all", groupIDs))
	return groupIDs, guildPriority, nil
}

// lobbyCreateSessionRequest is a request to create a new session.
func (p *EvrPipeline) lobbyCreateSessionRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.LobbyCreateSessionRequest)
	response := NewMatchmakingResult(logger, request.Mode, request.Channel)

	features := ctx.Value(ctxFeaturesKey{}).([]string)
	requiredFeatures := ctx.Value(ctxRequiredFeaturesKey{}).([]string)

	// Get the GroupID from the context
	groupID := request.Channel
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
		"channel":  request.Channel.String(),
		"level":    request.Level.String(),
		"team_idx": strconv.FormatInt(int64(request.TeamIndex), 10),
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

	isDeveloper, err := checkIfGlobalDeveloper(ctx, p.runtimeModule, session.userID)
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

	ml := &EvrMatchState{
		Level:            request.Level,
		LobbyType:        LobbyType(request.LobbyType),
		Mode:             request.Mode,
		Open:             true,
		SessionSettings:  &request.SessionSettings,
		TeamIndex:        TeamIndex(request.TeamIndex),
		GroupID:          &request.Channel,
		RequiredFeatures: requiredFeatures,
		Broadcaster: MatchBroadcaster{
			VersionLock: uint64(request.VersionLock),
			Regions:     uniqueRegions,
			GroupIDs:    priorities,
			Features:    features,
		},
	}

	// Start the search in a goroutine.
	go func() error {
		// Set some defaults
		partySize := 1 // TODO FIXME this should include the party size

		joinFn := func(matchID MatchID, query string) error {
			logger := logger.With(zap.String("mid", matchID.String()))

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
			return NewMatchmakingResult(logger, ml.Mode, *ml.GroupID).SendErrorToSession(session, err)
		}

		// Create a matching session
		timeout := 15 * time.Minute
		msession, err := p.matchmakingRegistry.Create(ctx, logger, session, ml, partySize, timeout, errorFn, joinFn)
		if err != nil {
			return response.SendErrorToSession(session, status.Errorf(codes.Internal, "Failed to create matchmaking session: %v", err))
		}
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
	ml := &EvrMatchState{}
	if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), ml); err != nil {
		return response.SendErrorToSession(session, status.Errorf(codes.NotFound, err.Error()))
	}
	if ml.GroupID == nil {
		ml.GroupID = &uuid.Nil
	}

	metricsTags := map[string]string{
		"team":    TeamIndex(request.TeamIndex).String(),
		"mode":    ml.Mode.String(),
		"channel": ml.GroupID.String(),
		"level":   ml.Level.String(),
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
		isModerator, _ := checkIfGlobalModerator(ctx, p.runtimeModule, session.userID)
		isDeveloper, _ := checkIfGlobalDeveloper(ctx, p.runtimeModule, session.userID)

		// Let developers and moderators join public matches
		if request.TeamIndex != int16(Spectator) && !isDeveloper && !isModerator && time.Since(ml.StartTime) < time.Second*10 {
			// Allow if the match is over 15 seconds old, to allow matchmaking to properly populate the match
			err = status.Errorf(codes.InvalidArgument, "Match is a newly started public match")
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

	if err = p.JoinEvrMatch(ctx, logger, session, "", matchToken, int(request.TeamIndex)); err != nil {
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

// lobbyPlayerSessionsRequest is called when a client requests the player sessions for a list of EchoVR IDs.
// Player Sessions are UUIDv5 of the MatchID and EVR-ID
func (p *EvrPipeline) lobbyPlayerSessionsRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	message := in.(*evr.LobbyPlayerSessionsRequest)

	matchID, err := NewMatchID(message.LobbyID, p.node)
	if err != nil {
		return fmt.Errorf("failed to create match ID: %w", err)
	}
	entrantID := uuid.NewV5(message.LobbyID, message.EvrID().String())

	presence, err := PresenceByEntrantID(p.runtimeModule, matchID, entrantID)
	if err != nil {
		return fmt.Errorf("failed to get lobby presence for entrant `%s`: %w", entrantID.String(), err)
	}

	entrantIDs := make([]uuid.UUID, len(message.PlayerEvrIDs))
	for _, e := range message.PlayerEvrIDs {
		entrantIDs = append(entrantIDs, uuid.NewV5(message.LobbyID, e.String()))
	}

	entrant := evr.NewLobbyEntrant(message.EvrId, message.LobbyID, entrantID, entrantIDs, int16(presence.TeamIndex))

	return session.SendEvr(entrant.VersionU(), entrant.Version2(), entrant.Version3())
}

func GetMatchBySessionID(nk runtime.NakamaModule, sessionID uuid.UUID) (matchID MatchID, presence runtime.Presence, err error) {

	presences, err := nk.StreamUserList(StreamModeService, sessionID.String(), StreamContextMatch.String(), "", true, true)
	if err != nil {
		return MatchID{}, nil, fmt.Errorf("failed to get stream presences: %w", err)
	}

	for _, presence := range presences {
		matchID := MatchIDFromStringOrNil(presence.GetStatus())
		if !matchID.IsNil() {
			return matchID, presence, nil
		}
	}

	return MatchID{}, nil, ErrorMatchNotFound
}

func PresenceByEntrantID(nk runtime.NakamaModule, matchID MatchID, entrantID uuid.UUID) (presence *EvrMatchPresence, err error) {

	presences, err := nk.StreamUserList(StreamModeEntrant, matchID.UUID().String(), entrantID.String(), matchID.Node(), true, true)
	if err != nil {
		return nil, fmt.Errorf("failed to get stream presences for entrant %s: %w", entrantID.String(), err)
	}

	if len(presences) == 0 {
		return nil, ErrorEntrantNotFound
	}

	if len(presences) > 1 {
		return nil, ErrorMultipleEntrantsFound
	}

	mp := &EvrMatchPresence{}
	if err := json.Unmarshal([]byte(presences[0].GetStatus()), mp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal presence: %w", err)
	}

	return mp, nil
}

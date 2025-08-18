package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server"
	"go.uber.org/zap"
)

type TeamAlignments map[string]int // map[UserID]Role

var createLobbyMu = &sync.Mutex{}

var LobbyTestCounter = 0

var ErrCreateLock = errors.New("failed to acquire create lock")

// lobbyJoinSessionRequest is a request to join a specific existing session.
func (p *EvrPipeline) lobbyFind(ctx context.Context, logger *zap.Logger, session *sessionEVR, lobbyParams *LobbySessionParameters) error {

	startTime := time.Now()

	// Do authorization checks related to the guild.
	if err := p.lobbyAuthorize(ctx, logger, session, lobbyParams); err != nil {
		return err
	}

	// Restrict matchmaking to public lobbies only
	switch lobbyParams.Mode {
	case evr.ModeArenaPublic, evr.ModeSocialPublic, evr.ModeCombatPublic:
	default:
		return NewLobbyError(BadRequest, fmt.Sprintf("`%s` is an invalid mode for matchmaking.", lobbyParams.Mode.String()))
	}

	// Cancel matchmaking after the timeout.
	ctx, cancel := context.WithTimeoutCause(ctx, lobbyParams.MatchmakingTimeout, ErrMatchmakingTimeout)
	defer cancel()

	// Join the "matchmaking" status stream
	if err := p.JoinMatchmakingStream(logger, session, lobbyParams); err != nil {
		return fmt.Errorf("failed to join matchmaking stream: %w", err)
	}

	// Monitor the matchmaking status stream, canceling the context if the stream is closed.
	go p.monitorMatchmakingStream(ctx, logger, session, lobbyParams, cancel)

	entrantSessionIDs := []uuid.UUID{session.id}

	var partyLabel *PartyLabel
	if lobbyParams.PartySharedKey != "" {
		var err error
		var isLeader bool
		var memberSessionIDs []uuid.UUID
		partyLabel, memberSessionIDs, isLeader, err = p.configureParty(ctx, logger, session, lobbyParams)
		if err != nil {
			return fmt.Errorf("failed to join party: %w", err)
		}

		if !isLeader {
			// Skip following the party leader if the member is not in a match (and headed to a social lobby)
			if lobbyParams.Mode != evr.ModeSocialPublic || !lobbyParams.CurrentMatchID.IsNil() {
				return p.PartyFollow(ctx, logger, session, lobbyParams, partyLabel)
			}
		} else {

			for _, memberSessionIDs := range memberSessionIDs {

				if memberSessionIDs == session.id {
					continue
				}

				entrantSessionIDs = append(entrantSessionIDs, memberSessionIDs)
			}
		}
	} else {
		lobbyParams.SetPartySize(1)
	}

	p.metrics.CustomCounter("lobby_find_match", lobbyParams.MetricsTags(), int64(lobbyParams.GetPartySize()))
	logger.Info("Finding match", zap.String("mode", lobbyParams.Mode.String()), zap.Int("party_size", lobbyParams.GetPartySize()))

	// Construct the entrant presences for the party members.
	entrants, err := PrepareEntrantPresences(ctx, logger, p.nk, p.sessionRegistry, lobbyParams, entrantSessionIDs...)
	if err != nil {
		return fmt.Errorf("failed to be party leader.: %w", err)
	}

	lobbyParams.SetPartySize(len(entrants))

	defer func() {

		isLeader := true

		if partyLabel != nil && partyLabel.Leader != nil && partyLabel.Leader.SessionId != session.id.String() {
			isLeader = false
		}

		// If this is the leader, or a solo player, send the metrics

		tags := lobbyParams.MetricsTags()
		tags["is_leader"] = strconv.FormatBool(isLeader)
		tags["party_size"] = strconv.Itoa(lobbyParams.GetPartySize())
		p.metrics.CustomTimer("lobby_find_duration", tags, time.Since(startTime))

		logger.Debug("Lobby find complete", zap.String("group_id", lobbyParams.GroupID.String()), zap.Int("party_size", lobbyParams.GetPartySize()), zap.String("mode", lobbyParams.Mode.String()), zap.Int("role", lobbyParams.Role), zap.Bool("leader", isLeader), zap.Int("duration", int(time.Since(startTime).Seconds())))
	}()

	// Check latency to active game servers.
	if err := p.CheckServerPing(ctx, logger, session, lobbyParams.GroupID.String()); err != nil {
		return fmt.Errorf("failed to check server ping: %w", err)
	}

	if !lobbyParams.CurrentMatchID.IsNil() {
		// Sometimes the client doesn't respond to the ping request, so delay for a few seconds.
		<-time.After(3 * time.Second)
	}

	// Only Apply the early quit penalty if it's a public arena match.
	if lobbyParams.Mode == evr.ModeArenaPublic && lobbyParams.EarlyQuitPenaltyLevel > 0 && ServiceSettings().Matchmaking.EnableEarlyQuitPenalty {

		// Default backfill interval
		interval := 1 * time.Second

		// Early quitters have a shorter backfill interval.
		switch lobbyParams.EarlyQuitPenaltyLevel {
		case 1:
			interval = 60 * time.Second
		case 2:
			interval = 120 * time.Second
		case 3:
			interval = 240 * time.Second
		}

		// Notify the user that they are an early quitter.
		message := fmt.Sprintf("Your early quit penalty is active (level %d), your matchmaking has been delayed by %d seconds.", lobbyParams.EarlyQuitPenaltyLevel, int(interval.Seconds()))
		if _, err := SendUserMessage(ctx, dg, lobbyParams.DiscordID, message); err != nil {
			logger.Warn("Failed to send message to user", zap.Error(err))
		}
		if guildGroup := p.guildGroupRegistry.Get(lobbyParams.GroupID.String()); guildGroup != nil {
			// Send an audit log message to the guild group.
			content := fmt.Sprintf("notified early quitter <@!%s> (%s): %s ", lobbyParams.DiscordID, session.Username(), message)
			if _, err = AuditLogSendGuild(p.appBot.dg, guildGroup, content); err != nil {
				logger.Warn("Failed to send audit log message", zap.Error(err))
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}

	if slices.Contains([]evr.Symbol{evr.ModeArenaPublic, evr.ModeCombatPublic}, lobbyParams.Mode) {
		// Start the matchmaking process.
		go func() {
			if err := p.lobbyMatchMakeWithFallback(ctx, logger, session, lobbyParams, partyLabel, entrants...); err != nil {
				logger.Error("Failed to matchmake", zap.Error(err))
			}
		}()
	}

	// Attempt to backfill until the timeout.
	enableFailsafe := true
	return p.lobbyBackfill(ctx, logger, session, lobbyParams, enableFailsafe, entrants...)

}

func (p *EvrPipeline) configureParty(ctx context.Context, logger *zap.Logger, session *sessionEVR, lobbyParams *LobbySessionParameters) (*PartyLabel, []uuid.UUID, bool, error) {

	// Join the party if a player has a party group id set.
	// The lobby group is the party that the user is currently in.
	partyLabel, isLeader, err := p.JoinPartyGroup(ctx, session, lobbyParams)
	if err != nil {
		if err == runtime.ErrPartyFull {
			return nil, nil, false, NewLobbyError(ServerIsFull, "party is full")
		}
		return nil, nil, false, fmt.Errorf("failed to join party group: %w", err)
	}
	logger.Debug("Joined party group", zap.String("partyID", partyLabel.ID.String()))

	rankPercentiles := make([]float64, 0, partyLabel.Size())
	// If this is the leader, then set the presence status to the current match ID.
	if isLeader {
		if !lobbyParams.CurrentMatchID.IsNil() && lobbyParams.Mode != evr.ModeSocialPublic {
			// If there are more than one player in the party, wait for the other players to start matchmaking.
			if partyLabel.Size() > 1 {
				select {
				case <-ctx.Done():
					return nil, nil, false, ctx.Err()
				case <-time.After(10 * time.Second):
				}
			}
		}
		stream := lobbyParams.MatchmakingStream()

		memberUsernames := make([]string, 0, partyLabel.Size())

		for _, member := range partyLabel.Presences {
			if member.GetSessionId() == session.id.String() {
				continue
			}
			memberUsernames = append(memberUsernames, member.GetUsername())

			meta, err := p.nk.StreamUserGet(stream.Mode, stream.Subject.String(), stream.Subcontext.String(), stream.Label, member.GetUserId(), member.GetSessionId())
			if err != nil {
				return nil, nil, false, fmt.Errorf("failed to get party stream: %w", err)
			} else if meta == nil {
				logger.Warn("Party member is not following the leader", zap.String("uid", member.GetUserId()), zap.String("sid", member.GetSessionId()), zap.String("leader_sid", session.id.String()))
				presence := &server.MatchmakerPresence{
					UserId:    member.GetUserId(),
					SessionID: uuid.FromStringOrNil(member.GetSessionId()),
					Username:  member.GetUsername(),
				}

				if err := p.nk.StreamUserKick(stream.Mode, stream.Subject.String(), stream.Subcontext.String(), stream.Label, presence); err != nil {
					return nil, nil, false, fmt.Errorf("failed to kick party member: %w", err)
				}
			} else {
				/*
					memberParams := &LobbySessionParameters{}
					if err := json.Unmarshal([]byte(member.Presence.GetStatus()), &memberParams); err != nil {
						return nil, nil, false, fmt.Errorf("failed to unmarshal member params: %w", err)
					}

					rankPercentiles = append(rankPercentiles, memberParams.GetRankPercentile())
				*/
			}
		}

		partySize := partyLabel.Size()
		logger.Debug("Party is ready", zap.String("leader", session.id.String()), zap.Int("size", partySize), zap.Strings("members", memberUsernames))

		if len(rankPercentiles) > 0 {
			// Average the rank percentiles
			averageRankPercentile := 0.0
			for _, rankPercentile := range rankPercentiles {
				averageRankPercentile += rankPercentile
			}
			averageRankPercentile /= float64(partySize)

			lobbyParams.SetRankPercentile(averageRankPercentile)
		}

		lobbyParams.SetPartySize(partySize)
	}

	memberSessionIDs := []uuid.UUID{session.id}
	// Add the party members to the sessionID slice
	for _, member := range partyLabel.Presences {
		if member.GetSessionId() == session.id.String() {
			continue
		}
		memberSessionIDs = append(memberSessionIDs, uuid.FromStringOrNil(member.GetSessionId()))
	}

	return partyLabel, memberSessionIDs, isLeader, nil
}

func (p *EvrPipeline) monitorMatchmakingStream(ctx context.Context, logger *zap.Logger, session *sessionEVR, lobbyParams *LobbySessionParameters, cancelFn context.CancelFunc) {

	// Monitor the stream and cancel the context (and matchmaking) if the stream is closed.
	// This stream tracks the user's matchmaking status.
	// This stream is untracked when the user cancels matchmaking.

	stream := lobbyParams.MatchmakingStream()
	defer LeaveMatchmakingStream(logger, session)
	for {
		select {
		case <-ctx.Done():
			// Check if the cancel was because of a timeout
			return
		case <-time.After(1 * time.Second):
		}

		// Check if the matchmaking stream has been closed.  (i.e. the user has canceled matchmaking)
		if session.tracker.GetLocalBySessionIDStreamUserID(session.id, stream, session.userID) == nil {
			<-time.After(1 * time.Second)
			cancelFn()
		}
	}
}

func (p *EvrPipeline) newLobby(ctx context.Context, logger *zap.Logger, lobbyParams *LobbySessionParameters, entrants ...*EvrMatchPresence) (*MatchLabel, error) {
	if createLobbyMu.TryLock() {
		go func() {
			// Hold the lock for enough time to create the server
			<-time.After(5 * time.Second)
			createLobbyMu.Unlock()
		}()
	} else {
		return nil, ErrFailedToAcquireLock
	}

	metricsTags := map[string]string{
		"version_lock": lobbyParams.VersionLock.String(),
		"group_id":     lobbyParams.GroupID.String(),
		"mode":         lobbyParams.Mode.String(),
	}

	p.metrics.CustomCounter("lobby_new", metricsTags, 1)

	settings := &MatchSettings{
		Mode:                lobbyParams.Mode,
		Level:               lobbyParams.Level,
		SpawnedBy:           lobbyParams.UserID.String(),
		GroupID:             lobbyParams.GroupID,
		StartTime:           time.Now().UTC(),
		Reservations:        entrants,
		ReservationLifetime: 30 * time.Second,
	}

	label, err := LobbyGameServerAllocate(ctx, server.NewRuntimeGoLogger(logger), p.nk, []string{lobbyParams.GroupID.String()}, lobbyParams.latencyHistory.Load().LatestRTTs(), settings, []string{lobbyParams.RegionCode}, true, false, ServiceSettings().Matchmaking.QueryAddons.Create)
	if err != nil {
		logger.Warn("Failed to allocate game server", zap.Error(err), zap.Any("settings", settings))
		return nil, err
	}

	return label, nil
}

func (p *EvrPipeline) lobbyBackfill(ctx context.Context, logger *zap.Logger, session server.Session, lobbyParams *LobbySessionParameters, enableFailsafe bool, entrants ...*EvrMatchPresence) error {
	interval := 3 * time.Second
	if lobbyParams.Mode == evr.ModeSocialPublic {
		interval = 1 * time.Second
	}

	// If the player has backfill disabled, set the backfill interval to an extreme number.
	if lobbyParams.DisableArenaBackfill && lobbyParams.Mode == evr.ModeArenaPublic {
		// Set a long backfill interval for arena matches.
		interval = 15 * time.Minute
	}

	// Backfill search query
	// Maximum RTT for a server to be considered for backfill

	includeMMR := false
	includeMaxRTT := false

	// Only use rank percentile for arena matches.
	if lobbyParams.Mode == evr.ModeArenaPublic {
		includeMMR = true
	}

	stream := lobbyParams.GuildGroupStream()
	count, err := p.nk.StreamCount(stream.Mode, stream.Subject.String(), "", stream.Label)
	if err != nil {
		logger.Error("Failed to get stream count", zap.Error(err))
	}

	// If there are fewer players online, reduce the fallback delay
	if !strings.Contains(p.node, "dev") {
		// If there are fewer than 16 players online, reduce the fallback delay
		if count < 24 {
			includeMMR = false
		}
	}

	var (
		query         = lobbyParams.BackfillSearchQuery(includeMMR, includeMaxRTT)
		fallbackTimer = time.NewTimer(lobbyParams.FallbackTimeout)
		failsafeTimer = time.NewTimer(lobbyParams.FailsafeTimeout)
		cycleCount    = 0
	)
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("context canceled: %w", ctx.Err())

		case <-fallbackTimer.C:

			// The fallback timer has expired. Reduce the search query.
			query = lobbyParams.BackfillSearchQuery(false, false)

		case <-failsafeTimer.C:
			if enableFailsafe {
				// The failsafe timer has expired. Create a match.
				query = lobbyParams.BackfillSearchQuery(false, false)

				// The failsafe timer has expired.
				// Create a match.
				logger.Warn("Failsafe timer expired. Creating a new match.")
				label, err := p.newLobby(ctx, logger, lobbyParams)
				if err != nil {
					// If the error is a lock error, just try again.
					if err == ErrFailedToAcquireLock {
						// Wait until after the "avoidance time" to give time for the server to be created.
						<-time.After(5 * time.Second)
						continue
					}

					// This should't happen unless there's no servers available.
					return NewLobbyErrorf(ServerFindFailed, "failed to create new lobby failsafe: %w", err)
				} else {
					<-time.After(1 * time.Second)
					// Player members will detect the join.
					if err := p.LobbyJoinEntrants(logger, label, entrants...); err != nil {
						// Send the error to the client
						// If it's full just try again.
						if LobbyErrorCode(err) == ServerIsFull {
							logger.Warn("Server is full, ignoring.")
							continue
						}
						return fmt.Errorf("failed to join failsafe-generated match: %w", err)
					}
					return nil
				}
			}
		case <-time.After(interval):

		}

		// List all matches that are open and have available slots.
		matches, err := ListMatchStates(ctx, p.nk, p.matchRegistry, query)
		if err != nil {
			return fmt.Errorf("failed to list matches: %w", err)
		}

		cycleCount++
		if len(matches) > 0 {
			logger.Debug("Found matches", zap.Int("count", len(matches)), zap.Any("query", query), zap.Int("cycle", cycleCount))
		} else {
			if cycleCount%10 == 0 {
				logger.Debug("No matches found", zap.Any("query", query), zap.Int("cycle", cycleCount))
				continue
			}
		}
		partySize := lobbyParams.GetPartySize()
		if partySize == 0 {
			logger.Warn("party size is 0")
			lobbyParams.SetPartySize(1)
			partySize = 1
		}

		matches = p.sortBackfillOptions(matches, lobbyParams)

		team := evr.TeamBlue

		for _, labelMeta := range matches {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			l := labelMeta.State

			// Social lobbies can only have one team
			if lobbyParams.Mode == evr.ModeSocialPublic {
				team = evr.TeamSocial
			} else {

				// Determine which team has the least players
				team = evr.TeamBlue
				if l.RoleCount(evr.TeamOrange) < l.RoleCount(evr.TeamBlue) {
					team = evr.TeamOrange
				}
			}
			if n, err := l.OpenSlotsByRole(team); err != nil {
				logger.Warn("Failed to get open slots by role", zap.Error(err))
				continue
			} else if n < len(entrants) {
				continue
			}

			// Set the role alignment for each entrant in the party
			for _, e := range entrants {
				e.RoleAlignment = team
			}

			logger := logger.With(zap.String("mid", l.ID.UUID.String()))

			logger.Debug("Joining backfill match.")
			p.metrics.CustomCounter("lobby_join_backfill", lobbyParams.MetricsTags(), int64(lobbyParams.GetPartySize()))

			if err := p.LobbyJoinEntrants(logger, l, entrants...); err != nil {
				// Send the error to the client
				// If it's full just try again.
				if LobbyErrorCode(err) == ServerIsFull {
					logger.Warn("Server is full, ignoring.")
					continue
				}
				return fmt.Errorf("failed to backfill existing match: %w", err)
			}
			return nil
		}

		// If the lobby is social, create a new social lobby.
		if lobbyParams.Mode == evr.ModeSocialPublic {
			// Create a new social lobby
			label, err := p.newLobby(ctx, logger, lobbyParams, entrants...)
			if err != nil {
				// If the error is a lock error, just try again.
				if err == ErrFailedToAcquireLock {
					// Wait a few seconds to give time for the server to be created.
					<-time.After(2 * time.Second)
					continue
				}

				// This should't happen unless there's no servers available.
				return NewLobbyErrorf(ServerFindFailed, "failed to create social lobby: %w", err)
			} else {
				<-time.After(1 * time.Second)
				// Player members will detect the join.
				if err := p.LobbyJoinEntrants(logger, label, entrants...); err != nil {
					// Send the error to the client
					// If it's full just try again.
					if LobbyErrorCode(err) == ServerIsFull {
						logger.Warn("Server is full, ignoring.")
						continue
					}
					return fmt.Errorf("failed to join auto-created lobby: %w", err)
				}
				return nil
			}
		}
	}
}

func (p *EvrPipeline) CheckServerPing(ctx context.Context, logger *zap.Logger, session *sessionEVR, groupID string) error {

	params, ok := LoadParams(session.Context())
	if !ok {
		return fmt.Errorf("failed to load lobby session parameters")
	}

	latencyHistory := params.latencyHistory.Load()

	presences, err := p.nk.StreamUserList(StreamModeGameServer, groupID, "", "", false, true)
	if err != nil {
		return fmt.Errorf("Error listing game servers: %v", err)
	}

	endpointMap := make(map[string]evr.Endpoint, len(presences))
	hostIPs := make([]string, 0, len(presences))
	for _, presence := range presences {
		gPresence := &GameServerPresence{}
		if err := json.Unmarshal([]byte(presence.GetStatus()), gPresence); err != nil {
			logger.Warn("Failed to unmarshal game server presence", zap.Error(err))
			continue
		}
		hostIPs = append(hostIPs, gPresence.Endpoint.GetExternalIP())
		if _, ok := endpointMap[gPresence.Endpoint.GetExternalIP()]; ok {
			continue
		}
		endpointMap[gPresence.Endpoint.GetExternalIP()] = gPresence.Endpoint
	}

	sortPingCandidatesByLatencyHistory(hostIPs, latencyHistory)

	candidates := make([]evr.Endpoint, 0, len(hostIPs))

	for _, ip := range hostIPs {
		candidates = append(candidates, endpointMap[ip])
		if len(candidates) >= 16 {
			break
		}
	}

	if err := SendEVRMessages(session, true, evr.NewLobbyPingRequest(275, candidates)); err != nil {
		return fmt.Errorf("failed to send ping request: %v", err)
	}

	return nil
}

func PrepareEntrantPresences(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, sessionRegistry server.SessionRegistry, lobbyParams *LobbySessionParameters, sessionIDs ...uuid.UUID) ([]*EvrMatchPresence, error) {

	entrantPresences := make([]*EvrMatchPresence, 0, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		session := sessionRegistry.Get(sessionID)
		if session == nil {
			logger.Warn("Session not found", zap.String("sid", sessionID.String()))
			continue
		}
		mmMode := lobbyParams.Mode
		if mmMode == evr.ModeSocialPublic {
			mmMode = evr.ModeArenaPublic
		}

		rankPercentile, err := MatchmakingRankPercentileLoad(ctx, nk, session.UserID().String(), lobbyParams.GroupID.String(), mmMode)
		if err != nil {
			logger.Warn("Failed to load rank percentile", zap.String("sid", sessionID.String()), zap.Error(err))
			rankPercentile = ServiceSettings().Matchmaking.RankPercentile.Default
		}

		rating, err := MatchmakingRatingLoad(ctx, nk, session.UserID().String(), lobbyParams.GroupID.String(), mmMode)
		if err != nil {
			logger.Warn("Failed to load rating", zap.String("sid", sessionID.String()), zap.Error(err))
			rating = NewDefaultRating()
		}

		presence, err := EntrantPresenceFromSession(session, lobbyParams.PartyID, lobbyParams.Role, rating, rankPercentile, lobbyParams.GroupID.String(), 0, "")
		if err != nil {
			logger.Warn("Failed to create entrant presence", zap.String("session_id", session.ID().String()), zap.Error(err))
			continue
		}

		entrantPresences = append(entrantPresences, presence)
	}

	if len(entrantPresences) == 0 {
		return nil, fmt.Errorf("no entrants found")
	}

	return entrantPresences, nil
}

func (p *EvrPipeline) PartyFollow(ctx context.Context, logger *zap.Logger, session *sessionEVR, params *LobbySessionParameters, partyLabel *PartyLabel) error {

	logger.Debug("User is member of party", zap.String("leader", partyLabel.Leader.GetUsername()))

	// This is a party member, wait for the party leader to join a match, or cancel matchmaking.
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
			// Time for the party leader to join a match.
		}
		leader := partyLabel.Leader
		if leader == nil {
			return NewLobbyError(BadRequest, "party leader not found")
		}

		leaderUserID := uuid.FromStringOrNil(leader.UserId)
		// Check if the leader has changed to this player.
		if leader.SessionId == session.id.String() {
			return NewLobbyError(BadRequest, "party leader has changed (to this player). Canceling matchmaking.")
		}

		leaderSessionID := uuid.FromStringOrNil(leader.SessionId)
		stream := server.PresenceStream{
			Mode:    StreamModeService,
			Subject: leaderSessionID,
			Label:   StreamLabelMatchService,
		}

		// Check if the leader is still matchmaking. If so, continue waiting.
		if p := p.tracker.GetLocalBySessionIDStreamUserID(leaderSessionID, params.MatchmakingStream(), leaderUserID); p != nil {
			// Leader is still matchmaking.
			continue
		}

		// Check if the party leader is still in a lobby/match.
		presence := p.tracker.GetLocalBySessionIDStreamUserID(leaderSessionID, stream, leaderUserID)
		if presence == nil {
			return NewLobbyError(BadRequest, fmt.Sprintf("party leader `%s` is no longer in a match.", leader.UserId))
		}

		// Check if the party leader is in a match.
		leaderMatchID := MatchIDFromStringOrNil(presence.GetStatus())
		if leaderMatchID.IsNil() {
			continue
		}

		// Wait 3 seconds, then check if this player is in the match as well (i.e. the matchmaker sent them to a match)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}

		stream = server.PresenceStream{
			Mode:    StreamModeService,
			Subject: session.id,
			Label:   StreamLabelMatchService,
		}
		memberMatchID := MatchID{}
		presence = p.tracker.GetLocalBySessionIDStreamUserID(session.id, stream, session.userID)
		if presence != nil {
			memberMatchID = MatchIDFromStringOrNil(presence.GetStatus())
		}

		if memberMatchID == leaderMatchID {
			// The leader is in a match, and this player is in the same match.
			continue
		} else {
			// If the leader is in a different public lobby, try to join it.
			label, err := MatchLabelByID(ctx, p.nk, leaderMatchID)
			if err != nil {
				return fmt.Errorf("failed to get match by session id: %w", err)
			} else if label == nil {
				continue
			}

			if !label.Open || label.PlayerCount >= label.PlayerLimit {
				// The leader's match is full.
				continue
			}

			switch label.Mode {

			case evr.ModeSocialPrivate, evr.ModeSocialPublic, evr.ModeCombatPublic, evr.ModeArenaPublic:
				// Join the leader's match.
				logger.Debug("Joining leader's lobby", zap.String("mid", leaderMatchID.String()))

				if err := p.lobbyJoin(ctx, logger, session, params, leaderMatchID); err != nil {
					code := LobbyErrorCode(err)
					if code == ServerIsFull || code == ServerIsLocked {
						<-time.After(5 * time.Second)
						continue
					}
					return fmt.Errorf("failed to join leader's social lobby: %w", err)
				}
				return nil
			default:
				// The leader is in a non-public match.
			}
		}
		// The leader is in a match, but this player is not.
		return NewLobbyError(ServerIsLocked, "party leader is in a match")
	}

}

func filterByPlayerAvailability(rttByPlayerByExtIP map[string]map[string]int) []string {

	maxPlayerCount := 0
	extIPsByAverageRTT := make(map[string]int)
	extIPsByPlayerCount := make(map[string]int)
	for extIP, players := range rttByPlayerByExtIP {
		extIPsByPlayerCount[extIP] += len(players)
		if len(players) > maxPlayerCount {
			maxPlayerCount = len(players)
		}

		averageRTT := 0
		for _, rtt := range players {
			averageRTT += rtt
		}
		averageRTT /= len(players)
	}

	// Sort by greatest player availability
	extIPs := make([]string, 0, len(extIPsByPlayerCount))
	for extIP := range extIPsByPlayerCount {
		extIPs = append(extIPs, extIP)
	}

	sort.SliceStable(extIPs, func(i, j int) bool {
		// Sort by player count first
		if extIPsByPlayerCount[extIPs[i]] > extIPsByPlayerCount[extIPs[j]] {
			return true
		} else if extIPsByPlayerCount[extIPs[i]] < extIPsByPlayerCount[extIPs[j]] {
			return false
		}

		// If the player count is the same, sort by RTT
		if extIPsByAverageRTT[extIPs[i]] < extIPsByAverageRTT[extIPs[j]] {
			return true
		}
		return false
	})

	return extIPs
}

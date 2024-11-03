package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

type TeamAlignments map[string]int // map[UserID]Role

var createSocialMu = sync.Mutex{}

var LobbyTestCounter = 0

var ErrCreateLock = errors.New("failed to acquire create lock")

// lobbyJoinSessionRequest is a request to join a specific existing session.
func (p *EvrPipeline) lobbyFind(ctx context.Context, logger *zap.Logger, session *sessionWS, lobbyParams *LobbySessionParameters) error {

	startTime := time.Now()
	defer func() {
		tags := lobbyParams.MetricsTags()
		tags["party_size"] = lobbyParams.PartySize.String()
		p.metrics.CustomTimer("lobby_find", tags, time.Since(startTime))
		logger.Debug("Lobby find complete", zap.String("group_id", lobbyParams.GroupID.String()), zap.Int64("partySize", lobbyParams.PartySize.Load()), zap.String("mode", lobbyParams.Mode.String()), zap.Int("duration", int(time.Since(startTime).Seconds())))
	}()

	// Do authorization checks related to the guild.
	if err := p.authorizeGuildGroupSession(ctx, session, lobbyParams.GroupID.String()); err != nil {
		return err
	}

	// Restrict matchmaking to public lobbies only
	switch lobbyParams.Mode {
	case evr.ModeArenaPublic, evr.ModeSocialPublic, evr.ModeCombatPublic:

	default:
		return NewLobbyError(BadRequest, "invalid mode")
	}

	// Cancel matchmaking after the timeout.
	ctx, cancel := context.WithTimeoutCause(ctx, p.matchmakingTicketTimeout(), ErrMatchmakingTimeout)
	defer cancel()
	go p.monitorMatchmakingStream(ctx, logger, session, lobbyParams, cancel)

	// The lobby group is the party that the user is currently in.
	lobbyGroup, isLeader, err := JoinPartyGroup(session, lobbyParams.PartyGroupName, lobbyParams.PartyID, lobbyParams.CurrentMatchID)
	if err != nil {
		return fmt.Errorf("failed to join party group: %w", err)
	}
	logger.Debug("Joined party group", zap.String("partyID", lobbyGroup.IDStr()))

	inLobby := !lobbyParams.CurrentMatchID.IsNil()

	// If the player is not in a social lobby yet, then immediately backfill to one.
	if !inLobby || lobbyParams.Mode == evr.ModeSocialPublic {
		entrantPresences, err := EntrantPresencesFromSessionIDs(logger, p.sessionRegistry, lobbyParams.PartyID, lobbyParams.GroupID, lobbyParams.Rating, lobbyParams.Role, session.id)
		if err != nil {
			return NewLobbyError(InternalError, "failed to create entrant presences")
		}

		return p.lobbyBackfill(ctx, logger, lobbyParams, entrantPresences)
	}
	// Party members will monitor the stream of the party leader to determine when to join a match.
	if !isLeader {
		return p.PartyFollow(ctx, logger, session, lobbyParams, lobbyGroup)
	}

	// Only ping the servers if the player is looking for a arena/combat match
	if lobbyParams.Mode != evr.ModeSocialPublic {
		if err := p.CheckServerPing(ctx, logger, session); err != nil {
			return fmt.Errorf("failed to check server ping: %w", err)
		}
	}

	// if the party is larger than one, then delay for 10 seconds to allow other members to start matchmake.
	// This is to prevent the party leader from starting matchmaking before the other members are ready.
	// This is only for non-social lobbies.
	if inLobby && lobbyGroup.Size() > 1 && lobbyParams.Mode != evr.ModeSocialPublic {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}

	// Remove any players not matchmaking.
	if err := p.pruneInactivePartyMembers(ctx, logger, session, lobbyParams, lobbyGroup); err != nil {
		return fmt.Errorf("failed to collect party entrants: %w", err)
	}

	// Construct the entrant presences for the party members.
	entrants, err := p.prepareEntrantPresences(ctx, logger, session, lobbyParams, lobbyGroup)
	if err != nil {
		return fmt.Errorf("failed to be party leader.: %w", err)
	}

	// If the player early quit their last match, they will not be matchmade.
	// Social lobbies are handled differently, they are created as needed (i.e. not matchmade)
	if !lobbyParams.IsEarlyQuitter && lobbyParams.Mode != evr.ModeSocialPublic {

		// Submit the matchmaking ticket
		if err := p.lobbyMatchMakeWithFallback(ctx, logger, session, lobbyParams, lobbyGroup); err != nil {
			return fmt.Errorf("failed to matchmake: %w", err)
		}
	}

	// Attempt to backfill until the timeout.
	return p.lobbyBackfill(ctx, logger, lobbyParams, entrants)
}

func (p *EvrPipeline) monitorMatchmakingStream(ctx context.Context, logger *zap.Logger, session *sessionWS, lobbyParams *LobbySessionParameters, cancelFn context.CancelFunc) {

	// Monitor the stream and cancel the context (and matchmaking) if the stream is closed.

	sendErr := func(err error) {
		if err := session.SendEvr(LobbySessionFailureFromError(lobbyParams.Mode, lobbyParams.GroupID, err)); err != nil {
			logger.Error("Failed to send lobby session failure message", zap.Error(err))
		}
	}

	// This stream tracks the user's matchmaking status.
	// This stream is untracked when the user cancels matchmaking.
	if err := JoinMatchmakingStream(logger, session, lobbyParams); err != nil {
		sendErr(err)
		return
	}

	stream := lobbyParams.MatchmakingStream()
	for {

		defer LeaveMatchmakingStream(logger, session)

		select {
		case <-ctx.Done():
			// Check if the cancel was because of a timeout
			if ctx.Err() == context.DeadlineExceeded {
				logger.Warn("Matchmaking timeout")
				if err := session.SendEvr(LobbySessionFailureFromError(lobbyParams.Mode, lobbyParams.GroupID, NewLobbyError(Timeout, "matchmaking timeout"))); err != nil {
					logger.Error("Failed to send lobby session failure message", zap.Error(err))
				}
			}
			return
		case <-time.After(1 * time.Second):
		}

		// Check if the matchmaking stream has been closed.  (i.e. the user has canceled matchmaking)
		if session.tracker.GetLocalBySessionIDStreamUserID(session.id, stream, session.userID) == nil {
			cancelFn()
		}
	}
}

func (p *EvrPipeline) newSocialLobby(ctx context.Context, logger *zap.Logger, versionLock evr.Symbol, groupID uuid.UUID) (*MatchLabel, error) {
	if createSocialMu.TryLock() {
		go func() {
			<-time.After(5 * time.Second)
			createSocialMu.Unlock()
		}()
	} else {
		return nil, ErrFailedToAcquireLock
	}

	metricsTags := map[string]string{
		"version_lock": versionLock.String(),
		"group_id":     groupID.String(),
	}

	p.metrics.CustomCounter("lobby_create_social", metricsTags, 1)

	qparts := []string{
		"+label.open:T",
		"+label.lobby_type:unassigned",
		"+label.broadcaster.regions:/(default)/",
		fmt.Sprintf("+label.broadcaster.group_ids:/(%s)/", Query.Escape(groupID.String())),
		//fmt.Sprintf("+label.broadcaster.version_lock:%s", versionLock.String()),
	}

	query := strings.Join(qparts, " ")

	labels, err := lobbyListGameServers(ctx, p.runtimeModule, query)
	if err != nil {
		logger.Warn("Failed to list game servers", zap.Any("query", query), zap.Error(err))
		return nil, err
	}

	// Retrieve the latency history of all online public players.
	// Identify servers where the majority of players have a ping other than 999 (or 0).
	// Sort servers by those with a ping less than 250 for all players.
	// Select the server with the best average ping for the highest number of players.
	label := &MatchLabel{}

	rttByPlayerByExtIP, err := rttByPlayerByExtIP(ctx, logger, p.db, p.runtimeModule, groupID.String())
	if err != nil {
		logger.Warn("Failed to get RTT by player by extIP", zap.Error(err))
	} else {
		extIPs := sortByGreatestPlayerAvailability(rttByPlayerByExtIP)
		for _, extIP := range extIPs {
			for _, l := range labels {
				if l.Broadcaster.Endpoint.GetExternalIP() == extIP {
					label = l
					break
				}
			}
		}
	}

	// If no label was found, just pick a random one
	if label.ID.IsNil() {
		label = labels[rand.Intn(len(labels))]
	}

	if err := lobbyPrepareSession(ctx, logger, p.matchRegistry, label.ID, evr.ModeSocialPublic, evr.LevelSocial, uuid.Nil, groupID, TeamAlignments{}, time.Now().UTC()); err != nil {
		logger.Error("Failed to prepare session", zap.Error(err), zap.String("mid", label.ID.UUID.String()))
		return nil, err
	}

	match, _, err := p.matchRegistry.GetMatch(ctx, label.ID.String())
	if err != nil {
		return nil, fmt.Errorf("failed to get match: %w", err)
	} else if match == nil {
		logger.Warn("Match not found", zap.String("mid", label.ID.UUID.String()))
		return nil, ErrMatchNotFound
	}

	label = &MatchLabel{}
	if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), label); err != nil {
		return nil, fmt.Errorf("failed to unmarshal match label: %w", err)
	}
	return label, nil

}

func (p *EvrPipeline) lobbyBackfill(ctx context.Context, logger *zap.Logger, lobbyParams *LobbySessionParameters, entrants []*EvrMatchPresence) error {

	// Default backfill interval
	interval := 15 * time.Second

	// Early quitters have a shorter backfill interval.
	if lobbyParams.IsEarlyQuitter || lobbyParams.Mode == evr.ModeSocialPublic {
		interval = 3 * time.Second
	}

	// If the player has backfill disabled, set the backfill interval to an extreme number.
	if lobbyParams.DisableArenaBackfill && lobbyParams.Mode == evr.ModeArenaPublic {
		// Set a long backfill interval for arena matches.
		interval = 15 * time.Minute
	}

	// Backfill search query
	// Maximum RTT for a server to be considered for backfill
	maxRTT := 250

	if lobbyParams.Mode == evr.ModeSocialPublic {
		// any server is fine for social lobbies
		maxRTT = 999
	}

	query := lobbyParams.BackfillSearchQuery(maxRTT)
	rtts := lobbyParams.latencyHistory.LatestRTTs()
	startDelay := time.NewTimer(1 * time.Second)

	for {
		var err error
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-startDelay.C:
		case <-time.After(interval):
		}

		// List all matches that are open and have available slots.
		matches, err := ListMatchStates(ctx, p.runtimeModule, query)
		if err != nil {
			return fmt.Errorf("failed to list matches: %w", err)
		}

		logger.Debug("Found matches", zap.Int("count", len(matches)), zap.Any("query", query))
		// Sort the labels by least open slots, then ping

		// Sort the matches by open slots and then by latency
		slices.SortFunc(matches, func(a, b *MatchLabelMeta) int {
			// Sort by open slots first
			if s := b.State.OpenPlayerSlots() - a.State.OpenPlayerSlots(); s != 0 {
				return s
			}
			// If the open slots are the same, sort by latency
			return rtts[a.State.Broadcaster.Endpoint.GetExternalIP()] - rtts[b.State.Broadcaster.Endpoint.GetExternalIP()]
		})

		partySize := lobbyParams.GetPartySize()
		var selected *MatchLabel

		team := evr.TeamBlue

		for _, labelMeta := range matches {
			l := labelMeta.State

			// Check if the match is full
			if l.OpenPlayerSlots() < partySize {
				continue
			}

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

			if l.OpenSlotsByRole(team) < partySize {
				continue
			}

			// Match found
			selected = l
			break
		}

		// If no match was found, continue.
		// If the lobby is social, create a new social lobby.
		if selected == nil && lobbyParams.Mode == evr.ModeSocialPublic {
			// Create a new social lobby
			selected, err = p.newSocialLobby(ctx, logger, lobbyParams.VersionLock, lobbyParams.GroupID)
			if err != nil {

				// If the error is a lock error, just try again.
				if err == ErrFailedToAcquireLock {
					logger.Warn("Failed to acquire create lock")
					continue
				}

				// This should't happen unless there's no servers available.
				return NewLobbyErrorf(ServerFindFailed, "failed to find social lobby: %w", err)
			}
		}

		if selected == nil {
			// No match found
			continue
		}

		// Set the role alignment for each entrant in the party
		for _, e := range entrants {
			e.RoleAlignment = team
		}

		logger := logger.With(zap.String("mid", selected.ID.UUID.String()))

		logger.Debug("Joining backfill match.")
		p.metrics.CustomCounter("lobby_join_backfill", lobbyParams.MetricsTags(), int64(lobbyParams.PartySize.Load()))

		// Player members will detect the join.
		if err := p.LobbyJoinEntrants(logger, selected, entrants); err != nil {
			// Send the error to the client
			// If it's full just try again.
			if LobbyErrorCode(err) == ServerIsFull {
				logger.Warn("Server is full, ignoring.")
				continue
			}
			return fmt.Errorf("failed to join backfill match: %w", err)
		}
		return nil
	}
}

func (p *EvrPipeline) CheckServerPing(ctx context.Context, logger *zap.Logger, session *sessionWS) error {
	// Check latency to active game servers.
	doneCh := make(chan error)

	go func() {
		defer close(doneCh)

		// Wait for the client to be ready.
		<-time.After(1 * time.Second)

		activeEndpoints := make([]evr.Endpoint, 0, 100)
		p.broadcasterRegistrationBySession.Range(func(_ string, b *MatchBroadcaster) bool {
			activeEndpoints = append(activeEndpoints, b.Endpoint)
			return true
		})

		if err := PingGameServers(ctx, logger, session, p.db, activeEndpoints); err != nil {
			doneCh <- err
		}
		doneCh <- nil
	}()

	// Wait for the ping response to complete
	var err error
	select {
	case <-time.After(5 * time.Second):
		logger.Warn("Timed out waiting for ping responses message.")
	case err = <-doneCh:
	}
	return err
}

func (p *EvrPipeline) pruneInactivePartyMembers(ctx context.Context, logger *zap.Logger, session *sessionWS, params *LobbySessionParameters, lobbyGroup *LobbyGroup) error {

	// Remove any players not matchmaking.
	for _, member := range lobbyGroup.List() {
		if member.Presence.GetSessionId() == session.id.String() {
			continue
		}

		sessionID := uuid.FromStringOrNil(member.Presence.GetSessionId())
		userID := uuid.FromStringOrNil(member.Presence.GetUserId())
		if session.tracker.GetLocalBySessionIDStreamUserID(sessionID, params.MatchmakingStream(), userID) == nil {
			// Kick the player from the party.
			logger.Debug("Kicking player from party, because they are not matchmaking.", zap.String("uid", member.Presence.GetUserId()))
			session.tracker.UntrackLocalByModes(sessionID, map[uint8]struct{}{StreamModeParty: {}}, PresenceStream{})
		}
	}

	params.SetPartySize(len(lobbyGroup.List()))

	return nil
}

func (p *EvrPipeline) prepareEntrantPresences(ctx context.Context, logger *zap.Logger, session *sessionWS, params *LobbySessionParameters, lobbyGroup *LobbyGroup) ([]*EvrMatchPresence, error) {

	// prepare the entrant presences for all party members
	sessionIDs := []uuid.UUID{session.id}
	for _, member := range lobbyGroup.List() {
		if member.Presence.GetSessionId() == session.id.String() {
			continue
		}
		sessionIDs = append(sessionIDs, uuid.FromStringOrNil(member.Presence.GetSessionId()))
	}

	entrantPresences, err := EntrantPresencesFromSessionIDs(logger, p.sessionRegistry, params.PartyID, params.GroupID, params.Rating, params.Role, sessionIDs...)
	if err != nil {
		return nil, NewLobbyError(InternalError, "failed to create entrant presences")
	}

	if len(entrantPresences) == 0 {
		logger.Error("No entrants found. Cancelling matchmaking.")
		return nil, NewLobbyError(InternalError, "no entrants found")
	}

	return entrantPresences, nil
}
func (p *EvrPipeline) PartyFollow(ctx context.Context, logger *zap.Logger, session *sessionWS, params *LobbySessionParameters, lobbyGroup *LobbyGroup) error {

	logger.Debug("User is member of party", zap.String("leader", lobbyGroup.GetLeader().GetUsername()))
	// This is a party member, wait for the party leader to join a match, or cancel matchmaking.
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
		leader := lobbyGroup.GetLeader()
		// Check if the leader has changed to this player.
		if leader == nil || leader.SessionId == session.id.String() {
			return NewLobbyError(BadRequest, "party leader changed")
		}
		leaderSessionID := uuid.FromStringOrNil(leader.SessionId)
		stream := PresenceStream{
			Mode:    StreamModeService,
			Subject: leaderSessionID,
			Label:   StreamLabelMatchService,
		}

		// Check if the party leader has joined a match.
		presence := session.pipeline.tracker.GetLocalBySessionIDStreamUserID(leaderSessionID, stream, uuid.FromStringOrNil(leader.UserId))
		if presence == nil {
			return NewLobbyError(BadRequest, "party leader left the party")
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

		stream = PresenceStream{
			Mode:    StreamModeService,
			Subject: session.id,
			Label:   StreamLabelMatchService,
		}

		presence = session.pipeline.tracker.GetLocalBySessionIDStreamUserID(session.id, stream, session.userID)
		if presence == nil {
			return NewLobbyError(BadRequest, "this member is not in a match")
		}

		memberMatchID := MatchIDFromStringOrNil(presence.GetStatus())
		if memberMatchID.IsNil() {
			continue
		}

		if memberMatchID == leaderMatchID {
			// The leader is in a match, and this player is in the same match.
			continue
		} else {
			// If the leader is in a different public lobby, try to join it.
			label, err := MatchLabelByID(ctx, p.runtimeModule, leaderMatchID)
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

			case evr.ModeSocialPublic, evr.ModeCombatPublic, evr.ModeArenaPublic:
				// Join the leader's match.
				logger.Debug("Joining leader's lobby", zap.String("mid", leaderMatchID.String()))
				params.CurrentMatchID = leaderMatchID
				if err := p.lobbyJoin(ctx, logger, session, params); err != nil {
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

// Wrapper for the matchRegistry.ListMatches function.
func listMatches(ctx context.Context, nk runtime.NakamaModule, limit int, minSize int, maxSize int, query string) ([]*api.Match, error) {
	return nk.MatchList(ctx, limit, true, "", &minSize, &maxSize, query)
}

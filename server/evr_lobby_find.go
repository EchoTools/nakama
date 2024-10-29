package server

import (
	"context"
	"errors"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

var LobbyTestCounter = 0

var ErrCreateLock = errors.New("failed to acquire create lock")
var MatchmakingTimeout = 5 * time.Minute

//var MatchmakingTimeout = 30 * time.Second

// lobbyJoinSessionRequest is a request to join a specific existing session.
func (p *EvrPipeline) lobbyFind(ctx context.Context, logger *zap.Logger, session *sessionWS, lobbyParams *LobbySessionParameters) error {

	startTime := time.Now()
	defer func() {
		p.metrics.CustomTimer("lobby_find", lobbyParams.MetricsTags(), time.Since(startTime))
		logger.Debug("Lobby find complete", zap.Duration("duration", time.Since(startTime)))
	}()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Do authorization checks related to the guild.
	if err := p.authorizeGuildGroupSession(ctx, session, lobbyParams.GroupID.String()); err != nil {
		return err
	}

	switch lobbyParams.Mode {
	case evr.ModeArenaPublic, evr.ModeSocialPublic, evr.ModeCombatPublic:

	default:
		return NewLobbyError(BadRequest, "invalid mode")
	}

	// This stream tracks the user's matchmaking status.
	// This stream is untracked when the user cancels matchmaking.
	if err := JoinMatchmakingStream(logger, session, lobbyParams); err != nil {
		return errors.Join(NewLobbyError(InternalError, "failed to join matchmaking stream"), err)
	}

	// The lobby group is the party that the user is currently in.
	lobbyGroup, err := JoinLobbyGroup(session, lobbyParams.PartyGroupName, lobbyParams.PartyID, lobbyParams.CurrentMatchID)
	if err != nil {
		return errors.Join(NewLobbyError(InternalError, "failed to join lobby group"), err)
	}
	logger.Debug("Joined lobby group", zap.String("partyID", lobbyGroup.IDStr()))

	// Only do party operations if the player is current in a match (i.e not joining from the main menu)
	if !lobbyParams.CurrentMatchID.IsNil() {

		if lobbyGroup.GetLeader().SessionId != session.id.String() {
			return p.PartyFollow(ctx, logger, session, lobbyParams, lobbyGroup)
		}

		if err := p.PartyLead(ctx, logger, session, lobbyParams, lobbyGroup); err != nil {
			return errors.Join(NewLobbyError(InternalError, "failed to be party leader."), err)
		}
	}

	if err := p.CheckServerPing(ctx, logger, session); err != nil {
		return errors.Join(NewLobbyError(InternalError, "failed to check server ping"), err)
	}

	// If this is a public combat or arena match, then matchmake.
	if lobbyParams.Mode == evr.ModeArenaPublic || lobbyParams.Mode == evr.ModeCombatPublic {
		// Matchmake a new lobby session
		logger.Debug("matchmaking", zap.Any("members", lobbyGroup.List()))

		if err := p.lobbyMatchMakeWithFallback(ctx, logger, session, lobbyParams, lobbyGroup); err != nil {
			return errors.Join(NewLobbyError(InternalError, "failed to matchmake"), err)
		}
	}

	initialTimer := time.NewTimer(1 * time.Second)
	backfillInterval := 6 * time.Second
	timeout := time.After(MatchmakingTimeout)

	for {
		var err error
		select {
		case <-ctx.Done():
			if ctx.Err() != nil && ctx.Err() != context.Canceled {
				return errors.Join(NewLobbyError(BadRequest, "context error"), ctx.Err())
			}
			return nil
		case <-timeout:
			return NewLobbyError(Timeout, "matchmaking timeout")
		case <-initialTimer.C:
			logger.Debug("initial timer")
		case <-time.After(backfillInterval):
		}

		if lobbyParams.DisableArenaBackfill && lobbyParams.Mode == evr.ModeArenaPublic {
			continue
		}

		entrants, err := EntrantPresencesFromSessionIDs(logger, p.sessionRegistry, lobbyParams.PartyID, lobbyParams.GroupID, lobbyParams.Rating, lobbyParams.Role, session.id)
		if err != nil {
			return NewLobbyError(InternalError, "failed to create entrant presences")
		}

		if len(entrants) == 0 {
			logger.Error("No entrants found. Cancelling matchmaking.")
			return nil
		}

		entrant := entrants[0]

		label, team, err := p.lobbyQueue.GetUnfilledMatch(ctx, lobbyParams)
		if err == ErrNoUnfilledMatches {
			continue
		} else if err != nil {
			return errors.Join(NewLobbyError(InternalError, "failed to get unfilled match"), err)
		}

		logger := logger.With(zap.String("mid", label.ID.UUID.String()))

		logger.Debug("Joining backfill match.")
		p.metrics.CustomCounter("lobby_join_backfill", lobbyParams.MetricsTags(), int64(lobbyParams.PartySize.Load()))

		label, serverSession, err := p.LobbySessionGet(ctx, logger, label.ID)
		if err != nil {
			logger.Debug("Failed to get match session", zap.Error(err))
			continue
		}
		// Player members will detect the join.
		if err := p.LobbyJoinEntrant(logger, serverSession, label, team, entrant); err != nil {
			// Send the error to the client
			// If it's full just try again.
			if LobbyErrorCode(err) == ServerIsFull {
				logger.Warn("Server is full, ignoring.")
				continue
			}
			return errors.Join(NewLobbyError(InternalError, "failed to join backfill match"), err)
		}
	}

	return nil
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

func (p *EvrPipeline) PartyLead(ctx context.Context, logger *zap.Logger, session *sessionWS, params *LobbySessionParameters, lobbyGroup *LobbyGroup) error {

	logger.Debug("User is leader of party")
	params.SetPartySize(len(lobbyGroup.List()))

	// Wait for the party
	delay := 10 * time.Second

	// If this is going back to a social lobby, don't wait.
	if params.Mode == evr.ModeSocialPublic {
		delay = 1 * time.Second
	}

	select {
	case <-ctx.Done():
		return nil
	case <-time.After(delay):
	}

	// Remove any players not matchmaking.
	for _, member := range lobbyGroup.List() {
		if member.Presence.GetSessionId() == session.id.String() {
			continue
		}

		sessionID := uuid.FromStringOrNil(member.Presence.GetSessionId())
		userID := uuid.FromStringOrNil(member.Presence.GetUserId())
		if session.tracker.GetLocalBySessionIDStreamUserID(sessionID, params.GroupStream(), userID) == nil {
			// Kick the player from the party.
			logger.Debug("Kicking player from party, because they are not matchmaking.", zap.String("uid", member.Presence.GetUserId()))
			session.tracker.UntrackLocalByModes(sessionID, map[uint8]struct{}{StreamModeParty: {}}, PresenceStream{})
		}
	}

	params.SetPartySize(len(lobbyGroup.List()))

	return nil
}
func (p *EvrPipeline) PartyFollow(ctx context.Context, logger *zap.Logger, session *sessionWS, params *LobbySessionParameters, lobbyGroup *LobbyGroup) error {

	logger.Debug("User is member of party", zap.String("leader", lobbyGroup.GetLeader().GetUsername()))
	// This is a party member, wait for the party leader to join a match, or cancel matchmaking.
	for {
		select {
		case <-ctx.Done():
			return nil
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
			return nil
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
				return errors.Join(NewLobbyError(InternalError, "failed to get match by session id"), err)
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
					return errors.Join(NewLobbyError(InternalError, "failed to join leader's social lobby"), err)
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

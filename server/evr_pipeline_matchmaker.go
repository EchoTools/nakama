package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// lobbyMatchmakerStatusRequest is a message requesting the status of the matchmaker.
func (p *EvrPipeline) lobbyMatchmakerStatusRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	_ = in.(*evr.LobbyMatchmakerStatusRequest)

	// TODO Check if the matchmaking ticket is still open
	err := session.SendEvr([]evr.Message{
		evr.NewLobbyMatchmakerStatusResponse(),
	})
	if err != nil {
		return fmt.Errorf("LobbyMatchmakerStatus: %v", err)
	}
	return nil
}

// authorizeMatchmaking checks if the user is allowed to join a public match or spawn a new match
func (p *EvrPipeline) authorizeMatchmaking(ctx context.Context, logger *zap.Logger, session *sessionWS, channel uuid.UUID) (bool, error) {
	// Send a match leave if this user is in another match
	if session.userID == uuid.Nil {
		return false, status.Errorf(codes.PermissionDenied, "User not authenticated")
	}

	sessionIDs := session.tracker.ListLocalSessionIDByStream(PresenceStream{Mode: StreamModeEvr, Subject: session.userID, Subcontext: svcMatchID})
	for _, foundSessionID := range sessionIDs {
		if foundSessionID == session.id {
			// Allow the current session, only disconnect any older ones.
			continue
		}
		// Disconnect the older session.
		logger.Debug("Disconnecting older session from matchmaking", zap.String("other_sid", foundSessionID.String()))
		p.tracker.UntrackLocalByModes(foundSessionID, matchStreamModes, PresenceStream{})
	}

	// Track this session as a matchmaking session.
	s := session
	s.tracker.TrackMulti(s.ctx, s.id, []*TrackerOp{
		// EVR packet data stream for the match session by userID, and service ID
		{
			Stream: PresenceStream{Mode: StreamModeEvr, Subject: s.userID, Subcontext: svcMatchID},
			Meta:   PresenceMeta{Format: s.format, Hidden: true},
		},
		// EVR packet data stream for the match session by Session ID and service ID
		{
			Stream: PresenceStream{Mode: StreamModeEvr, Subject: s.id, Subcontext: svcMatchID},
			Meta:   PresenceMeta{Format: s.format, Hidden: true},
		},
	}, s.userID, true)

	if channel == uuid.Nil {
		// Get the user's currently selection social channel
		channel, err := p.getPlayersCurrentChannel(ctx, session)
		if err != nil {
			return false, status.Errorf(codes.Internal, "Failed to get players current channel: %v", err)
		}
		if channel == uuid.Nil {
			return true, nil
		}
		// TODO FIXME check the user's channels for suspensions and add them as MUST NOT to the matchmaker.
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

// lobbyFindSessionRequest is a message requesting to find a public session to join.
func (p *EvrPipeline) lobbyFindSessionRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) (err error) {
	request := in.(*evr.LobbyFindSessionRequest)

	if request.Channel == uuid.Nil {
		logger.Debug("Channel is nil, getting the users current channel")
		// Get the users currently selected channel
		// Get the players current channel
		request.Channel, err = p.getPlayersCurrentChannel(ctx, session)
		if err != nil {
			return status.Errorf(codes.Internal, "Failed to get players current channel: %v", err)
		}
	}
	result := NewMatchmakingResult(request.Mode, request.Channel)

	// Check for suspensions on this channel, if this is a request for a public match.
	if authorized, err := p.authorizeMatchmaking(ctx, logger, session, request.Channel); !authorized {
		return result.SendErrorToSession(session, err)
	} else if err != nil {
		logger.Error("Failed to authorize matchmaking", zap.Error(err))
	}
	gracePeriod := MatchJoinGracePeriod

	// TODO Check if the user is in a party.
	partySize := 1
	ml := &EvrMatchState{
		Channel: &request.Channel,
		Level:   request.Level,
		MatchId: request.MatchingSession, // The existing lobby/match that the player is in (if any)
		Mode:    request.Mode,
		Open:    true,

		SessionSettings: &request.SessionSettings,
		TeamIndex:       TeamIndex(request.TeamIndex),

		Broadcaster: MatchBroadcaster{
			Platform:    request.Platform,
			VersionLock: request.VersionLock,
		},
	}

	// Unless this is a social lobby, wait for a grace period before starting the matchmaker
	if ml.Mode != evr.ModeSocialPublic {
		select {
		case <-time.After(gracePeriod):
		case <-ctx.Done():
			return result.SendErrorToSession(session, ErrMatchmakingTimeout)
		}
	}

	// Create a goroutine for the matching session
	go func() error {
		if s, ok := p.matchmakingRegistry.GetMatchingBySessionId(session.id); ok {
			logger.Error("Label for matching session", zap.Any("lable", s.Label))
			logger.Error("Matchmaking session already exists", zap.Any("session", s))
		}
		// Create a new matching session
		msession, err := p.matchmakingRegistry.Create(ctx, session, ml, partySize)
		if err != nil {
			return result.SendErrorToSession(session, err)
		}
		ctx := msession.Context()
		defer msession.Cancel(nil)

		switch {
		// Public matches
		case ml.Mode == evr.ModeArenaPublic || ml.Mode == evr.ModeCombatPublic || ml.Mode == evr.ModeSocialPublic:
			// Backfill any existing matches
			labels, err := p.Backfill(ctx, session, msession)
			if err != nil {
				return result.SendErrorToSession(session, err)
			}

			// Join the first match, or continue to create one
			if len(labels) > 0 {
				label := labels[0]
				err = p.JoinEvrMatch(ctx, session, label.MatchId.String(), *label.Channel, int(ml.TeamIndex))
				if err != nil {
					return result.SendErrorToSession(session, err)
				}
				return nil
			}

			// If it's a social lobby, create a new one, don't matchmake
			if ml.Mode == evr.ModeSocialPublic {
				// set a timeout

				stageTimer := time.NewTimer(30 * time.Second)
				for {
					// Stage 1: Check if there is an available lobby
					match, err := p.MatchCreate(ctx, session, msession, ml)

					switch status.Code(err) {

					case codes.OK:
						matchId := match.MatchId.String()
						err = p.JoinEvrMatch(ctx, session, matchId, request.Channel, int(ml.TeamIndex))
						if err != nil {
							return result.SendErrorToSession(session, err)
						}
						return nil

					case codes.Unavailable:
						// No servers are connected. This can happen if the server is starting up or shutting down.
						fallthrough
					case codes.ResourceExhausted:
						// All teh servers are being used.
						select {

						case <-time.After(5 * time.Second):
							continue
						case <-ctx.Done():
							return result.SendErrorToSession(session, ErrMatchmakingTimeout)
						case <-stageTimer.C:
							// Move Stage 2: Kill a private lobby that has existing with only 1-2 people in it.
							err := p.pruneMatches(ctx, session)
							if err != nil {
								return result.SendErrorToSession(session, err)
							}
						}
					}
				}
			}

			// Monitor the ticket in a separate goroutine.
			ticket, err := p.AddMatchmakingTicket(ctx, session, logger, msession)
			if err != nil {
				result.SendErrorToSession(session, err)
			}
			defer session.matchmaker.RemoveSessionAll(session.id.String())

			select {
			case <-time.After(10 * time.Minute):
				logger.Debug("Matchmaking ticket timeout", zap.String("ticket", ticket))
				// TODO FIXME Send matchmaking cancel to user
				msession.RemoveTicket(ticket)
				msession.Cancel(ErrMatchmakingTimeout)
				// Send timeout
				err := result.SendErrorToSession(session, ErrMatchmakingTimeout)
				if err != nil {
					logger.Error("Failed to send error to session", zap.Error(err))
				}

			case <-msession.Ctx.Done():
				// TODO FIXME Send matchmaker cancel to the matchmaker
				err := context.Cause(ctx)
				logger.Debug("Matchmaking session context done", zap.Error(err))
				if err != nil {
					logger.Error("Matchmaking session context error", zap.Error(err))
					err = result.SendErrorToSession(session, err)
					if err != nil {
						logger.Error("Failed to send error to session", zap.Error(err))
					}
				}
			case matchId := <-msession.MatchIdCh:
				logger.Debug("Match found", zap.String("mid", matchId))
				// join the match
				err := p.JoinEvrMatch(ctx, session, matchId, *ml.Channel, int(ml.TeamIndex))
				if err != nil {
					logger.Error("Failed to join match", zap.Error(err))
					err := result.SendErrorToSession(session, ErrMatchmakingTimeout)
					if err != nil {
						logger.Error("Failed to send error to session", zap.Error(err))
					}
				}
			}

			return nil
		case ml.Mode == evr.ModeArenaPrivate || ml.Mode == evr.ModeCombatPrivate || ml.Mode == evr.ModeSocialPrivate:
			// Create a new match
			match, err := p.MatchCreate(ctx, session, msession, ml)
			if err != nil {
				return result.SendErrorToSession(session, err)
			}
			matchId := match.MatchId.String()
			err = p.JoinEvrMatch(ctx, session, matchId, request.Channel, int(ml.TeamIndex))
			if err != nil {
				return result.SendErrorToSession(session, err)
			}
		default:
			return result.SendErrorToSession(session, status.Errorf(codes.InvalidArgument, "Invalid mode: %v", ml.Mode))
		}
		return nil
	}()

	return nil
}

// lobbyPingResponse is a message responding to a ping request.
func (p *EvrPipeline) lobbyPingResponse(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	response := in.(*evr.LobbyPingResponse)

	userID := session.UserID()
	// Validate the connection.
	if userID == uuid.Nil {
		return fmt.Errorf("session not authenticated")
	}

	p.matchmakingRegistry.ProcessPingResults(userID, response.Results)

	// Look up the matching session.
	msession, ok := p.matchmakingRegistry.GetMatchingBySessionId(session.id)
	if !ok {
		return fmt.Errorf("matching session not found")
	}
	// Send a signal to the matching session to continue searching.
	msession.PingCompleteCh <- nil
	return nil
}

// lobbyCreateSessionRequest is a request to create a new session.
func (p *EvrPipeline) lobbyCreateSessionRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.LobbyCreateSessionRequest)
	result := NewMatchmakingResult(request.Mode, request.Channel)
	// Check for suspensions on this channel. The user will not be allowed to create lobby's
	if authorized, err := p.authorizeMatchmaking(ctx, logger, session, request.Channel); !authorized {
		return result.SendErrorToSession(session, err)
	} else if err != nil {
		logger.Error("Failed to authorize matchmaking", zap.Error(err))
	}

	ml := &EvrMatchState{
		Channel:         &request.Channel,
		Level:           request.Level,
		LobbyType:       LobbyType(request.LobbyType),
		Mode:            request.Mode,
		Open:            true,
		SessionSettings: &request.SessionSettings,
		TeamIndex:       TeamIndex(request.TeamIndex),

		Broadcaster: MatchBroadcaster{
			Platform: request.Platform,

			VersionLock: uint64(request.VersionLock),
			Region:      request.Region,
		},
	}

	// Start the search in a goroutine.
	go func() error {
		// Set some defaults
		partySize := 1 // TODO FIXME this should include the party size

		// Create a matching session
		msession, err := p.matchmakingRegistry.Create(ctx, session, ml, partySize)
		if err != nil {
			return result.SendErrorToSession(session, status.Errorf(codes.Internal, "Failed to create matchmaking session: %v", err))
		}

		// Create a new match
		match, err := p.MatchCreate(ctx, session, msession, ml)
		if err != nil {
			return result.SendErrorToSession(session, err)
		}
		matchId := match.MatchId.String()
		err = p.JoinEvrMatch(msession.Ctx, session, matchId, request.Channel, int(ml.TeamIndex))
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

	// Make sure the match exists
	matchId := request.LobbyId.String() + "." + p.node
	match, _, err := p.matchRegistry.GetMatch(ctx, matchId)
	if err != nil {
		return NewMatchmakingResult(0, uuid.Nil).SendErrorToSession(session, err)
	}

	// Extract the label
	ml := &EvrMatchState{}
	if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), ml); err != nil {
		return NewMatchmakingResult(0, uuid.Nil).SendErrorToSession(session, err)
	}
	result := NewMatchmakingResult(ml.Mode, *ml.Channel)
	// Check for suspensions on this channel.
	if ml.Mode == evr.ModeArenaPublic || ml.Mode == evr.ModeCombatPublic || ml.Mode == evr.ModeSocialPublic {
		// Check for suspensions on this channel, if this is a request for a public match.
		if authorized, err := p.authorizeMatchmaking(ctx, logger, session, *ml.Channel); !authorized {
			return result.SendErrorToSession(session, err)
		} else if err != nil {
			logger.Error("Failed to authorize matchmaking", zap.Error(err))
		}
	}

	// Join the match
	if err = p.JoinEvrMatch(ctx, session, matchId, *ml.Channel, int(ml.TeamIndex)); err != nil {
		return result.SendErrorToSession(session, err)
	}
	return nil
}

// LobbyPendingSessionCancel is a message from the server to the client, indicating that the user wishes to cancel matchmaking.
func (p *EvrPipeline) lobbyPendingSessionCancel(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	// Look up the matching session.
	if matchingSession, ok := p.matchmakingRegistry.GetMatchingBySessionId(session.id); ok {
		matchingSession.Cancel(ErrMatchmakingCancelled)
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

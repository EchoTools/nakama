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
func (p *EvrPipeline) lobbyFindSessionRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.LobbyFindSessionRequest)
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
		Channel:         request.Channel,
		Level:           request.Level,
		MatchId:         request.MatchingSession, // The existing lobby/match that the player is in (if any)
		Mode:            request.Mode,
		Open:            true,
		Platform:        request.Platform,
		SessionSettings: request.SessionSettings,
		TeamIndex:       TeamIndex(request.TeamIndex),
		VersionLock:     request.VersionLock,
	}

	// Create a goroutine for the matching session
	go func() error {

		if ml.Mode == evr.ModeSocialPublic || ml.Mode == evr.ModeSocialPrivate {
			gracePeriod = 0
		}
		// Create a new matching session
		msession, err := p.matchmakingRegistry.Create(ctx, session, ml, partySize)
		if err != nil {
			return result.SendErrorToSession(session, err)
		}
		msession.RLock()
		ctx := msession.Ctx
		msession.RUnlock()
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
				err = p.JoinEvrMatch(ctx, session, label.MatchId.String(), label.Channel, int(ml.TeamIndex), gracePeriod)
				if err != nil {
					return result.SendErrorToSession(session, err)
				}
			}

			// If there are no existing matches to join, then try to matchmake for a new one
			if ml.Mode == evr.ModeArenaPublicAI { // But only if the mode is set to AI
				// Use Public AI matches as a way to test the Matchmaker system
				// Set the modes to public arena
				ml.Mode = evr.ModeArenaPublic
				ml.Level = evr.LevelArena

				// Add ticket in for a match
				ticket, err := p.AddMatchmakingTicket(ctx, session, logger, msession)
				if err != nil {
					return result.SendErrorToSession(session, err)
				}

				// Monitor the ticket in a separate goroutine.
				go func() {
					select {
					case <-time.After(900 * time.Second):
						// TODO Cancel the matchmaking session
					case <-msession.Ctx.Done():
						// TODO Cancel the matchmaking session
						msession.RemoveTicket(ticket)
					}
				}()
				return nil
			}

			fallthrough // fall through to create a match
		case ml.Mode == evr.ModeArenaPrivate || ml.Mode == evr.ModeCombatPrivate || ml.Mode == evr.ModeSocialPrivate:
			// Create a new match
			match, err := p.MatchCreate(ctx, session, msession, ml)
			if err != nil {
				return result.SendErrorToSession(session, err)
			}
			matchId := match.MatchId.String()
			err = p.JoinEvrMatch(ctx, session, matchId, request.Channel, int(ml.TeamIndex), gracePeriod)
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
		Channel:         request.Channel,
		Level:           request.Level,
		LobbyType:       LobbyType(request.LobbyType),
		Mode:            request.Mode,
		Open:            true,
		Platform:        request.Platform,
		Region:          request.Region,
		SessionSettings: request.SessionSettings,
		TeamIndex:       TeamIndex(request.TeamIndex),
		VersionLock:     uint64(request.VersionLock),
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
		err = p.JoinEvrMatch(msession.Ctx, session, matchId, request.Channel, int(ml.TeamIndex), 0)
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
	result := NewMatchmakingResult(ml.Mode, ml.Channel)
	// Check for suspensions on this channel.
	if ml.Mode == evr.ModeArenaPublic || ml.Mode == evr.ModeCombatPublic || ml.Mode == evr.ModeSocialPublic {
		// Check for suspensions on this channel, if this is a request for a public match.
		if authorized, err := p.authorizeMatchmaking(ctx, logger, session, ml.Channel); !authorized {
			return result.SendErrorToSession(session, err)
		} else if err != nil {
			logger.Error("Failed to authorize matchmaking", zap.Error(err))
		}
	}

	// Join the match
	if err = p.JoinEvrMatch(ctx, session, matchId, ml.Channel, int(ml.TeamIndex), 0); err != nil {
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

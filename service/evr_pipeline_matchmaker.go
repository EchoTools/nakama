package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama/v3/server"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	FriendStateFriends = iota
	FriendInvitationSent
	FriendInvitationReceived
	FriendStateBlocked
)

// lobbyMatchmakerStatusRequest is a message requesting the status of the matchmaker.
func (p *EvrPipeline) lobbyMatchmakerStatusRequest(ctx context.Context, logger *zap.Logger, session *sessionEVR, in evr.Message) error {
	_ = in.(*evr.LobbyMatchmakerStatusRequest)

	// This cannot have an unrequire message, otherwise the client will hang forever with "MATCHMAKING"
	err := session.SendEvr(evr.NewLobbyMatchmakerStatusResponse())
	if err != nil {
		return fmt.Errorf("LobbyMatchmakerStatus: %w", err)
	}
	return nil
}

func (p *EvrPipeline) lobbySessionRequest(ctx context.Context, logger *zap.Logger, session *sessionEVR, in evr.Message) error {
	request := in.(evr.LobbySessionRequest)

	go func() {

		lobbyParams, err := NewLobbyParametersFromRequest(ctx, logger, p.nk, session, in.(evr.LobbySessionRequest), "node")
		if err != nil {
			logger.Error("Failed to create lobby parameters", zap.Error(err))
			if err := session.SendEvr(LobbySessionFailureFromError(request.GetMode(), request.GetGroupID(), err)); err != nil {
				logger.Error("Failed to send lobby session failure message", zap.Error(err))
			}
			return
		}

		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		ctx = context.WithValue(ctx, ctxLobbyParametersKey{}, lobbyParams)

		if err := p.handleLobbySessionRequest(ctx, logger, session, request, lobbyParams); err != nil {
			if lobbyParams.Verbose {
				session.Send(&rtapi.Envelope{
					Message: &rtapi.Envelope_Error{
						Error: &rtapi.Error{
							Code:    int32(codes.Internal),
							Message: err.Error(),
						},
					},
				}, true)
			}

			if ctx.Err() == context.Canceled || errors.Is(err, context.Canceled) {
				logger.Debug("Lobby session request was canceled (context.Canceled)")
				return
			}
			logger.Error("Failed to process lobby session request", zap.Error(err))

			params, ok := LoadParams(ctx)
			if !ok {
				logger.Error("Failed to load params from context")
			} else {
				params.lastMatchmakingError.Store(err)
			}

			if _, err := p.appBot.LogUserErrorMessage(ctx, lobbyParams.GroupID.String(), fmt.Sprintf("```fix\n%s\n\n%T failed:\n %v\n```", session.Username(), in, err), false); err != nil {
				logger.Warn("Failed to log audit message", zap.Error(err))
			}

			if err := session.SendEvr(LobbySessionFailureFromError(request.GetMode(), request.GetGroupID(), err)); err != nil {
				logger.Error("Failed to send lobby session failure message", zap.Error(err))
			}
			return
		}
	}()
	return nil
}

// lobbyPingResponse is a message responding to a ping request.
func (p *EvrPipeline) lobbyPingResponse(ctx context.Context, logger *zap.Logger, session *sessionEVR, in evr.Message) error {
	response := in.(*evr.LobbyPingResponse)

	var (
		now            = time.Now().UTC()
		expiry         = now.Add(-14 * 24 * time.Hour)
		latencyHistory *LatencyHistory
		limit          = 25
	)

	params, ok := LoadParams(ctx)
	if !ok {
		return fmt.Errorf("failed to load params from context")
	}
	latencyHistory = params.latencyHistory.Load()

	for _, result := range response.Results {
		ip := result.ExternalIP
		if result.ExternalIP.IsUnspecified() {
			ip = result.InternalIP
		}

		latencyHistory.Add(ip, int(result.PingMilliseconds), limit, expiry)
	}

	if err := StorableWrite(ctx, p.nk, session.UserID().String(), latencyHistory); err != nil {
		return status.Errorf(codes.Internal, "failed to write latency history: %v", err)
	}
	return nil
}

func SendEVRMessages(session server.Session, unrequire bool, messages ...evr.Message) error {
	if session == nil {
		return fmt.Errorf("session is nil")
	}

	logger := session.Logger()
	isDebug := logger.Core().Enabled(zap.DebugLevel)
	if isDebug {
		msgnames := make([]string, 0, len(messages))
		for _, msg := range messages {
			msgnames = append(msgnames, fmt.Sprintf("%T", msg))
		}
		logger.Debug("Sending messages.", zap.Any("message", msgnames))
	}
	for _, message := range messages {
		if message == nil {
			continue
		}

		payload, err := evr.Marshal(message)
		if err != nil {
			return fmt.Errorf("could not marshal message: %w", err)
		}

		if err := session.SendBytes(payload, true); err != nil {
			return err
		}
	}
	if unrequire {
		if err := session.SendBytes(unrequireBytes, true); err != nil {
			return err
		}
	}

	return nil
}

func LeavePartyStream(s *sessionEVR) {
	s.tracker.UntrackLocalByModes(s.id, map[uint8]struct{}{server.StreamModeParty: {}}, server.PresenceStream{})
}

// LobbyPendingSessionCancel is a message from the server to the client, indicating that the user wishes to cancel matchmaking.
func (p *EvrPipeline) lobbyPendingSessionCancel(ctx context.Context, logger *zap.Logger, session *sessionEVR, in evr.Message) error {
	// Look up the matching session.

	err := LeaveMatchmakingStream(logger, session)
	if err != nil {
		logger.Warn("Failed to leave lobby group stream", zap.Error(err))
	}
	return nil
}

// lobbyPlayerSessionsRequest is called when a client requests the player sessions for a list of XP IDs.
// Player Sessions are UUIDv5 of the MatchID and EVR-ID
func (p *EvrPipeline) lobbyPlayerSessionsRequest(ctx context.Context, logger *zap.Logger, session *sessionEVR, in evr.Message) error {
	message := in.(*evr.LobbyPlayerSessionsRequest)

	matchID, err := NewMatchID(message.LobbyID, p.node)
	if err != nil {
		return fmt.Errorf("failed to create match ID: %w", err)
	}
	entrantID := NewEntrantID(matchID, message.EvrId)

	presence, err := PresenceByEntrantID(p.nk, matchID, entrantID)
	if err != nil {
		return fmt.Errorf("failed to get lobby presence for entrant `%s`: %w", entrantID.String(), err)
	}

	entrantIDs := make([]uuid.UUID, len(message.PlayerEvrIDs))
	for i, e := range message.PlayerEvrIDs {
		entrantIDs[i] = NewEntrantID(matchID, e)
	}

	entrant := evr.NewLobbyEntrant(message.EvrId, message.LobbyID, entrantID, entrantIDs, int16(presence.RoleAlignment))

	return session.SendEvr(entrant.Version3())
}

package server

import (
	"context"
	"fmt"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
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
func (p *EvrPipeline) lobbyMatchmakerStatusRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	_ = in.(*evr.LobbyMatchmakerStatusRequest)

	// TODO Check if the matchmaking ticket is still open
	err := session.SendEvr(evr.NewLobbyMatchmakerStatusResponse())
	if err != nil {
		return fmt.Errorf("LobbyMatchmakerStatus: %v", err)
	}
	return nil
}

func (p *EvrPipeline) lobbySessionRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(evr.LobbySessionRequest)
	go func() {
		if err := p.handleLobbySessionRequest(ctx, logger, session, request); err != nil {
			logger.Error("Failed to process lobby session request", zap.Error(err))
			session.SendEvr(LobbySessionFailureFromError(request.GetMode(), request.GetGroupID(), err))
		}
	}()
	return nil
}

// lobbyPingResponse is a message responding to a ping request.
func (p *EvrPipeline) lobbyPingResponse(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	response := in.(*evr.LobbyPingResponse)
	results := response.Results
	pipeline := session.pipeline

	latencyHistory, err := LoadLatencyHistory(ctx, logger, pipeline.db, session.userID)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to load latency history: %v", err)
	}

	for _, r := range results {
		if h, ok := latencyHistory[r.GetExternalIP()]; ok {
			h[time.Now().UTC().Unix()] = int(r.PingMilliseconds)
		} else {
			latencyHistory[r.GetExternalIP()] = map[int64]int{time.Now().UTC().Unix(): int(r.PingMilliseconds)}
		}
		// get the
	}

	if err := StoreLatencyHistory(ctx, logger, pipeline.db, session.metrics, session.storageIndex, session.userID, latencyHistory); err != nil {
		return status.Errorf(codes.Internal, "failed to store latency history: %v", err)
	}

	return nil
}

func SendEVRMessages(session Session, messages ...evr.Message) error {
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

	return nil
}

func LeavePartyStream(s *sessionWS) {
	s.tracker.UntrackLocalByModes(s.id, map[uint8]struct{}{StreamModeParty: {}}, PresenceStream{})
}

// LobbyPendingSessionCancel is a message from the server to the client, indicating that the user wishes to cancel matchmaking.
func (p *EvrPipeline) lobbyPendingSessionCancel(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	// Look up the matching session.

	err := LeaveMatchmakingStream(logger, session)
	if err != nil {
		logger.Warn("Failed to leave lobby group stream", zap.Error(err))
	}

	return session.SendEvr(evr.NewSTcpConnectionUnrequireEvent())
}

// lobbyPlayerSessionsRequest is called when a client requests the player sessions for a list of EchoVR IDs.
// Player Sessions are UUIDv5 of the MatchID and EVR-ID
func (p *EvrPipeline) lobbyPlayerSessionsRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	message := in.(*evr.LobbyPlayerSessionsRequest)

	matchID, err := NewMatchID(message.LobbyID, p.node)
	if err != nil {
		return fmt.Errorf("failed to create match ID: %w", err)
	}
	entrantID := NewEntrantID(matchID, message.EvrId)

	presence, err := PresenceByEntrantID(p.runtimeModule, matchID, entrantID)
	if err != nil {
		return fmt.Errorf("failed to get lobby presence for entrant `%s`: %w", entrantID.String(), err)
	}

	entrantIDs := make([]uuid.UUID, len(message.PlayerEvrIDs))
	for i, e := range message.PlayerEvrIDs {
		entrantIDs[i] = NewEntrantID(matchID, e)
	}

	entrant := evr.NewLobbyEntrant(message.EvrId, message.LobbyID, entrantID, entrantIDs, int16(presence.RoleAlignment))

	return session.SendEvr(entrant.VersionU(), entrant.Version2(), entrant.Version3())
}

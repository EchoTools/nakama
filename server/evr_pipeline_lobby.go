package server

import (
	"context"
	"fmt"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
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

	// This cannot have an unrequire message, otherwise the client will hang forever with "MATCHMAKING"
	err := session.SendEvr(evr.NewLobbyMatchmakerStatusResponse())
	if err != nil {
		return fmt.Errorf("LobbyMatchmakerStatus: %w", err)
	}
	return nil
}

func (p *EvrPipeline) lobbySessionRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(evr.LobbySessionRequest)

	/*
		if request.GetVersionLock() != evr.ToSymbol(evr.VersionLockPreFarewell) {
			if err := session.SendEvr(evr.NewLobbySessionFailure(request.GetMode(), request.GetGroupID(), evr.LobbySessionFailure_KickedFromLobbyGroup, fmt.Sprintf("Wrong version (%s). Ask for help in Echo VR Lounge.", request.GetVersionLock().String())).Version4()); err != nil {
				logger.Error("Failed to send lobby session failure message", zap.Error(err))
			}
			discordID := p.discordCache.UserIDToDiscordID(session.userID.String())

			// open channel to mmember
			channel, err := p.appBot.dg.UserChannelCreate(discordID)
			if err != nil {
				return status.Errorf(codes.Internal, "failed to create user channel: %v", err)
			}

			// send message to user
			_, err = p.appBot.dg.ChannelMessageSend(channel.ID, "Hey, you're trying to join a lobby but you're on the wrong version of the game. Please ask for help in <#779349159852769313>.")
			if err != nil {
				return status.Errorf(codes.Internal, "failed to send message to user: %v", err)
			}
			return nil
		}
	*/

	go func() {

		lobbyParams, err := NewLobbyParametersFromRequest(ctx, logger, session, in.(evr.LobbySessionRequest))
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
			logger.Error("Failed to process lobby session request", zap.Error(err))

			if err := p.appBot.LogAuditMessage(ctx, lobbyParams.GroupID.String(), fmt.Sprintf("```fix\n%s\n%T failed:\n %v\n```", session.Username(), in, err), false); err != nil {
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

	return session.SendEvrUnrequire()
}

func SendEVRMessages(session Session, unrequire bool, messages ...evr.Message) error {
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

	return session.SendEvrUnrequire()
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

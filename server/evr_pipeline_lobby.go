package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
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

func (p *EvrPipeline) lobbySessionRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {

	// Start a goroutine to handle the request.
	go func() {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		ctx = context.WithValue(ctx, ctxNodeKey{}, p.node)

		profile, err := p.profileRegistry.Load(ctx, session.userID)
		if err != nil {
			logger.Error("Failed to load profile", zap.Error(err))
			return
		}

		rating := profile.GetRating()
		ctx = context.WithValue(ctx, ctxRatingKey{}, rating)

		// Load the global matchmaking config
		gconfig, err := LoadMatchmakingSettings(ctx, p.runtimeModule, SystemUserID)
		if err != nil {
			logger.Error("Failed to load global matchmaking config", zap.Error(err))
			return
		}

		// Load the user's matchmaking config
		config, err := LoadMatchmakingSettings(ctx, p.runtimeModule, session.UserID().String())
		if err != nil {
			logger.Error("Failed to load matchmaking config", zap.Error(err))
			return
		}

		params := NewLobbyParametersFromRequest(ctx, in.(evr.LobbySessionRequest), gconfig, config)

		ctx = context.WithValue(ctx, ctxLobbyParametersKey{}, params)

		var matchID MatchID
		switch in.(type) {
		case *evr.LobbyFindSessionRequest:
			// Load the next match from the DB. (e.g. EchoTaxi hails)
			matchID, err = p.loadNextMatchFromDB(ctx, logger, session)
			if err != nil {
				err = status.Errorf(codes.Internal, "failed to load next match from DB: %v", err)
			} else if !matchID.IsNil() {
				LeavePartyStream(session)
				// If a match ID is found, join it.
				params.CurrentMatchID = matchID
				err = p.lobbyJoin(ctx, logger, session, params)
			} else if params.Role == evr.TeamSpectator {
				// Leave the party if the user is in one
				LeavePartyStream(session)
				// Spectators are only allowed in arena and combat matches.
				if params.Mode != evr.ModeArenaPublic && params.Mode != evr.ModeCombatPublic {
					err = fmt.Errorf("spectators are only allowed in arena and combat matches")
				} else {
					// Spectators don't matchmake, and they don't have a delay for backfill.
					// Spectators also don't time out.
					err = p.lobbyFindSpectate(ctx, logger, session, params)
				}
			} else {
				// Otherwise, find a match via the matchmaker or backfill.
				// This is also responsible for creation of social lobbies.
				err = p.lobbyFind(ctx, logger, session, params)
				if err != nil {
					// On error, leave any party the user might be a member of.
					LeavePartyStream(session)
				}
			}

		case *evr.LobbyJoinSessionRequest:
			LeavePartyStream(session)
			err = p.lobbyJoin(ctx, logger, session, params)

		case *evr.LobbyCreateSessionRequest:
			LeavePartyStream(session)
			matchID, err = p.lobbyCreate(ctx, logger, session, params)
			if err == nil {
				params.CurrentMatchID = matchID
				err = p.lobbyJoin(ctx, logger, session, params)
			}
		}

		// Return the error to the client.
		if err != nil {
			logger.Error("Failed to process lobby session request", zap.Error(err))
			session.SendEvr(params.ResponseFromError(err))
		}

	}()
	return nil
}

func (p *EvrPipeline) loadNextMatchFromDB(ctx context.Context, logger *zap.Logger, session *sessionWS) (MatchID, error) {

	config, err := LoadMatchmakingSettings(ctx, p.runtimeModule, session.UserID().String())
	if err != nil {
		return MatchID{}, fmt.Errorf("failed to load matchmaking config: %w", err)
	}

	if config.NextMatchID.IsNil() {
		return MatchID{}, nil
	}

	defer func() {
		config.NextMatchID = MatchID{}
		err = StoreMatchmakingSettings(ctx, p.runtimeModule, session.UserID().String(), config)
		if err != nil {
			logger.Warn("Failed to clear matchmaking config", zap.Error(err))
		}
	}()

	return config.NextMatchID, nil
}

// lobbyJoinSessionRequest is a request to join a specific existing session.
func (p *EvrPipeline) lobbyJoin(ctx context.Context, logger *zap.Logger, session *sessionWS, params SessionParameters) error {

	matchID, _ := NewMatchID(params.CurrentMatchID.UUID, p.node)

	label, err := MatchLabelByID(ctx, p.runtimeModule, matchID)
	if err != nil || label == nil {
		return status.Errorf(codes.Internal, "failed to get match label: %v", err)
	}

	presences, err := EntrantPresencesFromSessionIDs(logger, p.sessionRegistry, params.PartyID, params.GroupID, nil, params.Role, session.id)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to create presences: %v", err)
	}

	if err := LobbyJoinEntrants(ctx, logger, p.matchRegistry, p.sessionRegistry, p.tracker, p.profileRegistry, matchID, params.Role, presences); err != nil {
		return status.Errorf(codes.Internal, "failed to join match: %v", err)
	}

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

func (p *EvrPipeline) PrepareLobbyProfile(ctx context.Context, logger *zap.Logger, session *sessionWS, evrID evr.EvrId, userID, groupID string) {
	// prepare the profile ahead of time
	var err error
	displayName, ok := ctx.Value(ctxDisplayNameOverrideKey{}).(string)
	if !ok {
		displayName, err = GetDisplayNameByGroupID(ctx, p.runtimeModule, userID, groupID)
		if err != nil {
			logger.Warn("Failed to set display name.", zap.Error(err))
		}

	}

	if err := p.profileRegistry.SetLobbyProfile(ctx, session.userID, evrID, displayName); err != nil {
		logger.Warn("Failed to set lobby profile", zap.Error(err))
		return
	}
}

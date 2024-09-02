package server

import (
	"context"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

func (p *EvrPipeline) handleLobbySessionRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.LobbySessionRequest) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx = context.WithValue(ctx, ctxNodeKey{}, p.node)

	// Add a cache for guild group metadata
	ctx = context.WithValue(ctx, ctxGuildGroupMetadataCacheKey{}, &MapOf[uuid.UUID, *GroupMetadata]{})

	profile, err := p.profileRegistry.Load(ctx, session.userID)
	if err != nil {
		logger.Error("Failed to load profile", zap.Error(err))
		return NewLobbyErrorf(InternalError, "failed to load user profile")
	}

	rating := profile.GetRating()
	ctx = context.WithValue(ctx, ctxRatingKey{}, rating)

	// Load the global matchmaking config
	gconfig, err := LoadMatchmakingSettings(ctx, p.runtimeModule, SystemUserID)
	if err != nil {
		return NewLobbyErrorf(InternalError, "failed to load global matchmaking settings")
	}

	// Load the user's matchmaking config
	config, err := LoadMatchmakingSettings(ctx, p.runtimeModule, session.UserID().String())
	if err != nil {
		return NewLobbyErrorf(InternalError, "failed to load user matchmaking settings")
	}
	// Load friends to get blocked (ghosted) players
	cursor := ""
	friends := make([]*api.Friend, 0)
	for {
		users, err := ListFriends(ctx, logger, p.db, p.statusRegistry, session.userID, 100, nil, cursor)
		if err != nil {
			return NewLobbyErrorf(InternalError, "failed to list blocked players")
		}

		friends = append(friends, users.Friends...)

		cursor = users.Cursor
		if users.Cursor == "" {
			break
		}
	}

	latencyHistory, err := LoadLatencyHistory(ctx, logger, p.db, session.userID)
	if err != nil {
		return NewLobbyErrorf(InternalError, "Failed to load latency history: %s", err)
	}

	params := NewLobbyParametersFromRequest(ctx, in.(evr.LobbySessionRequest), &gconfig, &config, profile, latencyHistory, friends)

	ctx = context.WithValue(ctx, ctxLobbyParametersKey{}, params)

	var matchID MatchID
	switch in.(type) {
	case *evr.LobbyFindSessionRequest:

		if len(params.RequiredFeatures) > 0 {
			// reject matchmaking with required features
			return NewLobbyErrorf(MissingEntitlement, "required features not supported")
		}

		// Load the next match from the DB. (e.g. EchoTaxi hails)
		matchID, err = p.loadNextMatchFromDB(ctx, logger, session)
		if err != nil {
			return NewLobbyError(InternalError, "failed to load next match from DB")
		} else if !matchID.IsNil() {
			// If a match ID is found, join it.
			params.CurrentMatchID = matchID
			if _, _, err := p.matchRegistry.GetMatch(ctx, matchID.String()); err == nil {
				LeavePartyStream(session)
				p.metrics.CustomCounter("lobby_join_next_match", params.MetricsTags(), 1)
				logger.Info("Joining next match", zap.String("mid", matchID.String()))
				return p.lobbyJoin(ctx, logger, session, params)
			}
		} else if params.Role == evr.TeamSpectator {
			// Leave the party if the user is in one
			LeavePartyStream(session)
			// Spectators are only allowed in arena and combat matches.
			if params.Mode != evr.ModeArenaPublic && params.Mode != evr.ModeCombatPublic {
				err = NewLobbyErrorf(BadRequest, "spectators are only allowed in arena and combat matches")
			} else {
				// Spectators don't matchmake, and they don't have a delay for backfill.
				// Spectators also don't time out.
				p.metrics.CustomCounter("lobby_find_spectate", params.MetricsTags(), 1)
				logger.Info("Finding spectate match")
				return p.lobbyFindSpectate(ctx, logger, session, params)
			}
		} else {
			// Otherwise, find a match via the matchmaker or backfill.
			// This is also responsible for creation of social lobbies.
			p.metrics.CustomCounter("lobby_find_match", params.MetricsTags(), int64(params.PartySize))
			logger.Info("Finding match", zap.String("mode", params.Mode.String()), zap.Any("party_size", params.PartySize))
			err = p.lobbyFind(ctx, logger, session, params)
			if err != nil {
				// On error, leave any party the user might be a member of.
				LeavePartyStream(session)
				return err
			}
		}
		return nil
	case *evr.LobbyJoinSessionRequest:
		LeavePartyStream(session)
		p.metrics.CustomCounter("lobby_create_session", params.MetricsTags(), 1)
		logger.Info("Joining session", zap.String("mid", params.CurrentMatchID.String()))
		return p.lobbyJoin(ctx, logger, session, params)

	case *evr.LobbyCreateSessionRequest:

		if len(params.RequiredFeatures) > 0 {
			// Reject public creation
			if params.Mode == evr.ModeArenaPublic || params.Mode == evr.ModeCombatPublic || params.Mode == evr.ModeSocialPublic {
				return NewLobbyErrorf(MissingEntitlement, "required features not supported")
			}
		}

		LeavePartyStream(session)
		p.metrics.CustomCounter("lobby_create_session", params.MetricsTags(), 1)
		logger.Info("Creating session", zap.String("mode", params.Mode.String()), zap.String("level", params.Level.String()), zap.String("region", params.Region.String()))
		matchID, err = p.lobbyCreate(ctx, logger, session, params)
		if err == nil {
			params.CurrentMatchID = matchID
			return p.lobbyJoin(ctx, logger, session, params)
		} else {
			return err
		}
	}

	return nil
}

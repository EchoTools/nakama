package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

func (p *EvrPipeline) handleLobbySessionRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.LobbySessionRequest, lobbyParams *LobbySessionParameters) error {
	var err error
	var matchID MatchID

	switch in.(type) {
	case *evr.LobbyFindSessionRequest:

		// If a "next match ID" is set, send the user to that match. (i.e. Echo Taxi)
		if !lobbyParams.NextMatchID.IsNil() {
			LeavePartyStream(session)
			p.nk.metrics.CustomCounter("lobby_join_next_match", lobbyParams.MetricsTags(), 1)
			logger.Info("Joining next match", zap.String("mid", matchID.String()))
			return p.lobbyJoin(ctx, logger, session, lobbyParams, lobbyParams.NextMatchID)
		}

		if len(lobbyParams.RequiredFeatures) > 0 {
			// reject matchmaking with required features
			return NewLobbyErrorf(MissingEntitlement, "required features not supported in matchmaking.")
		}

		// Rewrite mode 'echo_arena_public_ai' as 'echo_combat' for PCVR users
		if lobbyParams.Mode == evr.ModeArenaPublicAI {

			params, ok := LoadParams(ctx)
			if ok && params.IsPCVR() {
				lobbyParams.Mode = evr.ModeCombatPublic
				lobbyParams.Level = evr.LevelUnspecified
			} else {
				// Otherwise, respond with a bad request
				return NewLobbyErrorf(BadRequest, "mode `%s` not supported", lobbyParams.Mode.String())
			}
		}

		if lobbyParams.Role == evr.TeamSpectator {
			// Leave the party if the user is in one
			LeavePartyStream(session)
			// Spectators are only allowed in arena and combat matches.
			if lobbyParams.Mode != evr.ModeArenaPublic && lobbyParams.Mode != evr.ModeCombatPublic {
				err = NewLobbyErrorf(BadRequest, "spectators are only allowed in arena and combat matches")
			} else {
				// Spectators don't matchmake, and they don't have a delay for backfill.
				// Spectators also don't timeout.
				p.nk.metrics.CustomCounter("lobby_find_spectate", lobbyParams.MetricsTags(), 1)
				logger.Info("Finding spectate match")
				return p.lobbyFindSpectate(ctx, logger, session, lobbyParams)
			}
		} else {
			// Otherwise, find a match via the matchmaker or backfill.
			// This is also responsible for creation of social lobbies.

			err = p.lobbyFind(ctx, logger, session, lobbyParams)
			if err == nil {
				return nil
			}
			code := InternalError
			if ctx.Err() == context.Canceled || errors.Is(err, context.Canceled) {
				return nil
			} else if errors.Is(err, context.DeadlineExceeded) {
				// Check the context to see if it was canceled or timed out
				err = ctx.Err()
			}
			if errors.Is(err, ErrMatchmakingTimeout) {
				logger.Warn("Matchmaking timed out", zap.String("mode", lobbyParams.Mode.String()), zap.Error(err))
				err = NewLobbyError(Timeout, "matchmaking timed out")
			} else {
				lobbyErr := &LobbyError{}
				if errors.As(err, &lobbyErr) {
					code = lobbyErr.code
				} else {
					logger.Warn("Unexpected error while finding match", zap.Error(err))
					code = InternalError
				}

				tags := lobbyParams.MetricsTags()
				tags["error_code"] = strconv.Itoa(int(code))
				tags["error_str"] = code.String()

				p.nk.metrics.CustomCounter("lobby_find_match_error", tags, int64(lobbyParams.GetPartySize()))
				// On error, leave any party the user might be a member of.
				LeavePartyStream(session)
			}
			return err
		}
		return nil

	case *evr.LobbyJoinSessionRequest:
		LeavePartyStream(session)
		p.nk.metrics.CustomCounter("lobby_join_session", lobbyParams.MetricsTags(), 1)
		logger.Info("Joining session", zap.String("mid", lobbyParams.CurrentMatchID.String()), zap.String("role", TeamIndex(lobbyParams.Role).String()))

		return p.lobbyJoin(ctx, logger, session, lobbyParams, lobbyParams.CurrentMatchID)

	case *evr.LobbyCreateSessionRequest:

		if len(lobbyParams.RequiredFeatures) > 0 {
			// Reject public creation
			if lobbyParams.Mode == evr.ModeArenaPublic || lobbyParams.Mode == evr.ModeCombatPublic || lobbyParams.Mode == evr.ModeSocialPublic {
				return NewLobbyErrorf(MissingEntitlement, "required features not supported")
			}
		}

		LeavePartyStream(session)
		p.nk.metrics.CustomCounter("lobby_create_session", lobbyParams.MetricsTags(), 1)
		logger.Info("Creating session", zap.String("mode", lobbyParams.Mode.String()), zap.String("level", lobbyParams.Level.String()), zap.String("region", lobbyParams.RegionCode))
		matchID, err = p.lobbyCreate(ctx, logger, session, lobbyParams)
		if err == nil {
			return p.lobbyJoin(ctx, logger, session, lobbyParams, matchID)
		} else {
			return err
		}
	}

	return nil
}

func LobbyPrepareSession(ctx context.Context, nk runtime.NakamaModule, matchID MatchID, settings *MatchSettings) (*MatchLabel, error) {

	response, err := SignalMatch(ctx, nk, matchID, SignalPrepareSession, settings)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare session: %w", err)
	}

	label := &MatchLabel{}
	if err := json.Unmarshal([]byte(response), label); err != nil {
		return nil, fmt.Errorf("failed to unmarshal match label: %w", err)
	}

	return label, nil
}

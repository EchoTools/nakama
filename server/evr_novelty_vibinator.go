package server

// vibinatorsGravity is a temporary novelty feature that redirects PCVR players who are
// matchmaking for Echo Arena (via social lobby) toward vibinator's Echo Combat activity.
//
// When the feature toggle is enabled and all preconditions are met, this function either:
//   - Redirects the player's matchmaking mode to echo_combat (if vibinator is matchmaking for combat), or
//   - Immediately joins the player/party to vibinator's in-progress echo_combat match (if there is room
//     for the entire party on one team).
//
// If neither condition applies, or if any precondition fails, the function is a no-op.

import (
"context"
	"encoding/json"
	"fmt"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

const (
	// vibinatorsUserID is the Nakama user ID for the target player "vibinator".
	vibinatorsUserID = "9defb5bd-4760-40ba-8917-c333caed7fd2"
)

// vibinatorsGravityAction describes the outcome of a vibinatorsGravityCheck call.
type vibinatorsGravityAction int

const (
	vibinatorsGravityNoOp         vibinatorsGravityAction = iota // nothing to do
	vibinatorsGravityRedirectMode                                // switch matchmaking mode to echo_combat
	vibinatorsGravityJoinMatch                                   // join vibinator's current match directly
)

// vibinatorsGravityCheck evaluates whether the "vibinator's gravity" novelty feature should
// redirect the requesting player. Returns the action to take and (for JoinMatch) the target label.
//
// Preconditions that must ALL be true for any action to occur:
//  1. The global feature toggle is enabled.
//  2. The player is PCVR (not standalone).
//  3. The player is not a spectator.
//  4. The player is currently in a social lobby (lobbyParams.Mode == ModeSocialPublic).
//  5. The player is matchmaking for echo_arena (social lobby find IS the echo_arena matchmaking path).
func vibinatorsGravityCheck(
	ctx context.Context,
	logger *zap.Logger,
	p *EvrPipeline,
	session *sessionWS,
	lobbyParams *LobbySessionParameters,
	entrants []*EvrMatchPresence,
) (vibinatorsGravityAction, *MatchLabel, error) {

	// --- Precondition 1: feature toggle ---
	settings := ServiceSettings()
	if settings == nil || !settings.EnableVibinatorsGravity {
		return vibinatorsGravityNoOp, nil, nil
	}

	// --- Precondition 2: PCVR only ---
	sessionParams, ok := LoadParams(session.Context())
	if !ok || !sessionParams.IsPCVR() {
		return vibinatorsGravityNoOp, nil, nil
	}

	// --- Precondition 3: not a spectator ---
	if lobbyParams.Role == evr.TeamSpectator {
		return vibinatorsGravityNoOp, nil, nil
	}

	// --- Precondition 4 & 5: social lobby mode (which IS the echo_arena matchmaking path) ---
	if lobbyParams.Mode != evr.ModeSocialPublic {
		return vibinatorsGravityNoOp, nil, nil
	}

	// --- Check vibinator's state ---

	// First: is vibinator currently in an echo_combat match?
	label, err := vibinatorsCurrentCombatMatch(ctx, p, logger)
	if err != nil {
		logger.Warn("vibinatorsGravity: failed to check vibinator's match state", zap.Error(err))
		// Non-fatal; fall through to matchmaking check.
	}

	if label != nil {
		// Vibinator is in an echo_combat match. Check whether the party can fit on one team.
		partySize := len(entrants)
		fitsBlue, _ := label.OpenSlotsByRole(evr.TeamBlue)
		fitsOrange, _ := label.OpenSlotsByRole(evr.TeamOrange)
		if fitsBlue >= partySize || fitsOrange >= partySize {
			logger.Info("vibinatorsGravity: redirecting party to vibinator's match",
				zap.String("match_id", label.ID.String()),
				zap.Int("party_size", partySize),
				zap.Int("open_blue", fitsBlue),
				zap.Int("open_orange", fitsOrange),
			)
			return vibinatorsGravityJoinMatch, label, nil
		}
		// Match exists but is too full for the party — no action.
		return vibinatorsGravityNoOp, nil, nil
	}

	// Second: is vibinator matchmaking for echo_combat?
	matchmaking, err := vibinatorsIsMatchmakingForCombat(ctx, p, logger)
	if err != nil {
		logger.Warn("vibinatorsGravity: failed to check vibinator's matchmaking state", zap.Error(err))
		return vibinatorsGravityNoOp, nil, nil
	}

	if matchmaking {
		logger.Info("vibinatorsGravity: redirecting player matchmaking mode to echo_combat")
		return vibinatorsGravityRedirectMode, nil, nil
	}

	return vibinatorsGravityNoOp, nil, nil
}

// vibinatorsCurrentCombatMatch returns the MatchLabel for vibinator's current echo_combat match,
// or nil if vibinator is not in one.
func vibinatorsCurrentCombatMatch(ctx context.Context, p *EvrPipeline, logger *zap.Logger) (*MatchLabel, error) {
	presences, err := p.nk.StreamUserList(StreamModeService, vibinatorsUserID, "", StreamLabelMatchService, false, true)
	if err != nil {
		return nil, fmt.Errorf("stream list: %w", err)
	}

	for _, presence := range presences {
		matchID := MatchIDFromStringOrNil(presence.GetStatus())
		if matchID.IsNil() {
			continue
		}

		label, err := MatchLabelByID(ctx, p.nk, matchID)
		if err != nil || label == nil {
			continue
		}

		if label.Mode != evr.ModeCombatPublic {
			continue
		}

		return label, nil
	}

	return nil, nil
}

// vibinatorsIsMatchmakingForCombat returns true if vibinator currently has an active
// matchmaking stream presence for echo_combat.
//
// Strategy: find vibinator's login session, load their LobbySessionParameters from context,
// then check the matchmaking stream for that GroupID to confirm they are queued for combat.
func vibinatorsIsMatchmakingForCombat(ctx context.Context, p *EvrPipeline, logger *zap.Logger) (bool, error) {
	// Get vibinator's active login session presence.
	loginPresences, err := p.nk.StreamUserList(StreamModeService, vibinatorsUserID, "", StreamLabelLoginService, false, true)
	if err != nil {
		return false, fmt.Errorf("login stream list: %w", err)
	}

	for _, loginPresence := range loginPresences {
		if loginPresence.GetUserId() != vibinatorsUserID {
			continue
		}

		sessionID := loginPresence.GetSessionId()
		if sessionID == "" {
			continue
		}

		// Look up the actual session to read its context parameters.
		vibinatorsSession := p.nk.sessionRegistry.Get(parseUUID(sessionID))
		if vibinatorsSession == nil {
			continue
		}

		vibinatorsParams, found := LoadParams(vibinatorsSession.Context())
		if !found {
			continue
		}

		// Check the matchmaking stream for this GroupID for vibinator's presence.
		groupID := vibinatorsParams.profile.GetActiveGroupID().String()
		mmPresences, err := p.nk.StreamUserList(StreamModeMatchmaking, groupID, "", "", false, true)
		if err != nil {
			logger.Warn("vibinatorsGravity: failed to list matchmaking stream", zap.Error(err))
			continue
		}

		for _, mmPresence := range mmPresences {
			if mmPresence.GetUserId() != vibinatorsUserID {
				continue
			}

			// Parse the matchmaking stream data to check the mode.
			var mmData MatchmakingStreamData
			if err := json.Unmarshal([]byte(mmPresence.GetStatus()), &mmData); err != nil {
				logger.Warn("vibinatorsGravity: failed to unmarshal matchmaking data", zap.Error(err))
				continue
			}

			if mmData.Parameters != nil && mmData.Parameters.Mode == evr.ModeCombatPublic {
				return true, nil
			}
		}
	}

	return false, nil
}

// parseUUID is a helper to parse a UUID string, returning the zero value on failure.
func parseUUID(s string) uuid.UUID {
	id, _ := uuid.FromString(s)
	return id
}

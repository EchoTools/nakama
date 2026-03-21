package server

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

const (
	// SuspensionDisconnectDelay is the delay before forcibly disconnecting
	// a suspended player's sessions. This gives time for unrequire messages
	// and match kick signals to be processed by the client.
	SuspensionDisconnectDelay = 60 * time.Second

	errCompoundDurationWithDW = "compound durations with 'd' (days) or 'w' (weeks) are not supported; use simple format like '2d' or convert to hours (e.g., '48h' instead of '2d')"
)

// SuspendConnectedUser enforces a suspension on a connected player.
// It is non-blocking: all slow work (match kick, delayed disconnect) runs in goroutines.
//
// Steps performed:
//  1. Mark all of the user's sessions as suspended (atomic flag on SessionParameters).
//     The pipeline will ignore all subsequent messages and log an audit entry.
//  2. Send an unrequire message to every login and match session.
//  3. Kick the player from their current match (sends a reject to the game server).
//  4. After SuspensionDisconnectDelay, forcibly disconnect all login and match sessions.
func SuspendConnectedUser(
	ctx context.Context,
	logger *zap.Logger,
	nk runtime.NakamaModule,
	sessionRegistry SessionRegistry,
	userID string,
) {
	userUUID := uuid.FromStringOrNil(userID)
	if userUUID.IsNil() {
		logger.Warn("SuspendConnectedUser called with invalid user ID", zap.String("user_id", userID))
		return
	}

	// Collect all EVR sessions belonging to this user.
	var sessions []*sessionWS
	sessionRegistry.Range(func(session Session) bool {
		if session.UserID() == userUUID && session.Format() == SessionFormatEVR {
			if ws, ok := session.(*sessionWS); ok {
				sessions = append(sessions, ws)
			}
		}
		return true
	})

	if len(sessions) == 0 {
		logger.Debug("SuspendConnectedUser: no active sessions found",
			zap.String("user_id", userID))
		return
	}

	// Step 1: Mark every session as suspended so the pipeline ignores future messages.
	for _, ws := range sessions {
		if params, ok := LoadParams(ws.Context()); ok {
			if params.suspended != nil {
				params.suspended.Store(true)
			}
		}
	}

	logger.Info("Marked user sessions as suspended",
		zap.String("user_id", userID),
		zap.Int("session_count", len(sessions)))

	// Step 2: Send unrequire to all sessions (non-blocking per session).
	for _, ws := range sessions {
		if err := ws.SendEvr(unrequireMessage); err != nil {
			logger.Debug("Failed to send unrequire to suspended session",
				zap.String("user_id", userID),
				zap.String("session_id", ws.ID().String()),
				zap.Error(err))
		}
	}

	// Step 3: Kick from match (if in one). Fire-and-forget goroutine.
	go func() {
		for _, ws := range sessions {
			matchID, _, err := GetMatchIDBySessionID(nk, ws.ID())
			if err != nil || matchID.IsNil() {
				continue
			}
			if err := KickPlayerFromMatch(ctx, nk, matchID, userID); err != nil {
				logger.Warn("Failed to kick suspended user from match",
					zap.String("user_id", userID),
					zap.String("match_id", matchID.String()),
					zap.Error(err))
			} else {
				logger.Info("Kicked suspended user from match",
					zap.String("user_id", userID),
					zap.String("match_id", matchID.String()))
			}
			break // Only need to kick from one match
		}
	}()

	// Step 4: Delayed disconnect of all sessions.
	go func() {
		time.Sleep(SuspensionDisconnectDelay)
		for _, ws := range sessions {
			sid := ws.ID()
			if err := nk.SessionDisconnect(context.Background(), sid.String(), runtime.PresenceReasonDisconnect); err != nil {
				logger.Debug("Failed to disconnect suspended session (may already be gone)",
					zap.String("user_id", userID),
					zap.String("session_id", sid.String()),
					zap.Error(err))
			}
		}
		logger.Info("Disconnected suspended user sessions",
			zap.String("user_id", userID),
			zap.Int("session_count", len(sessions)))
	}()
}

// GetMatchIDBySessionID looks up the current match for a session via the service stream.
func GetMatchIDBySessionID(nk runtime.NakamaModule, sessionID uuid.UUID) (matchID MatchID, presence runtime.Presence, err error) {

	presences, err := nk.StreamUserList(StreamModeService, sessionID.String(), "", StreamLabelMatchService, false, true)
	if err != nil {
		return MatchID{}, nil, fmt.Errorf("failed to get stream presences: %w", err)
	}
	if len(presences) == 0 {
		return MatchID{}, nil, ErrMatchNotFound
	}
	presence = presences[0]
	matchID = MatchIDFromStringOrNil(presences[0].GetStatus())
	if !matchID.IsNil() {
		// Verify that the user is actually in the match
		if meta, err := nk.StreamUserGet(StreamModeMatchAuthoritative, matchID.UUID.String(), "", matchID.Node, presence.GetUserId(), presence.GetSessionId()); err != nil || meta == nil {
			return MatchID{}, nil, ErrMatchNotFound
		}
		return matchID, presence, nil
	}

	return MatchID{}, nil, ErrMatchNotFound
}

// KickPlayerFromMatch signals a match to kick a specific player by user ID.
func KickPlayerFromMatch(ctx context.Context, nk runtime.NakamaModule, matchID MatchID, userID string) error {
	label, err := MatchLabelByID(ctx, nk, matchID)
	if err != nil {
		return fmt.Errorf("failed to get match label: %w", err)
	}

	presences, err := nk.StreamUserList(StreamModeMatchAuthoritative, matchID.UUID.String(), "", matchID.Node, false, true)
	if err != nil {
		return fmt.Errorf("failed to get stream presences: %w", err)
	}

	for _, presence := range presences {
		if presence.GetUserId() != userID {
			continue
		}
		if presence.GetSessionId() == label.GameServer.SessionID.String() {
			// Do not kick the game server
			continue
		}

		signal := SignalKickEntrantsPayload{
			UserIDs: []uuid.UUID{uuid.FromStringOrNil(userID)},
		}

		data := NewSignalEnvelope(userID, SignalKickEntrants, signal).String()

		// Signal the match to kick the entrants
		if _, err := nk.MatchSignal(ctx, matchID.String(), data); err != nil {
			return fmt.Errorf("failed to signal match: %w", err)
		}
	}

	return nil
}

// DisconnectUserID kicks a user from their match (optionally) and disconnects their sessions.
func DisconnectUserID(ctx context.Context, nk runtime.NakamaModule, userID string, kickFirst bool, includeLogin bool, includeGameserver bool) (int, error) {

	if kickFirst {
		// Kick the user from any matches they are in
		if matchID, _, err := GetMatchIDBySessionID(nk, uuid.FromStringOrNil(userID)); err == nil && !matchID.IsNil() {
			if err := KickPlayerFromMatch(ctx, nk, matchID, userID); err != nil {
				return 0, fmt.Errorf("failed to kick player from match: %w", err)
			}
		}
	}

	// Get the user's presences
	labels := []string{StreamLabelMatchService}
	if includeLogin {
		labels = append(labels, StreamLabelLoginService)
	} else if includeGameserver {
		labels = append(labels, StreamLabelGameServerService)
	}

	cnt := 0
	for _, l := range labels {

		presences, err := nk.StreamUserList(StreamModeService, userID, "", l, false, true)
		if err != nil {
			return 0, fmt.Errorf("failed to get stream presences: %w", err)
		}

		for _, presence := range presences {

			// Add a delay to allow the match to process the kick
			go func() {
				if kickFirst {
					<-time.After(5 * time.Second)
				}
				if err := nk.SessionDisconnect(ctx, presence.GetSessionId(), runtime.PresenceReasonDisconnect); err != nil {
					// Ignore the error
					return
				}
			}()

			cnt++
		}
	}
	return cnt, nil
}

// parseSuspensionDuration parses a duration string for suspension/ban durations.
// Supports formats like: "15m", "2h", "7d", "1w", "2h30m", "1h30m45s"
// If no unit is specified (e.g., "15"), defaults to minutes.
// Returns the parsed duration or an error if the format is invalid.
func parseSuspensionDuration(inputDuration string) (time.Duration, error) {
	duration := strings.TrimSpace(inputDuration)

	if duration == "" {
		return 0, nil
	}

	if duration == "0" {
		// Zero duration means void existing suspension
		return 0, nil
	}

	// Check for compound durations with unsupported units (d or w)
	hasD := strings.Contains(duration, "d")
	hasW := strings.Contains(duration, "w")
	hasOtherUnits := strings.ContainsAny(duration, "mhs")

	if hasD && hasW {
		return 0, fmt.Errorf(errCompoundDurationWithDW)
	}

	if (hasD || hasW) && hasOtherUnits {
		return 0, fmt.Errorf(errCompoundDurationWithDW)
	}

	// Try parsing with Go's time.ParseDuration first for compound durations (e.g., "2h25m")
	if parsedDuration, err := time.ParseDuration(duration); err == nil {
		if parsedDuration < 0 {
			return 0, fmt.Errorf("duration must be positive, got: %v", parsedDuration)
		}
		return parsedDuration, nil
	}

	// Fallback to custom parsing for simple durations with d/w units
	if len(duration) == 0 {
		return 0, fmt.Errorf("invalid duration format: empty string")
	}

	var unit time.Duration
	var numStr string
	lastChar := duration[len(duration)-1]

	switch lastChar {
	case 'm':
		unit = time.Minute
		numStr = duration[:len(duration)-1]
	case 'h':
		unit = time.Hour
		numStr = duration[:len(duration)-1]
	case 'd':
		unit = 24 * time.Hour
		numStr = duration[:len(duration)-1]
	case 'w':
		unit = 7 * 24 * time.Hour
		numStr = duration[:len(duration)-1]
	default:
		// No unit specified, default to minutes
		unit = time.Minute
		numStr = duration
	}

	durationVal, err := strconv.Atoi(numStr)
	if err != nil {
		return 0, fmt.Errorf("invalid duration format: %w", err)
	}

	if durationVal < 0 {
		return 0, fmt.Errorf("duration must be positive, got: %d", durationVal)
	}

	return time.Duration(durationVal) * unit, nil
}

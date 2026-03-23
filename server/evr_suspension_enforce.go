package server

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

const (
	errCompoundDurationWithDW = "compound durations with 'd' (days) or 'w' (weeks) are not supported; use simple format like '2d' or convert to hours (e.g., '48h' instead of '2d')"
)

// EnforceGuildSuspension enforces a guild suspension on a connected player.
// It is non-blocking: the match kick runs in a goroutine.
//
// Only matches belonging to one of the specified guildGroupIDs are affected.
// Sessions and server connections are never disconnected.
func EnforceGuildSuspension(
	ctx context.Context,
	logger *zap.Logger,
	nk runtime.NakamaModule,
	sessionRegistry SessionRegistry,
	userID string,
	guildGroupIDs []string,
) {
	userUUID := uuid.FromStringOrNil(userID)
	if userUUID.IsNil() {
		logger.Warn("EnforceGuildSuspension called with invalid user ID", zap.String("user_id", userID))
		return
	}

	if len(guildGroupIDs) == 0 {
		logger.Warn("EnforceGuildSuspension called with no guild group IDs", zap.String("user_id", userID))
		return
	}

	guildSet := make(map[string]bool, len(guildGroupIDs))
	for _, id := range guildGroupIDs {
		guildSet[id] = true
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
		logger.Debug("EnforceGuildSuspension: no active sessions found",
			zap.String("user_id", userID))
		return
	}

	logger.Info("Enforcing guild suspension — kicking from guild matches",
		zap.String("user_id", userID),
		zap.Strings("guild_group_ids", guildGroupIDs),
		zap.Int("session_count", len(sessions)))

	// Kick from matches that belong to the suspended guild(s). Fire-and-forget goroutine.
	go func() {
		kicked := make(map[string]bool)
		for _, ws := range sessions {
			matchID, _, err := GetMatchIDBySessionID(nk, ws.ID())
			if err != nil || matchID.IsNil() {
				continue
			}
			mid := matchID.String()
			if kicked[mid] {
				continue
			}

			// Check if this match belongs to one of the suspended guilds.
			label, err := MatchLabelByID(ctx, nk, matchID)
			if err != nil || label == nil {
				continue
			}
			if !guildSet[label.GetGroupID().String()] {
				continue
			}

			kicked[mid] = true

			if err := KickPlayerFromMatch(ctx, nk, matchID, userID); err != nil {
				logger.Warn("Failed to kick suspended user from match",
					zap.String("user_id", userID),
					zap.String("match_id", mid),
					zap.Error(err))
			} else {
				logger.Info("Kicked suspended user from guild match",
					zap.String("user_id", userID),
					zap.String("match_id", mid),
					zap.String("match_group_id", label.GetGroupID().String()))
			}
		}
	}()
}

// SendLobbyStatusNotify sends a LobbyStatusNotifyv2 message to a player session.
// The message is displayed in the game UI via the lobby status script expression.
func SendLobbyStatusNotify(session *sessionWS, channel uuid.UUID, message string, expiry time.Time, reason evr.StatusUpdateReason) {
	if session == nil {
		return
	}
	if expiry.IsZero() {
		expiry = time.Now().Add(24 * time.Hour)
	}
	msg := evr.NewLobbyStatusNotifyv2(channel, message, expiry, reason)
	if err := session.SendEvr(msg); err != nil {
		session.logger.Warn("Failed to send LobbyStatusNotify",
			zap.String("channel", channel.String()),
			zap.Error(err))
	}
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

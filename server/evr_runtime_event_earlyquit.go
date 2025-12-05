package server

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

var _ = Event(&EventEarlyQuit{})
var _ = Event(&EventMatchCompleted{})

// EventEarlyQuit is triggered when a player leaves a match early.
// This event is responsible for:
// - Incrementing the player's early quit penalty level
// - Updating the player's matchmaking tier
// - Recording the early quit to the leaderboard statistics
// - Sending Discord notifications on tier changes
type EventEarlyQuit struct {
	UserID      string     `json:"user_id"`
	SessionID   string     `json:"session_id"`
	MatchID     MatchID    `json:"match_id"`
	DisplayName string     `json:"display_name"`
	GroupID     string     `json:"group_id"`
	Mode        evr.Symbol `json:"mode"`
	DiscordID   string     `json:"discord_id,omitempty"`
}

// NewEventEarlyQuit creates a new EventEarlyQuit event.
func NewEventEarlyQuit(userID, sessionID string, matchID MatchID, displayName, groupID string, mode evr.Symbol) *EventEarlyQuit {
	return &EventEarlyQuit{
		UserID:      userID,
		SessionID:   sessionID,
		MatchID:     matchID,
		DisplayName: displayName,
		GroupID:     groupID,
		Mode:        mode,
	}
}

func (e *EventEarlyQuit) Process(ctx context.Context, logger runtime.Logger, dispatcher *EventDispatcher) error {
	var (
		nk              = dispatcher.nk
		db              = dispatcher.db
		sessionRegistry = dispatcher.sessionRegistry
	)

	logger = logger.WithFields(map[string]any{
		"event":        "early_quit",
		"user_id":      e.UserID,
		"session_id":   e.SessionID,
		"match_id":     e.MatchID.String(),
		"display_name": e.DisplayName,
		"group_id":     e.GroupID,
		"mode":         e.Mode.String(),
	})

	logger.Debug("Processing early quit event")

	// Record the early quit to leaderboard statistics
	if err := AccumulateLeaderboardStat(ctx, nk, e.UserID, e.DisplayName, e.GroupID, e.Mode, EarlyQuitStatisticID, 1); err != nil {
		logger.WithField("error", err).Warn("Failed to record early quit to leaderboard")
	}

	// Update the early quit config
	if err := processEarlyQuitIncrement(ctx, logger, nk, db, sessionRegistry, e.UserID, e.SessionID, e.MatchID); err != nil {
		logger.WithField("error", err).Warn("Failed to process early quit increment")
		return err
	}

	// Add metrics
	tags := map[string]string{
		"group_id": e.GroupID,
		"mode":     e.Mode.String(),
	}
	nk.MetricsCounterAdd("match_entrant_early_quit", tags, 1)

	return nil
}

// processEarlyQuitIncrement handles the core logic for incrementing early quit counters.
func processEarlyQuitIncrement(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, db *sql.DB, sessionRegistry SessionRegistry, userID, sessionID string, matchID MatchID) error {
	eqconfig := NewEarlyQuitConfig()

	if err := StorableRead(ctx, nk, userID, eqconfig, true); err != nil {
		logger.WithField("error", err).Warn("Failed to load early quitter config")
	}

	eqconfig.IncrementEarlyQuit()
	eqconfig.LastEarlyQuitMatchID = matchID

	// Check for tier change after early quit
	serviceSettings := ServiceSettings()
	oldTier, newTier, tierChanged := eqconfig.UpdateTier(serviceSettings.Matchmaking.EarlyQuitTier1Threshold)

	logger.WithFields(map[string]any{
		"old_tier":          oldTier,
		"new_tier":          newTier,
		"tier_changed":      tierChanged,
		"penalty_level":     eqconfig.EarlyQuitPenaltyLevel,
		"total_early_quits": eqconfig.TotalEarlyQuits,
	}).Debug("Early quitter tier update")

	if err := StorableWrite(ctx, nk, userID, eqconfig); err != nil {
		return fmt.Errorf("failed to write early quitter config: %w", err)
	}

	// Update session cache
	if playerSession := sessionRegistry.Get(uuid.FromStringOrNil(sessionID)); playerSession != nil {
		if params, ok := LoadParams(playerSession.Context()); ok {
			params.earlyQuitConfig.Store(eqconfig)
		}
	}

	// Send Discord DM if tier changed
	if tierChanged {
		sendTierChangeNotification(ctx, logger, db, userID, oldTier, newTier)
	}

	return nil
}

// EventMatchCompleted is triggered when a player completes a match successfully.
// This event is responsible for:
// - Decrementing the player's early quit penalty level
// - Updating the player's matchmaking tier
// - Sending Discord notifications on tier changes
type EventMatchCompleted struct {
	UserID      string     `json:"user_id"`
	SessionID   string     `json:"session_id"`
	MatchID     MatchID    `json:"match_id"`
	DisplayName string     `json:"display_name"`
	GroupID     string     `json:"group_id"`
	Mode        evr.Symbol `json:"mode"`
}

// NewEventMatchCompleted creates a new EventMatchCompleted event.
func NewEventMatchCompleted(userID, sessionID string, matchID MatchID, displayName, groupID string, mode evr.Symbol) *EventMatchCompleted {
	return &EventMatchCompleted{
		UserID:      userID,
		SessionID:   sessionID,
		MatchID:     matchID,
		DisplayName: displayName,
		GroupID:     groupID,
		Mode:        mode,
	}
}

func (e *EventMatchCompleted) Process(ctx context.Context, logger runtime.Logger, dispatcher *EventDispatcher) error {
	var (
		nk              = dispatcher.nk
		db              = dispatcher.db
		sessionRegistry = dispatcher.sessionRegistry
	)

	logger = logger.WithFields(map[string]any{
		"event":        "match_completed",
		"user_id":      e.UserID,
		"session_id":   e.SessionID,
		"match_id":     e.MatchID.String(),
		"display_name": e.DisplayName,
		"group_id":     e.GroupID,
		"mode":         e.Mode.String(),
	})

	logger.Debug("Processing match completed event")

	// Update the early quit config - decrement penalty level
	if err := processMatchCompletedDecrement(ctx, logger, nk, db, sessionRegistry, e.UserID, e.SessionID); err != nil {
		logger.WithField("error", err).Warn("Failed to process match completed decrement")
		return err
	}

	return nil
}

// processMatchCompletedDecrement handles the core logic for decrementing early quit penalties after match completion.
func processMatchCompletedDecrement(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, db *sql.DB, sessionRegistry SessionRegistry, userID, sessionID string) error {
	eqconfig := NewEarlyQuitConfig()

	if err := StorableRead(ctx, nk, userID, eqconfig, true); err != nil {
		logger.WithField("error", err).Warn("Failed to load early quitter config")
	}

	eqconfig.IncrementCompletedMatches()

	// Check for tier change after completing match
	serviceSettings := ServiceSettings()
	oldTier, newTier, tierChanged := eqconfig.UpdateTier(serviceSettings.Matchmaking.EarlyQuitTier1Threshold)

	logger.WithFields(map[string]any{
		"old_tier":                oldTier,
		"new_tier":                newTier,
		"tier_changed":            tierChanged,
		"penalty_level":           eqconfig.EarlyQuitPenaltyLevel,
		"total_completed_matches": eqconfig.TotalCompletedMatches,
	}).Debug("Match completed tier update")

	if err := StorableWrite(ctx, nk, userID, eqconfig); err != nil {
		return fmt.Errorf("failed to store early quitter config: %w", err)
	}

	// Update session cache
	if playerSession := sessionRegistry.Get(uuid.FromStringOrNil(sessionID)); playerSession != nil {
		if params, ok := LoadParams(playerSession.Context()); ok {
			params.earlyQuitConfig.Store(eqconfig)
		}
	}

	// Send Discord DM if tier changed
	if tierChanged {
		sendTierChangeNotification(ctx, logger, db, userID, oldTier, newTier)
	}

	return nil
}

// sendTierChangeNotification sends a Discord DM to the user when their matchmaking tier changes.
func sendTierChangeNotification(ctx context.Context, logger runtime.Logger, db *sql.DB, userID string, oldTier, newTier int32) {
	discordID, err := GetDiscordIDByUserID(ctx, db, userID)
	if err != nil {
		logger.WithField("error", err).Warn("Failed to get Discord ID for tier notification")
		return
	}

	appBot := globalAppBot.Load()
	if appBot == nil || appBot.dg == nil {
		return
	}

	var message string
	if oldTier < newTier {
		// Degraded to Tier 2+
		message = TierDegradedMessage
	} else {
		// Recovered to Tier 1
		message = TierRestoredMessage
	}

	if _, err := SendUserMessage(ctx, appBot.dg, discordID, message); err != nil {
		logger.WithField("error", err).Warn("Failed to send tier change DM")
	}
}

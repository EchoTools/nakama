package server

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

var _ = Event(&EventServerProfileUpdate{})

type EventServerProfileUpdate struct {
	UserID      string                  `json:"user_id"`
	SessionID   string                  `json:"session_id"`
	GroupID     string                  `json:"group_id"`
	DisplayName string                  `json:"display_name"`
	Mode        evr.Symbol              `json:"mode"`
	Update      evr.ServerProfileUpdate `json:"update"`
	MatchLabel  *MatchLabel             `json:"match_label,omitempty"`
	BlueWins    bool                    `json:"blue_wins"`
}

func NewEventServerProfileUpdate(userID, sessionID, groupID, displayName string, mode evr.Symbol, update evr.ServerProfileUpdate, matchLabel *MatchLabel, blueWins bool) *EventServerProfileUpdate {
	return &EventServerProfileUpdate{
		UserID:      userID,
		SessionID:   sessionID,
		GroupID:     groupID,
		DisplayName: displayName,
		Mode:        mode,
		Update:      update,
		MatchLabel:  matchLabel,
		BlueWins:    blueWins,
	}
}

func (s *EventServerProfileUpdate) Process(ctx context.Context, logger runtime.Logger, dispatcher *EventDispatcher) error {
	var (
		db              = dispatcher.db
		nk              = dispatcher.nk
		sessionRegistry = dispatcher.sessionRegistry
		statisticsQueue = dispatcher.statisticsQueue
	)

	// Increment completed matches for the player
	if err := incrementCompletedMatches(ctx, logger, nk, sessionRegistry, s.UserID, s.SessionID); err != nil {
		logger.WithField("error", err).Warn("Failed to increment completed matches")
	}

	// Update matchmaking ratings and rank percentile if applicable
	serviceSettings := ServiceSettings()
	validModes := []evr.Symbol{evr.ModeArenaPublic, evr.ModeCombatPublic}

	if serviceSettings.UseSkillBasedMatchmaking() && slices.Contains(validModes, s.Mode) && s.MatchLabel != nil {
		// Calculate and store new player ratings
		ratings := CalculateNewPlayerRatings(s.MatchLabel.Players, s.BlueWins)
		if rating, ok := ratings[s.SessionID]; ok {
			if err := MatchmakingRatingStore(ctx, nk, s.UserID, "", s.DisplayName, s.GroupID, s.Mode, rating); err != nil {
				logger.WithField("error", err).Warn("Failed to record rating to leaderboard")
			}
		} else {
			logger.WithField("session_id", s.SessionID).Warn("Failed to get player rating")
		}

		// Calculate a new rank percentile
		zapLogger := RuntimeLoggerToZapLogger(logger)
		if rankPercentile, err := CalculateSmoothedPlayerRankPercentile(ctx, zapLogger, db, nk, s.UserID, s.GroupID, s.Mode); err != nil {
			logger.WithField("error", err).Warn("Failed to calculate new player rank percentile")
		} else if err := MatchmakingRankPercentileStore(ctx, nk, s.UserID, s.DisplayName, s.GroupID, s.Mode, rankPercentile); err != nil {
			logger.WithField("error", err).Warn("Failed to record rank percentile to leaderboard")
		}
	}

	// Update the player's statistics, if the service settings allow it
	if serviceSettings.DisableStatisticsUpdates {
		return nil
	}

	var stats evr.Statistics

	// Select the correct statistics based on the mode
	switch s.Mode {
	case evr.ModeArenaPublic:
		// Arena stats are processed from the remote log set
		// see evr_runtime_event_remotelogset.go
	case evr.ModeCombatPublic:
		if s.Update.Statistics.Combat == nil {
			return fmt.Errorf("missing combat statistics")
		}
		stats = s.Update.Statistics.Combat
	default:
		return fmt.Errorf("unknown mode: %s", s.Mode)
	}

	// Get the players existing statistics
	prevPlayerStats, _, err := PlayerStatisticsGetID(ctx, db, nk, s.UserID, s.GroupID, []evr.Symbol{s.Mode}, s.Mode)
	if err != nil {
		return fmt.Errorf("failed to get player statistics: %w", err)
	}
	g := evr.StatisticsGroup{
		Mode:          s.Mode,
		ResetSchedule: evr.ResetScheduleAllTime,
	}

	// Use defaults if the player has no existing statistics
	prevStats, ok := prevPlayerStats[g]
	if !ok {
		prevStats = evr.NewServerProfile().Statistics[g]
	}

	entries, err := StatisticsToEntries(s.UserID, s.DisplayName, s.GroupID, s.Mode, prevStats, stats)
	if err != nil {
		return fmt.Errorf("failed to convert statistics to entries: %w", err)
	}

	return statisticsQueue.Add(entries)
}

// incrementCompletedMatches increments the completed matches counter for a player
// This is shared logic between EventServerProfileUpdate and EventRemoteLogSet
func incrementCompletedMatches(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, sessionRegistry SessionRegistry, userID, sessionID string) error {
	eqconfig := NewEarlyQuitConfig()
	if err := StorableRead(ctx, nk, userID, eqconfig, true); err != nil {
		logger.WithField("error", err).Warn("Failed to load early quitter config")
	} else {
		eqconfig.IncrementCompletedMatches()
		if err := StorableWrite(ctx, nk, userID, eqconfig); err != nil {
			logger.WithField("error", err).Warn("Failed to store early quitter config")
		}
	}
	if playerSession := sessionRegistry.Get(uuid.FromStringOrNil(sessionID)); playerSession != nil {
		if params, ok := LoadParams(playerSession.Context()); ok {
			params.earlyQuitConfig.Store(eqconfig)
		}
	}
	return nil
}

func StatisticsToEntries(userID, displayName, groupID string, mode evr.Symbol, prev, update evr.Statistics) ([]*StatisticsQueueEntry, error) {

	// Merge the update (match stats) into the previous stats (totals)
	// to get the new totals.

	// Use prev as the base for total.
	// If prev is nil, create a new instance.
	var total evr.Statistics
	if prev != nil {
		total = prev
	} else {
		newTotal := reflect.New(reflect.TypeOf(update).Elem()).Interface()
		var ok bool
		if total, ok = newTotal.(evr.Statistics); !ok {
			return nil, fmt.Errorf("failed to assert type to evr.Statistics")
		}
	}

	totalElem := reflect.ValueOf(total).Elem()
	updateElem := reflect.ValueOf(update).Elem()

	// Iterate over the fields in the update
	for i := 0; i < updateElem.NumField(); i++ {
		updateField := updateElem.Field(i)
		if updateField.IsNil() {
			continue
		}

		fieldType := updateElem.Type().Field(i)
		jsonTag := fieldType.Tag.Get("json")
		statName := strings.SplitN(jsonTag, ",", 2)[0]

		opTag := fieldType.Tag.Get("op")
		opName, _, _ := strings.Cut(opTag, ",")

		// Handle XP specially as it might be marked as 'rep' in some structs but should be additive
		if statName == "XP" {
			opName = "add"
		}

		totalField := totalElem.Field(i)
		if totalField.IsNil() {
			totalField.Set(reflect.New(fieldType.Type.Elem()))
		}

		totalStat := totalField.Interface().(*evr.StatisticValue)
		updateStat := updateField.Interface().(*evr.StatisticValue)

		switch opName {
		case "add":
			totalStat.SetValue(totalStat.GetValue() + updateStat.GetValue())
			totalStat.SetCount(totalStat.GetCount() + updateStat.GetCount())
		case "max":
			if updateStat.GetValue() > totalStat.GetValue() {
				totalStat.SetValue(updateStat.GetValue())
			}
			// case "rep", "avg":
			// Ignore calculated fields or replacements from match stats
		}
	}

	// Recalculate fields (percentages, averages, etc.) based on the new totals
	total.CalculateFields()

	resetSchedules := []evr.ResetSchedule{evr.ResetScheduleDaily, evr.ResetScheduleWeekly, evr.ResetScheduleAllTime}

	// Use OperatorSet for everything because the absolute totals have been calculated.
	// This avoids the Float64ToScore offset accumulation issue with OperatorIncrement.
	operator := OperatorSet

	// construct the entries
	entries := make([]*StatisticsQueueEntry, 0, len(resetSchedules)*totalElem.NumField())
	for i := 0; i < totalElem.NumField(); i++ {
		totalField := totalElem.Field(i)
		if totalField.IsNil() {
			continue
		}

		fieldType := totalElem.Type().Field(i)
		jsonTag := fieldType.Tag.Get("json")
		statName := strings.SplitN(jsonTag, ",", 2)[0]

		statValue := totalField.Interface().(*evr.StatisticValue).GetValue()

		// Skip stats that are not set or negative
		if statValue <= 0 {
			continue
		}

		for _, r := range resetSchedules {

			meta := LeaderboardMeta{
				GroupID:       groupID,
				Mode:          mode,
				StatName:      statName,
				Operator:      operator,
				ResetSchedule: r,
			}

			score, subscore, err := Float64ToScore(statValue)
			if err != nil {
				return nil, fmt.Errorf("failed to convert float64 to int64 pair: %w", err)
			}

			entries = append(entries, &StatisticsQueueEntry{
				BoardMeta:   meta,
				UserID:      userID,
				DisplayName: displayName,
				Score:       score,
				Subscore:    subscore,
				Metadata:    nil,
			})
		}
	}

	return entries, nil
}

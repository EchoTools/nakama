package server

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

var _ = Event(&EventServerProfileUpdate{})

type EventServerProfileUpdate struct {
	UserID      string                  `json:"user_id"`
	GroupID     string                  `json:"group_id"`
	DisplayName string                  `json:"display_name"`
	Mode        evr.Symbol              `json:"mode"`
	Update      evr.ServerProfileUpdate `json:"update"`
}

func NewEventServerProfileUpdate(userID, groupID, displayName string, mode evr.Symbol, update evr.ServerProfileUpdate) *EventServerProfileUpdate {
	return &EventServerProfileUpdate{
		UserID:      userID,
		GroupID:     groupID,
		DisplayName: displayName,
		Mode:        mode,
		Update:      update,
	}
}

func (s *EventServerProfileUpdate) Process(ctx context.Context, logger runtime.Logger, dispatcher *EventDispatcher) error {
	var (
		db              = dispatcher.db
		nk              = dispatcher.nk
		statisticsQueue = dispatcher.statisticsQueue
	)

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

	// Get the player's existing statistics: all-time, and the current day and
	// week (dailyWeeklyStatMode is this mode).
	prevPlayerStats, _, err := PlayerStatisticsGetID(ctx, db, nk, s.UserID, s.GroupID, []evr.Symbol{s.Mode}, s.Mode)
	if err != nil {
		return fmt.Errorf("failed to get player statistics: %w", err)
	}

	// Each schedule merges the match into its own row's current value, so a
	// daily row holds today's total and a weekly row this week's. Merging into
	// the all-time value and writing it to all three made every daily and
	// weekly board a copy of the all-time one.
	entries := make([]*StatisticsQueueEntry, 0)
	for _, r := range []evr.ResetSchedule{evr.ResetScheduleDaily, evr.ResetScheduleWeekly, evr.ResetScheduleAllTime} {
		g := evr.StatisticsGroup{
			Mode:          s.Mode,
			ResetSchedule: r,
		}

		// Use defaults if the player has no existing statistics
		prevStats, ok := prevPlayerStats[g]
		if !ok {
			prevStats = evr.NewServerProfile().Statistics[g]
		}

		scheduleEntries, err := StatisticsToEntries(s.UserID, s.DisplayName, s.GroupID, s.Mode, r, prevStats, stats)
		if err != nil {
			return fmt.Errorf("failed to convert %s statistics to entries: %w", r, err)
		}
		entries = append(entries, scheduleEntries...)
	}

	logger.WithFields(map[string]any{
		"uid":     s.UserID,
		"gid":     s.GroupID,
		"mode":    s.Mode.String(),
		"entries": len(entries),
	}).Debug("Server profile update queued")

	return statisticsQueue.Add(entries)
}

// StatisticsToEntries merges a match's stats (update) into prev, the player's
// current stats for one schedule, and returns the queue entries that set that
// schedule's rows to the result. prev must be that schedule's own stats: the
// daily total for the daily rows, not the all-time one.
func StatisticsToEntries(userID, displayName, groupID string, mode evr.Symbol, schedule evr.ResetSchedule, prev, update evr.Statistics) ([]*StatisticsQueueEntry, error) {

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

		totalStat, ok := totalField.Interface().(*evr.StatisticValue)
		if !ok {
			continue
		}
		updateStat, ok := updateField.Interface().(*evr.StatisticValue)
		if !ok {
			continue
		}

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

	// Use OperatorSet for everything because the absolute totals have been calculated.
	// This avoids the Float64ToScore offset accumulation issue with OperatorIncrement.
	operator := OperatorSet

	// construct the entries
	entries := make([]*StatisticsQueueEntry, 0, totalElem.NumField())
	for i := 0; i < totalElem.NumField(); i++ {
		totalField := totalElem.Field(i)
		if totalField.IsNil() {
			continue
		}

		fieldType := totalElem.Type().Field(i)
		jsonTag := fieldType.Tag.Get("json")
		statName := strings.SplitN(jsonTag, ",", 2)[0]

		sv, ok := totalField.Interface().(*evr.StatisticValue)
		if !ok || sv == nil {
			continue
		}
		statValue := sv.GetValue()

		// Ratings have one writer: the remote-log path, which computes them from
		// the match result. This update carries the prior boards it read, so
		// writing them back with OperatorSet would race the rating write through
		// the one FIFO queue and restore the pre-match Mu and Sigma. Every mode's
		// statistics pass through here, so this covers arena and combat alike.
		if IsTeamSkillRatingStat(statName) {
			continue
		}

		// Skip stats that are not set or negative
		if statValue <= 0 {
			continue
		}

		meta := LeaderboardMeta{
			GroupID:       groupID,
			Mode:          mode,
			StatName:      statName,
			Operator:      operator,
			ResetSchedule: schedule,
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
			Writer:      StatisticsWriterProfileUpdate,
		})
	}

	return entries, nil
}

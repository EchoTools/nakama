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

func StatisticsToEntries(userID, displayName, groupID string, mode evr.Symbol, prev, update evr.Statistics) ([]*StatisticsQueueEntry, error) {

	// Update the calculated fields
	if prev != nil {
		prev.CalculateFields()
	}
	update.CalculateFields()

	// Modify the update based on the previous stats
	updateElem := reflect.ValueOf(update).Elem()
	prevValue := reflect.ValueOf(prev)
	for i := range updateElem.NumField() {
		if field := updateElem.Field(i); !field.IsNil() && prevValue.IsValid() && !prevValue.IsNil() {
			stat := field.Interface().(*evr.StatisticValue)
			if stat == nil {
				continue
			}
			// If this is the XP field, just set it to the new value
			if stat.GetName() == "XP" {
				stat.SetValue(stat.GetValue())
			}
			// If the previous field exists, subtract the previous value from the current value
			prevField := prevValue.Elem().Field(i)
			if prevField.IsValid() && !prevField.IsNil() {
				if val := prevField.Interface().(*evr.StatisticValue); val != nil {
					stat.SetValue(stat.GetValue() - val.GetValue())
				}
			}
		}
	}

	resetSchedules := []evr.ResetSchedule{evr.ResetScheduleDaily, evr.ResetScheduleWeekly, evr.ResetScheduleAllTime}

	opMap := map[string]LeaderboardOperator{
		"avg": OperatorSet,
		"add": OperatorIncrement,
		"max": OperatorBest,
		"rep": OperatorSet,
	}

	// Create a map of stat names to their corresponding operator
	statsBaseType := reflect.ValueOf(evr.ArenaStatistics{}).Type()
	nameOperatorMap := make(map[string]LeaderboardOperator, statsBaseType.NumField())
	for i := range statsBaseType.NumField() {
		jsonTag := statsBaseType.Field(i).Tag.Get("json")
		statName := strings.SplitN(jsonTag, ",", 2)[0]
		opTag := statsBaseType.Field(i).Tag.Get("op")
		nameOperatorMap[statName] = opMap[opTag]
	}

	// construct the entries
	entries := make([]*StatisticsQueueEntry, 0, len(resetSchedules)*updateElem.NumField())
	for i := 0; i < updateElem.NumField(); i++ {
		updateField := updateElem.Field(i)

		for _, r := range resetSchedules {

			if updateField.IsNil() {
				continue
			}

			// Extract the JSON tag from the struct field
			jsonTag := updateElem.Type().Field(i).Tag.Get("json")
			statName := strings.SplitN(jsonTag, ",", 2)[0]

			meta := LeaderboardMeta{
				GroupID:       groupID,
				Mode:          mode,
				StatName:      statName,
				Operator:      nameOperatorMap[statName],
				ResetSchedule: r,
			}

			statValue := updateField.Interface().(*evr.StatisticValue).GetValue()

			// Skip stats that are not set or negative
			if statValue <= 0 {
				continue
			}

			score, err := Float64ToScore(statValue)
			if err != nil {
				return nil, fmt.Errorf("failed to convert float64 to int64 pair: %w", err)
			}

			entries = append(entries, &StatisticsQueueEntry{
				BoardMeta:   meta,
				UserID:      userID,
				DisplayName: displayName,
				Score:       score,
				Subscore:    0,
				Metadata:    nil,
			})
		}
	}

	return entries, nil
}

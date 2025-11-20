package server

import (
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

func TestStatisticsToEntries(t *testing.T) {
	tests := []struct {
		userID      string
		displayName string
		groupID     string
		mode        evr.Symbol
		update      evr.MatchTypeStats
		expected    []*StatisticsQueueEntry
	}{
		{
			userID:      "user1",
			displayName: "User One",
			groupID:     "group1",
			mode:        evr.ModeArenaPublic,
			update: evr.MatchTypeStats{
				Clears: 10,
			},
			expected: []*StatisticsQueueEntry{
				newStatQueueEntry("group1", evr.ModeArenaPublic, "Clears", operatorFromStatField(t, "Clears"), evr.ResetScheduleDaily, "user1", "User One", 1000000000000010, 0),
				newStatQueueEntry("group1", evr.ModeArenaPublic, "Clears", operatorFromStatField(t, "Clears"), evr.ResetScheduleWeekly, "user1", "User One", 1000000000000010, 0),
				newStatQueueEntry("group1", evr.ModeArenaPublic, "Clears", operatorFromStatField(t, "Clears"), evr.ResetScheduleAllTime, "user1", "User One", 1000000000000010, 0),
			},
		},
	}

	for _, test := range tests {
		entries, err := typeStatsToScoreMap(test.userID, test.displayName, test.groupID, test.mode, test.update)
		if err != nil {
			t.Errorf("StatisticsToEntries returned an error: %v", err)
		}

		if cmp.Diff(entries, test.expected) != "" {
			t.Errorf("StatisticsToEntries returned unexpected entries: %v", cmp.Diff(entries, test.expected))
		}
	}
}

func TestTypeStatsToScoreMap_UsesOperatorTags(t *testing.T) {
	entries, err := typeStatsToScoreMap(
		"user2",
		"User Two",
		"group2",
		evr.ModeArenaPublic,
		evr.MatchTypeStats{
			HatTricks:                    2,
			HighestPoints:                11,
			AveragePossessionTimePerGame: 42.5,
		},
	)
	if err != nil {
		t.Fatalf("typeStatsToScoreMap returned unexpected error: %v", err)
	}

	ops := make(map[string]LeaderboardOperator)
	for _, entry := range entries {
		if entry.BoardMeta.ResetSchedule != evr.ResetScheduleDaily {
			continue
		}
		ops[entry.BoardMeta.StatName] = entry.BoardMeta.Operator
	}

	if ops["HatTricks"] != OperatorIncrement {
		t.Fatalf("expected HatTricks to use OperatorIncrement, got %s", ops["HatTricks"])
	}
	if ops["HighestPoints"] != OperatorBest {
		t.Fatalf("expected HighestPoints to use OperatorBest, got %s", ops["HighestPoints"])
	}
	if ops["AveragePossessionTimePerGame"] != OperatorSet {
		t.Fatalf("expected AveragePossessionTimePerGame to use OperatorSet, got %s", ops["AveragePossessionTimePerGame"])
	}
}

func newStatQueueEntry(groupID string, mode evr.Symbol, statName string, operator LeaderboardOperator, reset evr.ResetSchedule, userID, displayName string, score, subscore int64) *StatisticsQueueEntry {
	return &StatisticsQueueEntry{
		BoardMeta: LeaderboardMeta{
			GroupID:       groupID,
			Mode:          mode,
			StatName:      statName,
			Operator:      operator,
			ResetSchedule: reset,
		},
		UserID:      userID,
		DisplayName: displayName,
		Score:       score,
		Subscore:    subscore,
	}
}

func operatorFromStatField(t *testing.T, fieldName string) LeaderboardOperator {
	t.Helper()
	field, ok := reflect.TypeOf(evr.MatchTypeStats{}).FieldByName(fieldName)
	if !ok {
		t.Fatalf("field %s not found on MatchTypeStats", fieldName)
	}
	switch field.Tag.Get("op") {
	case "add":
		return OperatorIncrement
	case "max":
		return OperatorBest
	default:
		return OperatorSet
	}
}

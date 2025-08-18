package service

import (
	"testing"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/google/go-cmp/cmp"
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
				{
					BoardMeta: LeaderboardMeta{
						GroupID:       "group1",
						Mode:          evr.ModeArenaPublic,
						StatName:      "Clears",
						Operator:      OperatorSet,
						ResetSchedule: evr.ResetScheduleDaily,
					},
					UserID:      "user1",
					DisplayName: "User One",
					Score:       1000000000000010, // 10.0 with new encoding: 1e15 + 10
					Subscore:    0,
				},
				{
					BoardMeta: LeaderboardMeta{
						GroupID:       "group1",
						Mode:          evr.ModeArenaPublic,
						StatName:      "Clears",
						Operator:      OperatorSet,
						ResetSchedule: evr.ResetScheduleWeekly,
					},
					UserID:      "user1",
					DisplayName: "User One",
					Score:       1000000000000010, // 10.0 with new encoding: 1e15 + 10
					Subscore:    0,
				},
				{
					BoardMeta: LeaderboardMeta{
						GroupID:       "group1",
						Mode:          evr.ModeArenaPublic,
						StatName:      "Clears",
						Operator:      OperatorSet,
						ResetSchedule: evr.ResetScheduleAllTime,
					},
					UserID:      "user1",
					DisplayName: "User One",
					Score:       1000000000000010, // 10.0 with new encoding: 1e15 + 10
					Subscore:    0,
				},
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

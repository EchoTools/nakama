package server

import (
	"reflect"
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
)

func TestTypeStatsToScoreMap_Realistic(t *testing.T) {
	msg := evr.MatchTypeStats{
		Assists:            2,
		Blocks:             2,
		BounceGoals:        0,
		Catches:            10,
		Clears:             9,
		Goals:              0,
		HatTricks:          0,
		Interceptions:      12,
		Passes:             16,
		Points:             0,
		PossessionTime:     132.479004,
		PunchesReceived:    15,
		Saves:              3,
		ShotsOnGoal:        5,
		ShotsOnGoalAgainst: 25,
		Steals:             1,
		Stuns:              12,
		ThreePointGoals:    0,
		TwoPointGoals:      0,
	}

	groupID := "group456"
	displayName := "TestUser"
	mode := evr.ToSymbol("echo_arena")
	userID := "user123"

	wantedEntries := make([]*StatisticsQueueEntry, 0)

	// Use reflect to iterate over msg fields, and generate wantedEntries
	msgValue := reflect.ValueOf(msg)
	msgType := reflect.TypeOf(msg)

	resetSchedules := []string{"daily", "weekly", "alltime"}

	for i := 0; i < msgValue.NumField(); i++ {
		field := msgValue.Field(i)
		fieldType := msgType.Field(i)
		fieldName := fieldType.Name

		// Skip zero values
		if field.Kind() == reflect.Float64 && field.Float() == 0 {
			continue
		}
		if (field.Kind() == reflect.Int || field.Kind() == reflect.Int64) && field.Int() == 0 {
			continue
		}

		// Convert field value to score, subscore
		score, subscore, err := Float64ToScore(field.Convert(reflect.TypeFor[float64]()).Float())
		if err != nil {
			t.Fatalf("Failed to convert field %s to score: %v", fieldName, err)
		}
		if score == 0 {
			continue
		}

		// Create entries for each reset schedule
		for _, schedule := range resetSchedules {
			operator := operatorFromStatField(t, fieldName)
			entry := &StatisticsQueueEntry{
				BoardMeta: LeaderboardMeta{
					GroupID:       groupID,
					Mode:          mode,
					StatName:      fieldName,
					Operator:      operator,
					ResetSchedule: evr.ResetSchedule(schedule),
				},
				UserID:      userID,
				DisplayName: displayName,
				Score:       score,
				Subscore:    subscore,
				Metadata:    nil,
			}
			wantedEntries = append(wantedEntries, entry)
		}
	}

	// for each field in msg, generate the score, subscore

	entries, err := typeStatsToScoreMap(userID, displayName, groupID, mode, msg)
	assert.NoError(t, err)
	assert.NotEmpty(t, entries)

	assert.Equal(t, len(wantedEntries), len(entries), "Number of entries should match expected count")

	for i, entry := range entries {
		assert.Equal(t, wantedEntries[i].BoardMeta, entry.BoardMeta, "BoardMeta mismatch at entry %d", i)
		assert.Equal(t, wantedEntries[i].UserID, entry.UserID, "UserID mismatch at entry %d", i)
		assert.Equal(t, wantedEntries[i].DisplayName, entry.DisplayName, "DisplayName mismatch at entry %d", i)
		assert.Equal(t, wantedEntries[i].Score, entry.Score, "Score mismatch at entry %d", i)
		assert.Equal(t, wantedEntries[i].Subscore, entry.Subscore, "Subscore mismatch at entry %d", i)
		assert.Equal(t, wantedEntries[i].Metadata, entry.Metadata, "Metadata mismatch at entry %d", i)
	}

}

package server

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
)

func TestIterateMatchTypeStatsFields(t *testing.T) {
	stats := evr.MatchTypeStats{
		Assists: 10,
		Goals:   0,
	}

	count := 0
	iterateMatchTypeStatsFields(stats, func(statName string, op LeaderboardOperator, value float64) {
		count++
		if statName == "assists" {
			assert.Equal(t, 10.0, value)
		}
	})
	assert.True(t, count > 0)
}

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

	// Generate expected entries by replicating the logic
	expectedEntries := make([]*StatisticsQueueEntry, 0)
	resetSchedules := []evr.ResetSchedule{evr.ResetScheduleDaily, evr.ResetScheduleWeekly, evr.ResetScheduleAllTime}

	v := reflect.ValueOf(msg)
	typeOfMsg := v.Type()

	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		structField := typeOfMsg.Field(i)

		opTag := structField.Tag.Get("op")
		opParts := strings.Split(opTag, ",")

		// Check for zero value and omitzero flag
		isZero := false
		switch field.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			isZero = field.Int() == 0
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			isZero = field.Uint() == 0
		case reflect.Float32, reflect.Float64:
			isZero = field.Float() == 0
		}

		if isZero && slices.Contains(opParts, "omitzero") {
			continue
		}

		jsonTag := structField.Tag.Get("json")
		statName := strings.SplitN(jsonTag, ",", 2)[0]

		op := OperatorSet
		switch opParts[0] {
		case "avg":
			op = OperatorSet
		case "add":
			op = OperatorIncrement
		case "max":
			op = OperatorBest
		case "rep":
			op = OperatorSet
		}

		var val float64
		switch field.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			val = float64(field.Int())
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			val = float64(field.Uint())
		case reflect.Float32, reflect.Float64:
			val = field.Float()
		}

		score, subscore, err := Float64ToScore(val)
		assert.NoError(t, err)

		for _, r := range resetSchedules {
			expectedEntries = append(expectedEntries, &StatisticsQueueEntry{
				BoardMeta: LeaderboardMeta{
					GroupID:       groupID,
					Mode:          mode,
					StatName:      statName,
					Operator:      op,
					ResetSchedule: r,
				},
				UserID:      userID,
				DisplayName: displayName,
				Score:       score,
				Subscore:    subscore,
			})
		}
	}

	entries, err := typeStatsToScoreMap(userID, displayName, groupID, mode, msg)
	assert.NoError(t, err)
	assert.NotEmpty(t, entries)

	assert.Equal(t, len(expectedEntries), len(entries), "Number of entries should match expected count")

	for i, entry := range entries {
		assert.Equal(t, expectedEntries[i].BoardMeta, entry.BoardMeta, "BoardMeta mismatch at entry %d", i)
		assert.Equal(t, expectedEntries[i].UserID, entry.UserID, "UserID mismatch at entry %d", i)
		assert.Equal(t, expectedEntries[i].DisplayName, entry.DisplayName, "DisplayName mismatch at entry %d", i)
		assert.Equal(t, expectedEntries[i].Score, entry.Score, "Score mismatch at entry %d", i)
		assert.Equal(t, expectedEntries[i].Subscore, entry.Subscore, "Subscore mismatch at entry %d", i)
	}
}

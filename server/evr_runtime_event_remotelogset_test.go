package server

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
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
		HighestSpeed:       46.062946,
		Interceptions:      12,
		LongestPossession:  8.221572,
		Passes:             16,
		Points:             0,
		PossessionTime:     132.479004,
		PunchesReceived:    15,
		Saves:              3,
		Score:              732,
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

		// Convert field value to score (multiply by 1 billion for precision)
		score, subscore, err := Float64ToScore(field.Convert(reflect.TypeOf(float64(0))).Float())
		if err != nil {
			t.Fatalf("Failed to convert field %s to score: %v", fieldName, err)
		}
		if score == 0 {
			continue
		}
		t.Fatalf("score: %d, subscore: %d", score, subscore)
		// Create entries for each reset schedule
		for _, schedule := range resetSchedules {
			entry := &StatisticsQueueEntry{
				BoardMeta: LeaderboardMeta{
					GroupID:       groupID,
					Mode:          mode,
					StatName:      fieldName,
					Operator:      "set",
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

func TestTypeStatsToScoreMap_AllFieldsSet(t *testing.T) {

	// ArenaStatistics fields must match dummyStats for this test to work
	// so we use evr.MatchTypeStats as dummyStats for the actual code
	// but here we test the logic with a similar struct

	// Use evr.ArenaStatistics for real test
	arenaStats := evr.ArenaStatistics{
		ArenaWins:   &evr.StatisticValue{Value: 10},
		ArenaLosses: &evr.StatisticValue{Value: 1},
		Goals:       &evr.StatisticValue{Value: 5},
		Saves:       &evr.StatisticValue{Value: 2},
		Level:       &evr.StatisticValue{Value: 4},
	}

	wantedEntries := []*StatisticsQueueEntry{
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "ArenaLosses",
				Operator:      "set",
				ResetSchedule: "daily",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       1000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "ArenaLosses",
				Operator:      "set",
				ResetSchedule: "weekly",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       1000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "ArenaLosses",
				Operator:      "set",
				ResetSchedule: "alltime",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       1000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "ArenaWins",
				Operator:      "set",
				ResetSchedule: "daily",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       10000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "ArenaWins",
				Operator:      "set",
				ResetSchedule: "weekly",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       10000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "ArenaWins",
				Operator:      "set",
				ResetSchedule: "alltime",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       10000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "Goals",
				Operator:      "set",
				ResetSchedule: "daily",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       5000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "Goals",
				Operator:      "set",
				ResetSchedule: "weekly",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       5000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "Goals",
				Operator:      "set",
				ResetSchedule: "alltime",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       5000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "Saves",
				Operator:      "set",
				ResetSchedule: "daily",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       2000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "Saves",
				Operator:      "set",
				ResetSchedule: "weekly",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       2000000000,
			Subscore:    0,
			Metadata:    nil,
		},
		{
			BoardMeta: LeaderboardMeta{
				GroupID:       "group456",
				Mode:          evr.ToSymbol("echo_arena"),
				StatName:      "Saves",
				Operator:      "set",
				ResetSchedule: "alltime",
			},
			UserID:      "user123",
			DisplayName: "TestUser",
			Score:       2000000000,
			Subscore:    0,
			Metadata:    nil,
		},
	}

	matchTypeStats := evr.MatchTypeStats{
		ArenaWins:   int64(arenaStats.ArenaWins.Value),
		ArenaLosses: int64(arenaStats.ArenaLosses.Value),
		Goals:       int64(arenaStats.Goals.Value),
		Saves:       int64(arenaStats.Saves.Value),
	}

	userID := "user123"
	displayName := "TestUser"
	groupID := "group456"
	mode := evr.ModeArenaPublic

	entries, err := typeStatsToScoreMap(userID, displayName, groupID, mode, matchTypeStats)
	assert.NoError(t, err)
	assert.NotEmpty(t, entries)

	assert.Equal(t, len(entries), 12, "There should be 12 entries for all stats and reset schedules")
	// Each non-zero stat should produce 3 entries (daily, weekly, alltime)

	for i, entry := range entries {
		assert.Equal(t, wantedEntries[i].BoardMeta, entry.BoardMeta, "BoardMeta mismatch at entry %d", i)
		assert.Equal(t, wantedEntries[i].UserID, entry.UserID, "UserID mismatch at entry %d", i)
		assert.Equal(t, wantedEntries[i].DisplayName, entry.DisplayName, "DisplayName mismatch at entry %d", i)
		assert.Equal(t, wantedEntries[i].Score, entry.Score, "Score mismatch at entry %d", i)
		assert.Equal(t, wantedEntries[i].Subscore, entry.Subscore, "Subscore mismatch at entry %d", i)
		assert.Equal(t, wantedEntries[i].Metadata, entry.Metadata, "Metadata mismatch at entry %d", i)
	}

}

func TestHandleMatchGoal(t *testing.T) {
	data := `{                                                                                                                   
      "message": "Goal",                                                                                                
      "message_type": "GOAL",                                                                                           
      "userid": "",                                                                                                     
      "[game_info][game_time]": 62.53521,                                                                               
      "[game_info][is_arena]": true,                                                                                    
      "[game_info][is_capture_point]": false,                                                                           
      "[game_info][is_combat]": false,                                                                                  
      "[game_info][is_payload]": false,                                                                                 
      "[game_info][is_private]": false,                                                                                 
      "[game_info][is_social]": false,                                                                                  
      "[game_info][level]": "mpl_arena_a",                                                                              
      "[game_info][match_type]": "Echo_Arena",                                                                          
      "[goal_type]": "LONG SHOT",                                                                                       
      "[player_info][displayname]": "foobar",                                                         
      "[player_info][teamid]": 0,                                                                                       
      "[player_info][userid]": "OVR-ORG-123412342134",                                                              
      "[prev_player][displayname]": "baz-",                                                                          
      "[prev_player][teamid]": 0,                                                                                       
      "[prev_player][userid]": "OVR-ORG-1234123412341234",                                                              
      "[session][uuid]": "{188A744E-3957-4737-B652-6FB6888493C9}",                                                      
      "[was_headbutt]": false                                                                                           
    }`
	g := &evr.RemoteLogGoal{}
	_ = json.Unmarshal([]byte(data), g)

	got := &MatchGameStateUpdate{}

	processMatchGoalIntoUpdate(g, got)

	want := &MatchGameStateUpdate{
		CurrentGameClock: time.Duration(62.53521 * float64(time.Second)),
		PauseDuration:    28 * time.Second,
		Goals: []*evr.MatchGoal{
			{
				GoalTime:    62.53521,
				GoalType:    "LONG SHOT",
				DisplayName: "foobar",
				TeamID:      0,
				XPID: evr.EvrId{
					PlatformCode: evr.OVR_ORG,
					AccountId:    123412342134,
				},
				PrevPlayerDisplayName: "baz-",
				PrevPlayerTeamID:      0,
				PrevPlayerXPID: evr.EvrId{
					PlatformCode: evr.OVR_ORG,
					AccountId:    1234123412341234,
				},
				PointsValue: 3,
			},
		},
	}

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("FromGoal() mismatch (-want +got):\n%s", diff)
	}
}

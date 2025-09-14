package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
)

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

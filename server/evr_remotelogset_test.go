package server

import (
	"testing"
	"time"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
)

func TestMatchGameStateUpdate_FromGoal(t *testing.T) {
	goal := evr.RemoteLogGoal{
		GameInfoGameTime:      123,
		GoalType:              "header",
		PlayerInfoDisplayName: "Player1",
		PlayerInfoTeamID:      0,
		PlayerInfoEvrID:       "OVR-ORG-123",
		PrevPlayerDisplayname: "Player2",
		PrevPlayerTeamID:      1,
		PrevPlayerEvrID:       "OVR-ORG-789",
		WasHeadbutt:           true,
	}

	update := &MatchGameStateUpdate{}
	update.FromGoal(goal)

	assert.Equal(t, int64(123000), update.CurrentRoundClockMs)
	assert.Equal(t, time.Duration(AfterGoalDuration+RespawnDuration+CatapultDuration)*time.Second, update.PauseDuration)
	assert.NotNil(t, update.Goals)
	assert.Len(t, update.Goals, 1)

	expectedGoal := &MatchGoal{
		GoalTime:              goal.GameInfoGameTime,
		GoalType:              goal.GoalType,
		Displayname:           goal.PlayerInfoDisplayName,
		Teamid:                goal.PlayerInfoTeamID,
		EvrID:                 goal.PlayerInfoEvrID,
		PrevPlayerDisplayName: goal.PrevPlayerDisplayname,
		PrevPlayerTeamID:      goal.PrevPlayerTeamID,
		PrevPlayerEvrID:       goal.PrevPlayerEvrID,
		WasHeadbutt:           goal.WasHeadbutt,
	}

	assert.Equal(t, expectedGoal, update.Goals[0])
}

package server

import (
	"testing"
	"time"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

func TestGameState_Update(t *testing.T) {
	tests := []struct {
		name       string
		goals      []*evr.MatchGoal
		wantBlue   int
		wantOrange int
	}{
		{
			name:       "No goals",
			goals:      []*evr.MatchGoal{},
			wantBlue:   0,
			wantOrange: 0,
		},
		{
			name: "Single goal for blue team",
			goals: []*evr.MatchGoal{
				{GoalType: "SLAM DUNK", TeamID: 0},
			},
			wantBlue:   2,
			wantOrange: 0,
		},
		{
			name: "Single goal for orange team",
			goals: []*evr.MatchGoal{
				{GoalType: "SLAM DUNK", TeamID: 1},
			},
			wantBlue:   0,
			wantOrange: 2,
		},
		{
			name: "Multiple goals for both teams",
			goals: []*evr.MatchGoal{
				{GoalType: "SLAM DUNK", TeamID: 0},
				{GoalType: "LONG SHOT", TeamID: 1},
				{GoalType: "BOUNCE SHOT", TeamID: 0},
			},
			wantBlue:   4,
			wantOrange: 3,
		},
		{
			name: "Unknown goal type",
			goals: []*evr.MatchGoal{
				{GoalType: "UNKNOWN", TeamID: 0},
			},
			wantBlue:   0,
			wantOrange: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &GameState{
				RoundClock:  NewRoundClock(10*time.Minute, time.Now()),
				BlueScore:   0,
				OrangeScore: 0}

			g.Update(tt.goals)
			if g.BlueScore != tt.wantBlue {
				t.Errorf("BlueScore = %v, want %v", g.BlueScore, tt.wantBlue)
			}
			if g.OrangeScore != tt.wantOrange {
				t.Errorf("OrangeScore = %v, want %v", g.OrangeScore, tt.wantOrange)
			}
		})
	}
}

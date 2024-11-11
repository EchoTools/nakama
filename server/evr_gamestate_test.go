package server

import (
	"testing"
	"time"
)

func TestGameState_Update(t *testing.T) {
	tests := []struct {
		name       string
		goals      []*MatchGoal
		wantBlue   int
		wantOrange int
	}{
		{
			name:       "No goals",
			goals:      []*MatchGoal{},
			wantBlue:   0,
			wantOrange: 0,
		},
		{
			name: "Single goal for blue team",
			goals: []*MatchGoal{
				{GoalType: "SLAM DUNK", Teamid: 0},
			},
			wantBlue:   2,
			wantOrange: 0,
		},
		{
			name: "Single goal for orange team",
			goals: []*MatchGoal{
				{GoalType: "SLAM DUNK", Teamid: 1},
			},
			wantBlue:   0,
			wantOrange: 2,
		},
		{
			name: "Multiple goals for both teams",
			goals: []*MatchGoal{
				{GoalType: "SLAM DUNK", Teamid: 0},
				{GoalType: "LONG SHOT", Teamid: 1},
				{GoalType: "BOUNCE SHOT", Teamid: 0},
			},
			wantBlue:   4,
			wantOrange: 3,
		},
		{
			name: "Unknown goal type",
			goals: []*MatchGoal{
				{GoalType: "UNKNOWN", Teamid: 0},
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
				OrangeScore: 0,
				Goals:       tt.goals,
			}
			g.Update()
			if g.BlueScore != tt.wantBlue {
				t.Errorf("BlueScore = %v, want %v", g.BlueScore, tt.wantBlue)
			}
			if g.OrangeScore != tt.wantOrange {
				t.Errorf("OrangeScore = %v, want %v", g.OrangeScore, tt.wantOrange)
			}
		})
	}
}

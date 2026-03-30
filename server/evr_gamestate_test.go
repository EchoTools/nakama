package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

func TestGameStatus_JSONMarshal(t *testing.T) {
	tests := []struct {
		status   GameStatus
		expected string
	}{
		{GameStatusUnspecified, `"unspecified"`},
		{GameStatusPreMatch, `"pre_match"`},
		{GameStatusRoundStart, `"round_start"`},
		{GameStatusPlaying, `"playing"`},
		{GameStatusScore, `"score"`},
		{GameStatusRoundOver, `"round_over"`},
		{GameStatusPostMatch, `"post_match"`},
		{GameStatusPreSuddenDeath, `"pre_sudden_death"`},
		{GameStatusSuddenDeath, `"sudden_death"`},
		{GameStatusPostSuddenDeath, `"post_sudden_death"`},
	}

	for _, tt := range tests {
		t.Run(tt.status.String(), func(t *testing.T) {
			b, err := json.Marshal(tt.status)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(b) != tt.expected {
				t.Errorf("got %s, want %s", string(b), tt.expected)
			}
		})
	}
}

func TestGameStatus_JSONUnmarshal(t *testing.T) {
	tests := []struct {
		input    string
		expected GameStatus
	}{
		{`"unspecified"`, GameStatusUnspecified},
		{`"pre_match"`, GameStatusPreMatch},
		{`"playing"`, GameStatusPlaying},
		{`"post_match"`, GameStatusPostMatch},
		{`"sudden_death"`, GameStatusSuddenDeath},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			var gs GameStatus
			if err := json.Unmarshal([]byte(tt.input), &gs); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gs != tt.expected {
				t.Errorf("got %d (%s), want %d (%s)", gs, gs, tt.expected, tt.expected)
			}
		})
	}
}

func TestGameStatus_UnknownValue(t *testing.T) {
	var gs GameStatus
	err := json.Unmarshal([]byte(`"bogus_status"`), &gs)
	if err == nil {
		t.Fatal("expected error for unknown GameStatus, got nil")
	}

	// Verify String() on an out-of-range value does not panic
	gs = GameStatus(999)
	s := gs.String()
	if s == "" {
		t.Fatal("String() on unknown GameStatus should not be empty")
	}
}

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
				SessionScoreboard: NewSessionScoreboard(10*time.Minute, time.Now()),
				BlueScore:         0,
				OrangeScore:       0}

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

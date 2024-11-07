package server

import (
	"log"
	"time"
)

type GameState struct {
	RoundDurationMs     int64        `json:"round_duration_ms"`       // The length of the round in seconds
	CurrentRoundClockMs int64        `json:"current_round_clock_ms"`  // The current elapsed time on the round clock in seconds
	ClockPauseMs        int64        `json:"pause_time_ms,omitempty"` // The round clock time when the game was paused
	UnpauseTimeMs       int64        `json:"unpause_time,omitempty"`  // The time at which the game will be unpaused
	IsPaused            bool         `json:"is_paused"`               // Whether the game is paused
	IsRoundOver         bool         `json:"is_round_over,omitempty"` // Whether the round is over
	BlueScore           int          `json:"blue_score"`              // The score of the blue team
	OrangeScore         int          `json:"orange_score"`            // The score of the orange team
	Goals               []*MatchGoal `json:"goals,omitempty"`         // The last goal scored
}

func NewGameState() *GameState {
	return &GameState{}
}

func (g *GameState) RemainingTime() time.Duration {
	if g.CurrentRoundClockMs >= g.RoundDurationMs || g.RoundDurationMs == 0 {
		return 0
	}
	return time.Duration(g.RoundDurationMs-g.CurrentRoundClockMs) * time.Millisecond
}

func (g *GameState) Update() {

	// If there is no unpause time, the game is stopped
	if g.UnpauseTimeMs == 0 {
		return
	}

	for _, goal := range g.Goals {
		points := GoalTypeToPoints(goal.GoalType)
		if points == 0 {
			log.Printf("Unknown goal type: %s", goal.GoalType)
			continue
		}
		if goal.Teamid == 0 {
			g.BlueScore += points
		} else {
			g.OrangeScore += points
		}
	}

	// If the round clock has reached the round duration, the round is over
	if g.CurrentRoundClockMs >= g.RoundDurationMs {
		g.CurrentRoundClockMs = 0
		g.ClockPauseMs = 0
		g.UnpauseTimeMs = 0
		g.IsPaused = true
		g.IsRoundOver = true
	}

	if time.Now().UnixMilli() >= g.UnpauseTimeMs {
		g.IsPaused = false
		g.CurrentRoundClockMs = g.ClockPauseMs + time.Now().UTC().UnixMilli() - g.UnpauseTimeMs
	} else {
		g.IsPaused = true
	}
}

func GoalTypeToPoints(goalType string) int {
	switch goalType {
	case "SLAM DUNK":
		return 2
	case "BOUNCE SHOT":
		return 2
	case "BUMPER SHOT":
		return 2
	case "HEADBUTT":
		return 2
	case "INSIDE SHOT":
		return 2
	case "LONG BOUNCE SHOT":
		return 3
	case "LONG BUMPER SHOT":
		return 3
	case "LONG HEADBUTT":
		return 3
	case "LONG SHOT":
		return 3
	default:
		return 0
	}
}

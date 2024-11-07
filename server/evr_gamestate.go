package server

import "time"

type GameState struct {
	RoundDurationMs     int64        `json:"round_duration_ms"`       // The length of the round in seconds
	CurrentRoundClockMs int64        `json:"current_round_clock_ms"`  // The current elapsed time on the round clock in seconds
	ClockPauseMs        int64        `json:"pause_time_ms,omitempty"` // The round clock time when the game was paused
	UnpauseTimeMs       int64        `json:"unpause_time,omitempty"`  // The time at which the game will be unpaused
	IsPaused            bool         `json:"is_paused"`               // Whether the game is paused
	IsRoundOver         bool         `json:"is_round_over,omitempty"` // Whether the round is over
	Goals               []*MatchGoal `json:"goals,omitempty"`         // The last goal scored
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

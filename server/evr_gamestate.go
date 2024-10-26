package server

import "time"

type GameState struct {
	RoundDurationMilliseconds int64      `json:"round_duration_ms"`       // The length of the round in seconds
	CurrentRoundClock         int64      `json:"current_round_clock_ms"`  // The current elapsed time on the round clock in seconds
	ClockPauseMilliseconds    int64      `json:"pause_time_ms,omitempty"` // The round clock time when the game was paused
	UnpauseTime               int64      `json:"unpause_time,omitempty"`  // The time at which the game will be unpaused
	IsPaused                  bool       `json:"is_paused"`               // Whether the game is paused
	IsRoundOver               bool       `json:"is_round_over,omitempty"` // Whether the round is over
	Goals                     []LastGoal `json:"goals,omitempty"`         // The last goal scored
}

func (g *GameState) Update() {

	// If there is no unpause time, the game is stopped
	if g.UnpauseTime == 0 {
		return
	}

	// If the round clock has reached the round duration, the round is over
	if g.CurrentRoundClock >= g.RoundDurationMilliseconds {
		g.CurrentRoundClock = 0
		g.ClockPauseMilliseconds = 0
		g.UnpauseTime = 0
		g.IsPaused = true
		g.IsRoundOver = true
	}

	if time.Now().UnixMilli() >= g.UnpauseTime {
		g.IsPaused = false
		g.CurrentRoundClock = g.ClockPauseMilliseconds + time.Now().UTC().UnixMilli() - g.UnpauseTime
	} else {
		g.IsPaused = true
	}
}

type LastGoal struct {
	GoalTime              float64 `json:"round_clock_secs"`
	GoalType              string  `json:"goal_type"`
	Displayname           string  `json:"player_display_name"`
	Teamid                int64   `json:"player_team_id"`
	EvrID                 string  `json:"player_user_id"`
	PrevPlayerDisplayName string  `json:"prev_player_display_name"`
	PrevPlayerTeamID      int64   `json:"prev_player_team_id"`
	PrevPlayerEvrID       string  `json:"prev_player_user_id"`
	WasHeadbutt           bool    `json:"was_headbutt"`
}

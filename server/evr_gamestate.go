package server

import "time"

type GameStateUpdate struct {
	RoundClock     float64       `json:"round_clock_secs"`          // The game clock time
	PausedDuration time.Duration `json:"paused_duration,omitempty"` // The time the game has been paused
}

type GameState struct {
	RoundDuration float64   `json:"round_duration_secs"`  // The length of the round
	RoundClock    float64   `json:"round_clock_secs"`     // The game clock time
	Paused        bool      `json:"paused"`               // Whether the game is paused
	MatchOver     bool      `json:"match_over,omitempty"` // Whether the match is over
	RoundOver     bool      `json:"round_over,omitempty"` // Whether the round is over
	LastGoal      *LastGoal `json:"last_goal,omitempty"`  // The last goal scored
	unpausingAt   time.Time // The time at which the game will unpaus
}

type LastGoal struct {
	GoalTime              float64 `json:"round_clock_secs"`
	GoalType              string  `json:"goal_type"`
	Displayname           string  `json:"display_name"`
	Teamid                int64   `json:"team_id"`
	EvrID                 string  `json:"user_id"`
	PrevPlayerDisplayName string  `json:"prev_player_display_name"`
	PrevPlayerTeamID      int64   `json:"prev_player_team_id"`
	PrevPlayerEvrID       string  `json:"prev_player_user_id"`
	WasHeadbutt           bool    `json:"was_headbutt"`
}

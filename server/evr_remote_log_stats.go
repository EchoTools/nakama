package server

import "github.com/heroiclabs/nakama/v3/server/evr"

type PostMatchStats struct {
	SessionUUID string     `json:"[session][uuid]"`
	MatchStats  MatchStats `json:"match_stats"`
	MatchType   string     `json:"match_type"`
	Message     string     `json:"message"`
	MessageType string     `json:"message_type"`
	Userid      string     `json:"userid"`
}

type MatchGoal struct {
	GoalTime              float64   `json:"round_clock_secs"`
	GoalType              string    `json:"goal_type"`
	DisplayName           string    `json:"player_display_name"`
	TeamID                TeamIndex `json:"player_team_id"`
	XPID                  evr.EvrId `json:"player_user_id"`
	PrevPlayerDisplayName string    `json:"prev_player_display_name"`
	PrevPlayerTeamID      TeamIndex `json:"prev_player_team_id"`
	PrevPlayerXPID        evr.EvrId `json:"prev_player_user_id"`
	WasHeadbutt           bool      `json:"was_headbutt"`
	PointsValue           int       `json:"points_value"`
}

type MatchStats struct {
	Assists            int64   `json:"Assists"`
	Blocks             int64   `json:"Blocks"`
	BounceGoals        int64   `json:"BounceGoals"`
	Catches            int64   `json:"Catches"`
	Clears             int64   `json:"Clears"`
	Goals              int64   `json:"Goals"`
	HatTricks          int64   `json:"HatTricks"`
	HighestSpeed       float64 `json:"HighestSpeed"`
	Interceptions      int64   `json:"Interceptions"`
	LongestPossession  float64 `json:"LongestPossession"`
	Passes             int64   `json:"Passes"`
	Points             int64   `json:"Points"`
	PossessionTime     float64 `json:"PossessionTime"`
	PunchesReceived    int64   `json:"PunchesReceived"`
	Saves              int64   `json:"Saves"`
	Score              int64   `json:"Score"`
	ShotsOnGoal        int64   `json:"ShotsOnGoal"`
	ShotsOnGoalAgainst int64   `json:"ShotsOnGoalAgainst"`
	Steals             int64   `json:"Steals"`
	Stuns              int64   `json:"Stuns"`
	ThreePointGoals    int64   `json:"ThreePointGoals"`
	TwoPointGoals      int64   `json:"TwoPointGoals"`
}

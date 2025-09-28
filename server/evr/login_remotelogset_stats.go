package evr

// Remote log PostMatchMatchStats
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

// Post-match match type statistics sent in the remote log message
type MatchTypeStats struct {
	ArenaLosses                  int64   `json:"ArenaLosses,omitempty"`
	ArenaMVPPercentage           float64 `json:"ArenaMVPPercentage,omitempty"`
	ArenaMVPs                    int64   `json:"ArenaMVPs,omitempty"`
	ArenaWinPercentage           float64 `json:"ArenaWinPercentage,omitempty"`
	ArenaWins                    int64   `json:"ArenaWins,omitempty"`
	Assists                      int64   `json:"Assists,omitempty"`
	AssistsPerGame               float64 `json:"AssistsPerGame,omitempty"`
	AveragePointsPerGame         float64 `json:"AveragePointsPerGame,omitempty"`
	AveragePossessionTimePerGame float64 `json:"AveragePossessionTimePerGame,omitempty"`
	AverageTopSpeedPerGame       float64 `json:"AverageTopSpeedPerGame,omitempty"`
	BlockPercentage              float64 `json:"BlockPercentage,omitempty"`
	Blocks                       int64   `json:"Blocks,omitempty"`
	BounceGoals                  int64   `json:"BounceGoals,omitempty"`
	BumperShots                  int64   `json:"BumperShots,omitempty"`
	Catches                      int64   `json:"Catches,omitempty"`
	Clears                       int64   `json:"Clears,omitempty"`
	CurrentArenaMVPStreak        int64   `json:"CurrentArenaMVPStreak,omitempty"`
	CurrentArenaWinStreak        int64   `json:"CurrentArenaWinStreak,omitempty"`
	GoalSavePercentage           float64 `json:"GoalSavePercentage,omitempty"`
	GoalScorePercentage          float64 `json:"GoalScorePercentage,omitempty"`
	Goals                        int64   `json:"Goals,omitempty"`
	GoalsPerGame                 float64 `json:"GoalsPerGame,omitempty"`
	HatTricks                    int64   `json:"HatTricks,omitempty"`
	HeadbuttGoals                int64   `json:"HeadbuttGoals,omitempty"`
	HighestArenaMVPStreak        int64   `json:"HighestArenaMVPStreak,omitempty"`
	HighestArenaWinStreak        int64   `json:"HighestArenaWinStreak,omitempty"`
	HighestPoints                int64   `json:"HighestPoints,omitempty"`
	HighestSaves                 int64   `json:"HighestSaves,omitempty"`
	HighestStuns                 int64   `json:"HighestStuns,omitempty"`
	Interceptions                int64   `json:"Interceptions,omitempty"`
	Passes                       int64   `json:"Passes,omitempty"`
	Points                       int64   `json:"Points,omitempty"`
	PossessionTime               float64 `json:"PossessionTime,omitempty"`
	PunchesReceived              int64   `json:"PunchesReceived,omitempty"`
	Saves                        int64   `json:"Saves,omitempty"`
	SavesPerGame                 float64 `json:"SavesPerGame,omitempty"`
	ShotsOnGoal                  int64   `json:"ShotsOnGoal,omitempty"`
	ShotsOnGoalAgainst           int64   `json:"ShotsOnGoalAgainst,omitempty"`
	Steals                       int64   `json:"Steals,omitempty"`
	Stuns                        int64   `json:"Stuns,omitempty"`
	StunsPerGame                 float64 `json:"StunsPerGame,omitempty"`
	ThreePointGoals              int64   `json:"ThreePointGoals,omitempty"`
	TopSpeedsTotal               float64 `json:"TopSpeedsTotal,omitempty"`
	TwoPointGoals                int64   `json:"TwoPointGoals,omitempty"`
	XP                           float64 `json:"XP,omitempty"`
}

type MatchGoal struct {
	GoalTime              float64 `json:"round_clock_secs"`
	GoalType              string  `json:"goal_type"`
	DisplayName           string  `json:"player_display_name"`
	TeamID                int64   `json:"player_team_id"`
	XPID                  EvrId   `json:"player_user_id"`
	PrevPlayerDisplayName string  `json:"prev_player_display_name"`
	PrevPlayerTeamID      int64   `json:"prev_player_team_id"`
	PrevPlayerXPID        EvrId   `json:"prev_player_user_id"`
	WasHeadbutt           bool    `json:"was_headbutt"`
	PointsValue           int     `json:"points_value"`
}

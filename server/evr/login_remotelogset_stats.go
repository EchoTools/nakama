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
	ArenaLosses                  int64   `json:"ArenaLosses,omitempty" op:"add"`
	ArenaMVPPercentage           float64 `json:"ArenaMVPPercentage,omitempty" op:"rep"`
	ArenaMVPs                    int64   `json:"ArenaMVPs,omitempty" op:"add"`
	ArenaWinPercentage           float64 `json:"ArenaWinPercentage,omitempty" op:"rep"`
	ArenaWins                    int64   `json:"ArenaWins,omitempty" op:"add"`
	Assists                      int64   `json:"Assists,omitempty" op:"add"`
	AssistsPerGame               float64 `json:"AssistsPerGame,omitempty" op:"avg"`
	AveragePointsPerGame         float64 `json:"AveragePointsPerGame,omitempty" op:"avg"`
	AveragePossessionTimePerGame float64 `json:"AveragePossessionTimePerGame,omitempty" op:"avg"`
	AverageTopSpeedPerGame       float64 `json:"AverageTopSpeedPerGame,omitempty" op:"avg"`
	BlockPercentage              float64 `json:"BlockPercentage,omitempty" op:"rep"`
	Blocks                       int64   `json:"Blocks,omitempty" op:"add"`
	BounceGoals                  int64   `json:"BounceGoals,omitempty" op:"add"`
	BumperShots                  int64   `json:"BumperShots,omitempty" op:"add"`
	Catches                      int64   `json:"Catches,omitempty" op:"add"`
	Clears                       int64   `json:"Clears,omitempty" op:"add"`
	CurrentArenaMVPStreak        int64   `json:"CurrentArenaMVPStreak,omitempty" op:"rep"`
	CurrentArenaWinStreak        int64   `json:"CurrentArenaWinStreak,omitempty" op:"rep"`
	GoalSavePercentage           float64 `json:"GoalSavePercentage,omitempty" op:"rep"`
	GoalScorePercentage          float64 `json:"GoalScorePercentage,omitempty" op:"rep"`
	Goals                        int64   `json:"Goals,omitempty" op:"add"`
	GoalsPerGame                 float64 `json:"GoalsPerGame,omitempty" op:"avg"`
	HatTricks                    int64   `json:"HatTricks,omitempty" op:"add"`
	HeadbuttGoals                int64   `json:"HeadbuttGoals,omitempty" op:"add"`
	HighestArenaMVPStreak        int64   `json:"HighestArenaMVPStreak,omitempty" op:"max"`
	HighestArenaWinStreak        int64   `json:"HighestArenaWinStreak,omitempty" op:"max"`
	HighestPoints                int64   `json:"HighestPoints,omitempty" op:"max"`
	HighestSaves                 int64   `json:"HighestSaves,omitempty" op:"max"`
	HighestStuns                 int64   `json:"HighestStuns,omitempty" op:"max"`
	Interceptions                int64   `json:"Interceptions,omitempty" op:"add"`
	Passes                       int64   `json:"Passes,omitempty" op:"add"`
	Points                       int64   `json:"Points,omitempty" op:"add"`
	PossessionTime               float64 `json:"PossessionTime,omitempty" op:"add"`
	PunchesReceived              int64   `json:"PunchesReceived,omitempty" op:"add"`
	Saves                        int64   `json:"Saves,omitempty" op:"add"`
	SavesPerGame                 float64 `json:"SavesPerGame,omitempty" op:"avg"`
	ShotsOnGoal                  int64   `json:"ShotsOnGoal,omitempty" op:"add"`
	ShotsOnGoalAgainst           int64   `json:"ShotsOnGoalAgainst,omitempty" op:"add"`
	Steals                       int64   `json:"Steals,omitempty" op:"add"`
	Stuns                        int64   `json:"Stuns,omitempty" op:"add"`
	StunsPerGame                 float64 `json:"StunsPerGame,omitempty" op:"avg"`
	ThreePointGoals              int64   `json:"ThreePointGoals,omitempty" op:"add"`
	TopSpeedsTotal               float64 `json:"TopSpeedsTotal,omitempty" op:"add"`
	TwoPointGoals                int64   `json:"TwoPointGoals,omitempty" op:"add"`
	XP                           float64 `json:"XP,omitempty" op:"add"`
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

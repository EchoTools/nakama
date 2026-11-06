package evr

import (
	"reflect"
	"strings"
)

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

	// Combat. Key names are the game's, as sent in match_stats; the eight from
	// CombatHealingReceived down are sent only here. Every name is also a
	// CombatStatistics field (TestCombatStatistics_IsTheSchemaTheRemoteLogStructsMirror).
	//
	// Every combat field is float64, including the counts. json.Unmarshal fails
	// the whole message when a decimal such as 3.000000 meets an int64 field,
	// and the game's number formatting is only observed for the keys in the
	// testdata fixtures. A float64 field accepts both forms.
	CombatAssists                  float64 `json:"CombatAssists,omitempty"`
	CombatBestEliminationStreak    float64 `json:"CombatBestEliminationStreak,omitempty"`
	CombatDamage                   float64 `json:"CombatDamage,omitempty"`
	CombatDamageAbsorbed           float64 `json:"CombatDamageAbsorbed,omitempty"`
	CombatDamageTaken              float64 `json:"CombatDamageTaken,omitempty"`
	CombatDeaths                   float64 `json:"CombatDeaths,omitempty"`
	CombatEliminations             float64 `json:"CombatEliminations,omitempty"`
	CombatHeadshotKills            float64 `json:"CombatHeadshotKills,omitempty"`
	CombatHealing                  float64 `json:"CombatHealing,omitempty"`
	CombatHillCaptures             float64 `json:"CombatHillCaptures,omitempty"`
	CombatHillDefends              float64 `json:"CombatHillDefends,omitempty"`
	CombatKills                    float64 `json:"CombatKills,omitempty"`
	CombatObjectiveDamage          float64 `json:"CombatObjectiveDamage,omitempty"`
	CombatObjectiveEliminations    float64 `json:"CombatObjectiveEliminations,omitempty"`
	CombatObjectiveTime            float64 `json:"CombatObjectiveTime,omitempty"`
	CombatSoloKills                float64 `json:"CombatSoloKills,omitempty"`
	CombatStuns                    float64 `json:"CombatStuns,omitempty"`
	CombatTeammateHealing          float64 `json:"CombatTeammateHealing,omitempty"`
	CombatHealingReceived          float64 `json:"CombatHealingReceived,omitempty"`
	CombatStunsReceived            float64 `json:"CombatStunsReceived,omitempty"`
	CombatScans                    float64 `json:"CombatScans,omitempty"`
	CombatScansReceived            float64 `json:"CombatScansReceived,omitempty"`
	CombatSuicides                 float64 `json:"CombatSuicides,omitempty"`
	CombatSelfDamage               float64 `json:"CombatSelfDamage,omitempty"`
	CombatCurrentEliminationStreak float64 `json:"CombatCurrentEliminationStreak,omitempty"`
	CombatEliminationDeathRatio    float64 `json:"CombatEliminationDeathRatio,omitempty"`
}

// Values returns every non-zero stat keyed by the game's own key name. It is
// what a match record keeps: the keys the game sent, without the zero-valued
// fields of the other mode.
func (s MatchStats) Values() map[string]float64 {
	v := reflect.ValueOf(s)
	t := v.Type()
	out := make(map[string]float64)
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		f := v.Field(i)
		var x float64
		switch {
		case f.CanInt():
			x = float64(f.Int())
		case f.CanFloat():
			x = f.Float()
		default:
			continue
		}
		if x != 0 {
			out[name] = x
		}
	}
	return out
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

	// Combat. Key names are the game's CR15NetProfileStats names, each also a
	// CombatStatistics field (core_account_statistics.go), which is the combat
	// schema: it cannot parse these bare numbers itself, so this mirrors its
	// names, and TestCombatStatistics_IsTheSchemaTheRemoteLogStructsMirror pins
	// the two together. float64 for the reason given on MatchStats.
	//
	// op:"-" keeps every one of them off the leaderboards from this path. The
	// Combat* boards have one writer, the game server's profile update
	// (EventServerProfileUpdate, evr_pipeline_login.go), which is authoritative
	// for the match and sets each board to what it read plus the match. The
	// TeamSkillRating* boards are the opposite: their one writer is the
	// remote-log path (processPostMatchMessages), and StatisticsToEntries drops
	// them from the profile update (IsTeamSkillRatingStat). A board
	// ID does not include the operator, so an "add" here would increment that
	// same row and every combat match would count twice
	// (TestCombatStats_OneWriterPerLeaderboardRow).
	CombatAssists                      float64 `json:"CombatAssists,omitempty" op:"-"`
	CombatAverageEliminationDeathRatio float64 `json:"CombatAverageEliminationDeathRatio,omitempty" op:"-"`
	CombatBestEliminationStreak        float64 `json:"CombatBestEliminationStreak,omitempty" op:"-"`
	CombatDamage                       float64 `json:"CombatDamage,omitempty" op:"-"`
	CombatDamageAbsorbed               float64 `json:"CombatDamageAbsorbed,omitempty" op:"-"`
	CombatDamageTaken                  float64 `json:"CombatDamageTaken,omitempty" op:"-"`
	CombatDeaths                       float64 `json:"CombatDeaths,omitempty" op:"-"`
	CombatEliminations                 float64 `json:"CombatEliminations,omitempty" op:"-"`
	CombatHeadshotKills                float64 `json:"CombatHeadshotKills,omitempty" op:"-"`
	CombatHealing                      float64 `json:"CombatHealing,omitempty" op:"-"`
	CombatHillCaptures                 float64 `json:"CombatHillCaptures,omitempty" op:"-"`
	CombatHillDefends                  float64 `json:"CombatHillDefends,omitempty" op:"-"`
	CombatKills                        float64 `json:"CombatKills,omitempty" op:"-"`
	CombatLosses                       float64 `json:"CombatLosses,omitempty" op:"-"`
	CombatMVPs                         float64 `json:"CombatMVPs,omitempty" op:"-"`
	CombatObjectiveDamage              float64 `json:"CombatObjectiveDamage,omitempty" op:"-"`
	CombatObjectiveEliminations        float64 `json:"CombatObjectiveEliminations,omitempty" op:"-"`
	CombatObjectiveTime                float64 `json:"CombatObjectiveTime,omitempty" op:"-"`
	CombatPayloadGamesPlayed           float64 `json:"CombatPayloadGamesPlayed,omitempty" op:"-"`
	CombatPayloadWinPercentage         float64 `json:"CombatPayloadWinPercentage,omitempty" op:"-"`
	CombatPayloadWins                  float64 `json:"CombatPayloadWins,omitempty" op:"-"`
	CombatPointCaptureGamesPlayed      float64 `json:"CombatPointCaptureGamesPlayed,omitempty" op:"-"`
	CombatPointCaptureWinPercentage    float64 `json:"CombatPointCaptureWinPercentage,omitempty" op:"-"`
	CombatPointCaptureWins             float64 `json:"CombatPointCaptureWins,omitempty" op:"-"`
	CombatSoloKills                    float64 `json:"CombatSoloKills,omitempty" op:"-"`
	CombatStuns                        float64 `json:"CombatStuns,omitempty" op:"-"`
	CombatTeammateHealing              float64 `json:"CombatTeammateHealing,omitempty" op:"-"`
	CombatWinPercentage                float64 `json:"CombatWinPercentage,omitempty" op:"-"`
	CombatWins                         float64 `json:"CombatWins,omitempty" op:"-"`
	CombatTies                         float64 `json:"CombatTies,omitempty" op:"-"`
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

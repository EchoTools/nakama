package server

import (
	"testing"
)

func TestDefaultArenaStatsIsComplete(t *testing.T) {
	t.Skip()
}

var preshutdownProfileStatistics = []byte(`
{
	"ArenaLosses": {
		"cnt": 1,
		"op": "add",
		"val": 313
	},
	"ArenaMVPPercentage": {
		"cnt": 1,
		"op": "rep",
		"val": 8.762887
	},
	"ArenaMVPs": {
		"cnt": 1,
		"op": "add",
		"val": 51
	},
	"ArenaTies": {
		"cnt": 1,
		"op": "add",
		"val": 0
	},
	"ArenaWinPercentage": {
		"cnt": 1,
		"op": "rep",
		"val": 46.219933
	},
	"ArenaWins": {
		"cnt": 1,
		"op": "add",
		"val": 269
	},
	"Assists": {
		"cnt": 1,
		"op": "add",
		"val": 139
	},
	"AssistsPerGame": {
		"cnt": 515,
		"op": "avg",
		"val": 107.96878
	},
	"AveragePointsPerGame": {
		"cnt": 522,
		"op": "avg",
		"val": 624.45679
	},
	"AveragePossessionTimePerGame": {
		"cnt": 566,
		"op": "avg",
		"val": 14895.59
	},
	"AverageTopSpeedPerGame": {
		"cnt": 573,
		"op": "avg",
		"val": 24027.426
	},
	"BlockPercentage": {
		"cnt": 1,
		"op": "rep",
		"val": 0.088626295
	},
	"Blocks": {
		"cnt": 1,
		"op": "add",
		"val": 3
	},
	"BounceGoals": {
		"cnt": 1,
		"op": "add",
		"val": 32
	},
	"Catches": {
		"cnt": 1,
		"op": "add",
		"val": 752
	},
	"Clears": {
		"cnt": 1,
		"op": "add",
		"val": 958
	},
	"CurrentArenaMVPStreak": {
		"cnt": 1,
		"op": "add",
		"val": 30
	},
	"CurrentArenaWinStreak": {
		"cnt": 1,
		"op": "add",
		"val": 157
	},
	"Goals": {
		"cnt": 1,
		"op": "add",
		"val": 260
	},
	"GoalSavePercentage": {
		"cnt": 1,
		"op": "rep",
		"val": 8.0425386
	},
	"GoalScorePercentage": {
		"cnt": 1,
		"op": "rep",
		"val": 40.498444
	},
	"GoalsPerGame": {
		"cnt": 522,
		"op": "avg",
		"val": 249.85077
	},
	"HatTricks": {
		"cnt": 1,
		"op": "add",
		"val": 11
	},
	"HighestArenaMVPStreak": {
		"cnt": 1,
		"op": "max",
		"val": 30
	},
	"HighestArenaWinStreak": {
		"cnt": 1,
		"op": "max",
		"val": 157
	},
	"HighestPoints": {
		"cnt": 1,
		"op": "max",
		"val": 12
	},
	"HighestSaves": {
		"cnt": 1,
		"op": "max",
		"val": 3
	},
	"HighestStuns": {
		"cnt": 1,
		"op": "max",
		"val": 27
	},
	"Interceptions": {
		"cnt": 1,
		"op": "add",
		"val": 471
	},
	"JoustsWon": {
		"cnt": 1,
		"op": "add",
		"val": 0
	},
	"Level": {
		"cnt": 1,
		"op": "add",
		"val": 50
	},
	"OnePointGoals": {
		"cnt": 1,
		"op": "add",
		"val": 0
	},
	"Passes": {
		"cnt": 1,
		"op": "add",
		"val": 662
	},
	"Points": {
		"cnt": 1,
		"op": "add",
		"val": 630
	},
	"PossessionTime": {
		"cnt": 1,
		"op": "add",
		"val": 15164.576
	},
	"PunchesReceived": {
		"cnt": 1,
		"op": "add",
		"val": 3385
	},
	"Saves": {
		"cnt": 1,
		"op": "add",
		"val": 242
	},
	"SavesPerGame": {
		"cnt": 521,
		"op": "avg",
		"val": 235.14339
	},
	"ShotsOnGoal": {
		"cnt": 1,
		"op": "add",
		"val": 642
	},
	"ShotsOnGoalAgainst": {
		"cnt": 1,
		"op": "add",
		"val": 3009
	},
	"Steals": {
		"cnt": 1,
		"op": "add",
		"val": 234
	},
	"StunPercentage": {
		"cnt": 1,
		"op": "rep",
		"val": 0
	},
	"Stuns": {
		"cnt": 1,
		"op": "add",
		"val": 4632
	},
	"StunsPerGame": {
		"cnt": 573,
		"op": "avg",
		"val": 4961.7314
	},
	"ThreePointGoals": {
		"cnt": 1,
		"op": "add",
		"val": 110
	},
	"TopSpeedsTotal": {
		"cnt": 1,
		"op": "add",
		"val": 22009.957
	},
	"TwoPointGoals": {
		"cnt": 1,
		"op": "add",
		"val": 150
	},
	"XP": {
		"cnt": 1,
		"op": "add",
		"val": 199
	}
}
`)

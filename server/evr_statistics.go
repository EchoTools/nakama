package server

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/rating"
	"github.com/intinig/go-openskill/types"
	"go.uber.org/thriftrw/ptr"
)

const (
	TabletStatisticIntegerValue = iota
	TabletStatisticFloatValue

	GamesPlayedStatisticID      = "GamesPlayed"
	RankPercentileStatisticID   = "RankPercentile"
	SkillRatingMuStatisticID    = "SkillRatingMu"
	SkillRatingSigmaStatisticID = "SkillRatingSigma"
	LobbyTimeStatisticID        = "LobbyTime"
)

var tabletStatisticTypeMap = map[evr.Symbol]map[string]int{
	evr.ModeArenaPublic: {
		"ArenaLosses":                  TabletStatisticIntegerValue,
		"ArenaMVPPercentage":           TabletStatisticFloatValue,
		"ArenaMVPs":                    TabletStatisticIntegerValue,
		"ArenaTies":                    TabletStatisticIntegerValue,
		"ArenaWinPercentage":           TabletStatisticFloatValue,
		"ArenaWins":                    TabletStatisticIntegerValue,
		"Assists":                      TabletStatisticIntegerValue,
		"AssistsPerGame":               TabletStatisticFloatValue,
		"AveragePointsPerGame":         TabletStatisticFloatValue,
		"AveragePossessionTimePerGame": TabletStatisticFloatValue,
		"AverageTopSpeedPerGame":       TabletStatisticFloatValue,
		"BlockPercentage":              TabletStatisticFloatValue,
		"Blocks":                       TabletStatisticIntegerValue,
		"BounceGoals":                  TabletStatisticIntegerValue,
		"BumperShots":                  TabletStatisticIntegerValue,
		"Catches":                      TabletStatisticIntegerValue,
		"Clears":                       TabletStatisticIntegerValue,
		"CurrentArenaMVPStreak":        TabletStatisticIntegerValue,
		"CurrentArenaWinStreak":        TabletStatisticIntegerValue,
		"Goals":                        TabletStatisticIntegerValue,
		"GoalSavePercentage":           TabletStatisticFloatValue,
		"GoalScorePercentage":          TabletStatisticFloatValue,
		"GoalsPerGame":                 TabletStatisticFloatValue,
		"HatTricks":                    TabletStatisticIntegerValue,
		"HeadbuttGoals":                TabletStatisticIntegerValue,
		"HighestArenaMVPStreak":        TabletStatisticIntegerValue,
		"HighestArenaWinStreak":        TabletStatisticIntegerValue,
		"HighestPoints":                TabletStatisticIntegerValue,
		"HighestSaves":                 TabletStatisticIntegerValue,
		"HighestStuns":                 TabletStatisticIntegerValue,
		"Interceptions":                TabletStatisticIntegerValue,
		"JoustsWon":                    TabletStatisticIntegerValue,
		"Level":                        TabletStatisticIntegerValue,
		"OnePointGoals":                TabletStatisticIntegerValue,
		"Passes":                       TabletStatisticIntegerValue,
		"Points":                       TabletStatisticIntegerValue,
		"PossessionTime":               TabletStatisticFloatValue,
		"PunchesReceived":              TabletStatisticIntegerValue,
		"Saves":                        TabletStatisticIntegerValue,
		"SavesPerGame":                 TabletStatisticFloatValue,
		"ShotsOnGoal":                  TabletStatisticIntegerValue,
		"ShotsOnGoalAgainst":           TabletStatisticIntegerValue,
		"Steals":                       TabletStatisticIntegerValue,
		"StunPercentage":               TabletStatisticFloatValue,
		"Stuns":                        TabletStatisticIntegerValue,
		"StunsPerGame":                 TabletStatisticFloatValue,
		"ThreePointGoals":              TabletStatisticIntegerValue,
		"TopSpeedsTotal":               TabletStatisticFloatValue,
		"TwoPointGoals":                TabletStatisticIntegerValue,
		"XP":                           TabletStatisticIntegerValue,
		GamesPlayedStatisticID:         TabletStatisticIntegerValue,

		SkillRatingSigmaStatisticID: TabletStatisticFloatValue,
		SkillRatingMuStatisticID:    TabletStatisticFloatValue,
		RankPercentileStatisticID:   TabletStatisticFloatValue,
	},
	evr.ModeCombatPublic: {
		"CombatAssists":                      TabletStatisticIntegerValue,
		"CombatAverageEliminationDeathRatio": TabletStatisticFloatValue,
		"CombatBestEliminationStreak":        TabletStatisticIntegerValue,
		"CombatDamage":                       TabletStatisticFloatValue,
		"CombatDamageAbsorbed":               TabletStatisticFloatValue,
		"CombatDamageTaken":                  TabletStatisticFloatValue,
		"CombatDeaths":                       TabletStatisticIntegerValue,
		"CombatEliminations":                 TabletStatisticIntegerValue,
		"CombatHeadshotKills":                TabletStatisticIntegerValue,
		"CombatHealing":                      TabletStatisticFloatValue,
		"CombatHillCaptures":                 TabletStatisticIntegerValue,
		"CombatHillDefends":                  TabletStatisticIntegerValue,
		"CombatKills":                        TabletStatisticIntegerValue,
		"CombatLosses":                       TabletStatisticIntegerValue,
		"CombatMVPs":                         TabletStatisticIntegerValue,
		"CombatObjectiveDamage":              TabletStatisticFloatValue,
		"CombatObjectiveEliminations":        TabletStatisticIntegerValue,
		"CombatObjectiveTime":                TabletStatisticFloatValue,
		"CombatPayloadGamesPlayed":           TabletStatisticIntegerValue,
		"CombatPayloadWinPercentage":         TabletStatisticFloatValue,
		"CombatPayloadWins":                  TabletStatisticIntegerValue,
		"CombatPointCaptureGamesPlayed":      TabletStatisticIntegerValue,
		"CombatPointCaptureWinPercentage":    TabletStatisticFloatValue,
		"CombatPointCaptureWins":             TabletStatisticIntegerValue,
		"CombatSoloKills":                    TabletStatisticIntegerValue,
		"CombatStuns":                        TabletStatisticIntegerValue,
		"CombatTeammateHealing":              TabletStatisticFloatValue,
		"CombatWinPercentage":                TabletStatisticFloatValue,
		"CombatWins":                         TabletStatisticIntegerValue,
		"Level":                              TabletStatisticIntegerValue,
		"XP":                                 TabletStatisticIntegerValue,
		GamesPlayedStatisticID:               TabletStatisticIntegerValue,
		SkillRatingSigmaStatisticID:          TabletStatisticFloatValue,
		SkillRatingMuStatisticID:             TabletStatisticFloatValue,
		RankPercentileStatisticID:            TabletStatisticFloatValue,
		LobbyTimeStatisticID:                 TabletStatisticFloatValue,
	},
	evr.ModeSocialPublic: {
		LobbyTimeStatisticID: TabletStatisticFloatValue,
	},
	evr.ModeSocialPrivate: {
		LobbyTimeStatisticID: TabletStatisticFloatValue,
	},
	evr.ModeCombatPrivate: {
		LobbyTimeStatisticID: TabletStatisticFloatValue,
	},
	evr.ModeArenaPrivate: {
		LobbyTimeStatisticID: TabletStatisticFloatValue,
	},
}

func ScoreToTableStatistic(mode evr.Symbol, name string, score, subscore int64) any {
	switch tabletStatisticTypeMap[mode][name] {
	case TabletStatisticIntegerValue:
		return TabletStatisticInteger{}.ScoreToValue(score, subscore)
	case TabletStatisticFloatValue:
		return TabletStatisticFloat{}.ScoreToValue(score, subscore)
	default:
		return nil
	}
}

func TabletStatisticToScore(mode evr.Symbol, name string, v any) (int64, int64) {
	switch tabletStatisticTypeMap[mode][name] {
	case TabletStatisticIntegerValue:
		return TabletStatisticInteger{}.ValueToScore(v)
	case TabletStatisticFloatValue:
		return TabletStatisticFloat{}.ValueToScore(v)
	default:
		return 0, 0
	}
}

type TabletStatisticFloat struct{}

func (TabletStatisticFloat) ScoreToValue(score, subscore int64) any {
	f, _ := strconv.ParseFloat(fmt.Sprintf("%d.%d", score, subscore), 64)
	return f
}

func (TabletStatisticFloat) ValueToScore(v any) (int64, int64) {
	if v == nil {
		return 0, 0
	}
	switch v := v.(type) {
	case float64:
		str := strconv.FormatFloat(v, 'f', -1, 64)
		s := strings.Split(str, ".")

		whole, _ := strconv.ParseInt(s[0], 10, 64)
		fractional, _ := strconv.ParseInt(s[1], 10, 64)
		return whole, fractional
	case int64:
		return v, 0
	default:
		return 0, 0
	}
}

type TabletStatisticInteger struct{}

func (TabletStatisticInteger) ValueToScore(v any) (int64, int64) {
	if v == nil {
		return 0, 0
	}
	switch v := v.(type) {
	case float64:
		return int64(v), 0
	case int64:
		return v, 0
	default:
		return 0, 0
	}
}

func (TabletStatisticInteger) ScoreToValue(score, subscore int64) any {
	return int64(score)
}

func MatchmakingRatingLoad(ctx context.Context, nk runtime.NakamaModule, groupID, userID string, mode evr.Symbol) (types.Rating, error) {
	// Look for an existing account.

	var sigma, mu float64

	structMap := map[string]*float64{
		SkillRatingMuStatisticID: &sigma,
		"SkillRatingSigma":       &mu,
	}

	for statName, ptr := range structMap {
		boardID := StatisticBoardID(groupID, mode, statName, "alltime")

		_, records, _, _, err := nk.LeaderboardRecordsList(ctx, boardID, []string{userID}, 10000, "", 0)
		if err != nil {
			continue
		}

		if len(records) == 0 {
			return NewDefaultRating(), nil
		}

		record := records[0]
		*ptr = ScoreToValue(record.Score, record.Subscore)
	}
	if sigma == 0 || mu == 0 {
		return NewDefaultRating(), nil
	}
	return rating.NewWithOptions(&types.OpenSkillOptions{
		Mu:    ptr.Float64(mu),
		Sigma: ptr.Float64(sigma),
	}), nil
}

func MatchmakingRatingStore(ctx context.Context, nk runtime.NakamaModule, userID, displayName, groupID string, mode evr.Symbol, rating types.Rating) error {

	scores := map[string]float64{
		StatisticBoardID(groupID, mode, SkillRatingSigmaStatisticID, "alltime"): rating.Sigma,
		StatisticBoardID(groupID, mode, SkillRatingMuStatisticID, "alltime"):    rating.Mu,
	}

	for id, value := range scores {
		score, subscore := ValueToScore(value)

		// Write the record
		if _, err := nk.LeaderboardRecordWrite(ctx, id, userID, displayName, score, subscore, nil, nil); err != nil {
			// Try to create the leaderboard
			err = nk.LeaderboardCreate(ctx, id, true, "desc", "set", "", nil, true)
			if err != nil {
				return fmt.Errorf("Leaderboard create error: %w", err)
			} else {
				// Retry the write
				_, err := nk.LeaderboardRecordWrite(ctx, id, userID, displayName, score, subscore, nil, nil)
				if err != nil {
					return fmt.Errorf("Leaderboard record write error: %w", err)
				}
			}
		}
	}

	return nil
}

func MatchmakingRankPercentileLoad(ctx context.Context, nk runtime.NakamaModule, userID, groupID string, mode evr.Symbol) (percentile float64, err error) {

	boardID := StatisticBoardID(groupID, mode, RankPercentileStatisticID, "alltime")

	_, records, _, _, err := nk.LeaderboardRecordsList(ctx, boardID, []string{userID}, 10000, "", 0)
	if err != nil {
		return ServiceSettings().Matchmaking.RankPercentile.Default, nil
	}

	if len(records) == 0 {
		return ServiceSettings().Matchmaking.RankPercentile.Default, nil
	}

	return ScoreToValue(records[0].Score, records[0].Subscore), nil
}

func MatchmakingRankPercentileStore(ctx context.Context, nk runtime.NakamaModule, userID, username string, groupID string, mode evr.Symbol, percentile float64) error {

	id := StatisticBoardID(groupID, mode, RankPercentileStatisticID, "alltime")

	score, subscore := ValueToScore(percentile)

	// Write the record
	_, err := nk.LeaderboardRecordWrite(ctx, id, userID, username, score, subscore, nil, nil)

	if err != nil {
		// Try to create the leaderboard
		err = nk.LeaderboardCreate(ctx, id, true, "asc", "set", "", nil, true)

		if err != nil {
			return fmt.Errorf("Leaderboard create error: %w", err)
		} else {
			// Retry the write
			_, err := nk.LeaderboardRecordWrite(ctx, id, userID, username, score, subscore, nil, nil)
			if err != nil {
				return fmt.Errorf("Leaderboard record write error: %w", err)
			}
		}
	}

	return nil
}

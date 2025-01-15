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
	GameServerTimeStatisticsID  = "GameServerTime"
	EarlyQuitStatisticID        = "EarlyQuits"
)

func MatchmakingRatingLoad(ctx context.Context, nk runtime.NakamaModule, userID, groupID string, mode evr.Symbol) (types.Rating, error) {
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

func MatchmakingRankPercentileStore(ctx context.Context, nk runtime.NakamaModule, userID, username, groupID string, mode evr.Symbol, percentile float64) error {

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

func ValueToScore(v float64) (int64, int64) {
	// If it's a whole number, return it as such.
	if v == float64(int64(v)) {
		return int64(v), 0
	}

	// Otherwise, split the float into whole and fractional parts.
	str := strconv.FormatFloat(float64(v), 'f', -1, 64)
	s := strings.Split(str, ".")

	// Parse the whole and fractional parts as integers.
	whole, _ := strconv.ParseInt(s[0], 10, 64)
	fractional, _ := strconv.ParseInt(s[1], 10, 64)

	return whole, fractional
}

func ScoreToValue(score int64, subscore int64) float64 {
	// If there's no subscore, return the score as a whole number.
	if subscore == 0 {
		return float64(score)
	}

	// Otherwise, combine the score and subscore as a float.
	f, _ := strconv.ParseFloat(fmt.Sprintf("%d.%d", score, subscore), 64)
	return f
}

func StatisticBoardID(groupID string, mode evr.Symbol, statName string, resetSchedule evr.ResetSchedule) string {
	return fmt.Sprintf("%s:%s:%s:%s", groupID, mode.String(), statName, resetSchedule)
}

func ParseStatisticBoardID(id string) (groupID string, mode evr.Symbol, statName string, resetSchedule string, err error) {
	parts := strings.SplitN(id, ":", 4)
	if len(parts) != 4 {
		err = fmt.Errorf("invalid leaderboard ID: %s", id)
		return
	}
	return parts[0], evr.ToSymbol(parts[1]), parts[2], parts[3], nil
}

func ResetScheduleToCron(resetSchedule evr.ResetSchedule) string {
	switch resetSchedule {
	case evr.ResetScheduleDaily:
		return "0 16 * * *"
	case evr.ResetScheduleWeekly:
		return "0 16 * * 4"
	case evr.ResetScheduleAllTime:
		fallthrough
	default:
		return ""
	}
}

func OperatorToLeaderboardOperator(op int) string {
	switch op {
	case LeaderboardOperatorIncrement:
		return "incr"
	case LeaderboardOperatorDecrement:
		return "decr"
	case LeaderboardOperatorBest:
		return "best"
	case LeaderboardOperatorSet:
		return "set"
	default:
		return "set"
	}
}

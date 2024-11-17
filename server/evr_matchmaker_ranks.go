package server

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

type Periodicity string

var (
	percentileStateIDsByMode = map[evr.Symbol][]string{
		evr.ModeArenaPublic: {
			"ArenaGamesPlayed",
			"ArenaWins",
			"ArenaLosses",
			"ArenaWinPercentage",
			"AssistsPerGame",
			"AveragePointsPerGame",
			"AverageTopSpeedPerGame",
			"BlockPercentage",
			"GoalScorePercentage",
			"GoalsPerGame",
		},
	}
)

func RecalculatePlayerRankPercentile(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, userID string, mode evr.Symbol, periodicity string) (float64, map[string]*api.LeaderboardRecord, error) {

	if _, ok := percentileStateIDsByMode[mode]; !ok {
		return 0.5, nil, nil
	}

	boardIDs := make([]string, 0, len(percentileStateIDsByMode[mode]))
	for _, id := range percentileStateIDsByMode[mode] {
		boardIDs = append(boardIDs, fmt.Sprintf("%s:%s:%s", mode.String(), id, periodicity))
	}

	percentile, err := LeaderboardRankPercentile(ctx, logger, nk, userID, boardIDs)
	if err != nil {
		return 0.0, nil, err
	}

	return percentile, nil, nil
}

func LeaderboardRankPercentile(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, userID string, boardIDs []string) (float64, error) {

	percentiles := make([]float64, 0, len(boardIDs))

	for _, boardID := range boardIDs {

		records, _, _, _, err := nk.LeaderboardRecordsList(ctx, boardID, []string{userID}, 10000, "", 0)
		if err != nil {
			continue
		}

		if len(records) == 0 {
			continue
		}

		// Find the user's rank.
		var rank int64 = -1
		for _, record := range records {
			if record.OwnerId == userID {
				rank = record.Rank
				break
			}
		}
		if rank == -1 {
			continue
		}

		// Calculate the percentile.
		percentiles = append(percentiles, float64(rank)/float64(len(records)))

	}

	percentile := 0.0

	if len(percentiles) == 0 {
		return 0.3, nil
	}

	for _, p := range percentiles {
		percentile += p
	}
	percentile /= float64(len(percentiles))

	return percentile, nil
}

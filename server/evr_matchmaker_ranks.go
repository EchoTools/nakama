package server

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

func RecalculatePlayerRankPercentile(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, userID string, mode evr.Symbol, periodicity string, defaultRankPercentile float64, boardNameWeights map[string]float64) (float64, error) {

	percentiles := make([]float64, 0, len(boardNameWeights))

	for boardName, weight := range boardNameWeights {

		boardID := fmt.Sprintf("%s:%s:%s", mode.String(), boardName, periodicity)

		records, _, _, _, err := nk.LeaderboardRecordsList(ctx, boardID, []string{userID}, 10000, "", 0)
		if err != nil {
			continue
		}

		if len(records) == 0 {
			percentiles = append(percentiles, defaultRankPercentile)
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

		percentile := float64(rank) / float64(len(records))
		// Calculate the percentile.
		percentiles = append(percentiles, percentile*weight)

	}

	percentile := 0.0

	if len(percentiles) == 0 {
		return defaultRankPercentile, nil
	}

	for _, p := range percentiles {
		percentile += p
	}
	percentile /= float64(len(percentiles))

	return percentile, nil
}

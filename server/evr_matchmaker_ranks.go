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
	weights := make([]float64, 0, len(boardNameWeights))

	for boardName, weight := range boardNameWeights {

		boardID := fmt.Sprintf("%s:%s:%s", mode.String(), boardName, periodicity)

		records, _, _, _, err := nk.LeaderboardRecordsList(ctx, boardID, []string{userID}, 10000, "", 0)
		if err != nil {
			continue
		}

		if len(records) == 0 {
			percentiles = append(percentiles, defaultRankPercentile)
			weights = append(weights, weight)
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

		weights = append(weights, weight)
		percentiles = append(percentiles, percentile)

	}

	percentile := 0.0

	if len(percentiles) == 0 {
		return defaultRankPercentile, nil
	}

	for _, p := range percentiles {
		percentile += p
	}
	percentile /= float64(len(percentiles))

	percentile, err := normalizedWeightedAverage(percentiles, weights)
	if err != nil {
		return defaultRankPercentile, err
	}

	return percentile, nil
}

func normalizedWeightedAverage(values, weights []float64) (float64, error) {
	if len(values) != len(weights) {
		return 0, fmt.Errorf("values and weights must have the same length")
	}

	// Normalize weights to sum to 1
	var weightSum float64
	for _, w := range weights {
		weightSum += w
	}

	if weightSum == 0 {
		return 0, fmt.Errorf("sum of weights must not be zero")
	}

	var sum float64
	for i := range values {
		normalizedWeight := weights[i] / weightSum
		sum += values[i] * normalizedWeight
	}

	return sum, nil
}

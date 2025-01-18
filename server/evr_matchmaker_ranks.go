package server

import (
	"context"
	"database/sql"
	"fmt"

	"go.uber.org/zap"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

func CalculateSmoothedPlayerRankPercentile(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, userID, groupID string, mode evr.Symbol) (float64, error) {
	settings := ServiceSettings().Matchmaking.RankPercentile

	if len(settings.LeaderboardWeights) == 0 {
		return settings.Default, nil
	}

	if mode == evr.ModeSocialPublic {
		mode = evr.ModeArenaPublic
	}

	dampingPercentile, err := RecalculatePlayerRankPercentile(ctx, logger, nk, userID, groupID, mode, settings.ResetScheduleDamper, settings.Default, settings.LeaderboardWeights[mode])
	if err != nil {
		return 0.0, fmt.Errorf("failed to get damping percentile: %w", err)
	}

	activePercentile, err := RecalculatePlayerRankPercentile(ctx, logger, nk, userID, groupID, mode, settings.ResetSchedule, dampingPercentile, settings.LeaderboardWeights[mode])
	if err != nil {
		return 0.0, fmt.Errorf("failed to get active percentile: %w", err)
	}

	percentile := activePercentile + (dampingPercentile-activePercentile)*settings.DampeningFactor

	return percentile, nil
}

func RecalculatePlayerRankPercentile(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, userID, groupID string, mode evr.Symbol, resetSchedule evr.ResetSchedule, defaultRankPercentile float64, boardNameWeights map[string]float64) (float64, error) {

	percentiles := make([]float64, 0, len(boardNameWeights))
	weights := make([]float64, 0, len(boardNameWeights))

	for boardName, weight := range boardNameWeights {

		boardID := StatisticBoardID(groupID, mode, boardName, resetSchedule)

		_, ownerRecords, _, _, err := nk.LeaderboardRecordsList(ctx, boardID, []string{userID}, 10000, "", 0)
		if err != nil {
			continue
		}

		if len(ownerRecords) == 0 {
			percentiles = append(percentiles, defaultRankPercentile)
			weights = append(weights, weight)
			continue
		}

		// Find the user's rank.
		var rank int64 = -1
		for _, record := range ownerRecords {
			if record.OwnerId == userID {
				rank = record.Rank
				break
			}
		}
		if rank == -1 {
			continue
		}

		percentile := float64(rank) / float64(len(ownerRecords))

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

func retrieveLatestLeaderboardRecords(ctx context.Context, db *sql.DB, userID string) (map[string]float64, error) {

	query := `
	WITH ranked_records AS (
		SELECT 
			leaderboard_id,
			owner_id,
			expiry_time,
			score,
			subscore,
			ROW_NUMBER() OVER (
				PARTITION BY leaderboard_id 
				ORDER BY expiry_time DESC
			) AS rank
		FROM leaderboard_record
		WHERE owner_id = $1
	)
	SELECT leaderboard_id, owner_id, expiry_time, score, subscore
	FROM ranked_records
	WHERE rank = 1;`

	rows, err := db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to query latest leaderboard records: %w", err)
	}
	defer rows.Close()

	records := make(map[string]float64)

	for rows.Next() {
		var leaderboardID, ownerID string
		var expiryTime int64
		var score, subscore int64

		err = rows.Scan(&leaderboardID, &ownerID, &expiryTime, &score, &subscore)
		if err != nil {
			return nil, fmt.Errorf("failed to scan latest leaderboard record: %w", err)
		}

		records[leaderboardID] = ScoreToValue(score, subscore)

	}

	return records, nil
}

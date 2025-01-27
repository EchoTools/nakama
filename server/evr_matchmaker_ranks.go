package server

import (
	"context"
	"database/sql"
	"fmt"

	"go.uber.org/zap"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

func CalculateSmoothedPlayerRankPercentile(ctx context.Context, logger *zap.Logger, db *sql.DB, nk runtime.NakamaModule, userID, groupID string, mode evr.Symbol) (float64, error) {
	settings := ServiceSettings().Matchmaking.RankPercentile

	if len(settings.LeaderboardWeights) == 0 {
		return settings.Default, nil
	}

	if mode == evr.ModeSocialPublic {
		mode = evr.ModeArenaPublic
	}

	dampingPercentile, err := RecalculatePlayerRankPercentile(ctx, logger, db, nk, userID, groupID, mode, settings.ResetScheduleDamper, settings.Default, settings.LeaderboardWeights[mode])
	if err != nil {
		return 0.0, fmt.Errorf("failed to get damping percentile: %w", err)
	}

	activePercentile, err := RecalculatePlayerRankPercentile(ctx, logger, db, nk, userID, groupID, mode, settings.ResetSchedule, dampingPercentile, settings.LeaderboardWeights[mode])
	if err != nil {
		return 0.0, fmt.Errorf("failed to get active percentile: %w", err)
	}

	percentile := activePercentile + (dampingPercentile-activePercentile)*settings.DampeningFactor

	return percentile, nil
}

func RecalculatePlayerRankPercentile(ctx context.Context, logger *zap.Logger, db *sql.DB, nk runtime.NakamaModule, userID, groupID string, mode evr.Symbol, resetSchedule evr.ResetSchedule, defaultRankPercentile float64, boardNameWeights map[string]float64) (float64, error) {

	boardWeights := make(map[string]float64)
	for boardName, weight := range boardNameWeights {
		boardWeights[StatisticBoardID(groupID, mode, boardName, resetSchedule)] = weight
	}

	percentile, err := retrieveRankPercentile(ctx, db, userID, boardWeights)
	if err != nil {
		return defaultRankPercentile, fmt.Errorf("failed to retrieve leaderboard ranks: %w", err)
	}

	return percentile, nil
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

func retrieveRankPercentile(ctx context.Context, db *sql.DB, userID string, boardWeights map[string]float64) (float64, error) {

	query := `
	WITH leaderboard_weights AS (
    SELECT
        unnest($1::text[]) AS leaderboard_id,
        unnest($2::float8[]) AS weight
	),
	ranked_leaderboard AS (
		SELECT
			lr.owner_id,
			lr.leaderboard_id,
			RANK() OVER (
				PARTITION BY lr.leaderboard_id
				ORDER BY lr.score DESC, lr.subscore DESC, lr.update_time ASC
			) AS rank,
			COUNT(*) OVER (PARTITION BY lr.leaderboard_id) AS total_records,
			lw.weight
		FROM leaderboard_record lr
		JOIN leaderboard_weights lw
		ON lr.leaderboard_id = lw.leaderboard_id
		WHERE lr.leaderboard_id = ANY($1)
		AND lr.expiry_time > NOW()
	),
	calculated_percentiles AS (
		SELECT 
			leaderboard_id,
			rank,
			(1.0 - ((rank::float - 1) / total_records)) * 100 * weight AS weighted_percentile
		FROM ranked_leaderboard
		WHERE owner_id = $3
	)
	SELECT 
		COALESCE(AVG(weighted_percentile), 0.0) AS aggregate_percentile
	FROM calculated_percentiles;`

	boardIDs := make([]string, 0, len(boardWeights))
	weights := make([]float64, 0, len(boardWeights))
	for boardID, weight := range boardWeights {
		boardIDs = append(boardIDs, boardID)
		weights = append(weights, weight)
	}

	rows, err := db.QueryContext(ctx, query, boardIDs, weights, userID)
	if err != nil {
		return 0.0, fmt.Errorf("failed to query leaderboard ranks: %w", err)
	}
	defer rows.Close()

	if rows.Next() {
		var percentile float64
		err = rows.Scan(&percentile)
		if err != nil {
			return 0.0, fmt.Errorf("failed to scan leaderboard rank: %w", err)
		}

		return percentile, nil
	}

	return 0.0, nil
}

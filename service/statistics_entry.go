package service

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/heroiclabs/nakama-common/runtime"
)

type StatisticsQueueEntry struct {
	BoardMeta   LeaderboardMeta
	UserID      string
	DisplayName string
	Score       int64
	Subscore    int64
	Metadata    map[string]string
}

// This converts the operator to the override value for the leaderboard record.
func (e StatisticsQueueEntry) Override() *int {
	var o int
	switch e.BoardMeta.Operator {
	case OperatorBest:
		o = 1
	case OperatorSet:
		o = 2
	case OperatorIncrement:
		o = 3
	case OperatorDecrement:
		o = 4
	default:
		return nil
	}
	return &o
}

type StatisticsQueue struct {
	logger runtime.Logger
	ch     chan []*StatisticsQueueEntry
}

// prune any expired statistics from the database
func pruneExpiredLeaderboardRecords(ctx context.Context, db *sql.DB) error {
	// it must have an expiration date
	query := `
	DELETE 
		FROM 
			leaderboard_record 
		WHERE 
			expiry_time != '1970-01-01 00:00:00+00'
			AND expiry_time < NOW() - INTERVAL '7 day'
		`

	if _, err := db.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("failed to prune expired leaderboard records: %w", err)
	}
	return nil
}

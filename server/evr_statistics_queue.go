package server

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

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

// Override converts the entry operator to a LeaderboardRecordWrite override value.
// Only OperatorSet (replace) and OperatorBest (keep best) are allowed.
// Incr/decr must be resolved to Set before reaching this function (see RMW block).
// Any other operator is rejected — the producer sent invalid data.
func (e StatisticsQueueEntry) Override() *int {
	var o int
	switch e.BoardMeta.Operator {
	case OperatorBest:
		o = 1
	case OperatorSet:
		o = 2
	default:
		return nil
	}
	return &o
}

// applyIncrementDecrement resolves an incr/decr entry to a SET entry
// with the computed value. The entry is mutated in place.
// Only call for entries with OperatorIncrement or OperatorDecrement.
func applyIncrementDecrement(e *StatisticsQueueEntry, currentVal float64) error {
	deltaVal, err := ScoreToFloat64(e.Score, e.Subscore)
	if err != nil {
		return fmt.Errorf("failed to decode delta score: %w", err)
	}

	var newVal float64
	if e.BoardMeta.Operator == OperatorIncrement {
		newVal = currentVal + deltaVal
	} else {
		newVal = currentVal - deltaVal
	}

	newScore, newSubscore, err := Float64ToScore(newVal)
	if err != nil {
		return fmt.Errorf("failed to encode new score: %w", err)
	}

	e.Score = newScore
	e.Subscore = newSubscore
	e.BoardMeta.Operator = OperatorSet
	return nil
}

// processSingleEntry handles one statistics queue entry:
// validates, resolves incr/decr, checks override, writes leaderboard record.
// Returns true if the entry was processed, false if it was skipped.
func processSingleEntry(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, e *StatisticsQueueEntry) bool {
	// This expects the score and subscore to already be translated to the correct values.
	if e.Score < 0 || e.Subscore < 0 {
		logger.WithFields(map[string]any{
			"leaderboard_id": e.BoardMeta.ID(),
			"score":          e.Score,
			"subscore":       e.Subscore,
		}).Warn("Negative score")
		return false
	} else if e.Score == 0 && e.Subscore == 0 {
		return false
	}

	if !slices.Contains(ValidLeaderboardModes, e.BoardMeta.Mode) {
		return false
	}

	// If the operator is increment or decrement, read the current value, decode it, add the delta, and write it back.
	// This is because the leaderboard uses a float64 encoding that is not compatible with simple integer arithmetic.
	if e.BoardMeta.Operator == OperatorIncrement || e.BoardMeta.Operator == OperatorDecrement {
		var currentVal float64
		// Handle the case where the leaderboard might not exist yet.
		_, ownerRecords, _, _, err := nk.LeaderboardRecordsList(ctx, e.BoardMeta.ID(), []string{e.UserID}, 1, "", 0)
		if err != nil {
			if err == ErrLeaderboardNotFound {
				// Leaderboard doesn't exist, so current value is 0.
				currentVal = 0
			} else {
				logger.WithFields(map[string]any{
					"error":          err.Error(),
					"leaderboard_id": e.BoardMeta.ID(),
				}).Error("Failed to list leaderboard records")
				return false
			}
		} else {
			if len(ownerRecords) > 0 {
				currentVal, err = ScoreToFloat64(ownerRecords[0].Score, ownerRecords[0].Subscore)
				if err != nil {
					logger.WithFields(map[string]any{
						"error":          err.Error(),
						"leaderboard_id": e.BoardMeta.ID(),
					}).Error("Failed to decode current score")
					return false
				}
			} else {
				// Record doesn't exist, so current value is 0.
				currentVal = 0
			}
		}

		if err := applyIncrementDecrement(e, currentVal); err != nil {
			logger.WithFields(map[string]any{
				"error":          err.Error(),
				"leaderboard_id": e.BoardMeta.ID(),
			}).Error("Failed to apply increment/decrement")
			return false
		}
	}

	var md map[string]any
	if e.Metadata != nil {
		md = make(map[string]any)
		for k, v := range e.Metadata {
			md[k] = v
		}
	}

	override := e.Override()
	if override == nil {
		logger.WithFields(map[string]any{
			"leaderboard_id": e.BoardMeta.ID(),
			"user_id":        e.UserID,
			"operator":       string(e.BoardMeta.Operator),
		}).Error("Statistics queue entry rejected: invalid operator")
		return false
	}

	if _, err := nk.LeaderboardRecordWrite(ctx, e.BoardMeta.ID(), e.UserID, e.DisplayName, e.Score, e.Subscore, md, override); err != nil {

		// Try to create the leaderboard
		if err = nk.LeaderboardCreate(ctx, e.BoardMeta.ID(), true, "desc", string(OperatorSet), ResetScheduleToCron(e.BoardMeta.ResetSchedule), map[string]any{}, true); err != nil {

			logger.WithFields(map[string]any{
				"leaderboard_id": e.BoardMeta.ID(),
				"error":          err.Error(),
			}).Error("Failed to create leaderboard")

		} else {
			logger.WithFields(map[string]any{
				"leaderboard_id": e.BoardMeta.ID(),
			}).Debug("Leaderboard created")

			if _, err := nk.LeaderboardRecordWrite(ctx, e.BoardMeta.ID(), e.UserID, e.DisplayName, e.Score, e.Subscore, md, override); err != nil {
				logger.WithFields(map[string]any{
					"error":          err.Error(),
					"leaderboard_id": e.BoardMeta.ID(),
					"user_id":        e.UserID,
					"score":          e.Score,
					"subscore":       e.Subscore,
				}).Error("Failed to write leaderboard record")
			}

		}
	}

	return true
}

type StatisticsQueue struct {
	logger runtime.Logger
	ch     chan []*StatisticsQueueEntry
}

func NewStatisticsQueue(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) *StatisticsQueue {
	ch := make(chan []*StatisticsQueueEntry, 8*3*100) // three matches ending at the same time, 100 records per player
	r := &StatisticsQueue{
		logger: logger,
		ch:     ch,
	}

	go func() {

		pruneTicker := time.NewTicker(24 * time.Hour)
		defer pruneTicker.Stop()

		for {
			select {
			case <-ctx.Done():
				return

			case <-pruneTicker.C:
				if err := pruneExpiredLeaderboardRecords(ctx, db); err != nil {
					logger.WithField("error", err).Error("Failed to prune expired leaderboard records")
				}
			case entries := <-ch:
				for _, e := range entries {
					processSingleEntry(ctx, logger, nk, e)
				}
			}
		}
	}()

	return r
}

func (r *StatisticsQueue) Add(entries []*StatisticsQueueEntry) error {

	select {
	case r.ch <- entries:
		r.logger.WithFields(map[string]any{
			"count": len(entries),
		}).Debug("Leaderboard record queued")
		return nil
	default:
		r.logger.WithFields(map[string]any{
			"count": len(entries),
		}).Warn("Leaderboard record write queue full, dropping entry")
		return fmt.Errorf("queue full")
	}
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

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
// Always returns OperatorSet (2) — the resolved value to write — because:
//   - OperatorIncrement/Decrement are resolved to Set via RMW before reaching this.
//   - OperatorBest is resolved to Set via RMW before reaching this.
//   - Using anything other than Set here would interact with the leaderboard's creation
//     default operator (which is also Set), producing confusing or incorrect behaviour.
//
// Returns nil only when the operator is invalid, which causes the caller to reject the entry.
func (e StatisticsQueueEntry) Override() *int {
	switch e.BoardMeta.Operator {
	case OperatorSet:
		o := 2 // SET
		return &o
	default:
		return nil
	}
}

// readCurrentVal reads the current leaderboard value for the entry's owner.
// Returns (value, true) on success, or (0, false) if the record cannot be read.
// A missing leaderboard or missing record is not an error — it returns (0, true).
func readCurrentVal(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, e *StatisticsQueueEntry) (float64, bool) {
	_, ownerRecords, _, _, err := nk.LeaderboardRecordsList(ctx, e.BoardMeta.ID(), []string{e.UserID}, 1, "", 0)
	if err != nil {
		if err == ErrLeaderboardNotFound {
			return 0, true
		}
		logger.WithFields(map[string]any{
			"error":          err.Error(),
			"leaderboard_id": e.BoardMeta.ID(),
		}).Error("Failed to list leaderboard records")
		return 0, false
	}
	if len(ownerRecords) == 0 {
		return 0, true
	}
	val, err := ScoreToFloat64(ownerRecords[0].Score, ownerRecords[0].Subscore)
	if err != nil {
		logger.WithFields(map[string]any{
			"error":          err.Error(),
			"leaderboard_id": e.BoardMeta.ID(),
		}).Error("Failed to decode current score")
		return 0, false
	}
	return val, true
}

// applyBest resolves a "best" entry to a SET entry by keeping the higher of the
// current stored value and the incoming value. The entry is mutated in place.
// Only call for entries with OperatorBest.
func applyBest(e *StatisticsQueueEntry, currentVal float64) error {
	incomingVal, err := ScoreToFloat64(e.Score, e.Subscore)
	if err != nil {
		return fmt.Errorf("failed to decode incoming score: %w", err)
	}

	// Leaderboards use descending sort ("desc"), so "best" = highest value.
	newVal := incomingVal
	if currentVal > incomingVal {
		newVal = currentVal
	}

	newScore, newSubscore, err := Float64ToScore(newVal)
	if err != nil {
		return fmt.Errorf("failed to encode best score: %w", err)
	}

	e.Score = newScore
	e.Subscore = newSubscore
	e.BoardMeta.Operator = OperatorSet
	return nil
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

	// Operators that require a read-modify-write (RMW) to produce a resolved SET value:
	//   - Increment/Decrement: new = current ± delta
	//   - Best: new = max(current, incoming)   (leaderboard sort is "desc", so higher wins)
	// After RMW the entry's operator is updated to OperatorSet so Override() accepts it.
	switch e.BoardMeta.Operator {
	case OperatorIncrement, OperatorDecrement:
		currentVal, ok := readCurrentVal(ctx, logger, nk, e)
		if !ok {
			return false
		}
		if err := applyIncrementDecrement(e, currentVal); err != nil {
			logger.WithFields(map[string]any{
				"error":          err.Error(),
				"leaderboard_id": e.BoardMeta.ID(),
			}).Error("Failed to apply increment/decrement")
			return false
		}

	case OperatorBest:
		currentVal, ok := readCurrentVal(ctx, logger, nk, e)
		if !ok {
			return false
		}
		if err := applyBest(e, currentVal); err != nil {
			logger.WithFields(map[string]any{
				"error":          err.Error(),
				"leaderboard_id": e.BoardMeta.ID(),
			}).Error("Failed to apply best operator")
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

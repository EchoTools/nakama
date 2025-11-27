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

func NewStatisticsQueue(logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) *StatisticsQueue {
	ch := make(chan []*StatisticsQueueEntry, 8*3*100) // three matches ending at the same time, 100 records per player
	r := &StatisticsQueue{
		logger: logger,
		ch:     ch,
	}

	go func() {

		ctx := context.Background()

		pruneTicker := time.NewTicker(24 * time.Hour)

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
					// This expects the score and subscore to already be translated to the correct values.
					if e.Score < 0 || e.Subscore < 0 {
						logger.WithFields(map[string]any{
							"leaderboard_id": e.BoardMeta.ID(),
							"score":          e.Score,
							"subscore":       e.Subscore,
						}).Warn("Negative score")
						continue
					} else if e.Score == 0 && e.Subscore == 0 {
						continue
					}

					if !slices.Contains(ValidLeaderboardModes, e.BoardMeta.Mode) {
						continue
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
								continue
							}
						} else {
							if len(ownerRecords) > 0 {
								currentVal, err = ScoreToFloat64(ownerRecords[0].Score, ownerRecords[0].Subscore)
								if err != nil {
									logger.WithFields(map[string]any{
										"error":          err.Error(),
										"leaderboard_id": e.BoardMeta.ID(),
									}).Error("Failed to decode current score")
									continue
								}
							} else {
								// Record doesn't exist, so current value is 0.
								currentVal = 0
							}
						}

						deltaVal, err := ScoreToFloat64(e.Score, e.Subscore)
						if err != nil {
							logger.WithFields(map[string]any{
								"error":          err.Error(),
								"leaderboard_id": e.BoardMeta.ID(),
							}).Error("Failed to decode delta score")
							continue
						}

						var newVal float64
						if e.BoardMeta.Operator == OperatorIncrement {
							newVal = currentVal + deltaVal
						} else {
							newVal = currentVal - deltaVal
						}

						newScore, newSubscore, err := Float64ToScore(newVal)
						if err != nil {
							logger.WithFields(map[string]any{
								"error":          err.Error(),
								"leaderboard_id": e.BoardMeta.ID(),
							}).Error("Failed to encode new score")
							continue
						}

						// Update the entry to be a SET operation with the new value
						e.Score = newScore
						e.Subscore = newSubscore
						e.BoardMeta.Operator = OperatorSet
					}

					var md map[string]any
					if e.Metadata != nil {
						md = make(map[string]any)
						for k, v := range e.Metadata {
							md[k] = v
						}
					}

					if _, err := nk.LeaderboardRecordWrite(ctx, e.BoardMeta.ID(), e.UserID, e.DisplayName, e.Score, e.Subscore, md, e.Override()); err != nil {

						// Try to create the leaderboard
						if err = nk.LeaderboardCreate(ctx, e.BoardMeta.ID(), true, "desc", string(e.BoardMeta.Operator), ResetScheduleToCron(e.BoardMeta.ResetSchedule), map[string]any{}, true); err != nil {

							logger.WithFields(map[string]any{
								"leaderboard_id": e.BoardMeta.ID(),
								"error":          err.Error(),
							}).Error("Failed to create leaderboard")

						} else {
							logger.WithFields(map[string]any{
								"leaderboard_id": e.BoardMeta.ID(),
							}).Debug("Leaderboard created")

							if _, err := nk.LeaderboardRecordWrite(ctx, e.BoardMeta.ID(), e.UserID, e.DisplayName, e.Score, e.Subscore, md, e.Override()); err != nil {
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

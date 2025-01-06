package server

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

var _ = SystemMigrator(&MigrationLeaderboardPrune{})

type MigrationLeaderboardPrune struct{}

func (m *MigrationLeaderboardPrune) MigrateSystem(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) error {

	validLeaderboards := make(map[string]int, 0)

	// First, remove any invalid boards.
	for mode, leaderboard := range tabletStatisticTypeMap {
		for statName, operator := range leaderboard {
			for _, resetSchedule := range []string{"alltime", "monthly", "weekly", "daily"} {
				id := StatisticBoardID("", mode, statName, resetSchedule)[1:]
				validLeaderboards[id] = operator
			}

		}
	}

	toDelete := make([]string, 0)
	firstStartTime := time.Now()
	var listCursor string
	for {

		list, err := nk.LeaderboardList(100, listCursor)
		if err != nil {
			return nil
		}
		listCursor = list.Cursor
	LeaderboardLoop:
		for _, leaderboard := range list.Leaderboards {
			boardStartTime := time.Now()
			_, suffix, found := strings.Cut(leaderboard.Id, ":")
			if !found {
				// Invalid (legacy uuid) leaderboard
				toDelete = append(toDelete, leaderboard.Id)
				continue
			}
			_ = suffix
			/*
				if _, ok := validLeaderboards[suffix]; !ok || !found {
					toDelete = append(toDelete, leaderboard.Id)
					continue
				}
			*/

			records := make([]*api.LeaderboardRecord, 0)
			var chunk []*api.LeaderboardRecord
			var recordCursor string
			s := strings.Split(leaderboard.Id, ":")
			if len(s) != 3 {
				logger.Warn("Invalid leaderboard")
				continue
			}
			mode := s[0]
			statName := s[1]
			resetSchedule := s[2]

			if resetSchedule != "alltime" || strings.Contains(leaderboard.Id, "Percentile") || strings.Contains(leaderboard.Id, "Rating") {
				// Only move all time
				toDelete = append(toDelete, leaderboard.Id)
				continue
			}

			dst := "147afc9d-2819-4197-926d-5b3f92790edc:" + leaderboard.Id
			movedCount := 0
			startTime := time.Now()
			logger := logger.WithFields(map[string]interface{}{
				"src": leaderboard.Id,
				"dst": dst,
			})
			if err := nk.LeaderboardRanksDisable(ctx, leaderboard.Id); err != nil {
				logger.WithField("error", err).Warn("Failed to disable leaderboard ranks")
			}

			for {

				chunk, _, recordCursor, _, err = nk.LeaderboardRecordsList(ctx, leaderboard.Id, nil, 200, recordCursor, 0)
				if err != nil {
					logger.WithField("error", err).Warn("Failed to list leaderboard records")
					continue LeaderboardLoop
				}
				records = append(records, chunk...)
				if recordCursor == "" {
					break
				}
			}

			op, ok := validLeaderboards[mode+":"+statName+":alltime"]
			if !ok {
				logger.Warn("Invalid leaderboard")
				continue LeaderboardLoop
			}
			opstr := ""
			switch op {
			case LeaderboardOperatorSet:
				opstr = "set"
			case LeaderboardOperatorBest:
				opstr = "best"
			case LeaderboardOperatorIncrement:
				opstr = "incr"
			}

			logger.WithFields(map[string]interface{}{
				"total_count": len(records),
			}).Debug("Starting move")

			// Do them in chunks of 200
			for len(chunk) > 0 {
				chunkStartTime := time.Now()
				// Split the records into chunks of 200, handling the last chunk
				if len(records) > 200 {
					chunk = records[:200]
					records = records[200:]
				} else {
					chunk = records
					records = nil
				}

				// Insert the records into the new leaderboard
				for _, record := range chunk {
					override := LeaderboardOperatorSet
					if _, err := nk.LeaderboardRecordWrite(ctx, dst, record.OwnerId, record.Username.Value, record.Score, record.Subscore, nil, &override); err != nil {
						// Create the leaderboard
						if err := nk.LeaderboardCreate(ctx, dst, leaderboard.Authoritative, "desc", opstr, "", nil, true); err != nil {
							logger.WithField("error", err).Warn("Failed to create leaderboard")
							continue
						}
					}
					if err := nk.LeaderboardRecordDelete(ctx, leaderboard.Id, record.OwnerId); err != nil {
						logger.WithField("error", err).Warn("Failed to delete leaderboard record")
						continue
					}
					movedCount++
				}
				logger.WithFields(map[string]interface{}{
					"chunk_duration": time.Since(chunkStartTime),
					"board_duration": time.Since(boardStartTime),
					"total_duration": time.Since(firstStartTime),
					"chunk_size":     len(chunk),
					"remaining":      len(records),
				}).Debug("Moved chunk")

				if len(records) == 0 {
					break
				}
			}

			logger.WithFields(map[string]interface{}{
				"src":      leaderboard.Id,
				"dst":      dst,
				"count":    movedCount,
				"duration": time.Since(startTime),
			}).Info("Moved leaderboard records.")

			if len(records) == 0 {
				toDelete = append(toDelete, leaderboard.Id)
			}

			// Enter the leaderboard records under the new name

			logger.WithFields(map[string]interface{}{
				"leaderboard": leaderboard.Id,
				"count":       len(records),
			}).Debug("Leaderboard records")
		}

		if listCursor == "" {
			break
		}
	}
	logger.WithFields(map[string]interface{}{
		"duration": time.Since(firstStartTime),
	}).Info("Moved leaderboards")
	/*
		for _, id := range toDelete {
			startTime := time.Now()
			if err := nk.LeaderboardDelete(ctx, id); err != nil {
				return nil
			}
			logger.WithFields(map[string]interface{}{
				"id":       id,
				"duration": time.Since(startTime),
			}).Info("Deleted invalid leaderboard")
		}
	*/

	return nil
}

func MoveLeaderboard(ctx context.Context, db *sql.DB, src, dst string) (int, error) {
	count := 0
	res, err := db.ExecContext(ctx, "UPDATE leaderboard SET id = $1 WHERE id = $2", dst, src)
	if err != nil {
		return count, err
	}
	c, err := res.RowsAffected()
	if err != nil {
		return count, err
	}
	count += int(c)

	if _, err := db.ExecContext(ctx, "UPDATE leaderboard_record SET leaderboard_id = $1 WHERE leaderboard_id = $2", dst, src); err != nil {
		return count, err
	}
	if err != nil {
		return count, err
	}
	c, err = res.RowsAffected()
	if err != nil {
		return count, err
	}
	count += int(c)
	return count, nil
}

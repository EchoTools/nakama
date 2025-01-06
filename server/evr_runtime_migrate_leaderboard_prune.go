package server

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

var _ = SystemMigrator(&MigrationLeaderboardPrune{})

type MigrationLeaderboardPrune struct{}

func (m *MigrationLeaderboardPrune) MigrateSystem(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) error {

	validLeaderboards := make(map[string]struct{}, 0)

	// First, remove any invalid boards.
	for mode, leaderboard := range tabletStatisticTypeMap {
		for statName := range leaderboard {
			for _, resetSchedule := range []string{"alltime", "monthly", "weekly", "daily"} {
				id := StatisticBoardID("", mode, statName, resetSchedule)[1:]
				validLeaderboards[id] = struct{}{}
			}

		}
	}
	toDelete := make([]string, 0)
	toRename := make(map[string]string, 0)
	var listCursor string
	for {
		list, err := nk.LeaderboardList(100, listCursor)
		if err != nil {
			return nil
		}
		listCursor = list.Cursor

		for _, leaderboard := range list.Leaderboards {

			// Valid leaderboard without the group name
			if _, ok := validLeaderboards[leaderboard.Id]; ok {
				// Rename it with the echovr lounge uuid
				newId := "147afc9d-2819-4197-926d-5b3f92790edc:" + leaderboard.Id
				toRename[leaderboard.Id] = newId
				continue
			}

			_, suffix, found := strings.Cut(leaderboard.Id, ":")
			if !found {
				// Invalid (legacy uuid) leaderboard
				toDelete = append(toDelete, leaderboard.Id)
				continue
			}

			if _, ok := validLeaderboards[suffix]; !ok || !found {
				toDelete = append(toDelete, leaderboard.Id)
				continue
			}

		}

		if listCursor == "" {
			break
		}
	}

	for src, dst := range toRename {
		startTime := time.Now()
		if err := MoveLeaderboard(ctx, db, src, dst); err != nil {
			return nil
		}
		logger.WithFields(map[string]interface{}{
			"src":      src,
			"dst":      dst,
			"duration": time.Since(startTime),
		}).Info("Renamed leaderboard")
	}
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

func MoveLeaderboard(ctx context.Context, db *sql.DB, src, dst string) error {
	_, err := db.ExecContext(ctx, "UPDATE leaderboard SET id = $1 WHERE id = $2", dst, src)
	return err
}

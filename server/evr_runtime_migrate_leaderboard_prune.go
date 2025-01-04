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

	leaderboardUpdateTimes := make(map[string]time.Time)

	toDelete := make([]string, 0)

	validLeaderboards := make(map[string]struct{}, 0)

	// First, remove any invalid boards.
	for mode, leaderboard := range tabletStatisticTypeMap {
		for statName := range leaderboard {
			for _, resetSchedule := range []string{"alltime", "monthly", "weekly", "daily"} {
				id := StatisticBoardID("", mode, statName, resetSchedule)
				validLeaderboards[id] = struct{}{}
			}

		}
	}

	var listCursor string
	for {
		list, err := nk.LeaderboardList(100, listCursor)
		if err != nil {
			return nil
		}
		listCursor = list.Cursor

		for _, leaderboard := range list.Leaderboards {

			if _, ok := validLeaderboards[leaderboard.Id]; ok {
				// Rename it with the echovr lounge uuid
				newId := "147afc9d-2819-4197-926d-5b3f92790edc:" + leaderboard.Id
				_ = newId
			}

			suffix, found := strings.CutPrefix(leaderboard.Id, ":")

			if _, ok := validLeaderboards[suffix]; !ok || !found {
				logger.Info("Found bad leaderboard: ", leaderboard.Id)
				continue
			}

			// If the board doesn't have colons (like the old uuid ones), delete it.
			if s := strings.SplitN(leaderboard.Id, ":", 4); len(s) != 4 {
				toDelete = append(toDelete, leaderboard.Id)
				continue
			}

			var recordCursor string
			var records []*api.LeaderboardRecord
			for {
				records, _, recordCursor, _, err = nk.LeaderboardRecordsList(ctx, leaderboard.Id, nil, 200, recordCursor, 0)
				if err != nil {
					continue
				}

				for _, record := range records {
					if record.UpdateTime.AsTime().After(leaderboardUpdateTimes[leaderboard.Id]) {
						leaderboardUpdateTimes[leaderboard.Id] = record.UpdateTime.AsTime()
					}
				}

				if recordCursor == "" {
					break
				}
			}
		}

		if listCursor == "" {
			break
		}
	}

	for _, id := range toDelete {

		if err := nk.LeaderboardDelete(ctx, id); err != nil {
			return nil
		}
		logger.WithField("id", id).Info("Deleted leaderboard.")
	}

	return nil
}

package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/jackc/pgx/v5/pgtype"
)

var _ = SystemMigrator(&MigrationLeaderboardRecords{})

type MigrationLeaderboardRecords struct{}

func (m *MigrationLeaderboardRecords) MigrateSystem(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) error {
	<-time.After(10 * time.Second)

	type leaderboardKey struct {
		leaderboardID string
		expiryTime    pgtype.Timestamptz
	}
	type leaderboardRecord struct {
		ownerID   string
		score     int64
		subscore  int64
		discordID string
	}

	allRecords := make(map[leaderboardKey][]leaderboardRecord)

	rows, err := db.QueryContext(ctx, `
		SELECT owner_id, custom_id, leaderboard_id, expiry_time, score, subscore
		FROM leaderboard AS l LEFT JOIN leaderboard_record AS lr ON lr.leaderboard_id = l.id LEFT JOIN users AS u ON u.id = lr.owner_id WHERE l.metadata->>'scaling_factor' IS NULL
	`)
	if err != nil {
		return fmt.Errorf("failed to query leaderboard_record: %w", err)
	}
	defer rows.Close()

	// Iterate through the records, mutate the values, and update them
	for rows.Next() {
		var (
			dbOwnerID       string
			dbDiscordID     string
			dbLeaderboardID string
			dbScore         int64
			dbSubscore      int64
			dbExpiryTime    pgtype.Timestamptz
		)

		err := rows.Scan(&dbOwnerID, &dbDiscordID, &dbLeaderboardID, &dbExpiryTime, &dbScore, &dbSubscore)
		if err != nil {
			return fmt.Errorf("failed to scan leaderboard_record: %w", err)
		}

		// Combine score and subscore into a float64
		combined := fmt.Sprintf("%d.%d", dbScore, dbSubscore)
		// Convert the combined string to a float64
		combinedFloat, err := strconv.ParseFloat(combined, 64)
		if err != nil {
			return fmt.Errorf("failed to parse combined score `%s`: %w", combined, err)
		}

		score, err := Float64ToScore(combinedFloat)
		if err != nil {
			return fmt.Errorf("failed to convert float64 to int64 pair: %w", err)
		}
		// Store the high and low values in the map
		key := leaderboardKey{
			leaderboardID: dbLeaderboardID,
			expiryTime:    dbExpiryTime,
		}

		record := leaderboardRecord{
			ownerID:   dbOwnerID,
			score:     score,
			subscore:  0,
			discordID: dbDiscordID,
		}

		// Append the record to the map
		if _, exists := allRecords[key]; !exists {
			allRecords[key] = []leaderboardRecord{}
		}
		allRecords[key] = append(allRecords[key], record)
	}

	logger.WithField("records_count", len(allRecords)).Info("Migrating leaderboard records to scaled float64")

	leaderboardQuery := `
	UPDATE leaderboard AS l
	SET metadata = jsonb_set(metadata, '{scaling_factor}', to_jsonb(2))
	WHERE l.id = $1;
	  `

	query := `
    UPDATE leaderboard_record AS lr
    SET score = $4, subscore = $5, metadata = $6
    WHERE lr.owner_id = $1
      AND lr.leaderboard_id = $2
      AND lr.expiry_time = $3;
`

	// Bulk update one leaderboard at a time.
	for key, records := range allRecords {
		leaderboardID := key.leaderboardID
		expiryTime := key.expiryTime

		params := [][]any{}

		// Prepare the values for the bulk update
		for _, record := range records {

			metadata, err := json.Marshal(map[string]any{
				"discord_id": record.discordID,
			})
			if err != nil {
				return fmt.Errorf("failed to marshal metadata: %w", err)
			}
			params = append(params, []any{
				record.ownerID,
				leaderboardID,
				expiryTime,
				record.score,
				record.subscore,
				string(metadata),
			})
		}

		if err := ExecuteInTx(ctx, db, func(tx *sql.Tx) error {

			// Execute the statement for each value
			for _, p := range params {
				if _, err := tx.ExecContext(ctx, query, p...); err != nil {
					return fmt.Errorf("failed to execute statement: %w", err)
				}
			}

			_, err = tx.ExecContext(ctx, leaderboardQuery, leaderboardID)
			if err != nil {
				return fmt.Errorf("failed to update leaderboard metadata: %w", err)
			}
			return nil
		}); err != nil {
			return err
		}

		logger.WithFields(map[string]any{
			"leaderboard_id": leaderboardID,
			"expiry_time":    key.expiryTime,
			"records_count":  len(params),
		}).Info("Updated leaderboard records for leaderboard_id %s", leaderboardID)

	}

	return nil
}

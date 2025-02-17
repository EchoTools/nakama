package server

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

type SystemMigrator interface {
	MigrateSystem(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) error
}

type UserMigrator interface {
	MigrateUser(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, userID string) error
}

func MigrateSystem(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) {
	// Combine the loadouts into one storage object

	migrations := []SystemMigrator{
		&MigrationDevicesHistory{},
		//&MigrationGuildGroups{},
		//&MigrationLeaderboardPrune{},
	}

	for _, m := range migrations {
		if err := m.MigrateSystem(ctx, logger, db, nk); err != nil {
			logger.WithField("error", err).Error("Error migrating system data")
		}
	}

	if err := MigrateAllUsers(ctx, logger, nk, db); err != nil {
		logger.WithField("error", err).Error("Error migrating all users")
	}

}

func MigrateAllUsers(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, db *sql.DB) error {
	query := `
	SELECT
		user_id
	FROM
		storage
	WHERE
		collection = 'DisplayNames'
		AND key = 'history'
	ORDER BY
		update_time DESC
	`

	rows, err := db.Query(query)
	if err != nil {
		return fmt.Errorf("error fetching users: %w", err)
	}

	userIDs := make([]string, 0)
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			return fmt.Errorf("error scanning user id: %w", err)
		}
		userIDs = append(userIDs, userID)
	}

	for _, userID := range userIDs {
		startTime := time.Now()
		if err := MigrateUser(ctx, RuntimeLoggerToZapLogger(logger), nk, db, userID); err != nil {
			return fmt.Errorf("error migrating user data: %w", err)
		}
		<-time.After(time.Since(startTime)) // Give the system time to recover
	}

	logger.WithField("count", len(userIDs)).Info("Migrated all users")

	return nil
}

func MigrateUser(ctx context.Context, zapLogger *zap.Logger, nk runtime.NakamaModule, db *sql.DB, userID string) error {
	logger := NewRuntimeGoLogger(zapLogger)

	migrations := []UserMigrator{}
	startTime := time.Now()

	for _, m := range migrations {
		logger := logger.WithFields(map[string]interface{}{
			"uid":       userID,
			"migration": fmt.Sprintf("%T", m),
		})

		if err := m.MigrateUser(ctx, logger, db, nk, userID); err != nil {
			metricsTags := map[string]string{
				"migration": fmt.Sprintf("%T", m),
			}
			nk.MetricsCounterAdd("migration_error_count", metricsTags, 1)
			logger.WithField("error", err).Error("Error migrating user data")

		}
	}

	nk.MetricsTimerRecord("migration_latency", nil, time.Since(startTime))
	logger.WithFields(map[string]interface{}{
		"uid":      userID,
		"duration": time.Since(startTime),
	}).Info("Migrated user")
	return nil
}

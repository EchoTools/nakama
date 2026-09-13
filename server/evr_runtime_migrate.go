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

type UserMigrater interface {
	MigrateUser(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, userID string) error
}

func MigrateSystem(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) {
	systemMigrations := []SystemMigrator{
		// MigrationBreakIgnoredAlts is deliberately NOT registered. It is
		// redundant work against production storage, and the type is left in
		// the tree only so the capability is not lost.
		//
		// It can only ever DELETE. Its whole body is a scan for links whose
		// every item is covered by matchIgnoredAltPattern (allItemsIgnored,
		// evr_migration_ignored_alts.go:134) followed by two map deletes and
		// two reciprocal writes; it issues no discovery query and calls neither
		// LoginAlternateSearch nor UpdateAlternates, so it cannot create a
		// link.
		//
		// MigrationClearAlternateMatches, which ran immediately after it,
		// clears each account's links wholesale and rebuilds them from a fresh
		// search. Whatever Break deleted, and whatever it wrote to the far side
		// of each pair, was therefore overwritten by the very next migration
		// for every account the rebuild reached -- so Break's storage traffic
		// bought nothing and was paid against the live database.
		//
		// One population it did reach is NOT covered: an account whose every
		// discovery item is an ignored value has no search patterns, and
		// MigrationClearAlternateMatches now leaves such accounts untouched
		// rather than erasing them (evr_migration_clear_alts.go, the
		// AltSearchPatterns check in phase 2). Breaking those accounts' links
		// is still available on demand, to an operator, through
		// CGNATCleanupRPC -- which is the same test applied deliberately
		// rather than on every boot.
		&MigrationClearAlternateMatches{},
	}

	allUserMigrations := []UserMigrater{
		//&MigrationLoadouts{},
	}

	if len(systemMigrations) == 0 && len(allUserMigrations) == 0 {
		return
	}

	// No fixed startup delay. MigrationClearAlternateMatches waits, bounded,
	// for exactly what it needs -- settings on the CGNAT detector and a
	// configured IP info cache -- and refuses without them (waitReady).

	for _, m := range systemMigrations {
		startTime := time.Now()
		logger := logger.WithField("migration", fmt.Sprintf("%T", m))

		if err := m.MigrateSystem(ctx, logger, db, nk); err != nil {
			logger.WithField("error", err).Error("Error migrating system data")
		} else {
			logger.WithField("duration", time.Since(startTime)).Info("Migrated complete.")
		}
	}

	if len(allUserMigrations) != 0 {
		if err := MigrateAllUsers(ctx, logger, nk, db, allUserMigrations); err != nil {
			logger.WithField("error", err).Error("Error migrating all users")
		}
	}

}

func MigrateAllUsers(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, db *sql.DB, migrations []UserMigrater) error {
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

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("error fetching users: %w", err)
	}
	defer rows.Close()

	userIDs := make([]string, 0)
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			return fmt.Errorf("error scanning user id: %w", err)
		}
		userIDs = append(userIDs, userID)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating user rows: %w", err)
	}

	for _, userID := range userIDs {
		startTime := time.Now()
		if err := MigrateUser(ctx, RuntimeLoggerToZapLogger(logger), nk, db, userID, migrations); err != nil {
			return fmt.Errorf("error migrating user data: %w", err)
		}
		<-time.After(time.Since(startTime)) // Give the system time to recover
	}

	logger.WithField("count", len(userIDs)).Info("Migrated all users")

	return nil
}

func MigrateUser(ctx context.Context, zapLogger *zap.Logger, nk runtime.NakamaModule, db *sql.DB, userID string, migrations []UserMigrater) error {
	logger := NewRuntimeGoLogger(zapLogger)

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

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
		&PruneSystemGroups{},
		&MigrationCombineStoredCosmeticLoadouts{},
		&MigrationLeaderboardPrune{},
		//&MigrationLeaderboardPrune{},

	}

	for _, m := range migrations {
		if err := m.MigrateSystem(ctx, logger, db, nk); err != nil {
			logger.WithField("error", err).Error("Error migrating system data")
		}
	}

}

func MigrateUser(ctx context.Context, zapLogger *zap.Logger, nk runtime.NakamaModule, db *sql.DB, userID string) error {
	logger := NewRuntimeGoLogger(zapLogger)

	migrations := []UserMigrator{
		&MigrateUserLoginHistory{},
		&MigrationUserVRMLEntitlementsToWallet{},
		&MigrateUserGameProfile{},
	}
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
			logger.Error("Error migrating user data")
		}
	}

	nk.MetricsTimerRecord("migration_latency", nil, time.Since(startTime))

	return nil
}

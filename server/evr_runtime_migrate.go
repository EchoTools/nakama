package server

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/heroiclabs/nakama-common/runtime"
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
		//&MigrationCombineStoredCosmeticLoadouts{},
		//&MigrationLeaderboardPrune{},

	}

	for _, m := range migrations {
		if err := m.MigrateSystem(ctx, logger, db, nk); err != nil {
			logger.WithField("error", err).Error("Error migrating system data")
		}
	}

}

func MigrateUser(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, db *sql.DB, userID string) error {

	migrations := []UserMigrator{
		&MigrateUserLoginHistory{},
		&MigrationUserVRMLEntitlementsToWallet{},
		&MigrateUserGameProfile{},
	}

	for _, m := range migrations {
		logger := logger.WithFields(map[string]interface{}{
			"uid":       userID,
			"migration": fmt.Sprintf("%T", m),
		})

		if err := m.MigrateUser(ctx, logger, db, nk, userID); err != nil {
			logger.Error("Error migrating user data")
		}
	}

	return nil
}

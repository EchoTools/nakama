package server

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"
)

var _ = UserMigrator(&MigrateUserLoginHistory{})

type MigrateUserLoginHistory struct{}

func (m *MigrateUserLoginHistory) MigrateUser(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, userID string) error {

	if LoginStorageCollection != "Devices" {
		// Move the LoginHistory from Devices to Login
		if objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
			{
				Collection: "Devices",
				Key:        "history",
				UserID:     userID,
			},
		}); err != nil {
			logger.WithField("error", err).Error("Error fetching login history")
		} else if len(objs) > 0 {

			if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{
				{
					Collection:      LoginStorageCollection,
					Key:             LoginHistoryStorageKey,
					UserID:          userID,
					Value:           objs[0].Value,
					PermissionRead:  0,
					PermissionWrite: 0,
				},
			}); err != nil {
				logger.WithField("error", err).Error("Error writing login history")
			}

			/*
				if err := nk.StorageDelete(ctx, []*runtime.StorageDelete{
					{
						Collection: "Devices",
						Key:        "loginHistory",
						UserID:     userID,
					},
				}); err != nil {
					logger.WithField("error", err).Error("Error deleting login history")
				}
			*/
		}
	}
	loginHistory, err := LoginHistoryLoad(ctx, nk, userID)
	if err != nil {
		return fmt.Errorf("error loading login history: %w", err)
	}

	updated := false

	for k, _ := range loginHistory.History {
		xpid, clientIP, _ := strings.Cut(k, ":")
		if xpid == "" || xpid == "UNK-0" || clientIP == "" {
			delete(loginHistory.History, k)
			updated = true
			break
		}
	}

	if !updated {
		return nil
	}

	if err := LoginHistoryStore(ctx, nk, userID, loginHistory); err != nil {
		return fmt.Errorf("error saving login history: %w", err)
	}

	return nil
}

package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

var _ = UserMigrator(&MigrateUserLoginHistory{})

type MigrateUserLoginHistory struct{}

func (m *MigrateUserLoginHistory) MigrateUser(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, userID string) error {

	if LoginStorageCollection == "Devices" {
		return nil
	}

	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: LoginStorageCollection,
			Key:        LoginHistoryStorageKey,
			UserID:     userID,
		},
		{
			Collection: "Devices",
			Key:        "loginHistory",
			UserID:     userID,
		},
	})

	if err != nil {
		logger.WithField("error", err).Error("Error fetching login history")
	}

	if len(objs) == 0 {
		return nil
	}

	doMigration := true
	var oldHistoryObj *api.StorageObject

	for _, obj := range objs {
		switch obj.Collection {
		case LoginStorageCollection:
			doMigration = false
		case "Devices":
			oldHistoryObj = obj
		}
	}

	if doMigration {
		history := &LoginHistory{}
		if err := json.Unmarshal([]byte(objs[0].Value), history); err != nil {
			return fmt.Errorf("error unmarshalling login history: %w", err)
		}
		history.userID = userID

		for k, _ := range history.History {
			xpid, clientIP, _ := strings.Cut(k, ":")
			if xpid == "" || xpid == "UNK-0" || clientIP == "" {
				delete(history.History, k)
			}
		}

		if err := LoginHistoryStore(ctx, nk, userID, history); err != nil {
			return fmt.Errorf("error saving login history: %w", err)
		}

	}

	if oldHistoryObj != nil {
		if err := nk.StorageDelete(ctx, []*runtime.StorageDelete{
			{
				Collection: "Devices",
				Key:        "loginHistory",
				UserID:     userID,
			},
		}); err != nil {
			return fmt.Errorf("error deleting login history: %w", err)
		}
	}

	return nil
}

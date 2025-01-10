package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

var _ = UserMigrator(&MigrateDisplayNameHistory{})

type MigrateDisplayNameHistory struct{}

func (m *MigrateDisplayNameHistory) MigrateUser(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, userID string) error {

	if DisplayNameCollection != "DisplayNames" {
		// Move the LoginHistory from Devices to Login
		if objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
			{
				Collection: "DisplayNames",
				Key:        "history",
				UserID:     userID,
			},
		}); err != nil {
			logger.WithField("error", err).Error("Error fetching login history")
		} else if len(objs) > 0 {

			type oldHistoryEntry struct {
				DisplayName string    `json:"display_name"`
				UpdateTime  time.Time `json:"update_time"`
			}
			type oldHistoryData struct {
				History  map[string][]oldHistoryEntry `json:"history"`
				Reserved []string                     `json:"reserved"`
			}

			account, err := nk.AccountGetId(ctx, userID)
			if err != nil {
				return fmt.Errorf("error fetching account: %w", err)
			}

			history, err := DisplayNameHistoryLoad(ctx, nk, userID)
			if err != nil {
				return fmt.Errorf("error loading display name history: %w", err)
			}

			oldHistory := oldHistoryData{}
			if err := json.Unmarshal([]byte(objs[0].Value), &oldHistory); err != nil {
				return fmt.Errorf("error unmarshalling display name cache: %w", err)
			}

			for groupID, entries := range oldHistory.History {
				for _, e := range entries {

					history.Set(groupID, e.DisplayName, e.UpdateTime, account.User.Username)
				}
			}

			for _, reserved := range oldHistory.Reserved {
				history.AddReserved(reserved)
			}

			if len(account.Devices) > 0 && account.DisableTime == nil {
				history.IsActive = true
			}

			if err := DisplayNameHistoryStore(ctx, nk, userID, history); err != nil {
				return fmt.Errorf("error storing display name history: %w", err)
			}

			if err := nk.StorageDelete(ctx, []*runtime.StorageDelete{
				{
					Collection: "DisplayNames",
					Key:        "history",
					UserID:     userID,
				},
			}); err != nil {
				logger.WithField("error", err).Error("Error deleting login history")
			}

		}
	}

	return nil
}

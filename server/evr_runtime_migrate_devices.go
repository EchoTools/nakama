package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

var _ = SystemMigrator(&MigrationDevicesHistory{})

type MigrationDevicesHistory struct{}

func (m *MigrationDevicesHistory) MigrateSystem(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) error {

	startTime := time.Now()

	oldEntries, loginHistories, err := queryAllDevicesHistoryUsers(ctx, logger, db)
	if err != nil {
		return err
	}

	if len(oldEntries) == 0 {
		logger.Info("No Devices History to migrate")
	}

	logger.WithFields(map[string]interface{}{
		"count": len(oldEntries),
	}).Info("Migrating Devices History")

	writeOps := make([]*runtime.StorageWrite, 0)
	deleteOps := make([]*runtime.StorageDelete, 0)

	for userID, entries := range oldEntries {

		loginHistory, ok := loginHistories[userID]
		if !ok {
			loginHistory = NewLoginHistory(userID)
		}

		updated := 0
		for id, oldentry := range entries {
			if loginHistory.History == nil {
				loginHistory.History = make(map[string]*LoginHistoryEntry)
			}

			if _, ok := loginHistory.History[id]; !ok {
				loginHistory.History[id] = oldentry
				updated += 1
			}
		}

		if updated > 0 {

			// Clear the notified groups
			loginHistory.GroupNotifications = make(map[string]map[string]time.Time)

			op, err := loginHistory.StorageWriteOp()
			if err != nil {
				logger.WithFields(map[string]interface{}{
					"user_id": userID,
					"error":   err,
				}).Error("Failed to create StorageWriteOp")
				continue
			}

			writeOps = append(writeOps, op)

			logger.WithFields(map[string]interface{}{
				"user_id": userID,
				"count":   updated,
			}).Info("Migrated Devices History")
		}

		// Delete old history
		deleteOps = append(deleteOps, &runtime.StorageDelete{
			Collection: "Devices",
			Key:        "history",
			UserID:     userID,
		})
	}

	for i := 0; i < len(writeOps); i += 100 {
		opStartTime := time.Now()
		end := i + 100
		if end > len(writeOps) {
			end = len(writeOps)
		}

		if _, err := nk.StorageWrite(ctx, writeOps[i:end]); err != nil {
			logger.WithField("error", err).Error("Failed to write migrated Devices History")
		}

		<-time.After(time.Since(opStartTime)) // Give the system time to recover
	}

	for i := 0; i < len(deleteOps); i += 100 {
		opStartTime := time.Now()
		end := i + 100
		if end > len(deleteOps) {
			end = len(deleteOps)
		}

		if err := nk.StorageDelete(ctx, deleteOps[i:end]); err != nil {
			logger.WithField("error", err).Error("Failed to delete old Devices History")
		}
		<-time.After(time.Since(opStartTime)) // Give the system time to recover
	}

	logger.WithFields(map[string]interface{}{
		"write_ops":        len(writeOps),
		"total_delete_ops": len(deleteOps),
		"duration":         time.Since(startTime),
	}).Info("Migrated all Devices History")

	return nil
}

func queryAllDevicesHistoryUsers(ctx context.Context, logger runtime.Logger, db *sql.DB) (map[string]map[string]*LoginHistoryEntry, map[string]*LoginHistory, error) {
	query := "SELECT collection, user_id, value, version FROM storage WHERE (collection = 'Devices' OR collection = 'Login') AND key = 'history'"
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	oldEntries := make(map[string]map[string]*LoginHistoryEntry, 0)
	currentHistories := make(map[string]*LoginHistory, 0)

	totalSize := 0
	for rows.Next() {
		var collection string
		var userID string
		var value string
		var version string
		if err := rows.Scan(&collection, &userID, &value, &version); err != nil {
			return nil, nil, err
		}

		totalSize += len(value)

		switch collection {
		case "Devices":
			var devicesHistory map[string]interface{}
			if err := json.Unmarshal([]byte(value), &devicesHistory); err != nil {
				logger.WithFields(map[string]interface{}{
					"user_id": userID,
					"error":   err,
				}).Error("Failed to unmarshal Devices History")
				continue
			}

			data, err := json.Marshal(devicesHistory["history"])
			if err != nil {
				logger.WithFields(map[string]interface{}{
					"user_id": userID,
					"error":   err,
				}).Error("Failed to marshal Devices History")
				continue
			}

			var entries map[string]*LoginHistoryEntry
			if err := json.Unmarshal(data, &entries); err != nil {
				logger.WithFields(map[string]interface{}{
					"user_id": userID,
					"error":   err,
				}).Error("Failed to unmarshal Devices History Entries")
				continue
			}

			oldEntries[userID] = entries

		case "Login":
			var loginHistory LoginHistory
			if err := json.Unmarshal([]byte(value), &loginHistory); err != nil {
				logger.WithFields(map[string]interface{}{
					"user_id": userID,
					"error":   err,
				}).Error("Failed to unmarshal Login History")
				continue
			}
			loginHistory.userID = userID
			loginHistory.version = version

			currentHistories[userID] = &loginHistory

		}
	}

	// Drop all currentHistories that do not have oldHistories
	for userID := range currentHistories {
		if _, ok := oldEntries[userID]; !ok {
			delete(currentHistories, userID)
		}
	}

	logger.Debug("Total size of Devices History: %d", totalSize)
	return oldEntries, currentHistories, nil
}

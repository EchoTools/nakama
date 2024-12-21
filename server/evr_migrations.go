package server

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

func MigrateUserData(ctx context.Context, nk runtime.NakamaModule, db *sql.DB, userID string, history *LoginHistory) error {
	var err error
	// EvrLogin

	var objs []*api.StorageObject

	removeOperations := make([]*runtime.StorageDelete, 0)

	if err := CombineHistoryRecords(ctx, nk, db, history); err != nil {
		return fmt.Errorf("error combining history records for %s: %w", userID, err)
	}

	if err := history.UpdateAlternateUserIDs(ctx, nk); err != nil {
		return fmt.Errorf("error updating alternate user IDs: %w", err)
	}

	// Remove all legacy records
	for _, collection := range []string{RemoteLogStorageCollection, DisplayNameCollection} {
		cursor := ""
		for {
			objs, cursor, err = nk.StorageList(ctx, SystemUserID, userID, collection, 100, cursor)
			if err != nil {
				return fmt.Errorf("error listing login history: %w", err)
			}

			for _, record := range objs {
				if record.Key == DisplayNameHistoryKey || record.Key == RemoteLogStorageJournalKey {
					continue
				}
				removeOperations = append(removeOperations, &runtime.StorageDelete{
					Collection: record.Collection,
					Key:        record.Key,
					UserID:     record.UserId,
					Version:    record.Version,
				})
			}

			if cursor == "" {
				break
			}
		}
	}

	if len(removeOperations) > 0 {
		if err := nk.StorageDelete(ctx, removeOperations); err != nil {
			return fmt.Errorf("error deleting records: %w", err)
		}
	}

	return nil
}

func CombineHistoryRecords(ctx context.Context, nk runtime.NakamaModule, db *sql.DB, loginHistory *LoginHistory) error {

	needsUpdate := false

	for k, _ := range loginHistory.History {
		xpid, clientIP, _ := strings.Cut(k, ":")
		if xpid == "" || xpid == "UNK-0" || clientIP == "" {
			needsUpdate = true
			break
		}
	}

	if !needsUpdate {
		return nil
	}

	combined := make(map[time.Time]*LoginHistoryEntry)
	for _, entry := range loginHistory.History {
		// round UpdatedAt to the nearest minute
		rounded := entry.UpdatedAt.Round(time.Minute)
		if _, ok := combined[rounded]; !ok {
			combined[rounded] = entry
		} else {
			if combined[rounded].XPID.IsNil() {
				combined[rounded].XPID = entry.XPID
			}
			if combined[rounded].ClientIP == "" {
				combined[rounded].ClientIP = entry.ClientIP
			}
		}
	}

	for _, entry := range combined {
		loginHistory.Insert(entry)
	}

	// Delete entries missing xpi or clientIP
	for k, _ := range loginHistory.History {
		xpid, clientIP, _ := strings.Cut(k, ":")
		if xpid == "" || xpid == "UNK-0" || clientIP == "" {
			delete(loginHistory.History, k)
		}
	}

	return nil
}

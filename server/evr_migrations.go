package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

func MigrateUserData(ctx context.Context, nk runtime.NakamaModule, db *sql.DB, userID string) error {

	history, err := LoginHistoryLoad(ctx, nk, userID)
	if err != nil {
		return fmt.Errorf("error getting device history: %w", err)
	}

	// EvrLogin
	evrLoginStorageCollection := "EvrLogins"
	clientAddrStorageCollection := "ClientAddrs"

	var objs []*api.StorageObject

	removeOperations := make([]*runtime.StorageDelete, 0)
	for _, collection := range []string{evrLoginStorageCollection, clientAddrStorageCollection} {
		cursor := ""
		for {

			if objs, cursor, err = nk.StorageList(ctx, SystemUserID, userID, collection, 100, cursor); err != nil {
				return fmt.Errorf("storage list error: %w", err)
			}

			for _, record := range objs {

				loginProfile := &evr.LoginProfile{}
				if err := json.Unmarshal([]byte(record.Value), loginProfile); err != nil {
					return fmt.Errorf("error unmarshalling login profile for %s: %v", record.GetKey(), err)
				}

				// Replace _'s with - in EvrID
				strings.Replace(record.Key, "OVR_ORG", "OVR-ORG", 1)

				var xpid evr.EvrId
				var clientAddr string

				switch record.Collection {
				case evrLoginStorageCollection:
					e, err := evr.ParseEvrId(record.Key)
					if err != nil {
						return fmt.Errorf("error parsing evrID: %w", err)
					}
					xpid = *e
				case clientAddrStorageCollection:
					clientAddr = record.Key
					if history.AuthorizedIPs == nil {
						history.AuthorizedIPs = make(map[string]time.Time)
					}
					// If it's within the past month
					if record.UpdateTime.AsTime().After(time.Now().AddDate(0, -1, 0)) {
						history.AuthorizedIPs[clientAddr] = record.UpdateTime.AsTime()
					}
				}

				history.Insert(&LoginHistoryEntry{
					UpdatedAt: record.UpdateTime.AsTime(),
					XPID:      xpid,
					ClientIP:  clientAddr,
					LoginData: loginProfile,
				})

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

	if err := CombineHistoryRecords(ctx, nk, db, userID, history); err != nil {
		return fmt.Errorf("error combining history records for %s: %w", userID, err)
	}

	if err := history.UpdateAlternateUserIDs(ctx, nk, userID); err != nil {
		return fmt.Errorf("error updating alternate user IDs: %w", err)
	}

	if err := LoginHistoryStore(ctx, nk, userID, history); err != nil {
		return fmt.Errorf("error storing login history: %w", err)
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

func CombineHistoryRecords(ctx context.Context, nk runtime.NakamaModule, db *sql.DB, userID string, loginHistory *LoginHistory) error {

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

func MigrateAllUserData(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, db *sql.DB) error {

	var cursor string
	var results []*api.StorageObject
	var err error

	for {
		if results, cursor, err = nk.StorageList(ctx, SystemUserID, "", "Devices", 100, cursor); err != nil {
			return fmt.Errorf("storage list error: %w", err)
		}

		for _, r := range results {
			loginHistory, err := LoginHistoryLoad(ctx, nk, r.UserId)
			if err != nil {
				return fmt.Errorf("error loading login history: %w", err)
			}

			if err := CombineHistoryRecords(ctx, nk, db, r.UserId, loginHistory); err != nil {
				return fmt.Errorf("error combining history records for %s: %w", r.UserId, err)
			}

			if err := loginHistory.UpdateAlternateUserIDs(ctx, nk, r.UserId); err != nil {
				return fmt.Errorf("error updating alternate user IDs: %w", err)
			}

			if err := LoginHistoryStore(ctx, nk, r.UserId, loginHistory); err != nil {
				return fmt.Errorf("error storing login history: %w", err)
			}
		}

		if cursor == "" {
			break
		}

	}

	cursor = ""

	for {

		if results, cursor, err = nk.StorageList(ctx, SystemUserID, "", "EvrLogins", 100, cursor); err != nil {
			return fmt.Errorf("storage list error: %w", err)
		}

		for _, r := range results {
			if err := MigrateUserData(ctx, nk, db, r.UserId); err != nil {
				return fmt.Errorf("error migrating user data for %s: %w", r.UserId, err)
			}

			logger.Debug("Migrated user data for %s", r.UserId)
		}

		if cursor == "" {
			break
		}
	}

	return nil
}

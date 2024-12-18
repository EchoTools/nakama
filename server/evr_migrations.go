package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

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

			// Associate evrLogins with clientAddrs by date/time

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

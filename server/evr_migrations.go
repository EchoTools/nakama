package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

func MigrateUserData(ctx context.Context, nk runtime.NakamaModule, db *sql.DB, userID string) error {
	var err error
	// EvrLogin

	if err := MigrateUserCosmetics(ctx, nk, userID); err != nil {
		return fmt.Errorf("error migrating user cosmetics: %w", err)
	}

	history, err := LoginHistoryLoad(ctx, nk, userID)
	if err != nil {
		return fmt.Errorf("error loading login history: %w", err)
	}

	var objs []*api.StorageObject

	removeOperations := make([]*runtime.StorageDelete, 0)

	if err := CombineHistoryRecords(ctx, nk, db, history); err != nil {
		return fmt.Errorf("error combining history records for %s: %w", userID, err)
	}

	if err := history.UpdateAlternates(ctx, nk); err != nil {
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

func Migrations(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) {
	// Combine the loadouts into one storage object

	if err := CombineStoredCosmeticLoadouts(ctx, nk); err != nil {
		logger.WithField("error", err).Error("Error combining stored cosmetic loadouts")
	}

	// Remove all leaderboards with uuid's for names
	cursor := ""
	for {
		list, err := nk.LeaderboardList(100, cursor)
		if err != nil {
			logger.WithField("error", err).Error("Error listing leaderboards")
			return
		}
		cursor = list.Cursor
		for _, leaderboard := range list.Leaderboards {
			delete := false
			// If the board doesn't have two colons, delete it.
			if s := strings.SplitN(leaderboard.Id, ":", 3); len(s) != 3 {
				delete = true
			}

			records, _, _, _, err := nk.LeaderboardRecordsList(ctx, leaderboard.Id, nil, 200, "", 0)
			if err != nil {
				continue
			}

			if len(records) <= 1 {
				delete = true
			}

			if !delete {
				continue
			}
			if err := nk.LeaderboardDelete(ctx, leaderboard.Id); err != nil {
				logger.WithField("error", err).Error("Error deleting leaderboard")
				return
			}

			logger.WithField("id", leaderboard.Id).Info("Deleted leaderboard.")
		}

		if cursor == "" {
			break
		}
	}

}

func CombineStoredCosmeticLoadouts(ctx context.Context, nk runtime.NakamaModule) error {
	objs, _, err := nk.StorageList(ctx, uuid.Nil.String(), uuid.Nil.String(), CosmeticLoadoutCollection, 100, "")
	if err != nil {
		return fmt.Errorf("failed to list objects: %w", err)
	}

	combined := make([]*StoredCosmeticLoadout, 0, len(objs))

	for _, obj := range objs {

		loadout := &StoredCosmeticLoadout{}
		if err := json.Unmarshal([]byte(obj.Value), loadout); err != nil {
			return fmt.Errorf("failed to unmarshal object: %w", err)
		}

		combined = append(combined, loadout)
	}

	data, err := json.Marshal(combined)
	if err != nil {
		return fmt.Errorf("failed to marshal combined loadouts: %w", err)
	}

	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:     CosmeticLoadoutCollection,
			Key:            CosmeticLoadoutKey,
			UserID:         uuid.Nil.String(),
			Value:          string(data),
			PermissionRead: 2,
		},
	}); err != nil {
		return fmt.Errorf("failed to write combined loadouts: %w", err)
	}

	// Remove the old loadout
	ops := make([]*runtime.StorageDelete, 0, len(objs))
	for _, obj := range objs {
		ops = append(ops, &runtime.StorageDelete{
			Collection: obj.Collection,
			Key:        obj.Key,
			UserID:     obj.UserId,
			Version:    obj.Version,
		})
	}

	if len(ops) > 0 {
		if err := nk.StorageDelete(ctx, ops); err != nil {
			return fmt.Errorf("failed to delete old loadouts: %w", err)
		}
	}

	return nil
}

func MigrateUserCosmetics(ctx context.Context, nk runtime.NakamaModule, userID string) error {
	account, err := nk.AccountGetId(ctx, userID)
	if err != nil {
		return err
	}

	wallet := make(map[string]int64, 0)
	if err := json.Unmarshal([]byte(account.Wallet), &wallet); err != nil {
		return err
	}

	changeset := WalletMigrateVRML(wallet)

	if len(changeset) == 0 {
		return nil
	}

	if _, _, err := nk.WalletUpdate(ctx, userID, changeset, nil, false); err != nil {
		return err
	}

	return nil
}

func WalletMigrateVRML(wallet map[string]int64) map[string]int64 {
	// Set the user's unlocked cosmetics based on their groups

	walletMap := map[string][]string{
		"VRML Season Preseason": {
			"rwd_tag_s1_vrml_preseason",
			"rwd_medal_s1_vrml_preseason",
		},
		"VRML Season 1": {
			"rwd_tag_s1_vrml_s1",
			"rwd_medal_s1_vrml_s1_user",
		},
		"VRML Season 1 Finalist": {
			"rwd_medal_s1_vrml_s1_finalist",
			"rwd_tag_s1_vrml_s1_finalist",
		},
		"VRML Season 1 Champion": {
			"rwd_medal_s1_vrml_s1_champion",
			"rwd_tag_s1_vrml_s1_champion",
		},
		"VRML Season 2": {
			"rwd_tag_s1_vrml_s2",
			"rwd_medal_s1_vrml_s2",
		},
		"VRML Season 2 Finalist": {
			"rwd_medal_s1_vrml_s2_finalist",
			"rwd_tag_s1_vrml_s2_finalist",
		},
		"VRML Season 2 Champion": {
			"rwd_medal_s1_vrml_s2_champion",
			"rwd_tag_s1_vrml_s2_champion",
		},
		"VRML Season 3": {
			"rwd_medal_s1_vrml_s3",
			"rwd_tag_s1_vrml_s3",
		},
		"VRML Season 3 Finalist": {
			"rwd_medal_s1_vrml_s3_finalist",
			"rwd_tag_s1_vrml_s3_finalist",
		},
		"VRML Season 3 Champion": {
			"rwd_medal_s1_vrml_s3_champion",
			"rwd_tag_s1_vrml_s3_champion",
		},
		"VRML Season 4": {
			"rwd_tag_0008",
			"rwd_medal_0006",
		},
		"VRML Season 4 Finalist": {
			"rwd_tag_0009",
			"rwd_medal_0007",
		},
		"VRML Season 4 Champion": {
			"rwd_tag_0010",
			"rwd_medal_0008",
		},
		"VRML Season 5": {
			"rwd_tag_0035",
		},
		"VRML Season 5 Finalist": {
			"rwd_tag_0036",
		},
		"VRML Season 5 Champion": {
			"rwd_tag_0037",
		},
		"VRML Season 6": {
			"rwd_tag_0040",
		},
		"VRML Season 6 Finalist": {
			"rwd_tag_0041",
		},
		"VRML Season 6 Champion": {
			"rwd_tag_0042",
		},
		"VRML Season 7": {
			"rwd_tag_0043",
		},
		"VRML Season 7 Finalist": {
			"rwd_tag_0044",
		},
		"VRML Season 7 Champion": {
			"rwd_tag_0045",
		},
	}

	changeset := make(map[string]int64, 0)
	for k, v := range wallet {
		if _, ok := walletMap[k]; ok {
			if v > 0 {
				changeset[k] = v * -1
				for _, item := range walletMap[k] {
					if _, ok := wallet[item]; !ok {
						changeset[item] = 1
					}
				}
			}
		}
	}

	return changeset
}

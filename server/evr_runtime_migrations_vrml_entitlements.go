package server

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/heroiclabs/nakama-common/runtime"
)

type MigrationUserVRMLEntitlementsToWallet struct{}

// Migrate all VRML entitlements to the user's wallet
func (m *MigrationUserVRMLEntitlementsToWallet) MigrateUser(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, userID string) error {
	account, err := nk.AccountGetId(ctx, userID)
	if err != nil {
		return err
	}

	wallet := make(map[string]int64, 0)
	if err := json.Unmarshal([]byte(account.Wallet), &wallet); err != nil {
		return err
	}

	changeset := m.migrateWallet(wallet)

	if len(changeset) == 0 {
		return nil
	}

	if _, _, err := nk.WalletUpdate(ctx, userID, changeset, nil, false); err != nil {
		return err
	}

	return nil
}

func (MigrationUserVRMLEntitlementsToWallet) migrateWallet(wallet map[string]int64) map[string]int64 {
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

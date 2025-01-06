package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/types"
	"go.uber.org/zap"
)

var _ = UserMigrator(&MigrateUserGameProfile{})

type MigrateUserGameProfile struct{}

func (m *MigrateUserGameProfile) MigrateUser(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, userID string) error {

	// Get the game profile
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: "GameProfiles",
			Key:        "gameProfile",
			UserID:     userID,
		},
	})
	if err != nil {
		logger.Error("Error fetching game profile", zap.Error(err))
		return fmt.Errorf("Error fetching game profile: %v", err)
	}
	if len(objs) == 0 {
		// Already migrated
		return nil
	}

	a, err := nk.AccountGetId(ctx, userID)
	if err != nil {
		logger.Error("Error fetching account", zap.Error(err))
		return fmt.Errorf("Error fetching account: %v", err)
	}

	account, err := NewEVRAccount(a)
	if err != nil {
		logger.Error("Error converting account", zap.Error(err))
		return fmt.Errorf("Error converting account: %v", err)
	}

	type GameProfileData struct {
		sync.RWMutex
		Login        evr.LoginProfile                          `json:"login"`
		Client       evr.ClientProfile                         `json:"client"`
		Server       evr.ServerProfile                         `json:"server"`
		Ratings      map[uuid.UUID]map[evr.Symbol]types.Rating `json:"ratings"`
		EarlyQuits   EarlyQuitStatistics                       `json:"early_quit"`
		GameSettings evr.RemoteLogGameSettings                 `json:"game_settings"`
		version      string                                    // The version of the profile from the DB
		isModified   bool                                      // Whether the profile is stale and needs to be updated
		jsonData     *json.RawMessage                          // The raw JSON data of the profile
	}

	profile := GameProfileData{}
	if err := json.Unmarshal([]byte(objs[0].Value), &profile); err != nil {
		logger.WithField("error", err).Error("Error unmarshalling game profile")
		return fmt.Errorf("Error unmarshalling game profile: %v", err)
	}

	errored := false
	if err := m.migrateClient(account, profile.Client); err != nil {
		logger.Error("Error migrating client profile", zap.Error(err))
		return fmt.Errorf("Error migrating client profile: %v", err)
	}

	if err := m.migrateMatchmakingSettings(ctx, nk, account); err != nil {
		logger.Error("Error migrating matchmaking settings", zap.Error(err))
		return fmt.Errorf("Error migrating matchmaking settings: %v", err)
	}

	if err := m.migrateRatings(ctx, nk, account, profile.Ratings); err != nil {
		logger.Error("Error migrating server profile", zap.Error(err))
		return fmt.Errorf("Error migrating server profile: %v", err)
	}

	if err := m.migrateServer(account, profile.Server); err != nil {
		logger.Error("Error migrating server profile", zap.Error(err))
		return fmt.Errorf("Error migrating server profile: %v", err)
	}

	// Save the account
	if err := nk.AccountUpdateId(ctx, userID, "", account.AccountMetadata.MarshalMap(), "", "", "", "", ""); err != nil {
		logger.Error("Error updating account", zap.Error(err))
		return fmt.Errorf("Error updating account: %v", err)
	}
	if !errored {

		// Delete the old profile
		if err := nk.StorageDelete(ctx, []*runtime.StorageDelete{
			{
				Collection: "GameProfiles",
				Key:        "gameProfile",
				UserID:     userID,
			},
		}); err != nil {
			logger.Error("Error deleting old game profile", zap.Error(err))
			return fmt.Errorf("Error deleting old game profile: %v", err)
		}

	}

	// Extract the rating, and put it in the leaderboard
	return nil
}

func (m *MigrateUserGameProfile) migrateClient(account *EVRAccount, profile evr.ClientProfile) error {

	account.CombatLoadout.CombatAbility = profile.CombatAbility
	account.CombatLoadout.CombatGrenade = profile.CombatGrenade
	account.CombatLoadout.CombatWeapon = profile.CombatWeapon
	account.CombatLoadout.CombatDominantHand = profile.CombatDominantHand
	account.TeamName = profile.TeamName
	account.LegalConsents = profile.LegalConsents

	return nil

}

func (m *MigrateUserGameProfile) migrateRatings(ctx context.Context, nk runtime.NakamaModule, account *EVRAccount, ratings map[uuid.UUID]map[evr.Symbol]types.Rating) error {

	for groupID, ratings := range ratings {
		for mode, rating := range ratings {
			if err := MatchmakingRatingStore(ctx, nk, account.User.Id, account.User.Username, groupID.String(), mode, rating); err != nil {
				return fmt.Errorf("Error storing rating: %v", err)
			}
		}
	}
	return nil
}

func (m *MigrateUserGameProfile) migrateServer(account *EVRAccount, profile evr.ServerProfile) error {
	account.AccountMetadata.LoadoutCosmetics.JerseyNumber = int64(profile.EquippedCosmetics.Number)
	account.AccountMetadata.LoadoutCosmetics.Loadout = profile.EquippedCosmetics.Instances.Unified.Slots
	return nil
}

func (m *MigrateUserGameProfile) migrateMatchmakingSettings(ctx context.Context, nk runtime.NakamaModule, account *EVRAccount) error {
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: MatchmakerStorageCollection,
			Key:        MatchmakingConfigStorageKey,
			UserID:     account.User.Id,
		},
	})
	if err != nil {
		return fmt.Errorf("Error fetching matchmaker data: %v", err)
	}
	if len(objs) == 0 {
		return nil
	}

	oldSettings := map[string]interface{}{}

	if err := json.Unmarshal([]byte(objs[0].Value), &oldSettings); err != nil {
		return fmt.Errorf("Error unmarshalling matchmaker data: %v", err)
	}

	newSettings := MatchmakingSettings{}

	var ok bool
	newSettings.LobbyGroupName, ok = oldSettings["lobby_group_name"].(string)
	if !ok {
		newSettings.LobbyGroupName = ""
	}

	data, err := json.Marshal(newSettings)
	if err != nil {
		return fmt.Errorf("Error marshalling new matchmaker data: %v", err)
	}

	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection: MatchmakerStorageCollection,
			Key:        MatchmakingConfigStorageKey,
			UserID:     account.User.Id,
			Value:      string(data),
		},
	}); err != nil {
		return fmt.Errorf("Error writing new matchmaker data: %v", err)
	}
	return nil
}

package server

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

var _ = UserMigrator(&MigrateUserIncompleteProfile{})

type MigrateUserIncompleteProfile struct{}

func (m *MigrateUserIncompleteProfile) MigrateUser(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, userID string) error {

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

	isUpdated := false

	if account.AccountMetadata.LoadoutCosmetics.Loadout == (evr.CosmeticLoadout{}) {
		account.AccountMetadata.LoadoutCosmetics.Loadout = evr.DefaultCosmeticLoadout()
		isUpdated = true
	} else {

		d := evr.DefaultCosmeticLoadout()
		// Check if any of the cosmetics are empty
		if account.AccountMetadata.LoadoutCosmetics.Loadout.Banner == "" {
			account.AccountMetadata.LoadoutCosmetics.Loadout.Banner = d.Banner
			isUpdated = true
		}
		if account.AccountMetadata.LoadoutCosmetics.Loadout.Booster == "" {
			account.AccountMetadata.LoadoutCosmetics.Loadout.Booster = d.Booster
			isUpdated = true
		}
		if account.AccountMetadata.LoadoutCosmetics.Loadout.Bracer == "" {
			account.AccountMetadata.LoadoutCosmetics.Loadout.Bracer = d.Bracer
			isUpdated = true
		}
		if account.AccountMetadata.LoadoutCosmetics.Loadout.Chassis == "" {
			account.AccountMetadata.LoadoutCosmetics.Loadout.Chassis = d.Chassis
			isUpdated = true
		}
		if account.AccountMetadata.LoadoutCosmetics.Loadout.Decal == "" {
			account.AccountMetadata.LoadoutCosmetics.Loadout.Decal = d.Decal
			isUpdated = true
		}
		if account.AccountMetadata.LoadoutCosmetics.Loadout.DecalBody == "" {
			account.AccountMetadata.LoadoutCosmetics.Loadout.DecalBody = d.DecalBody
			isUpdated = true
		}

		if account.AccountMetadata.LoadoutCosmetics.Loadout.Emissive == "" {
			account.AccountMetadata.LoadoutCosmetics.Loadout.Emissive = d.Emissive
			isUpdated = true
		}
		if account.AccountMetadata.LoadoutCosmetics.Loadout.Emote == "" {
			account.AccountMetadata.LoadoutCosmetics.Loadout.Emote = d.Emote
			isUpdated = true
		}
		if account.AccountMetadata.LoadoutCosmetics.Loadout.GoalFX == "" {
			account.AccountMetadata.LoadoutCosmetics.Loadout.GoalFX = d.GoalFX
			isUpdated = true
		}
		if account.AccountMetadata.LoadoutCosmetics.Loadout.Medal == "" {
			account.AccountMetadata.LoadoutCosmetics.Loadout.Medal = d.Medal
			isUpdated = true
		}
		if account.AccountMetadata.LoadoutCosmetics.Loadout.Pattern == "" {
			account.AccountMetadata.LoadoutCosmetics.Loadout.Pattern = d.Pattern
			isUpdated = true
		}
		if account.AccountMetadata.LoadoutCosmetics.Loadout.PatternBody == "" {
			account.AccountMetadata.LoadoutCosmetics.Loadout.PatternBody = d.PatternBody
			isUpdated = true
		}
		if account.AccountMetadata.LoadoutCosmetics.Loadout.PIP == "" {
			account.AccountMetadata.LoadoutCosmetics.Loadout.PIP = d.PIP
			isUpdated = true
		}
		if account.AccountMetadata.LoadoutCosmetics.Loadout.SecondEmote == "" {
			account.AccountMetadata.LoadoutCosmetics.Loadout.SecondEmote = d.SecondEmote
			isUpdated = true
		}
		if account.AccountMetadata.LoadoutCosmetics.Loadout.Tag == "" {
			account.AccountMetadata.LoadoutCosmetics.Loadout.Tag = d.Tag
		}
		if account.AccountMetadata.LoadoutCosmetics.Loadout.Tint == "" {
			account.AccountMetadata.LoadoutCosmetics.Loadout.Tint = d.Tint
			isUpdated = true
		}
		if account.AccountMetadata.LoadoutCosmetics.Loadout.TintAlignmentA == "" {
			account.AccountMetadata.LoadoutCosmetics.Loadout.TintAlignmentA = d.TintAlignmentA
			isUpdated = true
		}
		if account.AccountMetadata.LoadoutCosmetics.Loadout.TintAlignmentB == "" {
			account.AccountMetadata.LoadoutCosmetics.Loadout.TintAlignmentB = d.TintAlignmentB
			isUpdated = true
		}
		if account.AccountMetadata.LoadoutCosmetics.Loadout.TintBody == "" {
			account.AccountMetadata.LoadoutCosmetics.Loadout.TintBody = d.TintBody
			isUpdated = true
		}
		if account.AccountMetadata.LoadoutCosmetics.Loadout.Title == "" {
			account.AccountMetadata.LoadoutCosmetics.Loadout.Title = d.Title
			isUpdated = true
		}
	}

	if isUpdated {
		// Save the account
		if err := nk.AccountUpdateId(ctx, userID, "", account.AccountMetadata.MarshalMap(), account.AccountMetadata.GetActiveGroupDisplayName(), "", "", "", ""); err != nil {
			logger.Error("Error updating account", zap.Error(err))
			return fmt.Errorf("Error updating account: %v", err)
		}
	}
	// Extract the rating, and put it in the leaderboard
	return nil
}

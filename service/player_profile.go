package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/heroiclabs/nakama-common/runtime"
)

var (
	ErrProfileNotFound = fmt.Errorf("profile not found")
)

const (
	StorageCollectionPlayerProfile = "PlayerProfile"
	StorageKeyPlayerProfileDefault = "default"
	StorageIndexPlayerProfile      = "PlayerProfileIndex"
	// StorageKey is the guild ID
)

// PlayerProfile is a wrapper around evr.ServerProfile to implement StorableIndexer
type PlayerProfile struct {
	evr.ServerProfile
	// internal metadata for storage
	meta StorableMetadata
}

func (h *PlayerProfile) GuildGroupID() string {
	return h.Social.Channel.String()
}

func (h *PlayerProfile) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection:      StorageCollectionPlayerProfile,
		Key:             StorageKeyPlayerProfileDefault,
		PermissionRead:  2,
		PermissionWrite: 0,
		Version:         "", // No version tracking
	}
}

func (h *PlayerProfile) SetStorageMeta(meta StorableMetadata) {} // No-op, metadata is static

func (h *PlayerProfile) StorageIndexes() []StorableIndexMeta {
	return []StorableIndexMeta{{
		Name:           StorageIndexPlayerProfile,
		Collection:     StorageCollectionPlayerProfile,
		Fields:         []string{"xplatformid", "social.channel", "displayname"},
		SortableFields: nil,
		MaxEntries:     500,
		IndexOnly:      false,
	}}
}

func PlayerProfileFromParameters(ctx context.Context, db *sql.DB, nk runtime.NakamaModule, params SessionParameters, groupID string, modes []evr.Symbol, dailyWeeklyMode evr.Symbol) (*evr.ServerProfile, error) {
	return NewPlayerProfile(ctx, db, nk, params.profile, params.xpID, groupID, modes, dailyWeeklyMode, params.profile.GetGroupIGN(groupID))
}

func NewPlayerProfile(ctx context.Context, db *sql.DB, nk runtime.NakamaModule, evrProfile *EVRProfile, xpID evr.XPID, groupID string, modes []evr.Symbol, dailyWeeklyMode evr.Symbol, displayName string) (*evr.ServerProfile, error) {

	var wallet map[string]int64
	if err := json.Unmarshal([]byte(evrProfile.Wallet()), &wallet); err != nil {
		return nil, fmt.Errorf("failed to unmarshal wallet: %w", err)
	}

	cosmetics := make(map[string]map[string]bool)
	for m, c := range cosmeticDefaults(evrProfile.EnableAllCosmetics) {
		cosmetics[m] = make(map[string]bool, len(c))
		maps.Copy(cosmetics[m], c)
	}
	cosmetics = walletToCosmetics(wallet, cosmetics)

	cosmeticLoadout := evrProfile.LoadoutCosmetics.Loadout
	// If the player has "kissy lips" emote equipped, set their emote to default.
	if cosmeticLoadout.Emote == "emote_kissy_lips_a" {
		cosmeticLoadout.Emote = "emote_blink_smiley_a"
		cosmeticLoadout.SecondEmote = "emote_blink_smiley_a"
	}

	var developerFeatures *evr.DeveloperFeatures

	if evrProfile.GoldDisplayNameActive {
		developerFeatures = &evr.DeveloperFeatures{}
	}

	// Default to their main group if they are not a member of the group
	if _, ok := evrProfile.GetGroupDisplayName(groupID); !ok || groupID == "" {
		groupID = evrProfile.GetActiveGroupID().String()

	}

	if slices.Equal(modes, []evr.Symbol{0}) {
		modes = []evr.Symbol{evr.ModeArenaPublic, evr.ModeCombatPublic}
	}

	statsBySchedule, _, err := PlayerStatisticsGetID(ctx, db, nk, evrProfile.ID(), groupID, modes, dailyWeeklyMode)
	if err != nil {
		return nil, fmt.Errorf("failed to get user tablet statistics: %w", err)
	}

	if evrProfile.DisableAFKTimeout {
		developerFeatures = &evr.DeveloperFeatures{
			DisableAfkTimeout: true,
		}
	}

	return &evr.ServerProfile{
		DisplayName:       displayName,
		XPID:              xpID,
		SchemaVersion:     4,
		PublisherLock:     "echovrce",
		LobbyVersion:      1680630467,
		PurchasedCombat:   1,
		Statistics:        statsBySchedule,
		UnlockedCosmetics: cosmetics,
		EquippedCosmetics: evr.EquippedCosmetics{
			Number:     int(evrProfile.LoadoutCosmetics.JerseyNumber),
			NumberBody: int(evrProfile.LoadoutCosmetics.JerseyNumber),
			Instances: evr.CosmeticInstances{
				Unified: evr.UnifiedCosmeticInstance{
					Slots: cosmeticLoadout,
				},
			},
		},

		Social: evr.ServerSocial{
			Channel: evr.GUID(evrProfile.GetActiveGroupID()),
		},
		DeveloperFeatures: developerFeatures,
	}, nil
}

// GetPlayerProfileData retrieves the player profile data (JSON bytes) for a given xplatformid
func GetPlayerProfileData(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, callerID string, xpid evr.XPID) (json.RawMessage, error) {
	query := fmt.Sprintf("+value.xplatformid:%s", xpid.String())
	order := []string{"-value.xplatformid"}
	objs, _, err := nk.StorageIndexList(ctx, callerID, StorageIndexPlayerProfile, query, 1, order, "")
	if err != nil {
		return nil, fmt.Errorf("failed to list storage objects: %w", err)
	} else if len(objs.GetObjects()) == 0 {
		return nil, ErrProfileNotFound
	}
	data := objs.GetObjects()[0].GetValue()
	return json.RawMessage(data), nil
}

func StorePlayerProfileData(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, profile *evr.ServerProfile) error {
	pProfile := &PlayerProfile{
		ServerProfile: *profile,
	}
	if err := StorableWriteNk(ctx, nk, userID, pProfile); err != nil {
		return fmt.Errorf("failed to store player profile: %w", err)
	}
	return nil
}

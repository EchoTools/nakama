package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/samber/lo"
)

const (
	StorageCollectionGroupProfile = "GroupProfile"
	StorageKeyUnlockedItems       = "unlocks"
)

type GroupProfile struct {
	UserID        string       `json:"user_id"`
	GroupID       string       `json:"group_id"`
	UnlockedItems []evr.Symbol `json:"unlocked_items"`
	NewUnlocks    []evr.Symbol `json:"new_unlocks"`
	UpdateTime    time.Time    `json:"update_time"`
}

func (p GroupProfile) GetStorageID() StorageID {
	return StorageID{Collection: StorageCollectionGroupProfile, Key: p.GroupID}

}

func (p *GroupProfile) UpdateUnlockedItems(updated []evr.Symbol) {
	// Update the unlocked items, adding the new ones to newUnlocks
	added, removed := lo.Difference(updated, p.UnlockedItems)

	if len(added) == 0 && len(removed) == 0 {
		return
	}

	p.UnlockedItems = updated
	p.NewUnlocks = append(p.NewUnlocks, added...)

	// Ensure that all new unlocks are unique, and exist in the updated list
	updatedNewUnlocks := make([]evr.Symbol, 0, len(p.NewUnlocks))

	seen := make(map[evr.Symbol]struct{}, len(p.NewUnlocks))
	for _, unlock := range p.NewUnlocks {
		if _, ok := seen[unlock]; !ok {
			seen[unlock] = struct{}{}
			updatedNewUnlocks = append(updatedNewUnlocks, unlock)
		}
	}

	p.NewUnlocks = updatedNewUnlocks
	p.UpdateTime = time.Now()
}

type AccountMetadata struct {
	DisplayNameOverride        string            `json:"display_name_override"` // The display name override
	GlobalBanReason            string            `json:"global_ban_reason"`     // The global ban reason
	ActiveGroupID              string            `json:"active_group_id"`       // The active group ID
	Cosmetics                  AccountCosmetics  `json:"cosmetics"`             // The loadout
	GroupDisplayNames          map[string]string `json:"group_display_names"`   // The display names for each guild map[groupID]displayName
	DisableAFKTimeout          bool              `json:"disable_afk_timeout"`   // Disable AFK detection
	TargetUserID               string            `json:"target_user_id"`        // The target user ID to follow in public spaces
	DiscordAccountCreationTime time.Time         `json:"discord_create_time"`
}

func (a *AccountMetadata) GetActiveGroupID() uuid.UUID {
	return uuid.FromStringOrNil(a.ActiveGroupID)
}

func (a *AccountMetadata) SetActiveGroupID(id uuid.UUID) {
	a.ActiveGroupID = id.String()
}

func (a *AccountMetadata) GetGroupDisplayNameOrDefault(groupID string) string {
	if a.GroupDisplayNames == nil {
		a.GroupDisplayNames = make(map[string]string)
	}
	if dn, ok := a.GroupDisplayNames[groupID]; ok {
		return dn
	}
	return a.GetActiveGroupDisplayName()
}

func (a *AccountMetadata) SetGroupDisplayName(groupID, displayName string) {
	if a.GroupDisplayNames == nil {
		a.GroupDisplayNames = make(map[string]string)
	}
	a.GroupDisplayNames[groupID] = displayName
}

func (a *AccountMetadata) GetActiveGroupDisplayName() string {
	if a.GroupDisplayNames == nil {
		a.GroupDisplayNames = make(map[string]string)
	}
	return a.GroupDisplayNames[a.ActiveGroupID]
}

func (a *AccountMetadata) MarshalToMap() map[string]interface{} {
	b, _ := json.Marshal(a)
	var m map[string]interface{}
	_ = json.Unmarshal(b, &m)
	return m
}

type AccountCosmetics struct {
	JerseyNumber int64               `json:"number"`           // The loadout number (jersey number)
	Loadout      evr.CosmeticLoadout `json:"cosmetic_loadout"` // The loadout
}
type EVRLoginRecord struct {
	EvrID        evr.EvrId
	LoginProfile *evr.LoginProfile
	CreateTime   time.Time
	UpdateTime   time.Time
}

func GetEVRRecords(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userId string) (map[evr.EvrId]EVRLoginRecord, error) {
	listRecords, _, err := nk.StorageList(ctx, SystemUserID, userId, EvrLoginStorageCollection, 100, "")
	if err != nil {
		logger.WithField("err", err).Error("Storage list error.")
		return nil, fmt.Errorf("storage list error: %v", err)
	}

	records := make(map[evr.EvrId]EVRLoginRecord, len(listRecords))

	for _, record := range listRecords {
		var loginProfile evr.LoginProfile
		if err := json.Unmarshal([]byte(record.Value), &loginProfile); err != nil {
			return nil, fmt.Errorf("error unmarshalling login profile for %s: %v", record.GetKey(), err)
		}
		evrID, err := evr.ParseEvrId(record.Key)
		if err != nil {
			return nil, fmt.Errorf("error parsing evrID: %v", err)
		}
		records[*evrID] = EVRLoginRecord{
			EvrID:        *evrID,
			LoginProfile: &loginProfile,
			CreateTime:   record.CreateTime.AsTime(),
			UpdateTime:   record.UpdateTime.AsTime(),
		}
	}

	return records, nil
}

func GetDisplayNameRecords(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userId string) ([]*api.StorageObject, error) {
	listRecords, _, err := nk.StorageList(ctx, SystemUserID, userId, DisplayNameCollection, 150, "")
	if err != nil {
		logger.WithField("err", err).Error("Storage list error.")
		return nil, fmt.Errorf("storage list error: %v", err)
	}
	return listRecords, nil
}

func GetAddressRecords(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userId string) ([]*api.StorageObject, error) {
	listRecords, _, err := nk.StorageList(ctx, SystemUserID, userId, ClientAddrStorageCollection, 100, "")
	if err != nil {
		logger.WithField("err", err).Error("Storage list error.")
		return nil, fmt.Errorf("storage list error: %v", err)
	}
	return listRecords, nil
}

func GetUserIpAddresses(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userId string) ([]*api.StorageObject, error) {
	listRecords, _, err := nk.StorageList(ctx, SystemUserID, userId, IpAddressIndex, 100, "")
	if err != nil {
		logger.WithField("err", err).Error("Storage list error.")
		return nil, fmt.Errorf("storage list error: %v", err)
	}
	return listRecords, nil
}

func GetDisplayNameByGroupID(ctx context.Context, nk runtime.NakamaModule, userID, groupID string) (string, error) {
	account, err := nk.AccountGetId(ctx, userID)
	if err != nil {
		return "", fmt.Errorf("error getting account: %w", err)
	}
	md := AccountMetadata{}
	err = json.Unmarshal([]byte(account.GetUser().GetMetadata()), &md)
	if err != nil {
		return account.GetUser().GetDisplayName(), fmt.Errorf("error unmarshalling account user metadata: %w", err)
	}
	if dn := md.DisplayNameOverride; dn != "" {
		return dn, nil
	} else if dn := md.GetGroupDisplayNameOrDefault(groupID); dn != "" {
		return dn, nil
	} else if dn := account.GetUser().GetDisplayName(); dn != "" {
		return dn, nil
	} else {
		return account.GetUser().GetUsername(), nil
	}
}

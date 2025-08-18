package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/echotools/vrmlgo/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	StorageCollectionVRML  = "VRML"
	StorageKeyVRMLSummary  = "summary"
	StorageIndexVRMLUserID = "Index_VRMLUserID"
)

var (
	ErrPlayerNotFound = errors.New("player not found")
)

type VRMLPlayerSummary struct {
	User                      *vrmlgo.User                    `json:"user"`
	Player                    *vrmlgo.Player                  `json:"player"`
	Teams                     map[string]*vrmlgo.Team         `json:"teams"`        // map[teamID]team
	MatchCountsBySeasonByTeam map[VRMLSeasonID]map[string]int `json:"match_counts"` // map[seasonID]map[teamID]matchCount
}

func (VRMLPlayerSummary) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection: StorageCollectionVRML,
		Key:        StorageKeyVRMLSummary,
	}
}

func (v VRMLPlayerSummary) SetStorageMeta(meta StorableMetadata) {
	// VRMLPlayerSummary doesn't track version, so nothing to set
}

func (VRMLPlayerSummary) StorageIndexes() []StorableIndexMeta {

	// Register the storage index
	return []StorableIndexMeta{
		{
			Name:       StorageIndexVRMLUserID,
			Collection: StorageCollectionVRML,
			Key:        StorageKeyVRMLSummary,
			Fields:     []string{"userID"},
			MaxEntries: 1000000,
			IndexOnly:  true,
		},
	}
}

func (s *VRMLPlayerSummary) Entitlements() []*VRMLEntitlement {

	matchCountBySeason := make(map[VRMLSeasonID]int)
	for sID, teamID := range s.MatchCountsBySeasonByTeam {
		for _, c := range teamID {
			matchCountBySeason[sID] += c
		}
	}

	// Validate match counts
	entitlements := make([]*VRMLEntitlement, 0)

	for seasonID, matchCount := range matchCountBySeason {

		switch seasonID {

		// Pre-season and Season 1 have different requirements
		case VRMLPreSeason, VRMLSeason1:
			if matchCount > 0 {
				entitlements = append(entitlements, &VRMLEntitlement{
					SeasonID: seasonID,
				})
			}

		default:
			if matchCount >= 10 {
				entitlements = append(entitlements, &VRMLEntitlement{
					SeasonID: seasonID,
				})
			}
		}
	}

	return entitlements
}

func GetVRMLAccountOwner(ctx context.Context, nk runtime.NakamaModule, vrmlUserID string) (string, error) {
	// Check if the account is already owned by another user
	objs, _, err := nk.StorageIndexList(ctx, SystemUserID, StorageIndexVRMLUserID, fmt.Sprintf("+value.userID:%s", vrmlUserID), 100, nil, "")
	if err != nil {
		return "", fmt.Errorf("error checking ownership: %w", err)
	}

	if len(objs.Objects) == 0 {
		return "", nil
	}

	return objs.Objects[0].UserId, nil
}

func VRMLDeviceID(vrmlUserID string) string {
	return DeviceIDPrefixVRML + vrmlUserID
}

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type VRMLSeasonID string
type VRMLPrestige int

const (
	VRMLPlayer VRMLPrestige = iota
	VRMLFinalist
	VRMLChampion
)

const (
	VRMLPreSeason VRMLSeasonID = "b-VHi9XjGIR0Pv17C7P_Tw2"
	VRMLSeason1   VRMLSeasonID = "PS5eV-VOdnRPCAxRix9xlQ2"
	VRMLSeason2   VRMLSeasonID = "NLQNdfR3j0lyy6eLpLhIYw2"
	VRMLSeason3   VRMLSeasonID = "XPtJ0s7XBpsbDHrjS0e_3g2"
	VRMLSeason4   VRMLSeasonID = "YJoYnb3iWN8EcCrs92U04A2"
	VRMLSeason5   VRMLSeasonID = "XKVgWTi5AdpkSQnLhhs7bw2"
	VRMLSeason6   VRMLSeasonID = "n0PsCrBx_-rpjiXABiftSw2"
	VRMLSeason7   VRMLSeasonID = "la1xDIFPC6E-0eo429SKFA2"

	VRMLEchoArenaShortName = "EchoArena"
)

var vrmlSeasonDescriptionMap = map[VRMLSeasonID]string{
	VRMLPreSeason: "Pre-Season",
	VRMLSeason1:   "Season 1",
	VRMLSeason2:   "Season 2",
	VRMLSeason3:   "Season 3",
	VRMLSeason4:   "Season 4",
	VRMLSeason5:   "Season 5",
	VRMLSeason6:   "Season 6",
	VRMLSeason7:   "Season 7",
}

var vrmlCosmeticMap = map[VRMLSeasonID]map[VRMLPrestige][]string{
	VRMLPreSeason: {
		VRMLPlayer: {"rwd_tag_s1_vrml_preseason", "rwd_medal_s1_vrml_preseason"},
	},
	VRMLSeason1: {
		VRMLPlayer:   {"rwd_tag_s1_vrml_s1", "rwd_medal_s1_vrml_s1_user"},
		VRMLFinalist: {"rwd_medal_s1_vrml_s1_finalist", "rwd_tag_s1_vrml_s1_finalist", "rwd_tag_s1_vrml_s1", "rwd_medal_s1_vrml_s1_user"},
		VRMLChampion: {"rwd_medal_s1_vrml_s1_champion", "rwd_tag_s1_vrml_s1_champion", "rwd_medal_s1_vrml_s1_finalist", "rwd_tag_s1_vrml_s1_finalist", "rwd_tag_s1_vrml_s1", "rwd_medal_s1_vrml_s1_user"},
	},
	VRMLSeason2: {
		VRMLPlayer:   {"rwd_tag_s1_vrml_s2", "rwd_medal_s1_vrml_s2"},
		VRMLFinalist: {"rwd_medal_s1_vrml_s2_finalist", "rwd_tag_s1_vrml_s2_finalist", "rwd_tag_s1_vrml_s2", "rwd_medal_s1_vrml_s2"},
		VRMLChampion: {"rwd_medal_s1_vrml_s2_champion", "rwd_tag_s1_vrml_s2_champion", "rwd_medal_s1_vrml_s2_finalist", "rwd_tag_s1_vrml_s2_finalist", "rwd_tag_s1_vrml_s2", "rwd_medal_s1_vrml_s2"},
	},
	VRMLSeason3: {
		VRMLPlayer:   {"rwd_tag_s1_vrml_s3", "rwd_medal_s1_vrml_s3"},
		VRMLFinalist: {"rwd_medal_s1_vrml_s3_finalist", "rwd_tag_s1_vrml_s3_finalist", "rwd_tag_s1_vrml_s3", "rwd_medal_s1_vrml_s3"},
		VRMLChampion: {"rwd_medal_s1_vrml_s3_champion", "rwd_tag_s1_vrml_s3_champion", "rwd_medal_s1_vrml_s3_finalist", "rwd_tag_s1_vrml_s3_finalist", "rwd_tag_s1_vrml_s3", "rwd_medal_s1_vrml_s3"},
	},
	VRMLSeason4: {
		VRMLPlayer:   {"rwd_tag_0006", "rwd_medal_0006"},
		VRMLFinalist: {"rwd_tag_0007", "rwd_medal_0007", "rwd_tag_0006", "rwd_medal_0006"},
		VRMLChampion: {"rwd_tag_0008", "rwd_medal_0008", "rwd_tag_0007", "rwd_medal_0007", "rwd_tag_0006", "rwd_medal_0006"},
	},
	VRMLSeason5: {
		VRMLPlayer:   {"rwd_tag_0035"},
		VRMLFinalist: {"rwd_tag_0036", "rwd_tag_0035"},
		VRMLChampion: {"rwd_tag_0037", "rwd_tag_0036", "rwd_tag_0035"},
	},
	VRMLSeason6: {
		VRMLPlayer:   {"rwd_tag_0040"},
		VRMLFinalist: {"rwd_tag_0041", "rwd_tag_0040"},
		VRMLChampion: {"rwd_tag_0042", "rwd_tag_0041", "rwd_tag_0040"},
	},
	VRMLSeason7: {
		VRMLPlayer:   {"rwd_tag_0043"},
		VRMLFinalist: {"rwd_tag_0044", "rwd_tag_0043"},
		VRMLChampion: {"rwd_tag_0045", "rwd_tag_0044", "rwd_tag_0043"},
	},
}

type VRMLEntitlement struct {
	SeasonID VRMLSeasonID `json:"season_id"`
	Prestige VRMLPrestige `json:"prestige"`
}

func (e VRMLEntitlement) MarshalText() ([]byte, error) {
	switch e.Prestige {
	case VRMLPlayer:
		return []byte(e.SeasonID + ":player"), nil
	case VRMLFinalist:
		return []byte(e.SeasonID + ":finalist"), nil
	case VRMLChampion:
		return []byte(e.SeasonID + ":champion"), nil
	default:
		return nil, fmt.Errorf("invalid VRML prestige: %d", e.Prestige)
	}
}

func (e *VRMLEntitlement) UnmarshalText(text []byte) error {

	seasonID, prestige, found := strings.Cut(string(text), ":")
	if !found {
		return fmt.Errorf("invalid VRML entitlement: %s", text)
	}
	e.SeasonID = VRMLSeasonID(seasonID)
	switch prestige {
	case "player":
		e.Prestige = VRMLPlayer
	case "finalist":
		e.Prestige = VRMLFinalist
	case "champion":
		e.Prestige = VRMLChampion
	default:
		return fmt.Errorf("invalid VRML prestige: %s", prestige)
	}
	return nil
}

func (e VRMLEntitlement) Cosmetics() []string {
	return append(vrmlCosmeticMap[e.SeasonID][e.Prestige], []string{"decal_vrml_a", "emote_vrml_a"}...)
}

func AssignEntitlements(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, assignerID, assignerUsername, userID, vrmlUserID string, entitlements []*VRMLEntitlement) error {

	// Load the user's wallet
	account, err := nk.AccountGetId(ctx, userID)
	if err != nil {
		return fmt.Errorf("failed to get account for %s: %v", userID, err)
	}

	wallet := make(map[string]int64)

	if err := json.Unmarshal([]byte(account.Wallet), &wallet); err != nil {
		return status.Error(codes.Internal, "failed to unmarshal wallet")
	}

	// Create a changeset for the wallet
	changeset := make(map[string]int64, len(entitlements))

	for _, e := range entitlements {

		for _, cosmeticID := range e.Cosmetics() {
			name := "cosmetic:arena:" + cosmeticID

			// Make sure it is 1
			if v, ok := wallet[name]; !ok || v != 1 {
				changeset[name] = v*-1 + 1
			}
		}
	}

	metadata := map[string]interface{}{
		"assigner_username": assignerUsername,
		"assigner_id":       assignerID,
		"vrml_user_id":      vrmlUserID,
		"entitlements":      entitlements,
	}

	if _, _, err := nk.WalletUpdate(ctx, userID, changeset, metadata, true); err != nil {
		return fmt.Errorf("failed to update wallet for %s: %v", userID, err)
	}

	// Log the action
	logger.WithFields(map[string]interface{}{
		"assigner_id":      assignerID,
		"user_id":          userID,
		"vrml_user_id":     vrmlUserID,
		"entitlements":     entitlements,
		"cosmetic_changes": changeset,
	}).Info("assigned VRML entitlements")

	return nil
}

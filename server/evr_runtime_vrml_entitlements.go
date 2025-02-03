package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/echotools/vrmlgo"
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

func FetchMatchCountBySeason(vg *vrmlgo.Session) (map[VRMLSeasonID]int, error) {

	// Get the user's account information
	me, err := vg.Me(vrmlgo.WithUseCache(false))
	if err != nil {
		return nil, fmt.Errorf("failed to get user data: %v", err)
	}

	// Get the account information
	account, err := vg.Member(me.ID, vrmlgo.WithUseCache(false))
	if err != nil {
		return nil, fmt.Errorf("failed to get member data: %v", err)
	}

	// Get the game details
	gameDetails, err := vg.GameSearch(VRMLEchoArenaShortName)
	if err != nil {
		return nil, fmt.Errorf("failed to get game details: %v", err)
	}

	// Get the seasons for the game
	seasons, err := vg.Seasons(gameDetails.Game.ShortName)
	if err != nil {
		return nil, fmt.Errorf("failed to get seasons: %v", err)
	}

	// Create a map of seasons
	seasonNameMap := make(map[string]*vrmlgo.Season)
	for _, s := range seasons {
		seasonNameMap[s.Name] = s
	}

	// Get the player ID for this game
	playerID := account.PlayerID(gameDetails.Game.ShortName)

	// Get the match history for each team
	matchesBySeason := make(map[string][]string)
	for _, t := range account.Teams(gameDetails.Game.ShortName) {

		history, err := vg.TeamMatchesHistory(t)
		if err != nil {
			return nil, fmt.Errorf("failed to get team match history: %v", err)
		}

		// Create a map of matches by season
		for _, h := range history {
			matchesBySeason[h.SeasonName] = append(matchesBySeason[h.SeasonName], h.MatchID)
		}
	}

	// Get the match details for the first two matches of each season
	matchCountBySeasonID := make(map[VRMLSeasonID]int)
	for _, season := range seasons {

		for _, mID := range matchesBySeason[season.Name] {
			if matchCountBySeasonID[VRMLSeasonID(season.ID)] >= 2 {
				matchCountBySeasonID[VRMLSeasonID(season.ID)] = 10
				break
			}

			// Get the match details
			matchDetails, err := vg.Match(gameDetails.Game.ShortName, mID)
			if err != nil {
				return nil, fmt.Errorf("failed to get match details: %v", err)
			}

			// Skip forfeits
			if matchDetails.Match.IsForfeit {
				continue
			}

			// Count the number of matches the player is in
			for _, p := range matchDetails.Players() {

				// Check if the player is in the match
				if p.ID == playerID {
					season := seasonNameMap[matchDetails.Match.SeasonName]
					matchCountBySeasonID[VRMLSeasonID(season.ID)]++
				}
			}
		}
	}

	return matchCountBySeasonID, nil
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

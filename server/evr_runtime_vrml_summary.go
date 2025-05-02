package server

import (
	"fmt"

	"github.com/echotools/vrmlgo/v5"
)

var (
	ErrPlayerNotFound = fmt.Errorf("player not found")
)

type VRMLPlayerSummary struct {
	User                      *vrmlgo.User                    `json:"user"`
	Player                    *vrmlgo.Player                  `json:"player"`
	Teams                     map[string]*vrmlgo.Team         `json:"teams"`        // map[teamID]team
	MatchCountsBySeasonByTeam map[VRMLSeasonID]map[string]int `json:"match_counts"` // map[seasonID]map[teamID]matchCount
}

func (VRMLPlayerSummary) StorageMeta() StorageMeta {
	return StorageMeta{
		Collection: StorageCollectionVRML,
		Key:        StorageKeyVRMLSummary,
	}
}

func (VRMLPlayerSummary) StorageIndexes() []StorageIndexMeta {
	return nil
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

func (v *VRMLVerifier) playerSummary(vg *vrmlgo.Session, memberID string) (*VRMLPlayerSummary, error) {

	// Get the account information
	account, err := vg.Member(memberID, vrmlgo.WithUseCache(false))
	if err != nil {
		return nil, fmt.Errorf("failed to get member data: %v", err)
	}

	// Get the seasons for the game
	seasons, err := vg.GameSeasons(VRMLEchoArenaShortName)
	if err != nil {
		return nil, fmt.Errorf("failed to get seasons: %v", err)
	}

	// Get the player ID for this game

	var (
		player  *vrmlgo.Player
		teamIDs []string
		teams   = make(map[string]*vrmlgo.Team)
	)

	if playerID := account.PlayerID(VRMLEchoArenaShortName); playerID != "" {
		// Get the player details
		player, err = vg.Player(playerID)
		if err != nil {
			return nil, fmt.Errorf("failed to get player: %v", err)
		}

		teamIDs = account.TeamIDs(VRMLEchoArenaShortName)

	} else {
		// Try to discover the player ID if it is not found

		player, err = v.searchPlayerBySeasons(vg, seasons, memberID)
		if err != nil {
			return nil, fmt.Errorf("failed to find player: %v", err)
		}

		teamIDs = player.ThisGame.TeamIDs()
	}

	// Create a map of seasons
	seasonNameMap := make(map[string]*vrmlgo.Season)
	for _, s := range seasons {
		seasonNameMap[s.Name] = s
	}

	// Get the teams for the player

	for _, teamID := range teamIDs {

		details, err := vg.Team(teamID)
		if err != nil {
			return nil, fmt.Errorf("failed to get team details: %v", err)
		}

		teams[teamID] = details.Team
	}

	// Get the match history for each team
	matchesByTeamBySeason := make(map[VRMLSeasonID]map[string][]string)
	for _, teamID := range teamIDs {

		details, err := vg.Team(teamID)
		if err != nil {
			return nil, fmt.Errorf("failed to get team details: %v", err)
		}

		t := details.Team

		history, err := vg.TeamMatchesHistory(t.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to get team match history: %v", err)
		}

		// Create a map of matches by team, season
		for _, h := range history {
			seasonID := VRMLSeasonID(seasonNameMap[h.SeasonName].ID)
			if _, ok := matchesByTeamBySeason[seasonID]; !ok {
				matchesByTeamBySeason[seasonID] = make(map[string][]string)
			}

			if _, ok := matchesByTeamBySeason[seasonID][t.ID]; !ok {
				matchesByTeamBySeason[seasonID][t.ID] = make([]string, 0)
			}

			matchesByTeamBySeason[seasonID][t.ID] = append(matchesByTeamBySeason[seasonID][t.ID], h.MatchID)
		}
	}

	// Get the match details for the first two matches of each season
	matchCountsBySeasonID := make(map[VRMLSeasonID]map[string]int)

	for sID, matchesByTeam := range matchesByTeamBySeason {

		for tID, matchIDs := range matchesByTeam {

			for _, mID := range matchIDs {

				// Get the match details
				matchDetails, err := vg.GameMatch(VRMLEchoArenaShortName, mID)
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
					if p.ID == player.ThisGame.PlayerID {
						if _, ok := matchCountsBySeasonID[sID]; !ok {
							matchCountsBySeasonID[sID] = make(map[string]int)
						}
						matchCountsBySeasonID[sID][tID]++
					}
				}
			}
		}
	}
	member, err := vg.Member(memberID, vrmlgo.WithUseCache(false))
	if err != nil {
		return nil, fmt.Errorf("failed to get member data: %v", err)
	}

	return &VRMLPlayerSummary{
		User:                      member.User,
		Player:                    player,
		Teams:                     teams,
		MatchCountsBySeasonByTeam: matchCountsBySeasonID,
	}, nil
}

// Use a brute force search for the player ID, by searching for the player name in all seasons
func (*VRMLVerifier) searchPlayerBySeasons(vg *vrmlgo.Session, seasons []*vrmlgo.Season, vrmlID string) (*vrmlgo.Player, error) {

	member, err := vg.Member(vrmlID, vrmlgo.WithUseCache(false))
	if err != nil {
		return nil, fmt.Errorf("failed to get member data: %v", err)
	}

	for _, s := range seasons {

		players, err := vg.GamePlayersSearch(s.GameURLShort, s.ID, member.User.UserName)
		if err != nil {
			return nil, fmt.Errorf("failed to search for player: %v", err)
		}

		for _, p := range players {
			player, err := vg.Player(p.ID)
			if err != nil {
				return nil, fmt.Errorf("failed to get player: %v", err)
			}

			if player.User.UserID == member.User.ID {
				return player, nil
			}
		}
	}

	return nil, ErrPlayerNotFound
}

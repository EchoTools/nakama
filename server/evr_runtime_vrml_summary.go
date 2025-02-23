package server

import (
	"fmt"

	"github.com/echotools/vrmlgo/v3"
)

type VRMLPlayerSummary struct {
	User                      *vrmlgo.User                    `json:"user"`
	Player                    *vrmlgo.Player                  `json:"player"`
	Teams                     map[string]*vrmlgo.Team         `json:"teams"`        // map[teamID]team
	MatchCountsBySeasonByTeam map[VRMLSeasonID]map[string]int `json:"match_counts"` // map[seasonID]map[teamID]matchCount
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

	// Get the game details
	gameDetails, err := vg.GameSearch(VRMLEchoArenaShortName)
	if err != nil {
		return nil, fmt.Errorf("failed to get game details: %v", err)
	}

	// Get the player ID for this game
	playerID := account.PlayerID(gameDetails.Game.ShortName)

	player, err := vg.Player(playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get player: %v", err)
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

	// Get the teams for the player
	teams := make(map[string]*vrmlgo.Team)
	for _, teamID := range account.Teams(gameDetails.Game.ShortName) {

		details, err := vg.Team(teamID)
		if err != nil {
			return nil, fmt.Errorf("failed to get team details: %v", err)
		}

		teams[teamID] = details.Team
	}

	// Get the match history for each team
	matchesByTeamBySeason := make(map[VRMLSeasonID]map[string][]string)
	for _, teamID := range account.Teams(gameDetails.Game.ShortName) {

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

package server

import (
	"log"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

func oppositeArenaTeamID(teamID int64) int64 {
	// EchoVR arena teams are conventionally 0 (blue) and 1 (orange).
	// For other team IDs (spectator/social/etc), keep as-is.
	switch teamID {
	case 0:
		return 1
	case 1:
		return 0
	default:
		return teamID
	}
}

func scoringTeamIDForGoal(goal *evr.MatchGoal) int64 {
	if goal == nil {
		return 0
	}
	// A "SELF GOAL" should credit points to the opposing team, not the shooter's team.
	if goal.GoalType == "SELF GOAL" {
		return oppositeArenaTeamID(goal.TeamID)
	}
	return goal.TeamID
}

type TeamMetadata struct {
	Strength   float64 `json:"strength,omitempty"`
	PredictWin float64 `json:"predict_win,omitempty"`
}

type GameState struct {
	BlueScore              int                        `json:"blue_score"`                        // The score for the blue team
	OrangeScore            int                        `json:"orange_score"`                      // The score for the orange team
	SessionScoreboard      *SessionScoreboard         `json:"session_scoreboard,omitempty"`      // The session scoreboard
	MatchOver              bool                       `json:"match_over,omitempty"`              // Whether the round is over
	EquilibriumCoefficient float64                    `json:"equilibrium_coefficient,omitempty"` // The equilibrium coefficient for the game (how much the game is balanced)
	Teams                  map[TeamIndex]TeamMetadata `json:"teams,omitempty"`                   // Metadata for each team
}

func (g *GameState) GetSessionScoreboard() *SessionScoreboard {
	if g.SessionScoreboard == nil {
		return nil
	}
	return g.SessionScoreboard
}

func NewGameState() *GameState {
	return &GameState{}
}

func (g *GameState) Update(goals []*evr.MatchGoal) {

	g.BlueScore = 0
	g.OrangeScore = 0

	for _, goal := range goals {
		points := GoalTypeToPoints(goal.GoalType)
		if points == 0 {
			log.Printf("Unknown goal type: %s", goal.GoalType)
			continue
		}

		teamID := scoringTeamIDForGoal(goal)
		if teamID == 0 {
			g.BlueScore += points
		} else {
			g.OrangeScore += points
		}
	}

	// If the round clock has reached the round duration, the round is over

}

func GoalTypeToPoints(goalType string) int {
	switch goalType {
	case "SLAM DUNK":
		return 2
	case "BOUNCE SHOT":
		return 2
	case "BUMPER SHOT":
		return 2
	case "HEADBUTT":
		return 2
	case "INSIDE SHOT":
		return 2
	case "LONG BOUNCE SHOT":
		return 3
	case "LONG BUMPER SHOT":
		return 3
	case "LONG HEADBUTT":
		return 3
	case "LONG SHOT":
		return 3
	case "SELF GOAL":
		return 2
	default:
		return 0
	}
}

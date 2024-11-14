package server

import (
	"log"
)

type TeamMetadata struct {
	Strength    float64 `json:"strength,omitempty"`
	RatingMu    float64 `json:"rating_mu,omitempty"`
	RatingSigma float64 `json:"rating_sigma,omitempty"`
}

type GameState struct {
	BlueScore              int                     `json:"blue_score"`                        // The score for the blue team
	OrangeScore            int                     `json:"orange_score"`                      // The score for the orange team
	RoundClock             *RoundClock             `json:"round_clock,omitempty"`             // The round clock
	Goals                  []*MatchGoal            `json:"goals,omitempty"`                   // The goals scored in the game
	EquilibriumCoefficient float64                 `json:"equilibrium_coefficient,omitempty"` // The equilibrium coefficient for the game (how much the game is balanced)
	Teams                  map[string]TeamMetadata `json:"teams,omitempty"`                   // Metadata for each team
}

func NewGameState() *GameState {
	return &GameState{}
}

func (g *GameState) Update() {

	g.BlueScore = 0
	g.OrangeScore = 0

	for _, goal := range g.Goals {
		points := GoalTypeToPoints(goal.GoalType)
		if points == 0 {
			log.Printf("Unknown goal type: %s", goal.GoalType)
			continue
		}

		if goal.Teamid == 0 {
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
	default:
		return 0
	}
}

package server

import (
	"fmt"
	"log"
	"time"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

// GameStatus represents the current phase of a match, mirroring the nevr-proto GameStatus enum.
type GameStatus int

const (
	GameStatusUnspecified    GameStatus = 0
	GameStatusPreMatch       GameStatus = 1
	GameStatusRoundStart     GameStatus = 2
	GameStatusPlaying        GameStatus = 3
	GameStatusScore          GameStatus = 4
	GameStatusRoundOver      GameStatus = 5
	GameStatusPostMatch      GameStatus = 6
	GameStatusPreSuddenDeath GameStatus = 7
	GameStatusSuddenDeath    GameStatus = 8
	GameStatusPostSuddenDeath GameStatus = 9
	GameStatusRoundClosing    GameStatus = 10
)

var gameStatusNames = map[GameStatus]string{
	GameStatusUnspecified:     "unspecified",
	GameStatusPreMatch:        "pre_match",
	GameStatusRoundStart:      "round_start",
	GameStatusPlaying:         "playing",
	GameStatusScore:           "score",
	GameStatusRoundOver:       "round_over",
	GameStatusPostMatch:       "post_match",
	GameStatusPreSuddenDeath:  "pre_sudden_death",
	GameStatusSuddenDeath:     "sudden_death",
	GameStatusPostSuddenDeath: "post_sudden_death",
	GameStatusRoundClosing:    "round_closing",
}

var gameStatusValues = func() map[string]GameStatus {
	m := make(map[string]GameStatus, len(gameStatusNames))
	for k, v := range gameStatusNames {
		m[v] = k
	}
	return m
}()

func (g GameStatus) String() string {
	if name, ok := gameStatusNames[g]; ok {
		return name
	}
	return fmt.Sprintf("unknown(%d)", int(g))
}

func (g GameStatus) MarshalText() ([]byte, error) {
	return []byte(g.String()), nil
}

func (g *GameStatus) UnmarshalText(text []byte) error {
	s := string(text)
	if v, ok := gameStatusValues[s]; ok {
		*g = v
		return nil
	}
	return fmt.Errorf("unknown GameStatus: %q", s)
}

type Clock struct {
	Duration time.Duration `json:"duration,omitempty"`
	Elapsed  time.Duration `json:"elapsed,omitempty"`
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
	RoundClock             *Clock                     `json:"round_clock,omitempty"`             // The round clock information
}

func (g *GameState) GetSessionScoreboard() *SessionScoreboard {
	if g.SessionScoreboard == nil {
		return nil
	}
	return g.SessionScoreboard
}

func (g *GameState) IsMatchOver() bool {
	return g != nil && g.MatchOver
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

		if goal.TeamID == 0 {
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

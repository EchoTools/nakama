package server

import (
	"time"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

var (
	modeStatGroupMap = map[evr.Symbol]string{
		evr.ModeArenaPublic:  "arena",
		evr.ModeCombatPublic: "combat",
	}
)

type EarlyQuitStatistics struct {
	PenaltyExpiry int64          `json:"penalty_expiry,omitempty"`
	History       map[int64]bool `json:"history,omitempty"`
}

func (s *EarlyQuitStatistics) IncrementEarlyQuits() {
	if s.History == nil {
		s.History = make(map[int64]bool)
	}
	s.History[time.Now().Unix()] = true
}

func (s *EarlyQuitStatistics) IncrementCompletedMatches() {
	if s.History == nil {
		s.History = make(map[int64]bool)
	}
	s.History[time.Now().Unix()] = false
}

func (s *EarlyQuitStatistics) ApplyEarlyQuitPenalty(logger *zap.Logger, userID string, label *MatchLabel, playerStats evr.PlayerStatistics, penaltyPercent float64) {
	// This will apply a penalty to the player's statistics
	// The penalty is a float64 value 0 and 1.0 that will adjust the players ratio by that amount
	// A penalty of 0.1 will adjust the player's win/loss ratio by -10%

	// The following statistics are affected by the penalty:
	/*
		ArenaWinPercentage
		ArenaWins
		ArenaLosses
		HighestArenaWinStreak
	*/

	if penaltyPercent <= 0 || penaltyPercent > 1.0 {
		return
	}
	if label.Mode != evr.ModeArenaPublic {
		// Only apply the penalty to Arena matches
		return
	}

	if userID == label.GameServer.OperatorID.String() {
		// The broadcaster is not penalized
		return
	}

	gameClock := label.GameState.RoundClock.Current()
	roundDuration := label.GameState.RoundClock.Duration

	if gameClock == 0 || roundDuration == 0 {
		// The game has not started yet. No penalty is applied.
		return
	}

	remainingTime := label.GameState.RoundClock.Remaining()
	if remainingTime > 0 {
		// The game is still in progress. Set a penalty timer for double the length of the game time remaining.
		expiry := time.Now().Add(remainingTime * 2)
		s.PenaltyExpiry = expiry.UTC().Unix()
	} else {
		// The game is over. No penalty is applied.
		s.PenaltyExpiry = 0
	}

}

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

	if userID == label.Broadcaster.OperatorID {
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

	// Update the default stats with the player's stats
	if modeStats, ok := playerStats[modeStatGroupMap[label.Mode]]; ok {

		reqFields := []string{"ArenaWins", "ArenaLosses", "ArenaWinPercentage", "HighestArenaWinStreak", "ArenaTies"}
		for _, field := range reqFields {
			if _, ok := modeStats[field]; !ok {
				logger.Warn("Missing required field in player statistics", zap.String("field", field))

				switch field {
				case "ArenaWins":
					modeStats[field] = evr.MatchStatistic{
						Count:     1,
						Operation: "add",
						Value:     0,
					}
				case "ArenaLosses":
					modeStats[field] = evr.MatchStatistic{
						Count:     1,
						Operation: "add",
						Value:     0,
					}
				case "ArenaWinPercentage":
					modeStats[field] = evr.MatchStatistic{
						Count:     1,
						Operation: "rep",
						Value:     0.0,
					}
				case "HighestArenaWinStreak":
					modeStats[field] = evr.MatchStatistic{
						Count:     1,
						Operation: "max",
						Value:     0,
					}

				case "ArenaTies":
					modeStats[field] = evr.MatchStatistic{
						Count:     1,
						Operation: "add",
						Value:     0,
					}
				}
			}
		}

		ties := int64(modeStats["ArenaTies"].Value.(float64))
		wins := int64(modeStats["ArenaWins"].Value.(float64))
		losses := int64(modeStats["ArenaLosses"].Value.(float64))
		winPercentage := modeStats["ArenaWinPercentage"].Value.(float64)
		winStreak := int64(modeStats["HighestArenaWinStreak"].Value.(float64))

		// Increase the ties by 1 to count the early quit
		ties++

		// Subtract from the Wins, Add to the losses
		// Keep the total games the same

		delta := int64((float64(wins) * penaltyPercent) + 0.5)
		wins = max(0, wins-delta)
		losses += delta

		// Reduce the winstreak by 1
		winStreak = max(winStreak-1, 0)

		// Calculate the new win percentage
		totalMatches := wins + losses
		if totalMatches > 0 {
			winPercentage = float64(wins) / float64(totalMatches)
		}

		// Fix some stats
		for name, stat := range modeStats {
			switch name {
			case "AssistPerGame", "AveragePointsPerGame", "AveragePossessionTimePerGame", "AverageTopSpeedPerGame", "GoalsPerGame", "SavesPerGame":
				modeStats[name] = evr.MatchStatistic{
					Count:     totalMatches,
					Operation: "avg",
					Value:     stat.Value.(float64),
				}
			default:
				modeStats[name] = evr.MatchStatistic{
					Count:     1,
					Operation: stat.Operation,
					Value:     stat.Value,
				}
			}

			// Apply the new statistics
			modeStats["ArenaWins"] = evr.MatchStatistic{
				Count:     1,
				Operation: "add",
				Value:     wins,
			}
			modeStats["ArenaLosses"] = evr.MatchStatistic{
				Count:     1,
				Operation: "add",
				Value:     losses,
			}

			modeStats["ArenaTies"] = evr.MatchStatistic{
				Count:     1,
				Operation: "add",
				Value:     ties,
			}

			modeStats["ArenaWinPercentage"] = evr.MatchStatistic{
				Count:     1,
				Operation: "rep",
				Value:     winPercentage,
			}

			modeStats["HighestArenaWinStreak"] = evr.MatchStatistic{
				Count:     1,
				Operation: "max",
				Value:     winStreak,
			}

		}
		playerStats[modeStatGroupMap[label.Mode]] = modeStats
	}
}

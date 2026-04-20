package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// GamesPlayedLoad computes a player's all-time games played from ArenaWins +
// ArenaLosses leaderboard records. The GamesPlayed stat is a derived field
// that is never written as its own leaderboard, so read the source stats.
// Returns 0 when the records do not exist yet.
func GamesPlayedLoad(ctx context.Context, nk runtime.NakamaModule, userID, groupID string, mode evr.Symbol) (int, error) {
	total := 0
	for _, statName := range []string{"ArenaWins", "ArenaLosses"} {
		boardID := StatisticBoardID(groupID, mode, statName, evr.ResetScheduleAllTime)
		_, ownerRecords, _, _, err := nk.LeaderboardRecordsList(ctx, boardID, []string{userID}, 1, "", 0)
		if err != nil {
			if errors.Is(err, ErrLeaderboardNotFound) || errors.Is(err, runtime.ErrLeaderboardNotFound) {
				continue
			}
			return 0, err
		}
		if len(ownerRecords) == 0 {
			continue
		}
		val, err := ScoreToFloat64(ownerRecords[0].Score, ownerRecords[0].Subscore)
		if err != nil {
			return 0, fmt.Errorf("failed to decode %s score: %w", statName, err)
		}
		total += int(val)
	}
	return total, nil
}

// IsNewPlayer returns true if the player's games_played is below threshold,
// or if games_played data is unavailable (treats unknown players as new).
// When the property is missing or not a valid float64, the player is assumed
// to be new. A threshold of 0 disables new-player classification for players
// with a known games_played value, but players without the property are
// still treated as new.
func IsNewPlayer(entry runtime.MatchmakerEntry, threshold int) bool {
	props := entry.GetProperties()
	gp, ok := props["games_played"].(float64)
	if !ok {
		return true
	}
	return int(gp) < threshold
}

package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// GamesPlayedLoad reads the all-time GamesPlayed leaderboard record for
// a player. Returns 0 when the record does not exist yet.
func GamesPlayedLoad(ctx context.Context, nk runtime.NakamaModule, userID, groupID string, mode evr.Symbol) (int, error) {
	boardID := StatisticBoardID(groupID, mode, GamesPlayedStatisticID, evr.ResetScheduleAllTime)

	_, ownerRecords, _, _, err := nk.LeaderboardRecordsList(ctx, boardID, []string{userID}, 1, "", 0)
	if err != nil {
		if errors.Is(err, ErrLeaderboardNotFound) || errors.Is(err, runtime.ErrLeaderboardNotFound) {
			return 0, nil
		}
		return 0, err
	}

	if len(ownerRecords) == 0 {
		return 0, nil
	}

	val, err := ScoreToFloat64(ownerRecords[0].Score, ownerRecords[0].Subscore)
	if err != nil {
		return 0, fmt.Errorf("failed to decode GamesPlayed score: %w", err)
	}

	return int(val), nil
}

// IsNewPlayer returns true if the matchmaker entry's games_played numeric
// property is below the given threshold. If the property is missing or not
// a float64, the player is treated as new (returns true). A threshold of 0
// means no one is classified as new.
func IsNewPlayer(entry runtime.MatchmakerEntry, threshold int) bool {
	props := entry.GetProperties()
	gp, ok := props["games_played"].(float64)
	if !ok {
		return true
	}
	return int(gp) < threshold
}

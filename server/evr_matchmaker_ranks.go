package server

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

var (
	percentileBoardIDsByMode = func() map[string][]string {
		ids := map[string][]string{
			"echo_arena": {
				"ArenaGamesPlayed",
				"ArenaWins",
				"ArenaLosses",
				"ArenaWinPercentage",
				"AssistsPerGame",
				"AveragePointsPerGame",
				"AverageTopSpeedPerGame",
				"BlockPercentage",
				"GoalScorePercentage",
				"GoalsPerGame",
			},
		}
		for mode, boardIDs := range ids {
			for s := range boardIDs {
				boardIDs[s] = fmt.Sprintf("%s:%s:daily", mode, boardIDs[s])
			}
		}
		return ids
	}()
)

func OverallPercentileRecalculate(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, userID string, mode evr.Symbol) (float64, map[string]*api.LeaderboardRecord, error) {

	percentileBoardIDs, ok := percentileBoardIDsByMode[mode.String()]
	if !ok {
		return 0.5, nil, nil
	}

	statRecords, err := StatRecordsLoad(ctx, logger, nk, userID, percentileBoardIDs)
	if err != nil {
		return 0, nil, err
	}

	// Combine all the stat ranks into a single percentile.
	percentiles := make([]float64, 0, len(statRecords))
	for _, r := range statRecords {
		percentile := float64(r.GetRank()) / float64(r.GetNumScore())
		percentiles = append(percentiles, percentile)
	}

	// Calculate the overall percentile.
	overallPercentile := 0.0
	for _, p := range percentiles {
		overallPercentile += p
	}
	overallPercentile /= float64(len(percentiles))

	return overallPercentile, statRecords, nil
}

func StatRecordsLoad(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, userID string, boardIDs []string) (map[string]*api.LeaderboardRecord, error) {

	statRecords := make(map[string]*api.LeaderboardRecord)
	for _, boardID := range boardIDs {

		records, _, _, _, err := nk.LeaderboardRecordsList(ctx, boardID, []string{userID}, 10000, "", 0)
		if err != nil {
			continue
		}

		if len(records) == 0 {
			continue
		}

		statRecords[boardID] = records[0]
	}

	if len(statRecords) == 0 {
		return nil, nil
	}

	return statRecords, nil
}

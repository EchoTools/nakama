package server

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

type Periodicity string

var (
	percentileStateIDsByMode = map[evr.Symbol][]string{
		evr.ModeArenaPublic: {
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
)

func RecalculatePlayerRankPercentile(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, userID string, mode evr.Symbol, periodicity string) (float64, map[string]*api.LeaderboardRecord, error) {

	if _, ok := percentileStateIDsByMode[mode]; !ok {
		return 0.5, nil, nil
	}

	boardIDs := make([]string, 0, len(percentileStateIDsByMode[mode]))
	for _, id := range percentileStateIDsByMode[mode] {
		boardIDs = append(boardIDs, fmt.Sprintf("%s:%s:%s", mode.String(), id, periodicity))
	}

	statRecords, err := StatRecordsLoad(ctx, logger, nk, userID, boardIDs)
	if err != nil {
		return 0.0, nil, err
	}

	if statRecords == nil {
		return 0.0, nil, nil
	}

	// Combine all the stat ranks into a single percentile.
	percentiles := make([]float64, 0, len(statRecords))
	for _, r := range statRecords {
		if r.GetNumScore() == 0 {
			continue
		}
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

		_, records, _, _, err := nk.LeaderboardRecordsList(ctx, boardID, []string{userID}, 10000, "", 0)
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

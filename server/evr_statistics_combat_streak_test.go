package server

import (
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

// A best streak is the best of the matches, not their sum: the career row must
// hold the higher of the stored best and this match's best, as
// HighestArenaWinStreak does (op "max").
func TestStatisticsToEntries_CombatBestEliminationStreakKeepsTheBest(t *testing.T) {
	for _, tc := range []struct{ stored, match, want float64 }{{5, 3, 5}, {3, 5, 5}} {
		prev := &evr.CombatStatistics{CombatBestEliminationStreak: &evr.StatisticValue{Value: tc.stored, Count: 1}}
		update := &evr.CombatStatistics{CombatBestEliminationStreak: &evr.StatisticValue{Value: tc.match, Count: 1}}
		entries, err := StatisticsToEntries("00000000-0000-4000-8000-00000000c0b1", "synthetic-player", "00000000-0000-4000-8000-00000000c0b3", evr.ModeCombatPublic, evr.ResetScheduleAllTime, prev, update)
		if err != nil {
			t.Fatal(err)
		}
		var got float64
		found := false
		for _, e := range entries {
			if e.BoardMeta.StatName == "CombatBestEliminationStreak" && e.BoardMeta.ResetSchedule == evr.ResetScheduleAllTime {
				got, _ = ScoreToFloat64(e.Score, e.Subscore)
				found = true
			}
		}
		if !found || got != tc.want {
			t.Errorf("stored best %v, this match %v: wrote %v (found=%v), want %v", tc.stored, tc.match, got, found, tc.want)
		}
	}
}

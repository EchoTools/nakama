package evr

import (
	"math"
	"testing"
)

func statValue(v float64) *StatisticValue { return &StatisticValue{Value: v, Count: 1} }

// The game computes this ratio itself and sends it: the real type-stats
// fixture has CombatEliminations 7, CombatDeaths 4 and
// CombatAverageEliminationDeathRatio 1.75. CalculateFields must agree with the
// game on the same numbers, whatever the assists are.
func TestCombatStatistics_EliminationDeathRatioMatchesTheGame(t *testing.T) {
	sent := fixtureStatsObject(t, readRemoteLogFixture(t, "post_match_match_type_stats_combat.json"), "stats")
	for _, assists := range []float64{0, 3} {
		s := &CombatStatistics{
			CombatWins:                         statValue(sent["CombatWins"]),
			CombatEliminations:                 statValue(sent["CombatEliminations"]),
			CombatDeaths:                       statValue(sent["CombatDeaths"]),
			CombatAssists:                      statValue(assists),
			CombatAverageEliminationDeathRatio: &StatisticValue{},
		}
		s.CalculateFields()
		want := sent["CombatAverageEliminationDeathRatio"]
		if got := s.CombatAverageEliminationDeathRatio.GetValue(); math.Abs(got-want) > 1e-9 {
			t.Errorf("assists %v: ratio %v, the game computed %v from the same eliminations and deaths", assists, got, want)
		}
	}
}

// CalculateFields assigned three derived values through pointers that are nil
// whenever the stored profile lacks them, so it panicked instead of filling
// them in. Each case leaves the derived field nil.
func TestCombatStatistics_CalculateFieldsFillsNilDerivedFields(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stats *CombatStatistics
		field func(*CombatStatistics) *StatisticValue
		want  float64
	}{
		{
			name:  "CombatAverageEliminationDeathRatio",
			stats: &CombatStatistics{CombatWins: statValue(1), CombatAssists: statValue(3), CombatEliminations: statValue(6), CombatDeaths: statValue(3)},
			field: func(s *CombatStatistics) *StatisticValue { return s.CombatAverageEliminationDeathRatio },
			want:  2,
		},
		{
			name:  "CombatPayloadWinPercentage",
			stats: &CombatStatistics{CombatPayloadWins: statValue(1), CombatPayloadGamesPlayed: statValue(2)},
			field: func(s *CombatStatistics) *StatisticValue { return s.CombatPayloadWinPercentage },
			want:  50,
		},
		{
			name:  "CombatPointCaptureWinPercentage",
			stats: &CombatStatistics{CombatPointCaptureWins: statValue(1), CombatPointCaptureGamesPlayed: statValue(4)},
			field: func(s *CombatStatistics) *StatisticValue { return s.CombatPointCaptureWinPercentage },
			want:  25,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("CalculateFields panicked: %v", r)
				}
			}()
			tc.stats.CalculateFields()
			if got := tc.field(tc.stats).GetValue(); got != tc.want {
				t.Fatalf("%s = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

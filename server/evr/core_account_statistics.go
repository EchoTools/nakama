package evr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

type ResetSchedule string

func (s ResetSchedule) String() string {
	return string(s)
}

const (
	ResetScheduleAllTime ResetSchedule = "alltime"
	ResetScheduleDaily   ResetSchedule = "daily"
	ResetScheduleWeekly  ResetSchedule = "weekly"
)

var (
	TabletStatisticGroupArena  TabletStatisticGroup = TabletStatisticGroup(ModeArenaPublic)
	TabletStatisticGroupCombat TabletStatisticGroup = TabletStatisticGroup(ModeCombatPublic)
)

type TabletStatisticGroup Symbol

type StatisticsGroup struct {
	Mode          Symbol
	ResetSchedule ResetSchedule
}

func (g *StatisticsGroup) String() string {
	s, _ := g.MarshalText()
	return string(s)
}

func (g StatisticsGroup) MarshalText() ([]byte, error) {
	switch g.ResetSchedule {
	case ResetScheduleDaily:
		return []byte("daily_" + time.Now().UTC().Format("2006_01_02")), nil
	case ResetScheduleWeekly:
		return []byte("weekly_" + mostRecentThursday().Format("2006_01_02")), nil
	default:

		switch g.Mode {
		case ModeArenaPublic:
			return []byte("arena"), nil
		case ModeCombatPublic:
			return []byte("combat"), nil
		case ModeSocialPublic:
			return []byte("social"), nil
		case ModeSocialPrivate:
			return []byte("social_private"), nil
		case ModeArenaPrivate:
			return []byte("arena_private"), nil
		case ModeCombatPrivate:
			return []byte("combat_private"), nil
		default:
			return []byte(g.Mode.String()), nil
		}
	}

}

func (g *StatisticsGroup) UnmarshalText(data []byte) error {
	s := string(data)
	switch {
	case s == "arena":
		g.Mode = ModeArenaPublic
		g.ResetSchedule = ResetScheduleAllTime
	case s == "combat":
		g.Mode = ModeCombatPublic
		g.ResetSchedule = ResetScheduleAllTime
	case s == "social":
		g.Mode = ModeSocialPublic
		g.ResetSchedule = ResetScheduleAllTime
	case s == "social_private":
		g.Mode = ModeSocialPrivate
		g.ResetSchedule = ResetScheduleAllTime
	case s == "arena_private":
		g.Mode = ModeArenaPrivate
		g.ResetSchedule = ResetScheduleAllTime
	case s == "combat_private":
		g.Mode = ModeCombatPrivate
		g.ResetSchedule = ResetScheduleAllTime
	case s == "arena_practice_ai":
		g.Mode = ModeArenaPracticeAI
		g.ResetSchedule = ResetScheduleAllTime
	case s == "arena_public_ai":
		g.Mode = ModeArenaPublicAI
		g.ResetSchedule = ResetScheduleAllTime
	case len(s) > 6 && s[:6] == "daily_":
		g.Mode = ModeArenaPublic
		g.ResetSchedule = ResetScheduleDaily
	case len(s) > 7 && s[:7] == "weekly_":
		g.Mode = ModeArenaPublic
		g.ResetSchedule = ResetScheduleWeekly
	default:
		g.Mode = ToSymbol([]byte(s))
		g.ResetSchedule = ResetScheduleAllTime
	}
	return nil
}

type PlayerStatistics map[StatisticsGroup]Statistics

func (s PlayerStatistics) MarshalJSON() (b []byte, err error) {
	top := make(map[string]map[string]json.RawMessage)
	for g, stats := range s {

		switch g.Mode {
		case ModeArenaPublic:

			stats.(*ArenaStatistics).CalculateFields()
			if s := stats.(*ArenaStatistics); s.Level == nil || s.Level.Value == 0 || s.Level.Count == 0 {
				s.Level = &StatisticIntegerIncrement{
					IntegerStatistic{
						Count: 1,
						Value: 1,
					},
				}
			}

		case ModeCombatPublic:

			stats.(*CombatStatistics).CalculateFields()
			if s := stats.(*CombatStatistics); s.Level == nil || s.Level.Value == 0 || s.Level.Count == 0 {
				s.Level = &StatisticIntegerIncrement{
					IntegerStatistic{
						Count: 1,
						Value: 1,
					},
				}
			}

		default:
			stats.CalculateFields()
		}

		// Remove the stats with 0 count
		statMap := make(map[string]json.RawMessage)

		v := reflect.ValueOf(stats).Elem()
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			tag := t.Field(i).Tag.Get("json")
			s := strings.SplitN(tag, ",", 2)[0]

			// marshal the field
			if val, ok := v.Field(i).Interface().(Statistic); ok && !v.Field(i).IsNil() {
				if val.GetCount() > 0 {
					b, err := val.MarshalJSON()
					if err != nil {
						return nil, err
					}

					// include it if it's not a zero count
					if !bytes.Contains(b, []byte(`"cnt":0`)) {
						statMap[s] = b
					}
				}
			}
		}

		top[g.String()] = statMap
	}

	return json.Marshal(top)
}

func (s *PlayerStatistics) UnmarshalJSON(data []byte) error {
	if *s == nil {
		*s = make(PlayerStatistics)
	}

	m := make(map[string]json.RawMessage)
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}

	for k, v := range m {
		var g StatisticsGroup
		if err := g.UnmarshalText([]byte(k)); err != nil {
			return err
		}

		switch g.Mode {
		case ModeArenaPublic:
			stats := &ArenaStatistics{}
			if err := json.Unmarshal(v, stats); err != nil {
				return err
			}
			(*s)[g] = stats
		case ModeCombatPublic:
			stats := &CombatStatistics{}
			if err := json.Unmarshal(v, stats); err != nil {
				return err
			}
			(*s)[g] = stats
		default:
			stats := &GenericStats{}
			if err := json.Unmarshal(v, stats); err != nil {
				return err
			}
			(*s)[g] = stats
		}
	}

	return nil
}

type ArenaStatistics struct {
	ArenaLosses                  *StatisticIntegerIncrement `json:"ArenaLosses,omitempty"`
	ArenaMVPPercentage           *StatisticFloatSet         `json:"ArenaMVPPercentage,omitempty"`
	ArenaMVPS                    *StatisticIntegerIncrement `json:"ArenaMVPs,omitempty"`
	ArenaTies                    *StatisticIntegerIncrement `json:"ArenaTies,omitempty"`
	ArenaWinPercentage           *StatisticFloatSet         `json:"ArenaWinPercentage,omitempty"`
	ArenaWins                    *StatisticIntegerIncrement `json:"ArenaWins,omitempty"`
	Assists                      *StatisticIntegerIncrement `json:"Assists,omitempty"`
	AssistsPerGame               *StatisticFloatSet         `json:"AssistsPerGame,omitempty"`
	AveragePointsPerGame         *StatisticFloatSet         `json:"AveragePointsPerGame,omitempty"`
	AveragePossessionTimePerGame *StatisticFloatSet         `json:"AveragePossessionTimePerGame,omitempty"`
	AverageTopSpeedPerGame       *StatisticFloatSet         `json:"AverageTopSpeedPerGame,omitempty"`
	BlockPercentage              *StatisticFloatSet         `json:"BlockPercentage,omitempty"`
	Blocks                       *StatisticIntegerIncrement `json:"Blocks,omitempty"`
	BounceGoals                  *StatisticIntegerIncrement `json:"BounceGoals,omitempty"`
	BumperShots                  *StatisticIntegerIncrement `json:"BumperShots,omitempty"`
	Catches                      *StatisticIntegerIncrement `json:"Catches,omitempty"`
	Clears                       *StatisticIntegerIncrement `json:"Clears,omitempty"`
	CurrentArenaMVPStreak        *StatisticIntegerIncrement `json:"CurrentArenaMVPStreak,omitempty"`
	CurrentArenaWinStreak        *StatisticIntegerIncrement `json:"CurrentArenaWinStreak,omitempty"`
	GoalSavePercentage           *StatisticFloatSet         `json:"GoalSavePercentage,omitempty"`
	GoalScorePercentage          *StatisticFloatSet         `json:"GoalScorePercentage,omitempty"`
	Goals                        *StatisticIntegerIncrement `json:"Goals,omitempty"`
	GoalsPerGame                 *StatisticFloatSet         `json:"GoalsPerGame,omitempty"`
	HatTricks                    *StatisticIntegerIncrement `json:"HatTricks,omitempty"`
	HeadbuttGoals                *StatisticIntegerIncrement `json:"HeadbuttGoals,omitempty"`
	HighestArenaMVPStreak        *StatisticIntegerBest      `json:"HighestArenaMVPStreak,omitempty"`
	HighestArenaWinStreak        *StatisticIntegerBest      `json:"HighestArenaWinStreak,omitempty"`
	HighestPoints                *StatisticIntegerBest      `json:"HighestPoints,omitempty"`
	HighestSaves                 *StatisticIntegerBest      `json:"HighestSaves,omitempty"`
	HighestStuns                 *StatisticIntegerBest      `json:"HighestStuns,omitempty"`
	Interceptions                *StatisticIntegerIncrement `json:"Interceptions,omitempty"`
	JoustsWon                    *StatisticIntegerIncrement `json:"JoustsWon,omitempty"`
	Level                        *StatisticIntegerIncrement `json:"Level,omitempty"`
	OnePointGoals                *StatisticIntegerIncrement `json:"OnePointGoals,omitempty"`
	Passes                       *StatisticIntegerIncrement `json:"Passes,omitempty"`
	Points                       *StatisticIntegerIncrement `json:"Points,omitempty"`
	PossessionTime               *StatisticFloatIncrement   `json:"PossessionTime,omitempty"`
	PunchesReceived              *StatisticIntegerIncrement `json:"PunchesReceived,omitempty"`
	Saves                        *StatisticIntegerIncrement `json:"Saves,omitempty"`
	SavesPerGame                 *StatisticFloatSet         `json:"SavesPerGame,omitempty"`
	ShotsOnGoal                  *StatisticIntegerIncrement `json:"ShotsOnGoal,omitempty"`
	ShotsOnGoalAgainst           *StatisticIntegerIncrement `json:"ShotsOnGoalAgainst,omitempty"`
	Steals                       *StatisticIntegerIncrement `json:"Steals,omitempty"`
	StunPercentage               *StatisticFloatSet         `json:"StunPercentage,omitempty"`
	Stuns                        *StatisticIntegerIncrement `json:"Stuns,omitempty"`
	StunsPerGame                 *StatisticFloatSet         `json:"StunsPerGame,omitempty"`
	ThreePointGoals              *StatisticIntegerIncrement `json:"ThreePointGoals,omitempty"`
	TopSpeedsTotal               *StatisticFloatIncrement   `json:"TopSpeedsTotal,omitempty"`
	TwoPointGoals                *StatisticIntegerIncrement `json:"TwoPointGoals,omitempty"`
	XP                           *StatisticFloatIncrement   `json:"XP,omitempty"`
	GamesPlayed                  *StatisticIntegerIncrement `json:"GamesPlayed,omitempty"`
	SkillRatingMu                *StatisticFloatSet         `json:"SkillRatingMu,omitempty"`
	SkillRatingSigma             *StatisticFloatSet         `json:"SkillRatingSigma,omitempty"`
	RankPercentile               *StatisticFloatSet         `json:"RankPercentile,omitempty"`
	LobbyTime                    *StatisticFloatSet         `json:"LobbyTime,omitempty"`
	EarlyQuits                   *StatisticIntegerIncrement `json:"EarlyQuits,omitempty"`
	EarlyQuitPercentage          *StatisticFloatSet         `json:"EarlyQuitPercentage,omitempty"`
}

func (s *ArenaStatistics) CalculateFields() {

	if s == nil || s.ArenaWins == nil || s.ArenaLosses == nil {
		return
	}

	if s.EarlyQuits == nil {
		s.ArenaTies = &StatisticIntegerIncrement{
			IntegerStatistic{
				Value: 0,
				Count: 1,
			},
		}
		s.EarlyQuits = &StatisticIntegerIncrement{
			IntegerStatistic{
				Value: 0,
				Count: 1,
			},
		}
	}

	gamesPlayed := s.ArenaWins.GetValue() + s.ArenaLosses.GetValue() + s.EarlyQuits.GetValue()

	s.GamesPlayed = &StatisticIntegerIncrement{
		IntegerStatistic{
			Value: int64(gamesPlayed),
			Count: 1,
		},
	}

	// ArenaWinPercentage
	if gamesPlayed > 0 {

		if s.EarlyQuits != nil {

			s.ArenaTies = &StatisticIntegerIncrement{
				IntegerStatistic{
					Value: s.EarlyQuits.Value,
					Count: 1,
				},
			}

			s.EarlyQuitPercentage = &StatisticFloatSet{
				FloatStatistic{
					Value: s.EarlyQuits.GetValue() / gamesPlayed * 100,
					Count: 1,
				},
			}

		}

		if s.ArenaWins != nil {
			s.ArenaWinPercentage = &StatisticFloatSet{
				FloatStatistic{
					Value: s.ArenaWins.GetValue() / gamesPlayed * 100,
					Count: 1,
				},
			}
		}

		if s.Assists != nil {
			s.AssistsPerGame = &StatisticFloatSet{
				FloatStatistic{
					Value: s.Assists.GetValue() / gamesPlayed,
					Count: 1,
				},
			}
		}

		if s.Points != nil {
			s.AveragePointsPerGame = &StatisticFloatSet{
				FloatStatistic{
					Value: float64(s.Points.GetValue()) / gamesPlayed,
					Count: 1,
				},
			}
		}

		if s.PossessionTime != nil {
			s.AveragePossessionTimePerGame = &StatisticFloatSet{
				FloatStatistic{
					Value: s.PossessionTime.GetValue() / gamesPlayed,
					Count: 1,
				},
			}
		}

		if s.TopSpeedsTotal != nil {
			s.AverageTopSpeedPerGame = &StatisticFloatSet{
				FloatStatistic{
					Value: s.TopSpeedsTotal.GetValue() / gamesPlayed,
					Count: 1,
				},
			}
		}

		if s.Saves != nil && s.ShotsOnGoalAgainst != nil {
			if s.ShotsOnGoalAgainst.GetValue() > 0 {
				s.GoalSavePercentage = &StatisticFloatSet{
					FloatStatistic{
						Value: s.Saves.GetValue() / s.ShotsOnGoalAgainst.GetValue() * 100,
						Count: 1,
					},
				}
			}
		}

		if s.Goals != nil && s.ShotsOnGoal != nil {
			if s.ShotsOnGoal.GetValue() > 0 {
				s.GoalScorePercentage = &StatisticFloatSet{
					FloatStatistic{
						Value: s.Goals.GetValue() / s.ShotsOnGoal.GetValue() * 100,
						Count: 1,
					},
				}
			}

		}

		if s.Goals != nil {
			s.GoalsPerGame = &StatisticFloatSet{
				FloatStatistic{
					Value: s.Goals.GetValue() / gamesPlayed,
					Count: 1,
				},
			}
		}

		if s.Saves != nil {
			s.SavesPerGame = &StatisticFloatSet{
				FloatStatistic{
					Value: s.Saves.GetValue() / gamesPlayed,
					Count: 1,
				},
			}
		}

		if s.PunchesReceived != nil && s.PunchesReceived.GetValue() > 0 {
			if s.Stuns != nil {
				s.StunPercentage = &StatisticFloatSet{
					FloatStatistic{
						Value: s.Stuns.GetValue() / s.PunchesReceived.GetValue() * 100,
						Count: 1,
					},
				}
			}
			if s.Blocks != nil {
				s.BlockPercentage = &StatisticFloatSet{
					FloatStatistic{
						Value: s.Blocks.GetValue() / s.PunchesReceived.GetValue() * 100,
						Count: 1,
					},
				}
			}
		}

		if s.Stuns != nil {
			s.StunsPerGame = &StatisticFloatSet{
				FloatStatistic{
					Value: s.Stuns.GetValue() / gamesPlayed,
					Count: 1,
				},
			}
		}
	}
}

type CombatStatistics struct {
	CombatAssists                      *StatisticIntegerIncrement `json:"CombatAssists"`
	CombatAverageEliminationDeathRatio *StatisticFloatAverage     `json:"CombatAverageEliminationDeathRatio"`
	CombatBestEliminationStreak        *StatisticIntegerIncrement `json:"CombatBestEliminationStreak"`
	CombatDamage                       *StatisticFloatIncrement   `json:"CombatDamage"`
	CombatDamageAbsorbed               *StatisticFloatIncrement   `json:"CombatDamageAbsorbed"`
	CombatDamageTaken                  *StatisticFloatIncrement   `json:"CombatDamageTaken"`
	CombatDeaths                       *StatisticIntegerIncrement `json:"CombatDeaths"`
	CombatEliminations                 *StatisticIntegerIncrement `json:"CombatEliminations"`
	CombatHeadshotKills                *StatisticIntegerIncrement `json:"CombatHeadshotKills"`
	CombatHealing                      *StatisticFloatIncrement   `json:"CombatHealing"`
	CombatHillCaptures                 *StatisticIntegerIncrement `json:"CombatHillCaptures"`
	CombatHillDefends                  *StatisticIntegerIncrement `json:"CombatHillDefends"`
	CombatKills                        *StatisticIntegerIncrement `json:"CombatKills"`
	CombatLosses                       *StatisticIntegerIncrement `json:"CombatLosses"`
	CombatMVPs                         *StatisticIntegerIncrement `json:"CombatMVPs"`
	CombatObjectiveDamage              *StatisticFloatIncrement   `json:"CombatObjectiveDamage"`
	CombatObjectiveEliminations        *StatisticIntegerIncrement `json:"CombatObjectiveEliminations"`
	CombatObjectiveTime                *StatisticFloatIncrement   `json:"CombatObjectiveTime"`
	CombatPayloadGamesPlayed           *StatisticIntegerIncrement `json:"CombatPayloadGamesPlayed"`
	CombatPayloadWinPercentage         *StatisticFloatSet         `json:"CombatPayloadWinPercentage"`
	CombatPayloadWins                  *StatisticIntegerIncrement `json:"CombatPayloadWins"`
	CombatPointCaptureGamesPlayed      *StatisticIntegerIncrement `json:"CombatPointCaptureGamesPlayed"`
	CombatPointCaptureWinPercentage    *StatisticFloatSet         `json:"CombatPointCaptureWinPercentage"`
	CombatPointCaptureWins             *StatisticIntegerIncrement `json:"CombatPointCaptureWins"`
	CombatSoloKills                    *StatisticIntegerIncrement `json:"CombatSoloKills"`
	CombatStuns                        *StatisticIntegerIncrement `json:"CombatStuns"`
	CombatTeammateHealing              *StatisticFloatIncrement   `json:"CombatTeammateHealing"`
	CombatWinPercentage                *StatisticFloatSet         `json:"CombatWinPercentage"`
	CombatWins                         *StatisticIntegerIncrement `json:"CombatWins"`
	Level                              *StatisticIntegerIncrement `json:"Level"`
	XP                                 *StatisticFloatSet         `json:"XP"`
	GamesPlayed                        *StatisticIntegerIncrement `json:"GamesPlayed"`
	SkillRatingMu                      *StatisticFloatSet         `json:"SkillRatingMu"`
	SkillRatingSigma                   *StatisticFloatSet         `json:"SkillRatingSigma"`
	RankPercentile                     *StatisticFloatSet         `json:"RankPercentile"`
	LobbyTime                          *StatisticFloatSet         `json:"LobbyTime"`
}

func (s *CombatStatistics) CalculateFields() {
	if s.CombatWins != nil && s.GamesPlayed != nil && s.GamesPlayed.Value != 0 {
		s.CombatWinPercentage.Value = float64(s.CombatWins.Value) / float64(s.GamesPlayed.Value) * 100
	}

	if s.CombatAssists != nil && s.GamesPlayed != nil && s.GamesPlayed.Value != 0 {
		s.CombatAverageEliminationDeathRatio.Value = float64(s.CombatAssists.Value) / float64(s.GamesPlayed.Value)
	}

	if s.CombatPayloadWins != nil && s.CombatPayloadGamesPlayed != nil && s.CombatPayloadGamesPlayed.Value != 0 {
		s.CombatPayloadWinPercentage.Value = float64(s.CombatPayloadWins.Value) / float64(s.CombatPayloadGamesPlayed.Value) * 100
	}

	if s.CombatPointCaptureWins != nil && s.CombatPointCaptureGamesPlayed != nil && s.CombatPointCaptureGamesPlayed.Value != 0 {
		s.CombatPointCaptureWinPercentage.Value = float64(s.CombatPointCaptureWins.Value) / float64(s.CombatPointCaptureGamesPlayed.Value) * 100
	}
}

type GenericStats struct { // Privates and Social Lobby
	LobbyTime *StatisticFloatIncrement `json:"LobbyTime,omitempty"`
}

func (GenericStats) CalculateFields() {}

func statisticMarshalJSON[T int64 | float64 | float32](op string, cnt int64, val T) ([]byte, error) {
	return []byte(fmt.Sprintf("{\"val\":%v,\"op\":\"%s\",\"cnt\":%d}", float32(val), op, cnt)), nil
}

func Float64ToInt64Pair(f float64) (int64, int64) {
	whole := int64(f)                        // Extract whole number part
	fraction := f - float64(whole)           // Extract fractional part
	scaledFraction := int64(fraction * 1e18) // Scale to preserve precision
	return whole, scaledFraction
}

func Int64PairToFloat64(whole, fraction int64) float64 {
	return float64(whole) + float64(fraction)/1e18
}

type Statistics interface {
	CalculateFields()
}

type Statistic interface {
	GetValue() float64
	SetValue(float64)
	GetCount() int64
	SetCount(int64)
	FromScore(int64, int64)
	MarshalJSON() ([]byte, error)
}

type IntegerStatistic struct {
	Value int64 `json:"val"`
	Count int64 `json:"cnt"`
}

func (s *IntegerStatistic) GetCount() int64 {
	if s == nil {
		return 1
	}
	return s.Count
}

func (s *IntegerStatistic) SetCount(c int64) {
	s.Count = c
}

func (s *IntegerStatistic) GetValue() float64 {
	if s == nil {
		return 0
	}
	return float64(s.Value)
}

func (s *IntegerStatistic) SetValue(v float64) {
	s.Value = int64(v)
}

func (s *IntegerStatistic) FromScore(score, subscore int64) {
	s.Value = score
}

type FloatStatistic struct {
	Value float64 `json:"val"`
	Count int64   `json:"cnt"`
}

func (s *FloatStatistic) GetCount() int64 {
	if s == nil {
		return 0
	}
	return s.Count
}

func (s *FloatStatistic) SetCount(c int64) {
	s.Count = c
}

func (s *FloatStatistic) GetValue() float64 {
	if s == nil {
		return 0
	}
	return float64(s.Value)
}

func (s *FloatStatistic) SetValue(v float64) {
	s.Value = float64(v)
}

func (s *FloatStatistic) FromScore(score, subscore int64) {
	s.Value = Int64PairToFloat64(score, subscore)
}

type StatisticIntegerIncrement struct {
	IntegerStatistic
}

func (s StatisticIntegerIncrement) MarshalJSON() ([]byte, error) {
	return statisticMarshalJSON("add", s.Count, s.Value)
}

type StatisticIntegerSet struct {
	IntegerStatistic
}

func (s StatisticIntegerSet) MarshalJSON() ([]byte, error) {
	return statisticMarshalJSON("rep", s.Count, s.Value)
}

type StatisticIntegerBest struct {
	IntegerStatistic
}

func (s StatisticIntegerBest) MarshalJSON() ([]byte, error) {
	return statisticMarshalJSON("max", s.Count, s.Value)
}

type StatisticFloatIncrement struct {
	FloatStatistic
}

func (s StatisticFloatIncrement) MarshalJSON() ([]byte, error) {
	return statisticMarshalJSON("add", s.Count, s.Value)
}

type StatisticFloatSet struct {
	FloatStatistic
}

func (s StatisticFloatSet) MarshalJSON() ([]byte, error) {
	return statisticMarshalJSON("rep", s.Count, s.Value)
}

type StatisticFloatBest struct {
	FloatStatistic
}

func (s StatisticFloatBest) MarshalJSON() ([]byte, error) {
	return statisticMarshalJSON("max", s.Count, s.Value)
}

type StatisticFloatAverage struct {
	FloatStatistic
}

func (s StatisticFloatAverage) MarshalJSON() ([]byte, error) {
	return statisticMarshalJSON("avg", s.Count, s.Value)
}

func NewStatistics() PlayerStatistics {
	return PlayerStatistics{
		StatisticsGroup{Mode: ModeArenaPublic, ResetSchedule: ResetScheduleAllTime}: &ArenaStatistics{
			Level: &StatisticIntegerIncrement{
				IntegerStatistic{
					Count: 1,
					Value: 1,
				},
			},
		},
		StatisticsGroup{Mode: ModeCombatPublic, ResetSchedule: ResetScheduleAllTime}: &CombatStatistics{
			Level: &StatisticIntegerIncrement{
				IntegerStatistic{
					Count: 1,
					Value: 1,
				},
			},
		},
	}
}

// Echo normally resets stats on Thursday, so use that as the default
func mostRecentThursday() time.Time {
	now := time.Now().UTC()
	offset := (int(now.Weekday()) - int(time.Thursday) + 7) % 7
	return now.AddDate(0, 0, -offset)
}

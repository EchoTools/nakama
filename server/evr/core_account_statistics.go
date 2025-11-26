package evr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/intinig/go-openskill/rating"
	"github.com/intinig/go-openskill/types"
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
				s.Level = &StatisticValue{
					Count: 1,
					Value: 1,
				}
			}

		case ModeCombatPublic:

			stats.(*CombatStatistics).CalculateFields()
			if s := stats.(*CombatStatistics); s.Level == nil || s.Level.Value == 0 || s.Level.Count == 0 {
				s.Level = &StatisticValue{
					Count: 1,
					Value: 1,
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
			if val, ok := v.Field(i).Interface().(*StatisticValue); ok && !v.Field(i).IsNil() {
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
	ArenaLosses                  *StatisticValue `json:"ArenaLosses,omitempty" op:"add,omitzero" type:"int"`
	ArenaMVPPercentage           *StatisticValue `json:"ArenaMVPPercentage,omitempty" op:"rep,omitzero" type:"float"`
	ArenaMVPs                    *StatisticValue `json:"ArenaMVPs,omitempty" op:"add,omitzero" type:"int"`
	ArenaTies                    *StatisticValue `json:"ArenaTies,omitempty" op:"add,omitzero" type:"int"`
	ArenaWinPercentage           *StatisticValue `json:"ArenaWinPercentage,omitempty" op:"rep,omitzero" type:"float"`
	ArenaWins                    *StatisticValue `json:"ArenaWins,omitempty" op:"add,omitzero" type:"int"`
	Assists                      *StatisticValue `json:"Assists,omitempty" op:"add,omitzero" type:"int"`
	AssistsPerGame               *StatisticValue `json:"AssistsPerGame,omitempty" op:"avg,omitzero" type:"float"`
	AveragePointsPerGame         *StatisticValue `json:"AveragePointsPerGame,omitempty" op:"avg,omitzero" type:"float"`
	AveragePossessionTimePerGame *StatisticValue `json:"AveragePossessionTimePerGame,omitempty" op:"avg,omitzero" type:"float"`
	AverageTopSpeedPerGame       *StatisticValue `json:"AverageTopSpeedPerGame,omitempty" op:"avg,omitzero" type:"float"`
	BlockPercentage              *StatisticValue `json:"BlockPercentage,omitempty" op:"rep,omitzero" type:"float"`
	Blocks                       *StatisticValue `json:"Blocks,omitempty" op:"add,omitzero" type:"int"`
	BounceGoals                  *StatisticValue `json:"BounceGoals,omitempty" op:"add,omitzero" type:"int"`
	BumperShots                  *StatisticValue `json:"BumperShots,omitempty" op:"add,omitzero" type:"int"`
	Catches                      *StatisticValue `json:"Catches,omitempty" op:"add,omitzero" type:"int"`
	Clears                       *StatisticValue `json:"Clears,omitempty" op:"add,omitzero" type:"int"`
	CurrentArenaMVPStreak        *StatisticValue `json:"CurrentArenaMVPStreak,omitempty" op:"add,omitzero" type:"int"`
	CurrentArenaWinStreak        *StatisticValue `json:"CurrentArenaWinStreak,omitempty" op:"add,omitzero" type:"int"`
	GoalSavePercentage           *StatisticValue `json:"GoalSavePercentage,omitempty" op:"rep,omitzero" type:"float"`
	GoalScorePercentage          *StatisticValue `json:"GoalScorePercentage,omitempty" op:"rep,omitzero" type:"float"`
	Goals                        *StatisticValue `json:"Goals,omitempty" op:"add,omitzero" type:"int"`
	GoalsPerGame                 *StatisticValue `json:"GoalsPerGame,omitempty" op:"avg,omitzero" type:"float"`
	HatTricks                    *StatisticValue `json:"HatTricks,omitempty" op:"add,omitzero" type:"int"`
	HeadbuttGoals                *StatisticValue `json:"HeadbuttGoals,omitempty" op:"add,omitzero" type:"int"`
	HighestArenaMVPStreak        *StatisticValue `json:"HighestArenaMVPStreak,omitempty" op:"max,omitzero" type:"int"`
	HighestArenaWinStreak        *StatisticValue `json:"HighestArenaWinStreak,omitempty" op:"max,omitzero" type:"int"`
	HighestPoints                *StatisticValue `json:"HighestPoints,omitempty" op:"max,omitzero" type:"int"`
	HighestSaves                 *StatisticValue `json:"HighestSaves,omitempty" op:"max,omitzero" type:"int"`
	HighestStuns                 *StatisticValue `json:"HighestStuns,omitempty" op:"max,omitzero" type:"int"`
	Interceptions                *StatisticValue `json:"Interceptions,omitempty" op:"add,omitzero" type:"int"`
	JoustsWon                    *StatisticValue `json:"JoustsWon,omitempty" op:"add,omitzero" type:"int"`
	Level                        *StatisticValue `json:"Level,omitempty" op:"add,omitzero" type:"int"`
	OnePointGoals                *StatisticValue `json:"OnePointGoals,omitempty" op:"add,omitzero" type:"int"`
	Passes                       *StatisticValue `json:"Passes,omitempty" op:"add,omitzero" type:"int"`
	Points                       *StatisticValue `json:"Points,omitempty" op:"add,omitzero" type:"int"`
	PossessionTime               *StatisticValue `json:"PossessionTime,omitempty" op:"add,omitzero" type:"float"`
	PunchesReceived              *StatisticValue `json:"PunchesReceived,omitempty" op:"add,omitzero" type:"int"`
	Saves                        *StatisticValue `json:"Saves,omitempty" op:"add,omitzero" type:"int"`
	SavesPerGame                 *StatisticValue `json:"SavesPerGame,omitempty" op:"avg,omitzero" type:"float"`
	ShotsOnGoal                  *StatisticValue `json:"ShotsOnGoal,omitempty" op:"add,omitzero" type:"int"`
	ShotsOnGoalAgainst           *StatisticValue `json:"ShotsOnGoalAgainst,omitempty" op:"add,omitzero" type:"int"`
	Steals                       *StatisticValue `json:"Steals,omitempty" op:"add,omitzero" type:"int"`
	StunPercentage               *StatisticValue `json:"StunPercentage,omitempty" op:"rep,omitzero" type:"float"`
	Stuns                        *StatisticValue `json:"Stuns,omitempty" op:"add,omitzero" type:"int"`
	StunsPerGame                 *StatisticValue `json:"StunsPerGame,omitempty" op:"avg,omitzero" type:"float"`
	ThreePointGoals              *StatisticValue `json:"ThreePointGoals,omitempty" op:"add,omitzero" type:"int"`
	TopSpeedsTotal               *StatisticValue `json:"TopSpeedsTotal,omitempty" op:"add,omitzero" type:"float"`
	TwoPointGoals                *StatisticValue `json:"TwoPointGoals,omitempty" op:"add,omitzero" type:"int"`
	XP                           *StatisticValue `json:"XP,omitempty" op:"add,omitzero" type:"float"`
	GamesPlayed                  *StatisticValue `json:"GamesPlayed,omitempty" op:"add,omitzero" type:"int"`
	SkillRatingMu                *StatisticValue `json:"SkillRatingMu,omitempty" op:"rep,omitzero" type:"float"`
	SkillRatingSigma             *StatisticValue `json:"SkillRatingSigma,omitempty" op:"rep,omitzero" type:"float"`
	SkillRatingOrdinal           *StatisticValue `json:"SkillRatingOrdinal,omitempty" op:"rep,omitzero" type:"float"`
	RankPercentile               *StatisticValue `json:"RankPercentile,omitempty" op:"rep,omitzero" type:"float"`
	LobbyTime                    *StatisticValue `json:"LobbyTime,omitempty" op:"rep,omitzero" type:"float"`
	EarlyQuits                   *StatisticValue `json:"EarlyQuits,omitempty" op:"add,omitzero" type:"int"`
	EarlyQuitPercentage          *StatisticValue `json:"EarlyQuitPercentage,omitempty" op:"rep,omitzero" type:"float"`
}

func (s *ArenaStatistics) CalculateFields() {
	if s == nil {
		return
	}

	if s.EarlyQuits == nil {
		s.ArenaTies = &StatisticValue{
			Value: 0,
			Count: 1,
		}

		s.EarlyQuits = &StatisticValue{

			Value: 0,
			Count: 1,
		}

	}

	gamesPlayed := 0.0
	if s.ArenaWins != nil {
		gamesPlayed += s.ArenaWins.GetValue()
	}
	if s.ArenaLosses != nil {
		gamesPlayed += s.ArenaLosses.GetValue()
	}

	s.GamesPlayed = &StatisticValue{
		Value: float64(gamesPlayed),
		Count: 1,
	}

	// ArenaWinPercentage
	if gamesPlayed > 0 {

		if s.EarlyQuits != nil {

			s.ArenaTies = &StatisticValue{
				Value: s.EarlyQuits.Value,
				Count: 1,
			}

			s.EarlyQuitPercentage = &StatisticValue{
				Value: s.EarlyQuits.GetValue() / gamesPlayed * 100,
				Count: 1,
			}

		}

		if s.ArenaWins != nil {
			s.ArenaWinPercentage = &StatisticValue{
				Value: s.ArenaWins.GetValue() / gamesPlayed * 100,
				Count: 1,
			}
		}

		if s.Assists != nil {
			s.AssistsPerGame = &StatisticValue{
				Value: s.Assists.GetValue() / gamesPlayed,
				Count: 1,
			}
		}

		if s.Points != nil {
			s.AveragePointsPerGame = &StatisticValue{
				Value: float64(s.Points.GetValue()) / gamesPlayed,
				Count: 1,
			}
		}

		if s.PossessionTime != nil {
			s.AveragePossessionTimePerGame = &StatisticValue{
				Value: s.PossessionTime.GetValue() / gamesPlayed,
				Count: 1,
			}
		}

		if s.TopSpeedsTotal != nil {
			s.AverageTopSpeedPerGame = &StatisticValue{
				Value: s.TopSpeedsTotal.GetValue() / gamesPlayed,
				Count: 1,
			}
		}

		if s.Saves != nil && s.ShotsOnGoalAgainst != nil {
			if s.ShotsOnGoalAgainst.GetValue() > 0 {
				s.GoalSavePercentage = &StatisticValue{
					Value: s.Saves.GetValue() / s.ShotsOnGoalAgainst.GetValue() * 100,
					Count: 1,
				}
			}
		}

		if s.Goals != nil && s.ShotsOnGoal != nil {
			if s.ShotsOnGoal.GetValue() > 0 {
				s.GoalScorePercentage = &StatisticValue{
					Value: s.Goals.GetValue() / s.ShotsOnGoal.GetValue() * 100,
					Count: 1,
				}
			}

		}

		if s.Goals != nil {
			s.GoalsPerGame = &StatisticValue{
				Value: s.Goals.GetValue() / gamesPlayed,
				Count: 1,
			}
		}

		if s.Saves != nil {
			s.SavesPerGame = &StatisticValue{
				Value: s.Saves.GetValue() / gamesPlayed,
				Count: 1,
			}
		}

		if s.PunchesReceived != nil && s.PunchesReceived.GetValue() > 0 {
			if s.Stuns != nil {
				s.StunPercentage = &StatisticValue{
					Value: s.Stuns.GetValue() / s.PunchesReceived.GetValue() * 100,
					Count: 1,
				}
			}
			if s.Blocks != nil {
				s.BlockPercentage = &StatisticValue{
					Value: s.Blocks.GetValue() / s.PunchesReceived.GetValue() * 100,
					Count: 1,
				}
			}
		}

		if s.Stuns != nil {
			s.StunsPerGame = &StatisticValue{
				Value: s.Stuns.GetValue() / gamesPlayed,
				Count: 1,
			}
		}

		if s.SkillRatingMu != nil && s.SkillRatingSigma != nil {
			r := types.Rating{
				Sigma: s.SkillRatingSigma.GetValue(),
				Mu:    s.SkillRatingMu.GetValue(),
				Z:     3,
			}

			s.SkillRatingOrdinal = &StatisticValue{
				Value: rating.Ordinal(r),
				Count: 1,
			}
		}
	}
}

type CombatStatistics struct {
	CombatAssists                      *StatisticValue `json:"CombatAssists,omitempty" op:"add,omitzero" type:"int"`
	CombatAverageEliminationDeathRatio *StatisticValue `json:"CombatAverageEliminationDeathRatio" op:"rep,omitzero" type:"float"`
	CombatBestEliminationStreak        *StatisticValue `json:"CombatBestEliminationStreak,omitempty" op:"add,omitzero" type:"int"`
	CombatDamage                       *StatisticValue `json:"CombatDamage,omitempty" op:"add,omitzero" type:"float"`
	CombatDamageAbsorbed               *StatisticValue `json:"CombatDamageAbsorbed,omitempty" op:"add,omitzero" type:"float"`
	CombatDamageTaken                  *StatisticValue `json:"CombatDamageTaken,omitempty" op:"add,omitzero" type:"float"`
	CombatDeaths                       *StatisticValue `json:"CombatDeaths,omitempty" op:"add,omitzero" type:"int"`
	CombatEliminations                 *StatisticValue `json:"CombatEliminations,omitempty" op:"add,omitzero" type:"int"`
	CombatHeadshotKills                *StatisticValue `json:"CombatHeadshotKills,omitempty" op:"add,omitzero" type:"int"`
	CombatHealing                      *StatisticValue `json:"CombatHealing,omitempty" op:"add,omitzero" type:"float"`
	CombatHillCaptures                 *StatisticValue `json:"CombatHillCaptures,omitempty" op:"add,omitzero" type:"int"`
	CombatHillDefends                  *StatisticValue `json:"CombatHillDefends,omitempty" op:"add,omitzero" type:"int"`
	CombatKills                        *StatisticValue `json:"CombatKills,omitempty" op:"add,omitzero" type:"int"`
	CombatMVPs                         *StatisticValue `json:"CombatMVPs,omitempty" op:"add,omitzero" type:"int"`
	CombatObjectiveDamage              *StatisticValue `json:"CombatObjectiveDamage,omitempty" op:"add,omitzero" type:"float"`
	CombatObjectiveEliminations        *StatisticValue `json:"CombatObjectiveEliminations,omitempty" op:"add,omitzero" type:"int"`
	CombatObjectiveTime                *StatisticValue `json:"CombatObjectiveTime,omitempty" op:"add,omitzero" type:"float"`
	CombatPayloadGamesPlayed           *StatisticValue `json:"CombatPayloadGamesPlayed,omitempty" op:"add,omitzero" type:"int"`
	CombatPayloadWinPercentage         *StatisticValue `json:"CombatPayloadWinPercentage,omitempty" op:"rep,omitzero" type:"float"`
	CombatPayloadWins                  *StatisticValue `json:"CombatPayloadWins,omitempty" op:"add,omitzero" type:"int"`
	CombatPointCaptureGamesPlayed      *StatisticValue `json:"CombatPointCaptureGamesPlayed,omitempty" op:"add,omitzero" type:"int"`
	CombatPointCaptureWinPercentage    *StatisticValue `json:"CombatPointCaptureWinPercentage,omitempty" op:"rep,omitzero" type:"float"`
	CombatPointCaptureWins             *StatisticValue `json:"CombatPointCaptureWins,omitempty" op:"add,omitzero" type:"int"`
	CombatSoloKills                    *StatisticValue `json:"CombatSoloKills,omitempty" op:"add,omitzero" type:"int"`
	CombatStuns                        *StatisticValue `json:"CombatStuns,omitempty" op:"add,omitzero" type:"int"`
	CombatTeammateHealing              *StatisticValue `json:"CombatTeammateHealing,omitempty" op:"add,omitzero" type:"float"`
	CombatWins                         *StatisticValue `json:"CombatWins,omitempty" op:"add,omitzero" type:"int"`
	CombatLosses                       *StatisticValue `json:"CombatLosses,omitempty" op:"add,omitzero" type:"int"`
	CombatWinPercentage                *StatisticValue `json:"CombatWinPercentage,omitempty" op:"rep,omitzero" type:"float"`
	Level                              *StatisticValue `json:"Level,omitempty" op:"add,omitzero" type:"int"`
	XP                                 *StatisticValue `json:"XP,omitempty" op:"rep,omitzero" type:"float"`
	GamesPlayed                        *StatisticValue `json:"GamesPlayed,omitempty" op:"add,omitzero" type:"int"`
	SkillRatingMu                      *StatisticValue `json:"SkillRatingMu,omitempty" op:"rep,omitzero" type:"float"`
	SkillRatingSigma                   *StatisticValue `json:"SkillRatingSigma,omitempty" op:"rep,omitzero" type:"float"`
	SkillRatingOrdinal                 *StatisticValue `json:"SkillRatingOrdinal,omitempty" op:"rep,omitzero" type:"float"`
	RankPercentile                     *StatisticValue `json:"RankPercentile,omitempty" op:"rep,omitzero" type:"float"`
	LobbyTime                          *StatisticValue `json:"LobbyTime,omitempty" op:"rep,omitzero" type:"float"`
}

func (s *CombatStatistics) CalculateFields() {
	gamesPlayed := 0
	if s.CombatWins != nil {
		gamesPlayed += int(s.CombatWins.GetValue())
	}

	if s.CombatLosses != nil {
		gamesPlayed += int(s.CombatLosses.GetValue())
	}

	s.GamesPlayed = &StatisticValue{
		Value: float64(gamesPlayed),
		Count: 1,
	}

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

	if s.SkillRatingMu != nil && s.SkillRatingSigma != nil {
		r := types.Rating{
			Sigma: s.SkillRatingSigma.GetValue(),
			Mu:    s.SkillRatingMu.GetValue(),
			Z:     3,
		}

		s.SkillRatingOrdinal = &StatisticValue{
			Value: rating.Ordinal(r),
			Count: 1,
		}
	}

}

type GenericStats struct { // Privates and Social Lobby
	LobbyTime *StatisticValue `json:"LobbyTime,omitempty" op:"add,omitzero" type:"float"`
}

func (GenericStats) CalculateFields() {}

func statisticMarshalJSON[T int64 | float64 | float32](op string, cnt int64, val T) ([]byte, error) {
	return fmt.Appendf(nil, "{\"val\":%v,\"op\":\"%s\",\"cnt\":%d}", float32(val), op, cnt), nil
}

type Statistics interface {
	CalculateFields()
}

type StatisticValue struct {
	Value float64 `json:"val"`
	Count int64   `json:"cnt"`
}

func (s *StatisticValue) GetCount() int64 {
	if s == nil {
		return 0
	}
	return s.Count
}

func (s *StatisticValue) SetCount(c int64) {
	s.Count = c
}

func (s *StatisticValue) GetName() string {
	if t := reflect.TypeFor[*StatisticValue](); t != nil {
		if f, ok := t.Elem().FieldByName("name"); ok {
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			return name
		}
	}
	return ""
}
func (s *StatisticValue) GetValue() float64 {
	if s == nil {
		return 0
	}
	return float64(s.Value)
}

func (s *StatisticValue) SetValue(v float64) {
	s.Value = float64(v)
}

func (s *StatisticValue) Operator() string {
	if t := reflect.TypeFor[*StatisticValue](); t != nil {
		if f, ok := t.Elem().FieldByName("op"); ok {
			opTag := f.Tag.Get("op")
			op, _, _ := strings.Cut(opTag, ",")
			return op
		}
	}
	return ""
}

func (s *StatisticValue) ValueType() string {
	if t := reflect.TypeFor[*StatisticValue](); t != nil {
		if f, ok := t.Elem().FieldByName("type"); ok {
			return f.Tag.Get("type")
		}
	}
	return ""
}

func (s *StatisticValue) MarshalJSON() ([]byte, error) {
	// get the value of op field tag
	op := s.Operator()
	switch s.ValueType() {
	case "int":
		return statisticMarshalJSON(op, s.Count, int64(s.Value))
	case "float":
		return statisticMarshalJSON(op, s.Count, float32(s.Value))
	default:
		return statisticMarshalJSON(op, s.Count, float32(s.Value))
	}
}

func NewStatistics() PlayerStatistics {
	return PlayerStatistics{
		StatisticsGroup{Mode: ModeArenaPublic, ResetSchedule: ResetScheduleAllTime}: &ArenaStatistics{
			Level: &StatisticValue{
				Count: 1,
				Value: 1,
			},
		},
		StatisticsGroup{Mode: ModeCombatPublic, ResetSchedule: ResetScheduleAllTime}: &CombatStatistics{
			Level: &StatisticValue{
				Count: 1,
				Value: 1,
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

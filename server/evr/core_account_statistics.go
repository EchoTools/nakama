package evr

import (
	"encoding/json"
	"fmt"
	"strconv"
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

func (g TabletStatisticGroup) String() string {
	switch Symbol(g) {
	case ModeArenaPublic:
		return "arena"
	case ModeCombatPublic:
		return "combat"
	default:
		return Symbol(g).String()
	}
}

type StatisticsGroup struct {
	Mode          Symbol
	ResetSchedule ResetSchedule
}

func (g *StatisticsGroup) String() string {
	s, _ := g.MarshalText()
	return string(s)
}

func (g *StatisticsGroup) MarshalText() ([]byte, error) {
	switch g.ResetSchedule {
	case ResetScheduleDaily:
		return []byte("daily_" + time.Now().UTC().Format("2006_01_02")), nil
	case ResetScheduleWeekly:
		return []byte("weekly_" + mostRecentThursday().Format("2006_01_02")), nil
	}

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

func (s PlayerStatistics) MarshalJSON() ([]byte, error) {

	m := make(map[string]Statistics)
	for g, stats := range s {
		m[g.String()] = stats
	}

	return json.Marshal(m)
}

func (s *PlayerStatistics) UnmarshalJSON(data []byte) error {
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
	ArenaLosses                  *StatisticIntegerIncrement `json:"ArenaLosses"`
	ArenaMVPPercentage           *StatisticFloatSet         `json:"ArenaMVPPercentage"`
	ArenaMVPS                    *StatisticIntegerIncrement `json:"ArenaMVPs"`
	ArenaTies                    *StatisticIntegerIncrement `json:"ArenaTies"`
	ArenaWinPercentage           *StatisticFloatSet         `json:"ArenaWinPercentage"`
	ArenaWins                    *StatisticIntegerIncrement `json:"ArenaWins"`
	Assists                      *StatisticIntegerIncrement `json:"Assists"`
	AssistsPerGame               *StatisticFloatSet         `json:"AssistsPerGame"`
	AveragePointsPerGame         *StatisticFloatSet         `json:"AveragePointsPerGame"`
	AveragePossessionTimePerGame *StatisticFloatSet         `json:"AveragePossessionTimePerGame"`
	AverageTopSpeedPerGame       *StatisticFloatSet         `json:"AverageTopSpeedPerGame"`
	BlockPercentage              *StatisticFloatSet         `json:"BlockPercentage"`
	Blocks                       *StatisticIntegerIncrement `json:"Blocks"`
	BounceGoals                  *StatisticIntegerIncrement `json:"BounceGoals"`
	BumperShots                  *StatisticIntegerIncrement `json:"BumperShots"`
	Catches                      *StatisticIntegerIncrement `json:"Catches"`
	Clears                       *StatisticIntegerIncrement `json:"Clears"`
	CurrentArenaMVPStreak        *StatisticIntegerIncrement `json:"CurrentArenaMVPStreak"`
	CurrentArenaWinStreak        *StatisticIntegerIncrement `json:"CurrentArenaWinStreak"`
	Goals                        *StatisticIntegerIncrement `json:"Goals"`
	GoalSavePercentage           *StatisticFloatSet         `json:"GoalSavePercentage"`
	GoalScorePercentage          *StatisticFloatSet         `json:"GoalScorePercentage"`
	GoalsPerGame                 *StatisticFloatSet         `json:"GoalsPerGame"`
	HatTricks                    *StatisticIntegerIncrement `json:"HatTricks"`
	HeadbuttGoals                *StatisticIntegerIncrement `json:"HeadbuttGoals"`
	HighestArenaMVPStreak        *StatisticIntegerBest      `json:"HighestArenaMVPStreak"`
	HighestArenaWinStreak        *StatisticIntegerBest      `json:"HighestArenaWinStreak"`
	HighestPoints                *StatisticIntegerBest      `json:"HighestPoints"`
	HighestSaves                 *StatisticIntegerBest      `json:"HighestSaves"`
	HighestStuns                 *StatisticIntegerBest      `json:"HighestStuns"`
	Interceptions                *StatisticIntegerIncrement `json:"Interceptions"`
	JoustsWon                    *StatisticIntegerIncrement `json:"JoustsWon"`
	Level                        *StatisticIntegerIncrement `json:"Level"`
	OnePointGoals                *StatisticIntegerIncrement `json:"OnePointGoals"`
	Passes                       *StatisticIntegerIncrement `json:"Passes"`
	Points                       *StatisticIntegerIncrement `json:"Points"`
	PossessionTime               *StatisticFloatIncrement   `json:"PossessionTime"`
	PunchesReceived              *StatisticIntegerIncrement `json:"PunchesReceived"`
	Saves                        *StatisticIntegerIncrement `json:"Saves"`
	SavesPerGame                 *StatisticFloatSet         `json:"SavesPerGame"`
	ShotsOnGoal                  *StatisticIntegerIncrement `json:"ShotsOnGoal"`
	ShotsOnGoalAgainst           *StatisticIntegerIncrement `json:"ShotsOnGoalAgainst"`
	Steals                       *StatisticIntegerIncrement `json:"Steals"`
	StunPercentage               *StatisticFloatSet         `json:"StunPercentage"`
	Stuns                        *StatisticIntegerIncrement `json:"Stuns"`
	StunsPerGame                 *StatisticFloatSet         `json:"StunsPerGame"`
	ThreePointGoals              *StatisticIntegerIncrement `json:"ThreePointGoals"`
	TopSpeedsTotal               *StatisticFloatIncrement   `json:"TopSpeedsTotal"`
	TwoPointGoals                *StatisticIntegerIncrement `json:"TwoPointGoals"`
	XP                           *StatisticFloatSet         `json:"XP"`
	GamesPlayed                  *StatisticIntegerIncrement `json:"GamesPlayed"`
	SkillRatingMu                *StatisticFloatSet         `json:"SkillRatingMu"`
	SkillRatingSigma             *StatisticFloatSet         `json:"SkillRatingSigma"`
	RankPercentile               *StatisticFloatSet         `json:"RankPercentile"`
	LobbyTime                    *StatisticFloatSet         `json:"LobbyTime"`
}

func (s *ArenaStatistics) CalculateFields() {
	if s.ArenaWins != nil && s.GamesPlayed != nil && s.GamesPlayed.Value != 0 {
		s.ArenaWinPercentage.Value = float64(s.ArenaWins.Value) / float64(s.GamesPlayed.Value) * 100
	}

	if s.Assists != nil && s.GamesPlayed != nil && s.GamesPlayed.Value != 0 {
		s.AssistsPerGame.Value = float64(s.Assists.Value) / float64(s.GamesPlayed.Value)
	}

	if s.Points != nil && s.GamesPlayed != nil && s.GamesPlayed.Value != 0 {
		s.AveragePointsPerGame.Value = float64(s.Points.Value) / float64(s.GamesPlayed.Value)
	}

	if s.PossessionTime != nil && s.GamesPlayed != nil && s.GamesPlayed.Value != 0 {
		s.AveragePossessionTimePerGame.Value = s.PossessionTime.Value / float64(s.GamesPlayed.Value)
	}

	if s.TopSpeedsTotal != nil && s.GamesPlayed != nil && s.GamesPlayed.Value != 0 {
		s.AverageTopSpeedPerGame.Value = s.TopSpeedsTotal.Value / float64(s.GamesPlayed.Value)
	}

	if s.Saves != nil && s.ShotsOnGoalAgainst != nil && s.ShotsOnGoalAgainst.Value != 0 {
		s.GoalSavePercentage.Value = float64(s.Saves.Value) / float64(s.ShotsOnGoalAgainst.Value) * 100
	}

	if s.Goals != nil && s.ShotsOnGoal != nil && s.ShotsOnGoal.Value != 0 {
		s.GoalScorePercentage.Value = float64(s.Goals.Value) / float64(s.ShotsOnGoal.Value) * 100
	}

	if s.Goals != nil && s.GamesPlayed != nil && s.GamesPlayed.Value != 0 {
		s.GoalsPerGame.Value = float64(s.Goals.Value) / float64(s.GamesPlayed.Value)
	}

	if s.Saves != nil && s.GamesPlayed != nil && s.GamesPlayed.Value != 0 {
		s.SavesPerGame.Value = float64(s.Saves.Value) / float64(s.GamesPlayed.Value)
	}

	if s.Stuns != nil && s.GamesPlayed != nil && s.GamesPlayed.Value != 0 {
		s.StunPercentage.Value = float64(s.Stuns.Value) / float64(s.GamesPlayed.Value) * 100
	}

	if s.Stuns != nil && s.GamesPlayed != nil && s.GamesPlayed.Value != 0 {
		s.StunsPerGame.Value = float64(s.Stuns.Value) / float64(s.GamesPlayed.Value)
	}
}

type CombatStatistics struct {
	CombatAssists                      *StatisticIntegerIncrement `json:"CombatAssists"`
	CombatAverageEliminationDeathRatio *StatisticFloatSet         `json:"CombatAverageEliminationDeathRatio"`
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
	LobbyTime *StatisticFloatIncrement `json:"LobbyTime"`
}

func (GenericStats) CalculateFields() {}

func statisticMarshalJSON[T int64 | float64](op string, cnt int64, val T) ([]byte, error) {
	return []byte(fmt.Sprintf("{\"cnt\":%d,\"op\":\"%s\",\"val\":%v}", cnt, op, val)), nil
}

type Statistics interface {
	CalculateFields()
}

type Statistic interface {
	GetValue() float64
	SetValue(float64)
	FromScore(int64, int64)
}

type IntegerStatistic struct {
	Count int64 `json:"cnt"`
	Value int64 `json:"val"`
}

func (s IntegerStatistic) GetValue() float64 {
	return float64(s.Value)
}

func (s *IntegerStatistic) SetValue(v float64) {
	s.Value = int64(v)
}

func (s *IntegerStatistic) FromScore(score, subscore int64) {
	s.Value = score
}

type FloatStatistic struct {
	Count int64   `json:"cnt"`
	Value float64 `json:"val"`
}

func (s FloatStatistic) GetValue() float64 {
	return s.Value
}

func (s *FloatStatistic) SetValue(v float64) {
	s.Value = v
}

func (s *FloatStatistic) FromScore(score, subscore int64) {
	s.Value, _ = strconv.ParseFloat(fmt.Sprintf("%d.%d", score, subscore), 64)
}

type StatisticIntegerIncrement struct {
	IntegerStatistic
}
type StatisticIntegerSet struct {
	IntegerStatistic
}
type StatisticIntegerBest struct {
	IntegerStatistic
}

type StatisticFloatIncrement struct {
	FloatStatistic
}
type StatisticFloatSet struct {
	FloatStatistic
}
type StatisticFloatBest struct {
	FloatStatistic
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

func mostRecentThursday() time.Time {
	now := time.Now().UTC()
	offset := (int(now.Weekday()) - int(time.Thursday) + 7) % 7
	return now.AddDate(0, 0, -offset)
}

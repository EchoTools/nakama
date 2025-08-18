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
	ArenaLosses                  *StatisticValue `json:"ArenaLosses,omitempty" op:"add" type:"int"`
	ArenaMVPPercentage           *StatisticValue `json:"ArenaMVPPercentage,omitempty" op:"rep" type:"float"`
	ArenaMVPs                    *StatisticValue `json:"ArenaMVPs,omitempty" op:"add" type:"int"`
	ArenaTies                    *StatisticValue `json:"ArenaTies,omitempty" op:"add" type:"int"`
	ArenaWinPercentage           *StatisticValue `json:"ArenaWinPercentage,omitempty" op:"rep" type:"float"`
	ArenaWins                    *StatisticValue `json:"ArenaWins,omitempty" op:"add" type:"int"`
	Assists                      *StatisticValue `json:"Assists,omitempty" op:"add" type:"int"`
	AssistsPerGame               *StatisticValue `json:"AssistsPerGame,omitempty" op:"avg" type:"float"`
	AveragePointsPerGame         *StatisticValue `json:"AveragePointsPerGame,omitempty" op:"avg" type:"float"`
	AveragePossessionTimePerGame *StatisticValue `json:"AveragePossessionTimePerGame,omitempty" op:"avg" type:"float"`
	AverageTopSpeedPerGame       *StatisticValue `json:"AverageTopSpeedPerGame,omitempty" op:"avg" type:"float"`
	BlockPercentage              *StatisticValue `json:"BlockPercentage,omitempty" op:"rep" type:"float"`
	Blocks                       *StatisticValue `json:"Blocks,omitempty" op:"add" type:"int"`
	BounceGoals                  *StatisticValue `json:"BounceGoals,omitempty" op:"add" type:"int"`
	BumperShots                  *StatisticValue `json:"BumperShots,omitempty" op:"add" type:"int"`
	Catches                      *StatisticValue `json:"Catches,omitempty" op:"add" type:"int"`
	Clears                       *StatisticValue `json:"Clears,omitempty" op:"add" type:"int"`
	CurrentArenaMVPStreak        *StatisticValue `json:"CurrentArenaMVPStreak,omitempty" op:"add" type:"int"`
	CurrentArenaWinStreak        *StatisticValue `json:"CurrentArenaWinStreak,omitempty" op:"add" type:"int"`
	GoalSavePercentage           *StatisticValue `json:"GoalSavePercentage,omitempty" op:"rep" type:"float"`
	GoalScorePercentage          *StatisticValue `json:"GoalScorePercentage,omitempty" op:"rep" type:"float"`
	Goals                        *StatisticValue `json:"Goals,omitempty" op:"add" type:"int"`
	GoalsPerGame                 *StatisticValue `json:"GoalsPerGame,omitempty" op:"avg" type:"float"`
	HatTricks                    *StatisticValue `json:"HatTricks,omitempty" op:"add" type:"int"`
	HeadbuttGoals                *StatisticValue `json:"HeadbuttGoals,omitempty" op:"add" type:"int"`
	HighestArenaMVPStreak        *StatisticValue `json:"HighestArenaMVPStreak,omitempty" op:"max" type:"int"`
	HighestArenaWinStreak        *StatisticValue `json:"HighestArenaWinStreak,omitempty" op:"max" type:"int"`
	HighestPoints                *StatisticValue `json:"HighestPoints,omitempty" op:"max" type:"int"`
	HighestSaves                 *StatisticValue `json:"HighestSaves,omitempty" op:"max" type:"int"`
	HighestStuns                 *StatisticValue `json:"HighestStuns,omitempty" op:"max" type:"int"`
	Interceptions                *StatisticValue `json:"Interceptions,omitempty" op:"add" type:"int"`
	JoustsWon                    *StatisticValue `json:"JoustsWon,omitempty" op:"add" type:"int"`
	Level                        *StatisticValue `json:"Level,omitempty" op:"add" type:"int"`
	OnePointGoals                *StatisticValue `json:"OnePointGoals,omitempty" op:"add" type:"int"`
	Passes                       *StatisticValue `json:"Passes,omitempty" op:"add" type:"int"`
	Points                       *StatisticValue `json:"Points,omitempty" op:"add" type:"int"`
	PossessionTime               *StatisticValue `json:"PossessionTime,omitempty" op:"add" type:"float"`
	PunchesReceived              *StatisticValue `json:"PunchesReceived,omitempty" op:"add" type:"int"`
	Saves                        *StatisticValue `json:"Saves,omitempty" op:"add" type:"int"`
	SavesPerGame                 *StatisticValue `json:"SavesPerGame,omitempty" op:"avg" type:"float"`
	ShotsOnGoal                  *StatisticValue `json:"ShotsOnGoal,omitempty" op:"add" type:"int"`
	ShotsOnGoalAgainst           *StatisticValue `json:"ShotsOnGoalAgainst,omitempty" op:"add" type:"int"`
	Steals                       *StatisticValue `json:"Steals,omitempty" op:"add" type:"int"`
	StunPercentage               *StatisticValue `json:"StunPercentage,omitempty" op:"rep" type:"float"`
	Stuns                        *StatisticValue `json:"Stuns,omitempty" op:"add" type:"int"`
	StunsPerGame                 *StatisticValue `json:"StunsPerGame,omitempty" op:"avg" type:"float"`
	ThreePointGoals              *StatisticValue `json:"ThreePointGoals,omitempty" op:"add" type:"int"`
	TopSpeedsTotal               *StatisticValue `json:"TopSpeedsTotal,omitempty" op:"add" type:"float"`
	TwoPointGoals                *StatisticValue `json:"TwoPointGoals,omitempty" op:"add" type:"int"`
	XP                           *StatisticValue `json:"XP,omitempty" op:"add" type:"float"`
	GamesPlayed                  *StatisticValue `json:"GamesPlayed,omitempty" op:"add" type:"int"`
	SkillRatingMu                *StatisticValue `json:"SkillRatingMu,omitempty" op:"rep" type:"float"`
	SkillRatingSigma             *StatisticValue `json:"SkillRatingSigma,omitempty" op:"rep" type:"float"`
	SkillRatingOrdinal           *StatisticValue `json:"SkillRatingOrdinal,omitempty" op:"rep" type:"float"`
	RankPercentile               *StatisticValue `json:"RankPercentile,omitempty" op:"rep" type:"float"`
	LobbyTime                    *StatisticValue `json:"LobbyTime,omitempty" op:"rep" type:"float"`
	EarlyQuits                   *StatisticValue `json:"EarlyQuits,omitempty" op:"add" type:"int"`
	EarlyQuitPercentage          *StatisticValue `json:"EarlyQuitPercentage,omitempty" op:"rep" type:"float"`
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
		winsValue := s.ArenaWins.GetValue()
		// Validate wins value to prevent corrupted statistics
		if winsValue < 0 || winsValue > 1e6 {
			winsValue = 0
		}
		gamesPlayed += winsValue
	}
	if s.ArenaLosses != nil {
		lossesValue := s.ArenaLosses.GetValue()
		// Validate losses value to prevent corrupted statistics
		if lossesValue < 0 || lossesValue > 1e6 {
			lossesValue = 0
		}
		gamesPlayed += lossesValue
	}

	s.GamesPlayed = &StatisticValue{
		Value: float64(gamesPlayed),
		Count: 1,
	}

	// ArenaWinPercentage
	if gamesPlayed > 0 {

		if s.EarlyQuits != nil {
			// Validate EarlyQuits value to prevent corrupted statistics
			earlyQuitsValue := s.EarlyQuits.GetValue()
			if earlyQuitsValue < 0 || earlyQuitsValue > gamesPlayed || earlyQuitsValue > 1e6 {
				// Invalid value detected, reset to 0
				earlyQuitsValue = 0
			}

			s.ArenaTies = &StatisticValue{
				Value: earlyQuitsValue,
				Count: 1,
			}

			s.EarlyQuitPercentage = &StatisticValue{
				Value: earlyQuitsValue / gamesPlayed * 100,
				Count: 1,
			}

		}

		if s.ArenaWins != nil {
			// Validate ArenaWins value to prevent corrupted statistics
			winsValue := s.ArenaWins.GetValue()
			if winsValue < 0 || winsValue > gamesPlayed || winsValue > 1e6 {
				winsValue = 0
			}
			s.ArenaWinPercentage = &StatisticValue{
				Value: winsValue / gamesPlayed * 100,
				Count: 1,
			}
		}

		if s.Assists != nil {
			// Validate Assists value to prevent corrupted statistics
			assistsValue := s.Assists.GetValue()
			if assistsValue < 0 || assistsValue > 1e6 {
				assistsValue = 0
			}
			s.AssistsPerGame = &StatisticValue{
				Value: assistsValue / gamesPlayed,
				Count: 1,
			}
		}

		if s.Points != nil {
			// Validate Points value to prevent corrupted statistics
			pointsValue := s.Points.GetValue()
			if pointsValue < 0 || pointsValue > 1e9 {
				pointsValue = 0
			}
			s.AveragePointsPerGame = &StatisticValue{
				Value: pointsValue / gamesPlayed,
				Count: 1,
			}
		}

		if s.PossessionTime != nil {
			// Validate PossessionTime value to prevent corrupted statistics
			possessionValue := s.PossessionTime.GetValue()
			if possessionValue < 0 || possessionValue > 1e8 {
				possessionValue = 0
			}
			s.AveragePossessionTimePerGame = &StatisticValue{
				Value: possessionValue / gamesPlayed,
				Count: 1,
			}
		}

		if s.TopSpeedsTotal != nil {
			// Validate TopSpeedsTotal value to prevent corrupted statistics
			topSpeedsValue := s.TopSpeedsTotal.GetValue()
			if topSpeedsValue < 0 || topSpeedsValue > 1e8 {
				topSpeedsValue = 0
			}
			s.AverageTopSpeedPerGame = &StatisticValue{
				Value: topSpeedsValue / gamesPlayed,
				Count: 1,
			}
		}

		if s.Saves != nil && s.ShotsOnGoalAgainst != nil {
			savesValue := s.Saves.GetValue()
			shotsAgainstValue := s.ShotsOnGoalAgainst.GetValue()
			// Validate values to prevent corrupted statistics
			if savesValue >= 0 && savesValue <= shotsAgainstValue && 
			   savesValue <= 1e6 && shotsAgainstValue > 0 && shotsAgainstValue <= 1e6 {
				s.GoalSavePercentage = &StatisticValue{
					Value: savesValue / shotsAgainstValue * 100,
					Count: 1,
				}
			}
		}

		if s.Goals != nil && s.ShotsOnGoal != nil {
			goalsValue := s.Goals.GetValue()
			shotsValue := s.ShotsOnGoal.GetValue()
			// Validate values to prevent corrupted statistics
			if goalsValue >= 0 && goalsValue <= shotsValue && 
			   goalsValue <= 1e6 && shotsValue > 0 && shotsValue <= 1e6 {
				s.GoalScorePercentage = &StatisticValue{
					Value: goalsValue / shotsValue * 100,
					Count: 1,
				}
			}
		}

		if s.Goals != nil {
			// Validate Goals value to prevent corrupted statistics
			goalsValue := s.Goals.GetValue()
			if goalsValue < 0 || goalsValue > 1e6 {
				goalsValue = 0
			}
			s.GoalsPerGame = &StatisticValue{
				Value: goalsValue / gamesPlayed,
				Count: 1,
			}
		}

		if s.Saves != nil {
			// Validate Saves value to prevent corrupted statistics
			savesValue := s.Saves.GetValue()
			if savesValue < 0 || savesValue > 1e6 {
				savesValue = 0
			}
			s.SavesPerGame = &StatisticValue{
				Value: savesValue / gamesPlayed,
				Count: 1,
			}
		}

		if s.PunchesReceived != nil && s.PunchesReceived.GetValue() > 0 {
			punchesValue := s.PunchesReceived.GetValue()
			// Validate PunchesReceived value to prevent corrupted statistics
			if punchesValue < 0 || punchesValue > 1e6 {
				// Skip percentage calculations if invalid
			} else {
				if s.Stuns != nil {
					stunsValue := s.Stuns.GetValue()
					// Validate Stuns value to prevent corrupted statistics
					if stunsValue >= 0 && stunsValue <= punchesValue && stunsValue <= 1e6 {
						s.StunPercentage = &StatisticValue{
							Value: stunsValue / punchesValue * 100,
							Count: 1,
						}
					}
				}
				if s.Blocks != nil {
					blocksValue := s.Blocks.GetValue()
					// Validate Blocks value to prevent corrupted statistics
					if blocksValue >= 0 && blocksValue <= punchesValue && blocksValue <= 1e6 {
						s.BlockPercentage = &StatisticValue{
							Value: blocksValue / punchesValue * 100,
							Count: 1,
						}
					}
				}
			}
		}

		if s.Stuns != nil {
			// Validate Stuns value to prevent corrupted statistics
			stunsValue := s.Stuns.GetValue()
			if stunsValue < 0 || stunsValue > 1e6 {
				stunsValue = 0
			}
			s.StunsPerGame = &StatisticValue{
				Value: stunsValue / gamesPlayed,
				Count: 1,
			}
		}

		if s.SkillRatingMu != nil && s.SkillRatingSigma != nil {
			// Validate skill rating values to prevent corrupted statistics
			mu := s.SkillRatingMu.GetValue()
			sigma := s.SkillRatingSigma.GetValue()
			if mu < 0 || mu > 100 || sigma < 0 || sigma > 100 {
				// Use default skill rating values if corrupted
				mu = 25.0
				sigma = 8.333
			}
			
			r := types.Rating{
				Sigma: sigma,
				Mu:    mu,
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
	CombatAssists                      *StatisticValue `json:"CombatAssists,omitempty" op:"add" type:"int"`
	CombatAverageEliminationDeathRatio *StatisticValue `json:"CombatAverageEliminationDeathRatio" op:"rep" type:"float"`
	CombatBestEliminationStreak        *StatisticValue `json:"CombatBestEliminationStreak,omitempty" op:"add" type:"int"`
	CombatDamage                       *StatisticValue `json:"CombatDamage,omitempty" op:"add" type:"float"`
	CombatDamageAbsorbed               *StatisticValue `json:"CombatDamageAbsorbed,omitempty" op:"add" type:"float"`
	CombatDamageTaken                  *StatisticValue `json:"CombatDamageTaken,omitempty" op:"add" type:"float"`
	CombatDeaths                       *StatisticValue `json:"CombatDeaths,omitempty" op:"add" type:"int"`
	CombatEliminations                 *StatisticValue `json:"CombatEliminations,omitempty" op:"add" type:"int"`
	CombatHeadshotKills                *StatisticValue `json:"CombatHeadshotKills,omitempty" op:"add" type:"int"`
	CombatHealing                      *StatisticValue `json:"CombatHealing,omitempty" op:"add" type:"float"`
	CombatHillCaptures                 *StatisticValue `json:"CombatHillCaptures,omitempty" op:"add" type:"int"`
	CombatHillDefends                  *StatisticValue `json:"CombatHillDefends,omitempty" op:"add" type:"int"`
	CombatKills                        *StatisticValue `json:"CombatKills,omitempty" op:"add" type:"int"`
	CombatMVPs                         *StatisticValue `json:"CombatMVPs,omitempty" op:"add" type:"int"`
	CombatObjectiveDamage              *StatisticValue `json:"CombatObjectiveDamage,omitempty" op:"add" type:"float"`
	CombatObjectiveEliminations        *StatisticValue `json:"CombatObjectiveEliminations,omitempty" op:"add" type:"int"`
	CombatObjectiveTime                *StatisticValue `json:"CombatObjectiveTime,omitempty" op:"add" type:"float"`
	CombatPayloadGamesPlayed           *StatisticValue `json:"CombatPayloadGamesPlayed,omitempty" op:"add" type:"int"`
	CombatPayloadWinPercentage         *StatisticValue `json:"CombatPayloadWinPercentage,omitempty" op:"rep" type:"float"`
	CombatPayloadWins                  *StatisticValue `json:"CombatPayloadWins,omitempty" op:"add" type:"int"`
	CombatPointCaptureGamesPlayed      *StatisticValue `json:"CombatPointCaptureGamesPlayed,omitempty" op:"add" type:"int"`
	CombatPointCaptureWinPercentage    *StatisticValue `json:"CombatPointCaptureWinPercentage,omitempty" op:"rep" type:"float"`
	CombatPointCaptureWins             *StatisticValue `json:"CombatPointCaptureWins,omitempty" op:"add" type:"int"`
	CombatSoloKills                    *StatisticValue `json:"CombatSoloKills,omitempty" op:"add" type:"int"`
	CombatStuns                        *StatisticValue `json:"CombatStuns,omitempty" op:"add" type:"int"`
	CombatTeammateHealing              *StatisticValue `json:"CombatTeammateHealing,omitempty" op:"add" type:"float"`
	CombatWins                         *StatisticValue `json:"CombatWins,omitempty" op:"add" type:"int"`
	CombatLosses                       *StatisticValue `json:"CombatLosses,omitempty" op:"add" type:"int"`
	CombatWinPercentage                *StatisticValue `json:"CombatWinPercentage,omitempty" op:"rep" type:"float"`
	Level                              *StatisticValue `json:"Level,omitempty" op:"add" type:"int"`
	XP                                 *StatisticValue `json:"XP,omitempty" op:"rep" type:"float"`
	GamesPlayed                        *StatisticValue `json:"GamesPlayed,omitempty" op:"add" type:"int"`
	SkillRatingMu                      *StatisticValue `json:"SkillRatingMu,omitempty" op:"rep" type:"float"`
	SkillRatingSigma                   *StatisticValue `json:"SkillRatingSigma,omitempty" op:"rep" type:"float"`
	SkillRatingOrdinal                 *StatisticValue `json:"SkillRatingOrdinal,omitempty" op:"rep" type:"float"`
	RankPercentile                     *StatisticValue `json:"RankPercentile,omitempty" op:"rep" type:"float"`
	LobbyTime                          *StatisticValue `json:"LobbyTime,omitempty" op:"rep" type:"float"`
}

func (s *CombatStatistics) CalculateFields() {
	gamesPlayed := 0
	if s.CombatWins != nil {
		winsValue := s.CombatWins.GetValue()
		// Validate wins value to prevent corrupted statistics
		if winsValue < 0 || winsValue > 1e6 {
			winsValue = 0
		}
		gamesPlayed += int(winsValue)
	}

	if s.CombatLosses != nil {
		lossesValue := s.CombatLosses.GetValue()
		// Validate losses value to prevent corrupted statistics
		if lossesValue < 0 || lossesValue > 1e6 {
			lossesValue = 0
		}
		gamesPlayed += int(lossesValue)
	}

	s.GamesPlayed = &StatisticValue{
		Value: float64(gamesPlayed),
		Count: 1,
	}

	if s.CombatWins != nil && s.GamesPlayed != nil && s.GamesPlayed.Value != 0 {
		// Validate CombatWins value to prevent corrupted statistics
		winsValue := s.CombatWins.GetValue()
		if winsValue < 0 || winsValue > float64(gamesPlayed) || winsValue > 1e6 {
			winsValue = 0
		}
		s.CombatWinPercentage.Value = winsValue / float64(s.GamesPlayed.Value) * 100
	}

	if s.CombatAssists != nil && s.GamesPlayed != nil && s.GamesPlayed.Value != 0 {
		// Validate CombatAssists value to prevent corrupted statistics
		assistsValue := s.CombatAssists.GetValue()
		if assistsValue < 0 || assistsValue > 1e6 {
			assistsValue = 0
		}
		s.CombatAverageEliminationDeathRatio.Value = assistsValue / float64(s.GamesPlayed.Value)
	}

	if s.CombatPayloadWins != nil && s.CombatPayloadGamesPlayed != nil && s.CombatPayloadGamesPlayed.Value != 0 {
		// Validate payload values to prevent corrupted statistics
		payloadWinsValue := s.CombatPayloadWins.GetValue()
		payloadGamesValue := s.CombatPayloadGamesPlayed.GetValue()
		if payloadWinsValue < 0 || payloadWinsValue > payloadGamesValue || payloadWinsValue > 1e6 ||
		   payloadGamesValue < 0 || payloadGamesValue > 1e6 {
			payloadWinsValue = 0
		}
		s.CombatPayloadWinPercentage.Value = payloadWinsValue / payloadGamesValue * 100
	}

	if s.CombatPointCaptureWins != nil && s.CombatPointCaptureGamesPlayed != nil && s.CombatPointCaptureGamesPlayed.Value != 0 {
		// Validate point capture values to prevent corrupted statistics
		captureWinsValue := s.CombatPointCaptureWins.GetValue()
		captureGamesValue := s.CombatPointCaptureGamesPlayed.GetValue()
		if captureWinsValue < 0 || captureWinsValue > captureGamesValue || captureWinsValue > 1e6 ||
		   captureGamesValue < 0 || captureGamesValue > 1e6 {
			captureWinsValue = 0
		}
		s.CombatPointCaptureWinPercentage.Value = captureWinsValue / captureGamesValue * 100
	}

	if s.SkillRatingMu != nil && s.SkillRatingSigma != nil {
		// Validate skill rating values to prevent corrupted statistics
		mu := s.SkillRatingMu.GetValue()
		sigma := s.SkillRatingSigma.GetValue()
		if mu < 0 || mu > 100 || sigma < 0 || sigma > 100 {
			// Use default skill rating values if corrupted
			mu = 25.0
			sigma = 8.333
		}
		
		r := types.Rating{
			Sigma: sigma,
			Mu:    mu,
			Z:     3,
		}

		s.SkillRatingOrdinal = &StatisticValue{
			Value: rating.Ordinal(r),
			Count: 1,
		}
	}

}

type GenericStats struct { // Privates and Social Lobby
	LobbyTime *StatisticValue `json:"LobbyTime,omitempty" op:"add" type:"float"`
}

func (GenericStats) CalculateFields() {}

func statisticMarshalJSON[T int64 | float64 | float32](op string, cnt int64, val T) ([]byte, error) {
	return []byte(fmt.Sprintf("{\"val\":%v,\"op\":\"%s\",\"cnt\":%d}", float32(val), op, cnt)), nil
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
	if t := reflect.TypeOf(s); t != nil {
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
	if t := reflect.TypeOf(s); t != nil {
		if f, ok := t.Elem().FieldByName("op"); ok {
			return f.Tag.Get("op")
		}
	}
	return ""
}
func (s *StatisticValue) ValueType() string {
	if t := reflect.TypeOf(s); t != nil {
		if f, ok := t.Elem().FieldByName("type"); ok {
			return f.Tag.Get("type")
		}
	}
	return ""
}

func (s *StatisticValue) MarshalJSON() ([]byte, error) {
	// get the value of op field tag
	var op, typ string
	if t := reflect.TypeOf(s); t != nil {
		if f, ok := t.Elem().FieldByName("op"); ok {
			op = f.Tag.Get("op")
		}
		if f, ok := t.Elem().FieldByName("type"); ok {
			typ = f.Tag.Get("type")
		}
	}
	switch typ {
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

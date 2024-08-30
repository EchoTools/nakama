package server

import "encoding/json"

type StatisticIntegerAddition struct {
	Value int64
}

func (s StatisticIntegerAddition) MarshalJSON() ([]byte, error) {
	m := map[string]any{
		"cnt": 1,
		"op":  "add",
		"val": s.Value,
	}
	return json.Marshal(m)
}

func (s *StatisticIntegerAddition) UnmarshalJSON(data []byte) error {
	m := map[string]any{}
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	if m["val"] == nil {
		s.Value = 0
	} else {
		s.Value = int64(m["val"].(float64))
	}
	return nil
}

type StatisticFloat64Replacement struct {
	Value float64
}

func (s StatisticFloat64Replacement) MarshalJSON() ([]byte, error) {
	m := map[string]any{
		"cnt": 1,
		"op":  "rep",
		"val": s.Value,
	}
	return json.Marshal(m)
}

func (s *StatisticFloat64Replacement) UnmarshalJSON(data []byte) error {
	m := map[string]any{}
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	if m["val"] == nil {
		s.Value = 0
	} else {
		s.Value = m["val"].(float64)
	}
	return nil
}

type StatisticFloat64Average struct {
	Count int64
	Value float64
}

func (s StatisticFloat64Average) MarshalJSON() ([]byte, error) {
	m := map[string]any{
		"cnt": s.Count,
		"op":  "avg",
		"val": s.Value,
	}
	return json.Marshal(m)
}

func (s *StatisticFloat64Average) UnmarshalJSON(data []byte) error {
	m := map[string]any{}
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	if m["val"] == nil {
		s.Value = 0
	} else {
		s.Value = m["val"].(float64)
	}
	if m["cnt"] == nil {
		s.Count = 1
	} else {
		s.Count = int64(m["cnt"].(float64))
	}
	return nil
}

type StatisticIntegerMaximum struct {
	Value int64
}

func (s StatisticIntegerMaximum) MarshalJSON() ([]byte, error) {
	m := map[string]any{
		"cnt": 1,
		"op":  "max",
		"val": s.Value,
	}
	return json.Marshal(m)
}

func (s *StatisticIntegerMaximum) UnmarshalJSON(data []byte) error {
	m := map[string]any{}
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	if m["val"] == nil {
		s.Value = 0
	} else {
		s.Value = int64(m["val"].(float64))
	}
	return nil
}

type StatisticFloatAddition struct {
	Value float64
}

func (s StatisticFloatAddition) MarshalJSON() ([]byte, error) {
	m := map[string]any{
		"cnt": 1,
		"op":  "add",
		"val": s.Value,
	}
	return json.Marshal(m)
}

func (s *StatisticFloatAddition) UnmarshalJSON(data []byte) error {
	m := map[string]any{}
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	if m["val"] == nil {
		s.Value = 0
	} else {
		s.Value = m["val"].(float64)
	}
	return nil
}

type StatisticFloatReplacement struct {
	Value float64
}

func (s StatisticFloatReplacement) MarshalJSON() ([]byte, error) {
	m := map[string]any{
		"cnt": 1,
		"op":  "rep",
		"val": s.Value,
	}
	return json.Marshal(m)
}

func (s *StatisticFloatReplacement) UnmarshalJSON(data []byte) error {
	m := map[string]any{}
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	if m["val"] == nil {
		s.Value = 0
	} else {
		s.Value = m["val"].(float64)
	}
	return nil
}

type StatisticFloatAverage struct {
	Count int64
	Value float64
}

func (s StatisticFloatAverage) MarshalJSON() ([]byte, error) {
	m := map[string]any{
		"cnt": s.Count,
		"op":  "avg",
		"val": s.Value,
	}
	return json.Marshal(m)
}

func (s *StatisticFloatAverage) UnmarshalJSON(data []byte) error {
	m := map[string]any{}
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	if m["val"] == nil {
		s.Value = 0
	} else {
		s.Value = m["val"].(float64)
	}
	if m["cnt"] == nil {
		s.Count = 1
	} else {
		s.Count = int64(m["cnt"].(float64))
	}
	return nil
}

type StatisticFloatMaximum struct {
	Value float64
}

func (s StatisticFloatMaximum) MarshalJSON() ([]byte, error) {
	m := map[string]any{
		"cnt": 1,
		"op":  "max",
		"val": s.Value,
	}
	return json.Marshal(m)
}
func (s *StatisticFloatMaximum) UnmarshalJSON(data []byte) error {
	m := map[string]any{}
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	if m["val"] == nil {
		s.Value = 0
	} else {
		s.Value = m["val"].(float64)
	}
	return nil
}

type ArenaStats struct {
	ArenaLosses                  StatisticIntegerAddition  `json:"ArenaLosses"`
	ArenaMVPPercentage           StatisticFloatReplacement `json:"ArenaMVPPercentage"`
	ArenaMVPS                    StatisticIntegerAddition  `json:"ArenaMVPs"`
	ArenaTies                    StatisticIntegerAddition  `json:"ArenaTies"`
	ArenaWinPercentage           StatisticFloatReplacement `json:"ArenaWinPercentage"`
	ArenaWins                    StatisticIntegerAddition  `json:"ArenaWins"`
	Assists                      StatisticIntegerAddition  `json:"Assists"`
	AssistsPerGame               StatisticFloatAverage     `json:"AssistsPerGame"`
	AveragePointsPerGame         StatisticFloatAverage     `json:"AveragePointsPerGame"`
	AveragePossessionTimePerGame StatisticFloatAverage     `json:"AveragePossessionTimePerGame"`
	AverageTopSpeedPerGame       StatisticFloatAverage     `json:"AverageTopSpeedPerGame"`
	BlockPercentage              StatisticFloatReplacement `json:"BlockPercentage"`
	Blocks                       StatisticIntegerAddition  `json:"Blocks"`
	BounceGoals                  StatisticIntegerAddition  `json:"BounceGoals"`
	Catches                      StatisticIntegerAddition  `json:"Catches"`
	Clears                       StatisticIntegerAddition  `json:"Clears"`
	CurrentArenaMVPStreak        StatisticIntegerAddition  `json:"CurrentArenaMVPStreak"`
	CurrentArenaWinStreak        StatisticIntegerAddition  `json:"CurrentArenaWinStreak"`
	Goals                        StatisticIntegerAddition  `json:"Goals"`
	GoalSavePercentage           StatisticFloatReplacement `json:"GoalSavePercentage"`
	GoalScorePercentage          StatisticFloatReplacement `json:"GoalScorePercentage"`
	GoalsPerGame                 StatisticFloatAverage     `json:"GoalsPerGame"`
	HatTricks                    StatisticIntegerAddition  `json:"HatTricks"`
	HighestArenaMVPStreak        StatisticIntegerMaximum   `json:"HighestArenaMVPStreak"`
	HighestArenaWinStreak        StatisticIntegerMaximum   `json:"HighestArenaWinStreak"`
	HighestPoints                StatisticIntegerMaximum   `json:"HighestPoints"`
	HighestSaves                 StatisticIntegerMaximum   `json:"HighestSaves"`
	HighestStuns                 StatisticIntegerMaximum   `json:"HighestStuns"`
	Interceptions                StatisticIntegerAddition  `json:"Interceptions"`
	JoustsWon                    StatisticIntegerAddition  `json:"JoustsWon"`
	Level                        StatisticIntegerAddition  `json:"Level"`
	OnePointGoals                StatisticIntegerAddition  `json:"OnePointGoals"`
	Passes                       StatisticIntegerAddition  `json:"Passes"`
	Points                       StatisticIntegerAddition  `json:"Points"`
	PossessionTime               StatisticFloatAddition    `json:"PossessionTime"`
	PunchesReceived              StatisticIntegerAddition  `json:"PunchesReceived"`
	Saves                        StatisticIntegerAddition  `json:"Saves"`
	SavesPerGame                 StatisticFloatAverage     `json:"SavesPerGame"`
	ShotsOnGoal                  StatisticIntegerAddition  `json:"ShotsOnGoal"`
	ShotsOnGoalAgainst           StatisticIntegerAddition  `json:"ShotsOnGoalAgainst"`
	Steals                       StatisticIntegerAddition  `json:"Steals"`
	StunPercentage               StatisticFloatReplacement `json:"StunPercentage"`
	Stuns                        StatisticIntegerAddition  `json:"Stuns"`
	StunsPerGame                 StatisticFloatAverage     `json:"StunsPerGame"`
	ThreePointGoals              StatisticIntegerAddition  `json:"ThreePointGoals"`
	TopSpeedsTotal               StatisticFloatAddition    `json:"TopSpeedsTotal"`
	TwoPointGoals                StatisticIntegerAddition  `json:"TwoPointGoals"`
	XP                           StatisticIntegerAddition  `json:"XP"`
}

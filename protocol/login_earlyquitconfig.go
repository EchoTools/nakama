package evr

import "time"

type EarlyQuitConfig struct {
	SteadyPlayerLevel int32 `json:"steady_player_level,omitempty"`
	NumSteadyMatches  int32 `json:"num_steady_matches,omitempty"`
	PenaltyLevel      int32 `json:"penalty_level,omitempty"`
	PenaltyTimestamp  int64 `json:"penalty_ts,omitempty"`
}

func (c EarlyQuitConfig) PenaltyTimestampAsTime() time.Time {
	return time.Unix(c.PenaltyTimestamp, 0)
}

package server

import "github.com/heroiclabs/nakama-common/runtime"

type PredictedMatch struct {
	Candidate           []runtime.MatchmakerEntry `json:"match"`
	Draw                float64                   `json:"draw"`
	Size                int                       `json:"size"`
	TeamRankPercentileA float64                   `json:"rank_percentile_a"`
	TeamRankPercentileB float64                   `json:"rank_percentile_b"`
	RankPercentileDelta float64                   `json:"rank_percentile_delta"`
	TeamOrdinalA        float64                   `json:"team_ordinal_a"`
	TeamOrdinalB        float64                   `json:"team_ordinal_b"`
	OrdinalDelta        float64                   `json:"ordinal_delta"`
}

package server

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/types"
)

func TestCalculatePlayerRating(t *testing.T) {
	type args struct {
		evrID       evr.EvrId
		players     []PlayerInfo
		playerStats map[evr.EvrId]evr.MatchTypeStats
		blueWins    bool
	}
	tests := []struct {
		name string
		args args
		want map[string]types.Rating
	}{
		{
			name: "Player in blue team, blue wins",
			args: args{
				evrID: evr.EvrId{
					PlatformCode: 4,
					AccountId:    123456789,
				},
				players: []PlayerInfo{

					{
						SessionID:   "1",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 987654321},
						Team:        0,
						RatingMu:    30,
						RatingSigma: 7.5,
						JoinTime:    0.0,
					},
					{
						SessionID:   "2",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 123456789},
						Team:        0,
						RatingMu:    19,
						RatingSigma: 4.333,
						JoinTime:    0.0,
					},
					{
						SessionID:   "3",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 112233445},
						Team:        1,
						RatingMu:    20,
						RatingSigma: 6.666,
						JoinTime:    0.0,
					},
					{
						SessionID:   "4",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 556677889},
						Team:        1,
						RatingMu:    22,
						RatingSigma: 7.0,
						JoinTime:    0.0,
					},
				},
				playerStats: map[evr.EvrId]evr.MatchTypeStats{
					{PlatformCode: 4, AccountId: 987654321}: {Points: 4, Assists: 2},
					{PlatformCode: 4, AccountId: 123456789}: {Points: 2, Assists: 2},
					{PlatformCode: 4, AccountId: 112233445}: {Points: 2, Assists: 0},
					{PlatformCode: 4, AccountId: 556677889}: {Points: 4, Assists: 2},
				},
				blueWins: true,
			},
			want: map[string]types.Rating{
				"1": {Mu: 32.25100494750614, Sigma: 7.403291008004081, Z: 3},
				"2": {Mu: 19.620014923159122, Sigma: 4.325701896576898, Z: 3},
				"3": {Mu: 17.134731637125135, Sigma: 6.501092020874961, Z: 3},
				"4": {Mu: 21.584260659198744, Sigma: 6.787349653038609, Z: 3},
			},
		},
		{
			name: "Player in orange team, orange wins",
			args: args{
				evrID: evr.EvrId{
					PlatformCode: 4,
					AccountId:    123456789,
				},
				players: []PlayerInfo{
					{
						SessionID:   "112233445",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 112233445},
						Team:        0,
						RatingMu:    20,
						RatingSigma: 6.666,
						JoinTime:    0.0,
					},
					{
						SessionID:   "556677889",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 556677889},
						Team:        0,
						RatingMu:    22,
						RatingSigma: 7.0,
						JoinTime:    0.0,
					},
					{
						SessionID:   "987654321",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 987654321},
						Team:        1,
						RatingMu:    30,
						RatingSigma: 7.5,
						JoinTime:    0.0,
					},
					{
						SessionID:   "123456789",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 123456789},
						Team:        1,
						RatingMu:    19,
						RatingSigma: 4.333,
						JoinTime:    0.0,
					},
				},
				playerStats: map[evr.EvrId]evr.MatchTypeStats{
					{PlatformCode: 4, AccountId: 112233445}: {Points: 2, Assists: 0},
					{PlatformCode: 4, AccountId: 556677889}: {Points: 4, Assists: 2},
					{PlatformCode: 4, AccountId: 987654321}: {Points: 4, Assists: 2},
					{PlatformCode: 4, AccountId: 123456789}: {Points: 2, Assists: 2},
				},
				blueWins: false,
			},
			want: map[string]types.Rating{
				"112233445": {Mu: 17.134731637125135, Sigma: 6.501092020874961, Z: 3},
				"123456789": {Mu: 19.620014923159122, Sigma: 4.325701896576898, Z: 3},
				"556677889": {Mu: 21.584260659198744, Sigma: 6.787349653038609, Z: 3},
				"987654321": {Mu: 32.25100494750614, Sigma: 7.403291008004081, Z: 3},
			},
		},
		{
			name: "Player in blue team, orange wins",
			args: args{
				evrID: evr.EvrId{
					PlatformCode: 4,
					AccountId:    123456789,
				},
				players: []PlayerInfo{
					{
						SessionID:   "123456789",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 123456789},
						Team:        0,
						RatingMu:    12,
						RatingSigma: 8.333,
						JoinTime:    0.0,
					},
					{
						SessionID:   "987654321",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 987654321},
						Team:        0,
						RatingMu:    30,
						RatingSigma: 7.5,
						JoinTime:    0.0,
					},
					{
						SessionID:   "112233445",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 112233445},
						Team:        1,
						RatingMu:    20,
						RatingSigma: 6.666,
						JoinTime:    0.0,
					},
					{
						SessionID:   "556677889",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 556677889},
						Team:        1,
						RatingMu:    22,
						RatingSigma: 7.0,
						JoinTime:    0.0,
					},
				},
				playerStats: map[evr.EvrId]evr.MatchTypeStats{
					{PlatformCode: 4, AccountId: 123456789}: {Points: 0, Assists: 0},
					{PlatformCode: 4, AccountId: 987654321}: {Points: 0, Assists: 0},
					{PlatformCode: 4, AccountId: 112233445}: {Points: 0, Assists: 0},
					{PlatformCode: 4, AccountId: 556677889}: {Points: 0, Assists: 0},
				},
				blueWins: false,
			},
			want: map[string]types.Rating{
				"112233445": {Mu: 20.734078648705445, Sigma: 6.638048733401844, Z: 3},
				"123456789": {Mu: 12.430429521320995, Sigma: 8.184594812783692, Z: 3},
				"556677889": {Mu: 22.73008417680748, Sigma: 6.960619786170357, Z: 3},
				"987654321": {Mu: 27.884449958743556, Sigma: 7.365617736624244, Z: 3},
			},
		},
		{
			name: "Player in orange team, blue wins",
			args: args{
				evrID: evr.EvrId{
					PlatformCode: 4,
					AccountId:    112233445,
				},
				players: []PlayerInfo{
					{
						SessionID:   "987654321",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 987654321},
						Team:        0,
						RatingMu:    30,
						RatingSigma: 7.5,
						JoinTime:    0.0,
					},
					{
						SessionID:   "123456789",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 123456789},
						Team:        0,
						RatingMu:    12,
						RatingSigma: 8.333,
						JoinTime:    0.0,
					},
					{
						SessionID:   "112233445",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 112233445},
						Team:        1,
						RatingMu:    20,
						RatingSigma: 6.666,
						JoinTime:    0.0,
					},
					{
						SessionID:   "556677889",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 556677889},
						Team:        1,
						RatingMu:    22,
						RatingSigma: 7.0,
						JoinTime:    0.0,
					},
				},
				playerStats: map[evr.EvrId]evr.MatchTypeStats{
					{PlatformCode: 4, AccountId: 987654321}: {Points: 0, Assists: 0},
					{PlatformCode: 4, AccountId: 123456789}: {Points: 0, Assists: 0},
					{PlatformCode: 4, AccountId: 112233445}: {Points: 0, Assists: 0},
					{PlatformCode: 4, AccountId: 556677889}: {Points: 0, Assists: 0},
				},
				blueWins: true,
			},
			want: map[string]types.Rating{
				"112233445": {Mu: 19.501306348685958, Sigma: 6.58730676305727, Z: 3},
				"123456789": {Mu: 13.483755866429656, Sigma: 8.279910016496327, Z: 3},
				"556677889": {Mu: 21.201229156253063, Sigma: 6.89881982340915, Z: 3},
				"987654321": {Mu: 30.345453845567764, Sigma: 7.4283160798165335, Z: 3},
			},
		},
		{
			name: "Player alone",
			args: args{
				evrID: evr.EvrId{
					PlatformCode: 4,
					AccountId:    123456789,
				},
				players: []PlayerInfo{
					{
						SessionID:   "123456789",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 123456789},
						Team:        0,
						RatingMu:    30,
						RatingSigma: 7.5,
						JoinTime:    0.0,
					},
				},
				playerStats: map[evr.EvrId]evr.MatchTypeStats{
					{PlatformCode: 4, AccountId: 123456789}: {Points: 0, Assists: 0},
				},
				blueWins: true,
			},
			want: map[string]types.Rating{
				"123456789": {Mu: 30, Sigma: 7.505997601918082, Z: 3},
			},
		},
		{
			name: "Player not found",
			args: args{
				evrID: evr.EvrId{
					PlatformCode: 4,
					AccountId:    123456789,
				},
				players: []PlayerInfo{
					{
						SessionID:   "987654321",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 987654321},
						Team:        0,
						RatingMu:    30,
						RatingSigma: 7.5,
						JoinTime:    0.0,
					},
				},
				playerStats: map[evr.EvrId]evr.MatchTypeStats{
					{PlatformCode: 4, AccountId: 987654321}: {Points: 0, Assists: 0},
				},
				blueWins: true,
			},
			want: map[string]types.Rating{
				"987654321": {Mu: 30, Sigma: 7.505997601918082, Z: 3},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CalculateNewPlayerRatings(tt.args.players, tt.args.playerStats, tt.args.blueWins); cmp.Diff(got, tt.want) != "" {
				t.Errorf("calculatePlayerRating() = %s", cmp.Diff(got, tt.want))
			}
		})
	}
}

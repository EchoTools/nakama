package server

import (
	"reflect"
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/types"
)

func TestCalculatePlayerRating(t *testing.T) {
	type args struct {
		evrID    evr.EvrId
		players  []PlayerInfo
		blueWins bool
	}
	tests := []struct {
		name string
		args args
		want types.Rating
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
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 987654321},
						Team:        0,
						RatingMu:    30,
						RatingSigma: 7.5,
						JoinTime:    0.0,
					},
					{
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 123456789},
						Team:        0,
						RatingMu:    19,
						RatingSigma: 4.333,
						JoinTime:    0.0,
					},
					{
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 112233445},
						Team:        1,
						RatingMu:    20,
						RatingSigma: 6.666,
						JoinTime:    0.0,
					},
					{
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 556677889},
						Team:        1,
						RatingMu:    22,
						RatingSigma: 7.0,
						JoinTime:    0.0,
					},
				},
				blueWins: true,
			},
			want: types.Rating{Mu: 19.360315715344896, Sigma: 4.319230265645678},
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
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 112233445},
						Team:        0,
						RatingMu:    20,
						RatingSigma: 6.666,
						JoinTime:    0.0,
					},
					{
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 556677889},
						Team:        0,
						RatingMu:    22,
						RatingSigma: 7.0,
						JoinTime:    0.0,
					},
					{
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 987654321},
						Team:        1,
						RatingMu:    30,
						RatingSigma: 7.5,
						JoinTime:    0.0,
					},
					{
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 123456789},
						Team:        1,
						RatingMu:    19,
						RatingSigma: 4.333,
						JoinTime:    0.0,
					},
				},
				blueWins: false,
			},
			want: types.Rating{Mu: 19.360315715344896, Sigma: 4.319230265645678},
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
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 123456789},
						Team:        0,
						RatingMu:    12,
						RatingSigma: 8.333,
						JoinTime:    0.0,
					},
					{
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 987654321},
						Team:        0,
						RatingMu:    30,
						RatingSigma: 7.5,
						JoinTime:    0.0,
					},
					{
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 112233445},
						Team:        1,
						RatingMu:    20,
						RatingSigma: 6.666,
						JoinTime:    0.0,
					},
					{
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 556677889},
						Team:        1,
						RatingMu:    22,
						RatingSigma: 7.0,
						JoinTime:    0.0,
					},
				},
				blueWins: false,
			},
			want: types.Rating{Mu: 10.494158112090412, Sigma: 8.236456139167874},
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
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 987654321},
						Team:        0,
						RatingMu:    30,
						RatingSigma: 7.5,
						JoinTime:    0.0,
					},
					{
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 123456789},
						Team:        0,
						RatingMu:    12,
						RatingSigma: 8.333,
						JoinTime:    0.0,
					},
					{
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 112233445},
						Team:        1,
						RatingMu:    20,
						RatingSigma: 6.666,
						JoinTime:    0.0,
					},
					{
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 556677889},
						Team:        1,
						RatingMu:    22,
						RatingSigma: 7.0,
						JoinTime:    0.0,
					},
				},
				blueWins: true,
			},
			want: types.Rating{Mu: 19.03637684155125, Sigma: 6.6197969632771665},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, _ := CalculateNewPlayerRating(tt.args.evrID, tt.args.players, 4, tt.args.blueWins); reflect.DeepEqual(got, tt.want) {
				t.Errorf("calculatePlayerRating() = %v, want %v", got, tt.want)
			}
		})
	}
}

package service

import (
	"testing"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/google/go-cmp/cmp"
	"github.com/intinig/go-openskill/types"
)

func TestCalculatePlayerRating(t *testing.T) {
	type args struct {
		evrID    evr.XPID
		players  []PlayerInfo
		blueWins bool
	}
	tests := []struct {
		name string
		args args
		want map[string]types.Rating
	}{
		{
			name: "Player in blue team, blue wins",
			args: args{
				evrID: evr.XPID{
					PlatformCode: 4,
					AccountId:    123456789,
				},
				players: []PlayerInfo{

					{
						SessionID:   "1",
						XPID:        evr.XPID{PlatformCode: 4, AccountId: 987654321},
						Role:        0,
						RatingMu:    30,
						RatingSigma: 7.5,
						RatingScore: 6,
					},
					{
						SessionID:   "2",
						XPID:        evr.XPID{PlatformCode: 4, AccountId: 123456789},
						Role:        0,
						RatingMu:    19,
						RatingSigma: 4.333,
						RatingScore: 4,
					},
					{
						SessionID:   "3",
						XPID:        evr.XPID{PlatformCode: 4, AccountId: 112233445},
						Role:        1,
						RatingMu:    20,
						RatingSigma: 6.666,
						RatingScore: 2,
					},
					{
						SessionID:   "4",
						XPID:        evr.XPID{PlatformCode: 4, AccountId: 556677889},
						Role:        1,
						RatingMu:    22,
						RatingSigma: 7.0,
						RatingScore: 6,
					},
				},
				blueWins: true,
			},
			want: map[string]types.Rating{
				"1": {Mu: 29.51354881824657, Sigma: 7.397377605157138, Z: 3},
				"2": {Mu: 19.075596838556958, Sigma: 4.32566475663472, Z: 3},
				"3": {Mu: 20.14266178646694, Sigma: 6.622734811533342, Z: 3},
				"4": {Mu: 22.069139105906665, Sigma: 6.942133769169642, Z: 3},
			},
		},
		{
			name: "Player in orange team, orange wins",
			args: args{
				evrID: evr.XPID{
					PlatformCode: 4,
					AccountId:    123456789,
				},
				players: []PlayerInfo{
					{
						SessionID:   "112233445",
						XPID:        evr.XPID{PlatformCode: 4, AccountId: 112233445},
						Role:        0,
						RatingMu:    20,
						RatingSigma: 6.666,
					},
					{
						SessionID:   "556677889",
						XPID:        evr.XPID{PlatformCode: 4, AccountId: 556677889},
						Role:        0,
						RatingMu:    22,
						RatingSigma: 7.0,
					},
					{
						SessionID:   "987654321",
						XPID:        evr.XPID{PlatformCode: 4, AccountId: 987654321},
						Role:        1,
						RatingMu:    30,
						RatingSigma: 7.5,
					},
					{
						SessionID:   "123456789",
						XPID:        evr.XPID{PlatformCode: 4, AccountId: 123456789},
						Role:        1,
						RatingMu:    19,
						RatingSigma: 4.333,
					},
				},
				blueWins: false,
			},
			want: map[string]types.Rating{
				"112233445": {Mu: 20.14266178646694, Sigma: 6.622734811533342, Z: 3},
				"123456789": {Mu: 19.075596838556958, Sigma: 4.32566475663472, Z: 3},
				"556677889": {Mu: 22.069139105906665, Sigma: 6.942133769169642, Z: 3},
				"987654321": {Mu: 29.51354881824657, Sigma: 7.397377605157138, Z: 3},
			},
		},
		{
			name: "Player in blue team, orange wins",
			args: args{
				evrID: evr.XPID{
					PlatformCode: 4,
					AccountId:    123456789,
				},
				players: []PlayerInfo{
					{
						SessionID:   "123456789",
						XPID:        evr.XPID{PlatformCode: 4, AccountId: 123456789},
						Role:        0,
						RatingMu:    12,
						RatingSigma: 8.333,
					},
					{
						SessionID:   "987654321",
						XPID:        evr.XPID{PlatformCode: 4, AccountId: 987654321},
						Role:        0,
						RatingMu:    30,
						RatingSigma: 7.5,
					},
					{
						SessionID:   "112233445",
						XPID:        evr.XPID{PlatformCode: 4, AccountId: 112233445},
						Role:        1,
						RatingMu:    20,
						RatingSigma: 6.666,
					},
					{
						SessionID:   "556677889",
						XPID:        evr.XPID{PlatformCode: 4, AccountId: 556677889},
						Role:        1,
						RatingMu:    22,
						RatingSigma: 7.0,
					},
				},
				blueWins: false,
			},
			want: map[string]types.Rating{
				"112233445": {Mu: 20.079170415596188, Sigma: 6.631380089140646, Z: 3},
				"123456789": {Mu: 12.461070326499232, Sigma: 8.274574225729237, Z: 3},
				"556677889": {Mu: 22.00810922884636, Sigma: 6.954278202505372, Z: 3},
				"987654321": {Mu: 29.516973992402264, Sigma: 7.4224151743985205, Z: 3},
			},
		},
		{
			name: "Player in orange team, blue wins",
			args: args{
				evrID: evr.XPID{
					PlatformCode: 4,
					AccountId:    112233445,
				},
				players: []PlayerInfo{
					{
						SessionID:   "987654321",
						XPID:        evr.XPID{PlatformCode: 4, AccountId: 987654321},
						Role:        0,
						RatingMu:    30,
						RatingSigma: 7.5,
					},
					{
						SessionID:   "123456789",
						XPID:        evr.XPID{PlatformCode: 4, AccountId: 123456789},
						Role:        0,
						RatingMu:    12,
						RatingSigma: 8.333,
					},
					{
						SessionID:   "112233445",
						XPID:        evr.XPID{PlatformCode: 4, AccountId: 112233445},
						Role:        1,
						RatingMu:    20,
						RatingSigma: 6.666,
					},
					{
						SessionID:   "556677889",
						XPID:        evr.XPID{PlatformCode: 4, AccountId: 556677889},
						Role:        1,
						RatingMu:    22,
						RatingSigma: 7.0,
					},
				},
				blueWins: true,
			},
			want: map[string]types.Rating{
				"112233445": {Mu: 20.079170415596188, Sigma: 6.631380089140646, Z: 3},
				"123456789": {Mu: 12.461070326499232, Sigma: 8.274574225729237, Z: 3},
				"556677889": {Mu: 22.00810922884636, Sigma: 6.954278202505372, Z: 3},
				"987654321": {Mu: 29.516973992402264, Sigma: 7.4224151743985205, Z: 3},
			},
		},
		{
			name: "Player alone",
			args: args{
				evrID: evr.XPID{
					PlatformCode: 4,
					AccountId:    123456789,
				},
				players: []PlayerInfo{
					{
						SessionID:   "123456789",
						XPID:        evr.XPID{PlatformCode: 4, AccountId: 123456789},
						Role:        0,
						RatingMu:    30,
						RatingSigma: 7.5,
					},
				},
				blueWins: true,
			},
			want: map[string]types.Rating{
				"123456789": {Mu: 30, Sigma: 7.5, Z: 3},
			},
		},
		{
			name: "Player not found",
			args: args{
				evrID: evr.XPID{
					PlatformCode: 4,
					AccountId:    123456789,
				},
				players: []PlayerInfo{
					{
						SessionID:   "987654321",
						XPID:        evr.XPID{PlatformCode: 4, AccountId: 987654321},
						Role:        0,
						RatingMu:    30,
						RatingSigma: 7.5,
					},
				},
				blueWins: true,
			},
			want: map[string]types.Rating{
				"987654321": {Mu: 30, Sigma: 7.5, Z: 3},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CalculateNewPlayerRatings(tt.args.players, tt.args.blueWins); cmp.Diff(got, tt.want) != "" {
				t.Errorf("calculatePlayerRating() = %s", cmp.Diff(got, tt.want))
			}
		})
	}
}

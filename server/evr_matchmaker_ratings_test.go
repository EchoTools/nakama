package server

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/types"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestParseDeviceId(t *testing.T) {
	type args struct {
		token string
	}
	tests := []struct {
		name    string
		args    args
		want    *DeviceAuth
		wantErr bool
	}{
		{
			"valid token",
			args{
				token: "1343218412343402:OVR_ORG-3961234097123078:N/A:127.0.0.1",
			},
			&DeviceAuth{
				AppID: 1343218412343402,
				EvrID: evr.EvrId{
					PlatformCode: 4,
					AccountId:    3961234097123078,
				},
				HMDSerialNumber: "N/A",
				ClientIP:        "127.0.0.1",
			},
			false,
		},
		{
			"valid token, empty headset ID",
			args{
				token: "0:DMO-463990143344164620::104.8.177.198",
			},
			&DeviceAuth{
				AppID: 0,
				EvrID: evr.EvrId{
					PlatformCode: 3,
					AccountId:    463990143344164620,
				},
				HMDSerialNumber: "",
				ClientIP:        "104.8.177.198",
			},
			false,
		},
		{
			"empty string",
			args{
				token: "",
			},
			nil,
			true,
		},
		{
			"empty fields",
			args{
				token: "::",
			},
			nil,
			true,
		},
		{
			"symbols at end",
			args{
				token: "1343218412343402:OVR_ORG-3961234097123078:!@#!$##::@1203:\n!!!",
			},
			&DeviceAuth{
				AppID: 1343218412343402,
				EvrID: evr.EvrId{
					PlatformCode: 4,
					AccountId:    3961234097123078,
				},
				HMDSerialNumber: "!@#!$##::@1203:\n!!!",
			},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDeviceAuthToken(tt.args.token)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseDeviceId() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseDeviceId() = %v, want %v", got, tt.want)
			}
		})
	}
}

type MockEvrPipeline struct {
	EvrPipeline
}

func TestEvrPipeline_authenticateAccount(t *testing.T) {
	type fields struct {
		placeholderEmail string
		linkDeviceUrl    string
	}
	type args struct {
		ctx          context.Context
		session      *sessionWS
		deviceId     *DeviceAuth
		discordId    string
		userPassword string
		payload      evr.LoginProfile
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *api.Account
		errCode codes.Code
	}{
		{
			"discordId auth: userPassword empty",
			fields{
				placeholderEmail: "null.echovrce.com",
				linkDeviceUrl:    "https://echovrce.com/link",
			},
			args{
				ctx:          context.Background(),
				session:      &sessionWS{},
				deviceId:     &DeviceAuth{},
				discordId:    "1234567890",
				userPassword: "",
				payload:      evr.LoginProfile{},
			},
			nil,
			codes.InvalidArgument,
		},
		{
			"discordId auth: userPassword set",
			fields{
				placeholderEmail: "null.echovrce.com",
				linkDeviceUrl:    "https://echovrce.com/link",
			},
			args{
				ctx:          context.Background(),
				session:      &sessionWS{},
				deviceId:     &DeviceAuth{},
				discordId:    "1234567890",
				userPassword: "",
				payload:      evr.LoginProfile{},
			},
			nil,
			codes.InvalidArgument,
		},
	}
	for _, tt := range tests {
		logger = NewConsoleLogger(os.Stdout, true)
		t.Run(tt.name, func(t *testing.T) {
			p := &MockEvrPipeline{}
			got, err := p.authenticateAccount(tt.args.ctx, logger, tt.args.session, tt.args.deviceId, tt.args.discordId, tt.args.userPassword, tt.args.payload)
			if status.Code(err) != tt.errCode {
				t.Errorf("EvrPipeline.authenticateAccount() error = %v, wantErr %v", err, tt.errCode)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("EvrPipeline.authenticateAccount() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_updateProfileStats(t *testing.T) {
	type args struct {
		logger  *zap.Logger
		profile *GameProfileData
		update  evr.UpdatePayload
	}

	jsonData := `{
		"matchtype": -3791849610740453400,
		"sessionid": "37A5F6FF-FA07-4CEE-9D9B-61D3F0663070",
		"update": {
			"stats": {
				"arena": {
					"ArenaLosses": {
						"op": "add",
						"val": 1
					},
					"AveragePossessionTimePerGame": {
						"op": "rep",
						"val": 8.9664583
					},
					"AverageTopSpeedPerGame": {
						"op": "rep",
						"val": 47.514111
					},
					"Catches": {
						"op": "add",
						"val": 1
					},
					"Clears": {
						"op": "add",
						"val": 1
					},
					"HighestStuns": {
						"op": "max",
						"val": 3
					},
					"Level": {
						"op": "add",
						"val": 2
					},
					"Passes": {
						"op": "add",
						"val": 1
					},
					"PossessionTime": {
						"op": "add",
						"val": 8.9664583
					},
					"PunchesReceived": {
						"op": "add",
						"val": 6
					},
					"ShotsOnGoalAgainst": {
						"op": "add",
						"val": 18
					},
					"Stuns": {
						"op": "add",
						"val": 3
					},
					"StunsPerGame": {
						"op": "rep",
						"val": 3
					},
					"TopSpeedsTotal": {
						"op": "add",
						"val": 47.514111
					},
					"XP": {
						"op": "add",
						"val": 500
					}
				},
				"daily_2024_04_12": {
					"ArenaLosses": {
						"op": "add",
						"val": 1
					},
					"AveragePossessionTimePerGame": {
						"op": "rep",
						"val": 8.9664583
					},
					"AverageTopSpeedPerGame": {
						"op": "rep",
						"val": 47.514111
					},
					"Catches": {
						"op": "add",
						"val": 1
					},
					"Clears": {
						"op": "add",
						"val": 1
					},
					"HighestStuns": {
						"op": "max",
						"val": 3
					},
					"Passes": {
						"op": "add",
						"val": 1
					},
					"PossessionTime": {
						"op": "add",
						"val": 8.9664583
					},
					"PunchesReceived": {
						"op": "add",
						"val": 6
					},
					"ShotsOnGoalAgainst": {
						"op": "add",
						"val": 18
					},
					"Stuns": {
						"op": "add",
						"val": 3
					},
					"StunsPerGame": {
						"op": "rep",
						"val": 3
					},
					"TopSpeedsTotal": {
						"op": "add",
						"val": 47.514111
					},
					"XP": {
						"op": "add",
						"val": 1000
					}
				},
				"weekly_2024_04_08": {
					"ArenaLosses": {
						"op": "add",
						"val": 1
					},
					"AveragePossessionTimePerGame": {
						"op": "rep",
						"val": 8.9664583
					},
					"AverageTopSpeedPerGame": {
						"op": "rep",
						"val": 47.514111
					},
					"Catches": {
						"op": "add",
						"val": 1
					},
					"Clears": {
						"op": "add",
						"val": 1
					},
					"HighestStuns": {
						"op": "max",
						"val": 3
					},
					"Passes": {
						"op": "add",
						"val": 1
					},
					"PossessionTime": {
						"op": "add",
						"val": 8.9664583
					},
					"PunchesReceived": {
						"op": "add",
						"val": 6
					},
					"ShotsOnGoalAgainst": {
						"op": "add",
						"val": 18
					},
					"Stuns": {
						"op": "add",
						"val": 3
					},
					"StunsPerGame": {
						"op": "rep",
						"val": 3
					},
					"TopSpeedsTotal": {
						"op": "add",
						"val": 47.514111
					},
					"XP": {
						"op": "add",
						"val": 1000
					}
				}
			}
		}
	}`
	var update evr.UpdatePayload
	err := json.Unmarshal([]byte(jsonData), &update)
	if err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}
	data, err := json.Marshal(update.Update.StatsGroups)
	if err != nil {
		t.Fatalf("Failed to marshal JSON: %v", err)
	}
	profile := evr.ServerProfile{}
	err = json.Unmarshal([]byte(data), &profile.Statistics)
	if err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}
	t.Errorf("%v", profile.Statistics)
	tests := []struct {
		name    string
		args    args
		want    *GameProfileData
		wantErr bool
	}{
		{
			"update data",
			args{
				logger: zap.NewNop(),
				profile: &GameProfileData{
					Server: evr.ServerProfile{},
				},
				update: update,
			},
			&GameProfileData{
				Server: evr.ServerProfile{},
			},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			/*
				got, err := updateProfileStats(tt.args.logger, tt.args.profile, tt.args.update)

				if (err != nil) != tt.wantErr {
					t.Errorf("updateProfileStats() error = %v, wantErr %v", err, tt.wantErr)
					return
				}
				if !reflect.DeepEqual(got, tt.want) {
					t.Errorf("updateProfileStats() = %v, want %v", got, tt.want)
				}
			*/
		})
	}
}
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

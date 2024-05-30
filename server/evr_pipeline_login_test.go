package server

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
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
		want    *DeviceId
		wantErr bool
	}{
		{
			"valid token",
			args{
				token: "1343218412343402:OVR_ORG-3961234097123078:N/A",
			},
			&DeviceId{
				AppId: 1343218412343402,
				EvrId: evr.EvrId{
					PlatformCode: 4,
					AccountId:    3961234097123078,
				},
				HmdSerialNumber: "N/A",
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
			&DeviceId{
				AppId: 1343218412343402,
				EvrId: evr.EvrId{
					PlatformCode: 4,
					AccountId:    3961234097123078,
				},
				HmdSerialNumber: "!@#!$##::@1203:\n!!!",
			},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDeviceId(tt.args.token)
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

var _ = DiscordRegistry(testDiscordRegistry{})

type testDiscordRegistry struct {
	DiscordRegistry
}

func TestEvrPipeline_authenticateAccount(t *testing.T) {
	type fields struct {
		placeholderEmail string
		linkDeviceUrl    string
	}
	type args struct {
		ctx          context.Context
		session      *sessionWS
		deviceId     *DeviceId
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
				deviceId:     &DeviceId{},
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
				deviceId:     &DeviceId{},
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
		update  evr.StatsUpdate
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
	var update evr.StatsUpdate
	err := json.Unmarshal([]byte(jsonData), &update)
	if err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}

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

package server

import (
	"context"
	"reflect"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
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
		t.Run(tt.name, func(t *testing.T) {
			p := &MockEvrPipeline{}
			got, err := p.authenticateAccount(tt.args.ctx, tt.args.session, tt.args.deviceId, tt.args.discordId, tt.args.userPassword, tt.args.payload)
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

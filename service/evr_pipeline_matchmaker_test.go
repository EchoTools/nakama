package service

import (
	"context"
	"reflect"
	"testing"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/gofrs/uuid/v5"
	"github.com/google/go-cmp/cmp"
)

func TestNewSessionParametersFromLobbySessionRequest(t *testing.T) {
	logger := loggerForTest(t)
	matchID := MatchID{UUID: uuid.FromStringOrNil("c252375b-5401-4980-be60-e2fb5996b11f"), Node: "default"}

	request := &evr.LobbyFindSessionRequest{
		CurrentLobbyID:   matchID.UUID,
		VersionLock:      0xc62f01d78f77910d,
		Mode:             evr.ModeSocialPublic,
		Level:            evr.LevelUnspecified,
		Platform:         evr.ToSymbol("OVR"),
		CrossPlayEnabled: true,
		LoginSessionID:   uuid.FromStringOrNil("1251ac1f11bc11ef931a66d3ff8a653b"),
		GroupID:          uuid.FromStringOrNil("1234123411bc11ef931a66d3ff8a653b"),
		SessionSettings: evr.LobbySessionSettings{
			AppID: "1369078409873402",
			Mode:  int64(evr.ModeSocialPublic),
			Level: int64(evr.LevelUnspecified),
			SupportedFeatures: []string{
				"testfeature",
			},
		},
		Entrants: []evr.Entrant{
			{
				EvrID: evr.EvrId{
					PlatformCode: 4,
					AccountId:    3963667097037078,
				},
				Role: int8(evr.TeamUnassigned),
			},
		},
	}

	ctx := context.Background()

	type args struct {
		r *evr.LobbyFindSessionRequest
	}
	tests := []struct {
		name string
		args args
		want LobbySessionParameters
	}{
		{
			name: "Test sets basic values",
			args: args{
				r: request,
			},
			want: LobbySessionParameters{
				VersionLock:       request.VersionLock,
				AppID:             evr.ToSymbol(request.SessionSettings.AppID),
				GroupID:           request.GroupID,
				Mode:              request.Mode,
				Level:             request.Level,
				SupportedFeatures: request.SessionSettings.SupportedFeatures,
				CurrentMatchID:    matchID,
				Role:              evr.TeamUnassigned,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, _ := NewLobbyParametersFromRequest(ctx, logger, nil, &sessionEVR{}, tt.args.r, "node"); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("(- want / + got) %s", cmp.Diff(tt.want, got))
			}
		})
	}
}

package server

import (
	"errors"
	"fmt"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestLobbyErrorIs(t *testing.T) {
	type args struct {
		err  error
		code LobbyErrorCodeValue
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "Test LobbyErrorIs nil",
			args: args{
				err:  nil,
				code: 0,
			},
			want: false,
		},
		{
			name: "Test LobbyError is true",
			args: args{
				err:  NewLobbyError(ServerIsFull, "join attempt failed: not allowed"),
				code: ServerIsFull,
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LobbyErrorIs(tt.args.err, tt.args.code); got != tt.want {
				t.Errorf("LobbyErrorIs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLobbySessionFailureFromError(t *testing.T) {
	groupID := uuid.Must(uuid.NewV4())
	type args struct {
		mode    evr.Symbol
		groupID uuid.UUID
		err     error
	}
	tests := []struct {
		name string
		args args
		want *evr.LobbySessionFailurev4
	}{
		{
			name: "Test nil error",
			args: args{
				mode:    evr.ModeArenaPublic,
				groupID: groupID,
				err:     nil,
			},
			want: nil,
		},
		{
			name: "Test grpc status error",
			args: args{
				mode:    evr.ModeArenaPublic,
				groupID: groupID,
				err:     status.Error(codes.NotFound, "server could not be found"),
			},
			want: evr.NewLobbySessionFailure(evr.ModeArenaPublic, groupID, evr.LobbySessionFailure_ServerDoesNotExist, "server could not be found").Version4(),
		},
		{
			name: "Test LobbyError",
			args: args{
				mode:    evr.ModeArenaPublic,
				groupID: groupID,
				err:     NewLobbyError(ServerIsFull, "too many players"),
			},
			want: evr.NewLobbySessionFailure(evr.ModeArenaPublic, groupID, evr.LobbySessionFailure_ServerIsFull, "too many players").Version4(),
		},
		{
			name: "Test unknown error",
			args: args{
				mode:    evr.ModeArenaPublic,
				groupID: groupID,
				err:     errors.New("unknown error"),
			},
			want: evr.NewLobbySessionFailure(evr.ModeArenaPublic, groupID, evr.LobbySessionFailure_InternalError, "unknown error").Version4(),
		},
		{
			name: "Test wrapped error",
			args: args{
				mode:    evr.ModeArenaPublic,
				groupID: groupID,
				err:     fmt.Errorf("other error: %w", NewLobbyError(ServerFindFailed, "failed to find social lobby")),
			},
			want: evr.NewLobbySessionFailure(evr.ModeArenaPublic, groupID, evr.LobbySessionFailure_ServerFindFailed, "other error: server find failed: failed to find social lobby").Version4(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LobbySessionFailureFromError(tt.args.mode, tt.args.groupID, tt.args.err); got != nil && tt.want != nil {
				if got.ErrorCode != tt.want.ErrorCode || got.Message != tt.want.Message {
					t.Errorf("LobbySessionFailureFromError() = %v, want %v", got, tt.want)
				}
			} else if got != tt.want {
				t.Errorf("LobbySessionFailureFromError() = %v, want %v", got, tt.want)
			}
		})
	}
}
func TestLobbyErrorCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want LobbyErrorCodeValue
	}{
		{
			name: "Test nil error",
			err:  nil,
			want: LobbyUnknownError,
		},
		{
			name: "Test LobbyError",
			err:  NewLobbyError(ServerIsFull, "too many players"),
			want: ServerIsFull,
		},
		{
			name: "Test unknown error",
			err:  errors.New("unknown error"),
			want: LobbyUnknownError,
		},
		{
			name: "Test wrapped error",
			err:  fmt.Errorf("other error: %w", NewLobbyError(ServerFindFailed, "failed to find social lobby")),
			want: ServerFindFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LobbyErrorCode(tt.err); got != tt.want {
				t.Errorf("LobbyErrorCode() = %v, want %v", got, tt.want)
			}
		})
	}
}

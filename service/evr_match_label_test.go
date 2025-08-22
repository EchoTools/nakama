package service

import (
	"testing"
	"time"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/gofrs/uuid/v5"
)

func TestMatchLabel_GetPlayerCount(t *testing.T) {
	type fields struct {
		ID               MatchID
		Open             bool
		LobbyType        LobbyType
		Broadcaster      *GameServerPresence
		StartTime        time.Time
		SpawnedBy        string
		GroupID          *uuid.UUID
		GuildID          string
		GuildName        string
		Mode             evr.Symbol
		Level            evr.Symbol
		SessionSettings  *evr.LobbySessionSettings
		RequiredFeatures []string
		MaxSize          int
		Size             int
		PlayerCount      int
		PlayerLimit      int
		TeamSize         int
		TeamIndex        TeamIndex
		TeamMetadata     []TeamMetadata
		Players          []PlayerInfo
		TeamAlignments   map[string]int
		GameState        *GameState
	}
	tests := []struct {
		name   string
		fields fields
		want   int
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &MatchLabel{
				ID:               tt.fields.ID,
				Open:             tt.fields.Open,
				LobbyType:        tt.fields.LobbyType,
				GameServer:       tt.fields.Broadcaster,
				StartTime:        tt.fields.StartTime,
				SpawnedBy:        tt.fields.SpawnedBy,
				GroupID:          tt.fields.GroupID,
				Mode:             tt.fields.Mode,
				Level:            tt.fields.Level,
				SessionSettings:  tt.fields.SessionSettings,
				RequiredFeatures: tt.fields.RequiredFeatures,
				MaxSize:          tt.fields.MaxSize,
				Size:             tt.fields.Size,
				PlayerCount:      tt.fields.PlayerCount,
				PlayerLimit:      tt.fields.PlayerLimit,
				TeamSize:         tt.fields.TeamSize,
				Players:          tt.fields.Players,
				TeamAlignments:   tt.fields.TeamAlignments,
				GameState:        tt.fields.GameState,
			}
			if got := s.GetPlayerCount(); got != tt.want {
				t.Errorf("MatchLabel.GetPlayerCount() = %v, want %v", got, tt.want)
			}
		})
	}
}
func TestMatchLabel_RoleLimit(t *testing.T) {
	type fields struct {
		LobbyType   LobbyType
		Mode        evr.Symbol
		PlayerLimit int
		MaxSize     int
		TeamSize    int
	}
	type args struct {
		role int
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   int
	}{
		{
			name: "Social TeamSocial",
			fields: fields{
				LobbyType:   PublicLobby,
				Mode:        evr.ModeSocialPublic,
				PlayerLimit: 12,
			},
			args: args{role: evr.TeamSocial},
			want: 10,
		},
		{
			name: "Social TeamModerator",
			fields: fields{
				LobbyType:   PublicLobby,
				Mode:        evr.ModeSocialPublic,
				PlayerLimit: 12,
			},
			args: args{role: evr.TeamModerator},
			want: 10,
		},
		{
			name: "Social Other",
			fields: fields{
				LobbyType:   PublicLobby,
				Mode:        evr.ModeSocialPublic,
				PlayerLimit: 12,
			},
			args: args{role: evr.TeamSpectator},
			want: 0,
		},
		{
			name: "Private Match",
			fields: fields{
				LobbyType:   PrivateLobby,
				Mode:        evr.ModeArenaPrivate,
				PlayerLimit: 16,
			},
			args: args{role: evr.TeamBlue},
			want: 8,
		},
		{
			name: "Public Match Spectator",
			fields: fields{
				LobbyType:   PublicLobby,
				Mode:        evr.ModeArenaPublic,
				PlayerLimit: 8,
				MaxSize:     16,
			},
			args: args{role: evr.TeamSpectator},
			want: 2,
		},
		{
			name: "Public Match Unassigned",
			fields: fields{
				LobbyType:   PublicLobby,
				Mode:        evr.ModeArenaPublic,
				PlayerLimit: 8,
				MaxSize:     16,
			},
			args: args{role: evr.TeamUnassigned},
			want: 2,
		},
		{
			name: "Public Match TeamBlue",
			fields: fields{
				LobbyType:   PublicLobby,
				Mode:        evr.ModeArenaPublic,
				PlayerLimit: 8,
				TeamSize:    4,
			},
			args: args{role: evr.TeamBlue},
			want: 4,
		},
		{
			name: "Public Match TeamOrange",
			fields: fields{
				LobbyType:   PublicLobby,
				Mode:        evr.ModeArenaPublic,
				PlayerLimit: 8,
				TeamSize:    4,
			},
			args: args{role: evr.TeamOrange},
			want: 4,
		},

		{
			name: "Public Match (Combat) TeamOrange",
			fields: fields{
				LobbyType:   PublicLobby,
				Mode:        evr.ModeArenaPublic,
				PlayerLimit: 10,
				TeamSize:    5,
			},
			args: args{role: evr.TeamOrange},
			want: 4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &MatchLabel{
				LobbyType:   tt.fields.LobbyType,
				Mode:        tt.fields.Mode,
				PlayerLimit: tt.fields.PlayerLimit,
				MaxSize:     tt.fields.MaxSize,
				TeamSize:    tt.fields.TeamSize,
			}
			if got := s.roleLimit(tt.args.role); got != tt.want {
				t.Errorf("MatchLabel.RoleLimit() = %v, want %v", got, tt.want)
			}
		})
	}
}

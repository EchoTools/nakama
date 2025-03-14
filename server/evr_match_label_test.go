package server

import (
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

func TestMatchLabel_GetPlayerCount(t *testing.T) {
	type fields struct {
		ID                   MatchID
		Open                 bool
		LobbyType            LobbyType
		Broadcaster          *GameServerPresence
		StartTime            time.Time
		SpawnedBy            string
		GroupID              *uuid.UUID
		GuildID              string
		GuildName            string
		Mode                 evr.Symbol
		Level                evr.Symbol
		SessionSettings      *evr.LobbySessionSettings
		RequiredFeatures     []string
		MaxSize              int
		Size                 int
		PlayerCount          int
		PlayerLimit          int
		TeamSize             int
		TeamIndex            TeamIndex
		TeamMetadata         []TeamMetadata
		Players              []PlayerInfo
		TeamAlignments       map[string]int
		GameState            *GameState
		server               runtime.Presence
		levelLoaded          bool
		presenceMap          map[string]*EvrMatchPresence
		joinTimestamps       map[string]time.Time
		joinTimeMilliseconds map[string]int64
		sessionStartExpiry   int64
		tickRate             int64
		emptyTicks           int64
		terminateTick        int64
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
				ID:                   tt.fields.ID,
				Open:                 tt.fields.Open,
				LobbyType:            tt.fields.LobbyType,
				GameServer:           tt.fields.Broadcaster,
				StartTime:            tt.fields.StartTime,
				SpawnedBy:            tt.fields.SpawnedBy,
				GroupID:              tt.fields.GroupID,
				Mode:                 tt.fields.Mode,
				Level:                tt.fields.Level,
				SessionSettings:      tt.fields.SessionSettings,
				RequiredFeatures:     tt.fields.RequiredFeatures,
				MaxSize:              tt.fields.MaxSize,
				Size:                 tt.fields.Size,
				PlayerCount:          tt.fields.PlayerCount,
				PlayerLimit:          tt.fields.PlayerLimit,
				TeamSize:             tt.fields.TeamSize,
				Players:              tt.fields.Players,
				TeamAlignments:       tt.fields.TeamAlignments,
				GameState:            tt.fields.GameState,
				server:               tt.fields.server,
				levelLoaded:          tt.fields.levelLoaded,
				presenceMap:          tt.fields.presenceMap,
				joinTimestamps:       tt.fields.joinTimestamps,
				joinTimeMilliseconds: tt.fields.joinTimeMilliseconds,
				sessionStartExpiry:   tt.fields.sessionStartExpiry,
				tickRate:             tt.fields.tickRate,
				emptyTicks:           tt.fields.emptyTicks,
				terminateTick:        tt.fields.terminateTick,
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
func TestMatchLabel_CalculateRatingWeights(t *testing.T) {

	type fields struct {
		Players []PlayerInfo
		goals   []*MatchGoal
	}
	tests := []struct {
		name   string
		fields fields
		want   map[evr.EvrId]int
	}{
		{
			name: "Single goal, winning team Blue",
			fields: fields{
				Players: []PlayerInfo{
					{EvrID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 1}, Team: BlueTeam},
					{EvrID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 2}, Team: OrangeTeam},
				},
				goals: []*MatchGoal{
					{TeamID: BlueTeam, XPID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 1}, PointsValue: 10},
				},
			},
			want: map[evr.EvrId]int{
				{PlatformCode: evr.DMO, AccountId: 1}: 14, // 10 + 4 (winning team bonus)
				{PlatformCode: evr.DMO, AccountId: 2}: 0,
			},
		},
		{
			name: "Multiple goals, winning team Orange",
			fields: fields{
				Players: []PlayerInfo{
					{EvrID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 1}, Team: BlueTeam},
					{EvrID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 2}, Team: OrangeTeam},
					{EvrID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 3}, Team: OrangeTeam},
				},
				goals: []*MatchGoal{
					{TeamID: OrangeTeam, XPID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 2}, PointsValue: 5},
					{TeamID: OrangeTeam, XPID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 3}, PointsValue: 10},
					{TeamID: BlueTeam, XPID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 1}, PointsValue: 8},
				},
			},
			want: map[evr.EvrId]int{
				{PlatformCode: evr.DMO, AccountId: 1}: 8,  // 8
				{PlatformCode: evr.DMO, AccountId: 2}: 9,  // 5 + 4 (winning team bonus)
				{PlatformCode: evr.DMO, AccountId: 3}: 14, // 10 + 4 (winning team bonus)
			},
		},
		{
			name: "No goals scored", // This can't technically happen
			fields: fields{
				Players: []PlayerInfo{
					{EvrID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 1}, Team: BlueTeam},
					{EvrID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 2}, Team: OrangeTeam},
				},
				goals: []*MatchGoal{},
			},
			want: map[evr.EvrId]int{
				evr.EvrId{PlatformCode: evr.DMO, AccountId: 1}: 4,
				evr.EvrId{PlatformCode: evr.DMO, AccountId: 2}: 0,
			},
		},
		{
			name: "Goal with previous player XPID",
			fields: fields{
				Players: []PlayerInfo{
					{EvrID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 1}, Team: BlueTeam},
					{EvrID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 2}, Team: BlueTeam},
					{EvrID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 3}, Team: OrangeTeam},
				},
				goals: []*MatchGoal{
					{TeamID: BlueTeam, XPID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 1}, PrevPlayerXPID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 2}, PointsValue: 3},
				},
			},
			want: map[evr.EvrId]int{
				{PlatformCode: evr.DMO, AccountId: 1}: 7, // 3 + 4 (winning team bonus)
				{PlatformCode: evr.DMO, AccountId: 2}: 6, // 2 + 4 (previous player XPID points)
				{PlatformCode: evr.DMO, AccountId: 3}: 0, // 0 (losing team)
			},
		},
		{
			name: "Tie game",
			fields: fields{
				Players: []PlayerInfo{
					{EvrID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 1}, Team: BlueTeam},
					{EvrID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 2}, Team: OrangeTeam},
				},
				goals: []*MatchGoal{
					{TeamID: BlueTeam, XPID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 1}, PointsValue: 10},
					{TeamID: OrangeTeam, XPID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 2}, PointsValue: 10},
				},
			},
			want: map[evr.EvrId]int{
				{PlatformCode: evr.DMO, AccountId: 1}: 14,
				{PlatformCode: evr.DMO, AccountId: 2}: 10,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &MatchLabel{
				Players: tt.fields.Players,
				goals:   tt.fields.goals,
			}
			if got := l.CalculateRatingWeights(); !equalMaps(got, tt.want) {
				t.Errorf("MatchLabel.CalculateRatingWeights() = %v, want %v", got, tt.want)
			}
		})
	}
}

func equalMaps(a, b map[evr.EvrId]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

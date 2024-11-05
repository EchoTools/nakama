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
		Broadcaster          MatchBroadcaster
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
				Broadcaster:          tt.fields.Broadcaster,
				StartTime:            tt.fields.StartTime,
				SpawnedBy:            tt.fields.SpawnedBy,
				GroupID:              tt.fields.GroupID,
				GuildID:              tt.fields.GuildID,
				GuildName:            tt.fields.GuildName,
				Mode:                 tt.fields.Mode,
				Level:                tt.fields.Level,
				SessionSettings:      tt.fields.SessionSettings,
				RequiredFeatures:     tt.fields.RequiredFeatures,
				MaxSize:              tt.fields.MaxSize,
				Size:                 tt.fields.Size,
				PlayerCount:          tt.fields.PlayerCount,
				PlayerLimit:          tt.fields.PlayerLimit,
				TeamSize:             tt.fields.TeamSize,
				TeamIndex:            tt.fields.TeamIndex,
				TeamMetadata:         tt.fields.TeamMetadata,
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

package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/samber/lo"
	"go.uber.org/zap/zapcore"
)

func TestEvrMatch_EvrMatchState(t *testing.T) {
	type args struct {
		data string
	}
	tests := []struct {
		name string
		args args
		want MatchLabel
	}{
		{
			name: "EvrMatchStateUnmarshal",
			args: args{
				data: `{"id":"7aab54ba-90ae-4e7f-abcf-69b30f5e8db7.testnode","open":true,"lobby_type":"public","mode":"social_2.0","level":"mpl_lobby_b2","session_settings":{"appid":"1369078409873402","gametype":301069346851901300},"limit":15,"size":14}`,
			},
			want: MatchLabel{
				ID:       MatchID{UUID: uuid.Must(uuid.FromString("7aab54ba-90ae-4e7f-abcf-69b30f5e8db7")), Node: "testnode"},
				Open:     true,
				LobbyType: PublicLobby,
				Mode:     evr.ToSymbol("social_2.0"),
				Level:    evr.ToSymbol("mpl_lobby_b2"),
				SessionSettings: &evr.LobbySessionSettings{AppID: "1369078409873402", Mode: 301069346851901300},
				MaxSize:  15,
				Size:     14,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := &MatchLabel{}
			err := json.Unmarshal([]byte(tt.args.data), got)
			if err != nil {
				t.Fatalf("error unmarshalling data: %v", err)
			}

			if got.ID != tt.want.ID {
				t.Errorf("ID mismatch: got %v, want %v", got.ID, tt.want.ID)
			}
			if got.Open != tt.want.Open {
				t.Errorf("Open mismatch: got %v, want %v", got.Open, tt.want.Open)
			}
			if got.LobbyType != tt.want.LobbyType {
				t.Errorf("LobbyType mismatch: got %v, want %v", got.LobbyType, tt.want.LobbyType)
			}
			if got.Size != tt.want.Size {
				t.Errorf("Size mismatch: got %v, want %v", got.Size, tt.want.Size)
			}
			if got.MaxSize != tt.want.MaxSize {
				t.Errorf("MaxSize mismatch: got %v, want %v", got.MaxSize, tt.want.MaxSize)
			}
			if got.Mode != tt.want.Mode {
				t.Errorf("Mode mismatch: got %v, want %v", got.Mode, tt.want.Mode)
			}
			if got.Level != tt.want.Level {
				t.Errorf("Level mismatch: got %v, want %v", got.Level, tt.want.Level)
			}
			if tt.want.SessionSettings != nil {
				if got.SessionSettings == nil {
					t.Errorf("SessionSettings is nil, want %v", tt.want.SessionSettings)
				} else if !reflect.DeepEqual(got.SessionSettings, tt.want.SessionSettings) {
					t.Errorf("SessionSettings mismatch: got %v, want %v", got.SessionSettings, tt.want.SessionSettings)
				}
			}
		})
	}
}

func TestSelectTeamForPlayer(t *testing.T) {
	presencesstr := map[string]*EvrMatchPresence{
		"player1": {RoleAlignment: evr.TeamBlue},
		"player2": {RoleAlignment: evr.TeamOrange},
		"spec1":   {RoleAlignment: evr.TeamSpectator},
		"player4": {RoleAlignment: evr.TeamOrange},
		"player5": {RoleAlignment: evr.TeamOrange},
		"player6": {RoleAlignment: evr.TeamOrange},
		"player7": {RoleAlignment: evr.TeamOrange},
		"player8": {RoleAlignment: evr.TeamOrange},
		"spec2":   {RoleAlignment: evr.TeamSpectator},
	}

	presences := make(map[string]*EvrMatchPresence)
	for k, v := range presencesstr {
		u := uuid.NewV5(uuid.Nil, k).String()
		presences[u] = v
	}

	state := &MatchLabel{
		presenceMap: presences,
	}

	tests := []struct {
		name           string
		preferred      int
		lobbyType      LobbyType
		presences      map[string]*EvrMatchPresence
		expectedTeam   int
		expectedResult bool
	}{
		{
			name:           "UnassignedPlayer",
			lobbyType:      PrivateLobby,
			preferred:      evr.TeamUnassigned,
			presences:      map[string]*EvrMatchPresence{},
			expectedTeam:   evr.TeamBlue,
			expectedResult: true,
		},
		{
			name:      "Public match, blue team full, puts the player on orange",
			lobbyType: PublicLobby,
			preferred: evr.TeamBlue,
			presences: map[string]*EvrMatchPresence{
				"player1": {RoleAlignment: evr.TeamBlue},
				"player2": {RoleAlignment: evr.TeamBlue},
				"player3": {RoleAlignment: evr.TeamBlue},
				"player4": {RoleAlignment: evr.TeamBlue},
				"player5": {RoleAlignment: evr.TeamOrange},
				"player6": {RoleAlignment: evr.TeamSpectator},
				"player7": {RoleAlignment: evr.TeamOrange},
				"player8": {RoleAlignment: evr.TeamOrange},
			},
			expectedTeam:   evr.TeamOrange,
			expectedResult: true,
		},
		{
			name:      "Public match, orange team full, puts the player on blue",
			lobbyType: PublicLobby,
			preferred: evr.TeamOrange,
			presences: map[string]*EvrMatchPresence{
				"player1": {RoleAlignment: evr.TeamSpectator},
				"player2": {RoleAlignment: evr.TeamBlue},
				"player3": {RoleAlignment: evr.TeamBlue},
				"player4": {RoleAlignment: evr.TeamBlue},
				"player5": {RoleAlignment: evr.TeamOrange},
				"player6": {RoleAlignment: evr.TeamOrange},
				"player7": {RoleAlignment: evr.TeamOrange},
				"player8": {RoleAlignment: evr.TeamOrange},
			},
			expectedTeam:   evr.TeamBlue,
			expectedResult: true,
		},
		{
			name:      "Public match, teams equal, use preference",
			lobbyType: PublicLobby,
			preferred: evr.TeamOrange,
			presences: map[string]*EvrMatchPresence{
				"player1": {RoleAlignment: evr.TeamSpectator},
				"player2": {RoleAlignment: evr.TeamBlue},
				"player3": {RoleAlignment: evr.TeamBlue},
				"player4": {RoleAlignment: evr.TeamBlue},
				"player5": {RoleAlignment: evr.TeamOrange},
				"player6": {RoleAlignment: evr.TeamOrange},
				"player7": {RoleAlignment: evr.TeamOrange},
				"player8": {RoleAlignment: evr.TeamSpectator},
			},
			expectedTeam:   evr.TeamOrange,
			expectedResult: true,
		},
		{
			name:      "Public match, full reject",
			lobbyType: PublicLobby,
			preferred: evr.TeamBlue,
			presences: map[string]*EvrMatchPresence{
				"player1": {RoleAlignment: evr.TeamBlue},
				"player2": {RoleAlignment: evr.TeamBlue},
				"player3": {RoleAlignment: evr.TeamBlue},
				"player4": {RoleAlignment: evr.TeamBlue},
				"player5": {RoleAlignment: evr.TeamOrange},
				"player6": {RoleAlignment: evr.TeamOrange},
				"player7": {RoleAlignment: evr.TeamOrange},
				"player8": {RoleAlignment: evr.TeamOrange},
			},
			expectedTeam:   evr.TeamUnassigned,
			expectedResult: false,
		},
		{
			name:      "Public match, spectators full, reject",
			lobbyType: PublicLobby,
			preferred: evr.TeamSpectator,
			presences: map[string]*EvrMatchPresence{
				"player1": {RoleAlignment: evr.TeamBlue},
				"player2": {RoleAlignment: evr.TeamBlue},
				"player3": {RoleAlignment: evr.TeamSpectator},
				"player4": {RoleAlignment: evr.TeamSpectator},
				"player5": {RoleAlignment: evr.TeamSpectator},
				"player6": {RoleAlignment: evr.TeamSpectator},
				"player7": {RoleAlignment: evr.TeamSpectator},
				"player8": {RoleAlignment: evr.TeamSpectator},
			},
			expectedTeam:   evr.TeamUnassigned,
			expectedResult: false,
		},
		{
			name:      "Private match, use preference",
			lobbyType: PrivateLobby,
			preferred: evr.TeamOrange,
			presences: map[string]*EvrMatchPresence{
				"player1": {RoleAlignment: evr.TeamBlue},
				"player2": {RoleAlignment: evr.TeamBlue},
				"player3": {RoleAlignment: evr.TeamBlue},
				"player4": {RoleAlignment: evr.TeamBlue},
				"player5": {RoleAlignment: evr.TeamSpectator},
				"player6": {RoleAlignment: evr.TeamSpectator},
				"player7": {RoleAlignment: evr.TeamSpectator},
				"player8": {RoleAlignment: evr.TeamOrange},
			},
			expectedTeam:   evr.TeamOrange,
			expectedResult: true,
		},
		{
			name:      "Private match, use preference (5 player teams)",
			lobbyType: PrivateLobby,
			preferred: evr.TeamOrange,
			presences: map[string]*EvrMatchPresence{
				"player1": {RoleAlignment: evr.TeamSpectator},
				"player2": {RoleAlignment: evr.TeamBlue},
				"player3": {RoleAlignment: evr.TeamBlue},
				"player4": {RoleAlignment: evr.TeamBlue},
				"player5": {RoleAlignment: evr.TeamOrange},
				"player6": {RoleAlignment: evr.TeamOrange},
				"player7": {RoleAlignment: evr.TeamOrange},
				"player8": {RoleAlignment: evr.TeamOrange},
			},
			expectedTeam:   evr.TeamOrange,
			expectedResult: true,
		},
		{
			name:      "Private match, preference full, put on other team",
			lobbyType: PrivateLobby,
			preferred: evr.TeamOrange,
			presences: map[string]*EvrMatchPresence{
				"player1":  {RoleAlignment: evr.TeamSpectator},
				"player2":  {RoleAlignment: evr.TeamBlue},
				"player3":  {RoleAlignment: evr.TeamBlue},
				"player4":  {RoleAlignment: evr.TeamBlue},
				"player5":  {RoleAlignment: evr.TeamBlue},
				"player6":  {RoleAlignment: evr.TeamOrange},
				"player7":  {RoleAlignment: evr.TeamOrange},
				"player8":  {RoleAlignment: evr.TeamOrange},
				"player9":  {RoleAlignment: evr.TeamOrange},
				"player10": {RoleAlignment: evr.TeamOrange},
			},
			expectedTeam:   evr.TeamBlue,
			expectedResult: true,
		},
		{
			name:      "Full private match, puts the player on spectator",
			lobbyType: PrivateLobby,
			preferred: evr.TeamOrange,
			presences: map[string]*EvrMatchPresence{
				"player1":  {RoleAlignment: evr.TeamBlue},
				"player2":  {RoleAlignment: evr.TeamBlue},
				"player3":  {RoleAlignment: evr.TeamBlue},
				"player4":  {RoleAlignment: evr.TeamBlue},
				"player5":  {RoleAlignment: evr.TeamBlue},
				"player6":  {RoleAlignment: evr.TeamOrange},
				"player7":  {RoleAlignment: evr.TeamOrange},
				"player8":  {RoleAlignment: evr.TeamOrange},
				"player9":  {RoleAlignment: evr.TeamOrange},
				"player10": {RoleAlignment: evr.TeamOrange},
			},
			expectedTeam:   evr.TeamSpectator,
			expectedResult: true,
		},
		{
			name:      "Private match, spectators full, reject",
			lobbyType: PrivateLobby,
			preferred: evr.TeamSpectator,
			presences: map[string]*EvrMatchPresence{
				"player1": {RoleAlignment: evr.TeamBlue},
				"player2": {RoleAlignment: evr.TeamBlue},
				"player3": {RoleAlignment: evr.TeamSpectator},
				"player4": {RoleAlignment: evr.TeamSpectator},
				"player5": {RoleAlignment: evr.TeamSpectator},
				"player6": {RoleAlignment: evr.TeamSpectator},
				"player7": {RoleAlignment: evr.TeamSpectator},
				"player8": {RoleAlignment: evr.TeamSpectator},
			},
			expectedTeam:   evr.TeamUnassigned,
			expectedResult: false,
		},
		{
			name:      "full social lobby, reject",
			lobbyType: PublicLobby,
			preferred: evr.TeamSpectator,
			presences: map[string]*EvrMatchPresence{
				"player1":  {RoleAlignment: evr.TeamSocial},
				"player2":  {RoleAlignment: evr.TeamSocial},
				"player3":  {RoleAlignment: evr.TeamSocial},
				"player4":  {RoleAlignment: evr.TeamSocial},
				"player5":  {RoleAlignment: evr.TeamSocial},
				"player6":  {RoleAlignment: evr.TeamSocial},
				"player7":  {RoleAlignment: evr.TeamSocial},
				"player8":  {RoleAlignment: evr.TeamSocial},
				"player9":  {RoleAlignment: evr.TeamSocial},
				"player10": {RoleAlignment: evr.TeamSocial},
				"player11": {RoleAlignment: evr.TeamSocial},
				"player12": {RoleAlignment: evr.TeamSocial},
			},
			expectedTeam:   evr.TeamUnassigned,
			expectedResult: false,
		},
		{
			name:      "social lobby, moderator, allow",
			lobbyType: PublicLobby,
			preferred: evr.TeamModerator,
			presences: map[string]*EvrMatchPresence{
				"player1":  {RoleAlignment: evr.TeamSocial},
				"player2":  {RoleAlignment: evr.TeamSocial},
				"player3":  {RoleAlignment: evr.TeamSocial},
				"player4":  {RoleAlignment: evr.TeamSocial},
				"player5":  {RoleAlignment: evr.TeamSocial},
				"player6":  {RoleAlignment: evr.TeamSocial},
				"player7":  {RoleAlignment: evr.TeamSocial},
				"player8":  {RoleAlignment: evr.TeamSocial},
				"player9":  {RoleAlignment: evr.TeamSocial},
				"player10": {RoleAlignment: evr.TeamSocial},
				"player11": {RoleAlignment: evr.TeamSocial},
			},
			expectedTeam:   evr.TeamModerator,
			expectedResult: true,
		},
	}

	for _, tt := range tests {
		presence := &EvrMatchPresence{
			RoleAlignment: tt.preferred,
		}
		presencestr := make(map[string]*EvrMatchPresence)
		for k, v := range tt.presences {
			u := uuid.NewV5(uuid.Nil, k).String()
			presencestr[u] = v
		}

		state.presenceMap = presencestr
		state.MaxSize = SocialLobbyMaxSize
		state.LobbyType = tt.lobbyType
		if state.LobbyType == PublicLobby {
			state.TeamSize = 4
		} else {
			state.TeamSize = 5
		}

		t.Run(tt.name, func(t *testing.T) {
			_ = presence
			t.Error("selectTeamForPlayer() not implemented")
			/*
				team, result := selectTeamForPlayer(presence, state)

				if team != tt.expectedTeam {
					t.Errorf("selectTeamForPlayer() returned incorrect team, got: %d, want: %d", team, tt.expectedTeam)
				}

				if result != tt.expectedResult {
					t.Errorf("selectTeamForPlayer() returned incorrect result, got: %t, want: %t", result, tt.expectedResult)
				}
			*/

		})
	}
}

func TestSelectTeamForPlayer_With_Alighment(t *testing.T) {
	const (
		DMO_1  = "DMO-1"
		DMO_2  = "DMO-2"
		DMO_3  = "DMO-3"
		DMO_4  = "DMO-4"
		DMO_5  = "DMO-5"
		DMO_6  = "DMO-6"
		DMO_7  = "DMO-7"
		DMO_8  = "DMO-8"
		DMO_9  = "DMO-9"
		DMO_10 = "DMO-10"
		DMO_11 = "DMO-11"
		DMO_12 = "DMO-12"
		DMO_13 = "DMO-13"

		Blue       = evr.TeamBlue
		Orange     = evr.TeamOrange
		Spectator  = evr.TeamSpectator
		Unassigned = evr.TeamUnassigned
	)
	alignments := map[string]int{
		DMO_1:  Blue,
		DMO_2:  Blue,
		DMO_3:  Blue,
		DMO_4:  Blue,
		DMO_5:  Orange,
		DMO_6:  Orange,
		DMO_7:  Orange,
		DMO_8:  Orange,
		DMO_9:  Spectator,
		DMO_10: Spectator,
		DMO_11: Spectator,
		DMO_12: Spectator,
		DMO_13: Spectator,
	}

	tests := []struct {
		name          string
		lobbyType     LobbyType
		players       []string
		newPlayer     string
		preferredTeam int
		expectedTeam  int
		allowed       bool
	}{
		{
			name:      "Follows alignment even when unbalanced",
			lobbyType: PublicLobby,
			players: []string{
				DMO_1,
				DMO_2,
				DMO_3,
			},
			newPlayer:     DMO_4,
			preferredTeam: evr.TeamOrange,
			expectedTeam:  evr.TeamBlue,
			allowed:       true,
		},
	}

	for _, tt := range tests {
		// Existing players
		presences := make(map[string]*EvrMatchPresence)
		for _, player := range tt.players {
			u := uuid.NewV5(uuid.Nil, player).String()
			presences[u] = &EvrMatchPresence{
				RoleAlignment: alignments[player],
			}
		}

		// New Player
		presence := &EvrMatchPresence{
			EvrID:         *lo.Must(evr.ParseEvrId(tt.newPlayer)),
			RoleAlignment: tt.preferredTeam,
		}

		// Match State
		state := &MatchLabel{
			presenceMap: presences,
			MaxSize:     SocialLobbyMaxSize,
			LobbyType:   tt.lobbyType,
			TeamSize: func() int {
				if tt.lobbyType == PublicLobby {
					return 4
				} else {
					return 5
				}
			}(),
		}

		t.Run(tt.name, func(t *testing.T) {
			_, _ = presence, state
			t.Error("selectTeamForPlayer() not implemented")
			/*
				team, result := selectTeamForPlayer(presence, state)

				if team != tt.expectedTeam {
					t.Errorf("selectTeamForPlayer() returned incorrect team, got: %d, want: %d", team, tt.expectedTeam)
				}

				if result != tt.allowed {
					t.Errorf("selectTeamForPlayer() returned incorrect result, got: %t, want: %t", result, tt.allowed)
				}
			*/
		})
	}
}

func TestEvrMatch_MatchLoop(t *testing.T) {
	type args struct {
		tick     int64
		state_   interface{}
		messages []runtime.MatchData
	}

	consoleLogger := NewJSONLogger(os.Stdout, zapcore.ErrorLevel, JSONFormat)
	logger := NewRuntimeGoLogger(consoleLogger)

	tests := []struct {
		name string
		m    *EvrMatch
		args args
		want interface{}
	}{
		{
			name: "Match did not start on time.",
			m:    &EvrMatch{},
			args: args{
				tick: 500,
				state_: func() *MatchLabel {
					s := &MatchLabel{}
					s.server = &Presence{}
					s.tickRate = 10
					s.CreatedAt = time.Now().Add(-20 * time.Minute) // older than 15 minutes
					return s
				}(),

				messages: []runtime.MatchData{},
			},
			want: nil,
		},
		{
			name: "MatchLoop returns state.",
			m:    &EvrMatch{},
			args: args{
				tick: 500,
				state_: func() *MatchLabel {
					s := &MatchLabel{}
					s.server = &Presence{}
					s.tickRate = 10
					s.CreatedAt = time.Now() // recently created, won't hit idle timeout
					return s
				}(),

				messages: []runtime.MatchData{},
			},
			want: "non-nil", // match is within idle timeout, should return state
		},
		{
			name: "MatchLoop increments emptyTicks when no broadcaster",
			m:    &EvrMatch{},
			args: args{
				tick: 30 * 10,
				state_: &MatchLabel{
					emptyTicks: 0,
					tickRate:   10,
					presenceMap: map[string]*EvrMatchPresence{
						uuid.Must(uuid.NewV4()).String(): {},
					},
				},
				messages: []runtime.MatchData{},
			},
			want: "non-nil", // returns state; emptyTicks incremented but not past threshold
		},
		{
			name: "MatchLoop tolerates being without broadcaster for 5 seconds.",
			m:    &EvrMatch{},
			args: args{
				tick: 5 * 10,
				state_: func() *MatchLabel {
					state := &MatchLabel{}
					state.tickRate = 10
					state.presenceMap = map[string]*EvrMatchPresence{
						uuid.Must(uuid.NewV4()).String(): {},
					}

					return state
				}(),
				messages: []runtime.MatchData{},
			},
			want: "non-nil",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &EvrMatch{}
			ctx := context.Background()
			var db *sql.DB
			var nk runtime.NakamaModule
			var dispatcher runtime.MatchDispatcher

			got := m.MatchLoop(ctx, logger, db, nk, dispatcher, tt.args.tick, tt.args.state_, tt.args.messages)
			if tt.want == "non-nil" {
				if got == nil {
					t.Fatalf("expected non-nil state, got nil")
				}
			} else if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("- want / + got = %s", cmp.Diff(tt.want, got))
			}
		})
	}
}

func TestEvrMatch_MatchJoinAttempt(t *testing.T) {
	now := time.Now()
	serverConfig := GameServerPresence{
		SessionID:  uuid.Must(uuid.NewV4()),
		OperatorID: uuid.Must(uuid.NewV4()),
		ServerID:   12341234,
		Endpoint: evr.Endpoint{
			InternalIP: net.ParseIP("1.1.1.1"),
			ExternalIP: net.ParseIP("2.2.2.2"),
			Port:       12345,
		},
		RegionCodes:   []string{},
		VersionLock:   evr.ToSymbol(1234451234),
		GroupIDs:      []uuid.UUID{uuid.Must(uuid.NewV4())},
		Features:      []string{},
		GeoHash:       "asdf",
		Latitude:      1,
		Longitude:     2,
		ASNumber:      1234,
		TimeStepUsecs: 12000,
		NativeSupport: true,
		Tags:          make([]string, 0),
	}
	testStateFn := func() *MatchLabel {

		state := &MatchLabel{
			CreatedAt:        now,
			GameServer:       &serverConfig,
			Open:             false,
			LobbyType:        UnassignedLobby,
			Mode:             evr.ModeUnloaded,
			Level:            evr.LevelUnloaded,
			RequiredFeatures: make([]string, 0),
			Players:          make([]PlayerInfo, 0, SocialLobbyMaxSize),
			presenceMap:      make(map[string]*EvrMatchPresence, SocialLobbyMaxSize),
			reservationMap:   make(map[string]*slotReservation, 2),
			presenceByEvrID:  make(map[evr.EvrId]*EvrMatchPresence, SocialLobbyMaxSize),
			goals:            make([]*evr.MatchGoal, 0),

			TeamAlignments:       make(map[string]int, SocialLobbyMaxSize),
			joinTimestamps:       make(map[string]time.Time, SocialLobbyMaxSize),
			joinTimeMilliseconds: make(map[string]int64, SocialLobbyMaxSize),
			emptyTicks:           0,
			tickRate:             10,
		}
		state.rebuildCache()
		return state
	}

	presences := make([]*EvrMatchPresence, 0)
	for i := 0; i < 10; i++ {
		s := strconv.FormatInt(int64(i), 10)
		presence := &EvrMatchPresence{
			Node:           "testnode",
			SessionID:      uuid.NewV5(uuid.Nil, fmt.Sprintf("session-%d", i)),
			LoginSessionID: uuid.NewV5(uuid.Nil, fmt.Sprintf("login-%d", i)),
			UserID:         uuid.NewV5(uuid.Nil, fmt.Sprintf("user-%d", i)),
			EvrID:          evr.EvrId{PlatformCode: 4, AccountId: uint64(i)},
			DiscordID:      "10000" + s,
			ClientIP:       "127.0.0." + s,
			ClientPort:     "100" + s,
			Username:       "Test username" + s,
			DisplayName:    "Test User" + s,
			PartyID:        uuid.NewV5(uuid.Nil, fmt.Sprintf("party-%d", i)),
			RoleAlignment:  evr.TeamBlue,
			Query:          "testquery",
			SessionExpiry:  1234567890,
			EntrantID:      uuid.NewV5(uuid.Nil, fmt.Sprintf("entrant-%d", i)), // Generate a stable entrant ID for tests
		}
		presences = append(presences, presence)
	}

	type args struct {
		state_   interface{}
		presence runtime.Presence
		metadata map[string]string
	}
	tests := []struct {
		name  string
		m     *EvrMatch
		args  args
		want  interface{}
		want1 bool
		want2 string
	}{
		{
			name: "MatchJoinAttempt rejects join to unassigned lobby.",
			m:    &EvrMatch{},
			args: args{

				state_: func() *MatchLabel {
					state := testStateFn()
					state.Open = true
					state.LobbyType = UnassignedLobby

					return state
				}(),
				presence: presences[0],
				metadata: EntrantMetadata{Presence: presences[0]}.ToMatchMetadata(),
			},
			want: func() *MatchLabel {
				state := testStateFn()
				state.Open = true
				state.LobbyType = UnassignedLobby
				return state
			}(),
			want1: false,
			want2: ErrJoinRejectReasonUnassignedLobby.Error(),
		},
		{
			name: "MatchJoinAttempt allows spectators to join closed matches, that are not terminating.",
			m:    &EvrMatch{},
			args: args{
				state_: func() *MatchLabel {
					state := testStateFn()
					state.StartTime = time.Now()
					state.terminateTick = 0
					state.Open = false
					state.LobbyType = PublicLobby
					state.Mode = evr.ModeArenaPublic
					state.MaxSize = 3
					state.Size = 2
					state.PlayerLimit = 2
					state.PlayerCount = 2
					state.presenceMap = func() map[string]*EvrMatchPresence {
						m := make(map[string]*EvrMatchPresence)
						for _, p := range presences[1:3] {
							m[p.GetSessionId()] = p
						}
						return m
					}()

					state.rebuildCache()
					return state
				}(),
				presence: presences[0],
				metadata: func() map[string]string {
					meta := NewJoinMetadata(presences[0])
					meta.Presence.RoleAlignment = evr.TeamSpectator
					return meta.ToMatchMetadata()
				}(),
			},
			want: func() *MatchLabel {
				state := testStateFn()
				state.StartTime = time.Now()
				state.terminateTick = 0
				state.Open = false
				state.LobbyType = PublicLobby
				state.Mode = evr.ModeArenaPublic
				state.MaxSize = 3
				state.Size = 3
				state.PlayerLimit = 2
				state.PlayerCount = 2
				state.presenceMap = func() map[string]*EvrMatchPresence {
					m := make(map[string]*EvrMatchPresence)
					for _, p := range presences[1:3] {
						m[p.GetSessionId()] = p
					}
					return m
				}()
				state.rebuildCache()
				return state
			}(),
			want1: true,
			want2: presences[0].String(),
		},
		{
			name: "MatchJoinAttempt disallows spectators to join closed matches, that are terminating.",
			m:    &EvrMatch{},
			args: args{
				state_: func() *MatchLabel {
					state := testStateFn()

					state.StartTime = time.Now()
					state.terminateTick = 100
					state.Open = false
					state.LobbyType = PublicLobby
					state.Mode = evr.ModeArenaPublic
					state.MaxSize = 3
					state.Size = 2
					state.PlayerLimit = 2
					state.PlayerCount = 2
					state.presenceMap = func() map[string]*EvrMatchPresence {
						m := make(map[string]*EvrMatchPresence)
						for _, p := range presences[1:3] {
							m[p.GetSessionId()] = p
						}
						return m
					}()
					state.rebuildCache()
					return state
				}(),
				presence: presences[0],
				metadata: func() map[string]string {
					meta := NewJoinMetadata(presences[0])
					meta.Presence.RoleAlignment = evr.TeamSpectator
					return meta.ToMatchMetadata()
				}(),
			},
			want: func() *MatchLabel {
				state := testStateFn()
				state.MaxSize = 3
				state.Size = 2
				state.PlayerLimit = 2
				state.PlayerCount = 2
				state.presenceMap = func() map[string]*EvrMatchPresence {
					m := make(map[string]*EvrMatchPresence)
					for _, p := range presences[1:3] {
						m[p.GetSessionId()] = p
					}
					return m
				}()
				state.presenceMap[presences[0].GetSessionId()] = presences[0]
				state.rebuildCache()
				return state
			}(),
			want1: true,
			want2: presences[0].String(),
		},
		{
			name: "MatchJoinAttempt returns nil if match is full.",
			m:    &EvrMatch{},
			args: args{
				state_: func() *MatchLabel {
					state := &MatchLabel{}
					state.Open = true
					state.LobbyType = PublicLobby
					state.Mode = evr.ModeArenaPublic
					state.MaxSize = 3
					state.Size = 2
					state.PlayerLimit = 2
					state.PlayerCount = 2
					state.presenceMap = func() map[string]*EvrMatchPresence {
						m := make(map[string]*EvrMatchPresence)
						for _, p := range presences[1:3] {
							m[p.GetSessionId()] = p
						}
						return m
					}()
					state.rebuildCache()
					return state
				}(),
				presence: presences[0],
				metadata: NewJoinMetadata(presences[0]).ToMatchMetadata(),
			},
			want: func() *MatchLabel {
				state := &MatchLabel{}
				state.MaxSize = 3
				state.Size = 2
				state.PlayerLimit = 2
				state.PlayerCount = 2
				state.presenceMap = func() map[string]*EvrMatchPresence {
					m := make(map[string]*EvrMatchPresence)
					for _, p := range presences[1:3] {
						m[p.GetSessionId()] = p
					}
					return m
				}()
				state.rebuildCache()
				return state
			}(),
			want1: false,
		},
		{
			name: "MatchJoinAttempt returns nil if match is full.",
			m:    &EvrMatch{},
			args: args{
				state_: func() *MatchLabel {
					state := &MatchLabel{}
					state.LobbyType = PublicLobby
					state.Mode = evr.ModeArenaPublic
					state.MaxSize = 3
					state.Size = 2
					state.PlayerLimit = 2
					state.PlayerCount = 2
					state.presenceMap = func() map[string]*EvrMatchPresence {
						m := make(map[string]*EvrMatchPresence)
						for _, p := range presences[1:3] {
							m[p.GetSessionId()] = p
						}
						return m
					}()
					state.rebuildCache()
					return state
				}(),
				presence: presences[0],
				metadata: NewJoinMetadata(presences[0]).ToMatchMetadata(),
			},
			want: func() *MatchLabel {
				state := &MatchLabel{}
				state.MaxSize = 3
				state.Size = 2
				state.PlayerLimit = 2
				state.PlayerCount = 2
				state.presenceMap = func() map[string]*EvrMatchPresence {
					m := make(map[string]*EvrMatchPresence)
					for _, p := range presences[1:3] {
						m[p.GetSessionId()] = p
					}
					return m
				}()
				state.rebuildCache()
				return state
			}(),
			want1: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			ctx := context.Background()
			logger := func() runtime.Logger {
				logger := NewRuntimeGoLogger(NewJSONLogger(os.Stdout, zapcore.ErrorLevel, JSONFormat))
				return logger
			}()

			m := &EvrMatch{}
			got, got1, got2 := m.MatchJoinAttempt(ctx, logger, nil, nil, nil, 10, tt.args.state_, tt.args.presence, tt.args.metadata)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("EvrMatch.MatchJoinAttempt() (- want/+ got): %s", cmp.Diff(got, tt.want, cmpopts.IgnoreUnexported(MatchLabel{})))
			}
			if got1 != tt.want1 {
				t.Errorf("EvrMatch.MatchJoinAttempt() (- want/+ got): %s", cmp.Diff(got, tt.want1, cmpopts.IgnoreUnexported(MatchLabel{})))
			}
			if got2 != tt.want2 {
				t.Errorf("EvrMatch.MatchJoinAttempt() (- want/+ got): %s", cmp.Diff(got, tt.want2, cmpopts.IgnoreUnexported(MatchLabel{})))
			}
		})
	}
}
func TestEvrMatch_MatchJoinAttempt_Counts(t *testing.T) {
	now := time.Now()
	serverConfig := GameServerPresence{
		SessionID:  uuid.Must(uuid.NewV4()),
		OperatorID: uuid.Must(uuid.NewV4()),
		ServerID:   12341234,
		Endpoint: evr.Endpoint{
			InternalIP: net.ParseIP("1.1.1.1"),
			ExternalIP: net.ParseIP("2.2.2.2"),
			Port:       12345,
		},
		RegionCodes:   []string{},
		VersionLock:   evr.ToSymbol(1234451234),
		GroupIDs:      []uuid.UUID{uuid.Must(uuid.NewV4())},
		Features:      []string{},
		GeoHash:       "asdf",
		Latitude:      1,
		Longitude:     2,
		ASNumber:      1234,
		TimeStepUsecs: 12000,
		NativeSupport: true,
		Tags:          make([]string, 0),
	}
	testStateFn := func() *MatchLabel {

		state := &MatchLabel{
			CreatedAt:        now,
			GameServer:       &serverConfig,
			Open:             false,
			LobbyType:        UnassignedLobby,
			Mode:             evr.ModeUnloaded,
			Level:            evr.LevelUnloaded,
			RequiredFeatures: make([]string, 0),
			Players:          make([]PlayerInfo, 0, SocialLobbyMaxSize),
			presenceMap:      make(map[string]*EvrMatchPresence, SocialLobbyMaxSize),
			reservationMap:   make(map[string]*slotReservation, 2),
			presenceByEvrID:  make(map[evr.EvrId]*EvrMatchPresence, SocialLobbyMaxSize),
			goals:            make([]*evr.MatchGoal, 0),

			TeamAlignments:       make(map[string]int, SocialLobbyMaxSize),
			joinTimestamps:       make(map[string]time.Time, SocialLobbyMaxSize),
			joinTimeMilliseconds: make(map[string]int64, SocialLobbyMaxSize),
			emptyTicks:           0,
			tickRate:             10,
		}
		state.rebuildCache()
		return state
	}

	presences := make([]*EvrMatchPresence, 0)
	for i := 0; i < 10; i++ {
		s := strconv.FormatInt(int64(i), 10)
		presence := &EvrMatchPresence{
			Node:           "testnode",
			SessionID:      uuid.NewV5(uuid.Nil, fmt.Sprintf("session-%d", i)),
			LoginSessionID: uuid.NewV5(uuid.Nil, fmt.Sprintf("login-%d", i)),
			UserID:         uuid.NewV5(uuid.Nil, fmt.Sprintf("user-%d", i)),
			EvrID:          evr.EvrId{PlatformCode: 4, AccountId: uint64(i)},
			DiscordID:      "10000" + s,
			ClientIP:       "127.0.0." + s,
			ClientPort:     "100" + s,
			Username:       "Test username" + s,
			DisplayName:    "Test User" + s,
			PartyID:        uuid.NewV5(uuid.Nil, fmt.Sprintf("party-%d", i)),
			RoleAlignment:  evr.TeamBlue,
			Query:          "testquery",
			SessionExpiry:  1234567890,
		}
		presences = append(presences, presence)
	}

	type args struct {
		state_   interface{}
		presence runtime.Presence
		metadata map[string]string
	}
	tests := []struct {
		name        string
		m           *EvrMatch
		args        args
		wantAllowed bool
		wantReason  string
	}{
		{
			name: "MatchJoinAttempt returns full if both teams are full",
			m:    &EvrMatch{},
			args: args{
				state_: func() *MatchLabel {
					state := testStateFn()
					state.Open = true
					state.LobbyType = PublicLobby
					state.Mode = evr.ModeArenaPublic
					state.MaxSize = 16
					state.PlayerLimit = 8
					state.TeamSize = 4

					state.presenceMap = func() map[string]*EvrMatchPresence {
						m := make(map[string]*EvrMatchPresence)
						for i, p := range presences[1:9] {
							if i >= 4 {
								p.RoleAlignment = evr.TeamOrange
							}
							m[p.GetSessionId()] = p
						}
						return m
					}()
					state.rebuildCache()
					return state
				}(),
				presence: presences[0],
				metadata: func() map[string]string {
					presences[0].RoleAlignment = evr.TeamOrange
					return NewJoinMetadata(presences[0]).ToMatchMetadata()
				}(),
			},
			wantAllowed: false,
			wantReason:  ErrJoinRejectReasonLobbyFull.Error(),
		},
		{
			name: "MatchJoinAttempt returns full if both teams are full, and player has no team set",
			m:    &EvrMatch{},
			args: args{
				state_: func() *MatchLabel {
					state := testStateFn()
					state.Open = true
					state.LobbyType = PublicLobby
					state.Mode = evr.ModeArenaPublic
					state.MaxSize = 16
					state.PlayerLimit = 8
					state.TeamSize = 4

					state.presenceMap = func() map[string]*EvrMatchPresence {
						m := make(map[string]*EvrMatchPresence)
						for i, p := range presences[1:9] {
							if i >= 4 {
								p.RoleAlignment = evr.TeamOrange
							}
							m[p.GetSessionId()] = p
						}
						return m
					}()
					state.rebuildCache()
					return state
				}(),
				presence: presences[0],
				metadata: func() map[string]string {
					presences[0].RoleAlignment = evr.TeamUnassigned
					return NewJoinMetadata(presences[0]).ToMatchMetadata()
				}(),
			},
			wantAllowed: false,
			wantReason:  ErrJoinRejectReasonLobbyFull.Error(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			ctx := context.Background()
			logger := func() runtime.Logger {
				logger := NewRuntimeGoLogger(NewJSONLogger(os.Stdout, zapcore.ErrorLevel, JSONFormat))
				return logger
			}()

			m := &EvrMatch{}
			_, gotAllowed, gotResponse := m.MatchJoinAttempt(ctx, logger, nil, nil, nil, 10, tt.args.state_, tt.args.presence, tt.args.metadata)
			if gotAllowed != tt.wantAllowed {
				t.Errorf("EvrMatch.MatchJoinAttempt() gotAllowed = %v, want %v", gotAllowed, tt.wantAllowed)
			}
			if gotResponse != tt.wantReason {
				t.Errorf("EvrMatch.MatchJoinAttempt() gotResponse = %v, want %v", gotResponse, tt.wantReason)
			}
		})
	}
}

/*
func TestEvrMatch_processJoin(t *testing.T) {
	session1 := uuid.Must(uuid.NewV4())
	session2 := uuid.Must(uuid.NewV4())
	session3 := uuid.Must(uuid.NewV4())
	session4 := uuid.Must(uuid.NewV4())
	session5 := uuid.Must(uuid.NewV4())

	tests := []struct {
		name           string
		state          *MatchLabel
		entrant        *EvrMatchPresence
		expectedResult bool
		expectedError  error
	}{
		{
			name: "Player with reservation joins successfully",
			state: &MatchLabel{
				Mode:        evr.ModeArenaPublic,
				MaxSize:     2,
				TeamSize:    1,
				PlayerLimit: 2,
				reservationMap: map[string]*slotReservation{
					session1.String(): {Presence: &EvrMatchPresence{RoleAlignment: 0, SessionID: session1, EvrID: evr.EvrId{PlatformCode: 1, AccountId: 1}}, Expiry: time.Now().Add(time.Minute)},
				},
				presenceMap: map[string]*EvrMatchPresence{
					session2.String(): {RoleAlignment: 1, SessionID: session2, EvrID: evr.EvrId{PlatformCode: 1, AccountId: 2}},
				},
			},
			entrant:        &EvrMatchPresence{SessionID: session1, RoleAlignment: -1, EvrID: evr.EvrId{PlatformCode: 1, AccountId: 1}},
			expectedResult: true,
			expectedError:  nil,
		},
		{
			name: "Lobby full rejected (due to full slot)",
			state: &MatchLabel{
				Mode:        evr.ModeArenaPublic,
				TeamSize:    2,
				MaxSize:     6,
				PlayerLimit: 4,
				presenceMap: map[string]*EvrMatchPresence{
					session1.String(): {SessionID: session1, RoleAlignment: 0, EvrID: evr.EvrId{PlatformCode: 1, AccountId: 1}},
					session2.String(): {SessionID: session2, RoleAlignment: 0, EvrID: evr.EvrId{PlatformCode: 1, AccountId: 2}},
					session3.String(): {SessionID: session3, RoleAlignment: 1, EvrID: evr.EvrId{PlatformCode: 1, AccountId: 3}},
				},
				reservationMap: map[string]*slotReservation{
					session4.String(): {
						Presence: &EvrMatchPresence{SessionID: session4, RoleAlignment: 1, EvrID: evr.EvrId{PlatformCode: 1, AccountId: 4}},
						Expiry:   time.Now().Add(time.Minute),
					},
				},
			},
			entrant:        &EvrMatchPresence{SessionID: session5, RoleAlignment: 1, EvrID: evr.EvrId{PlatformCode: 1, AccountId: 5}},
			expectedResult: false,
			expectedError:  ErrJoinRejectReasonLobbyFull,
		},
		{
			name: "Duplicate join rejected",
			state: &MatchLabel{
				Mode:        evr.ModeArenaPublic,
				TeamSize:    4,
				MaxSize:     8,
				PlayerLimit: 8,
				presenceMap: map[string]*EvrMatchPresence{
					session1.String(): {SessionID: session1, EvrID: evr.EvrId{PlatformCode: 1, AccountId: 1}},
				},
			},
			entrant:        &EvrMatchPresence{SessionID: session2, EvrID: evr.EvrId{PlatformCode: 1, AccountId: 1}},
			expectedResult: false,
			expectedError:  ErrJoinRejectReasonDuplicateJoin,
		},
		{
			name: "Feature mismatch rejected",
			state: &MatchLabel{
				Mode:             evr.ModeArenaPublic,
				TeamSize:         4,
				MaxSize:          8,
				PlayerLimit:      8,
				RequiredFeatures: []string{"feature1"},
				presenceMap:      map[string]*EvrMatchPresence{},
			},
			entrant:        &EvrMatchPresence{SessionID: session1, SupportedFeatures: []string{"feature2"}},
			expectedResult: false,
			expectedError:  ErrJoinRejectReasonFeatureMismatch,
		},
		{
			name: "Assign role and join successfully",
			state: &MatchLabel{
				MaxSize:     4,
				TeamSize:    2,
				PlayerLimit: 8,
				Mode:        evr.ModeArenaPublic,
				presenceMap: map[string]*EvrMatchPresence{},
			},
			entrant:        &EvrMatchPresence{SessionID: session1, RoleAlignment: evr.TeamUnassigned, EvrID: evr.EvrId{PlatformCode: 1, AccountId: 1}},
			expectedResult: false,
			expectedError:  nil,
		},
		{
			name: "Arena match team full rejected",
			state: &MatchLabel{
				Mode:        evr.ModeArenaPublic,
				MaxSize:     8,
				PlayerLimit: 8,
				TeamSize:    4,
				presenceMap: map[string]*EvrMatchPresence{
					session1.String(): {SessionID: session1, RoleAlignment: evr.TeamOrange, EvrID: evr.EvrId{PlatformCode: 1, AccountId: 1}},
					session2.String(): {SessionID: session2, RoleAlignment: evr.TeamOrange, EvrID: evr.EvrId{PlatformCode: 1, AccountId: 2}},
					session3.String(): {SessionID: session3, RoleAlignment: evr.TeamOrange, EvrID: evr.EvrId{PlatformCode: 1, AccountId: 3}},
					session4.String(): {SessionID: session4, RoleAlignment: evr.TeamOrange, EvrID: evr.EvrId{PlatformCode: 1, AccountId: 4}},
				},
			},
			entrant:        &EvrMatchPresence{SessionID: session5, RoleAlignment: evr.TeamOrange, EvrID: evr.EvrId{PlatformCode: 1, AccountId: 5}},
			expectedResult: false,
			expectedError:  ErrJoinRejectReasonLobbyFull,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.state.rebuildCache()
			m := &EvrMatch{}
			gotResult, gotError := m.processJoin(tt.state, NewRuntimeGoLogger(logger), tt.entrant)
			if gotResult != tt.expectedResult {
				t.Errorf("processJoin() gotResult = %v, want %v", gotResult, tt.expectedResult)
			}
			if gotError != tt.expectedError {
				t.Errorf("processJoin() gotError = %v, want %v", gotError, tt.expectedError)
			}
		})
	}
}

*/

type reconnectTestPresence struct {
	*EvrMatchPresence
	reason runtime.PresenceReason
}

func (p reconnectTestPresence) GetReason() runtime.PresenceReason {
	return p.reason
}

type reconnectTestNakamaModule struct {
	runtime.NakamaModule
	storageWrites []*runtime.StorageWrite
	storageReads  []*runtime.StorageRead
	storageDelete []*runtime.StorageDelete
	streamUsers   []runtime.Presence
}

func (m *reconnectTestNakamaModule) StreamUserList(mode uint8, subject, subcontext, label string, includeHidden, includeNotHidden bool) ([]runtime.Presence, error) {
	return m.streamUsers, nil
}

func (m *reconnectTestNakamaModule) StreamUserLeave(mode uint8, subject, subcontext, label, userID, sessionID string) error {
	return nil
}

func (m *reconnectTestNakamaModule) MetricsCounterAdd(name string, tags map[string]string, delta int64) {
}

func (m *reconnectTestNakamaModule) MetricsTimerRecord(name string, tags map[string]string, value time.Duration) {
}

func (m *reconnectTestNakamaModule) Event(ctx context.Context, event *api.Event) error {
	return nil
}

func (m *reconnectTestNakamaModule) LeaderboardRecordsList(ctx context.Context, leaderboardId string, ownerIds []string, limit int, cursor string, expiry int64) ([]*api.LeaderboardRecord, []*api.LeaderboardRecord, string, string, error) {
	return nil, nil, "", "", runtime.ErrLeaderboardNotFound
}

func (m *reconnectTestNakamaModule) LeaderboardRecordWrite(ctx context.Context, leaderboardId, ownerID, username string, score, subscore int64, metadata map[string]any, operator *int) (*api.LeaderboardRecord, error) {
	return &api.LeaderboardRecord{}, nil
}

func (m *reconnectTestNakamaModule) LeaderboardCreate(ctx context.Context, id string, authoritative bool, sortOrder, operator, resetSchedule string, metadata map[string]any, enableRanks bool) error {
	return nil
}

func (m *reconnectTestNakamaModule) StorageRead(ctx context.Context, keys []*runtime.StorageRead) ([]*api.StorageObject, error) {
	m.storageReads = append(m.storageReads, keys...)
	for _, key := range keys {
		if key.Collection == StorageCollectionEarlyQuit && key.Key == StorageKeyEarlyQuit {
			return []*api.StorageObject{{
				Collection:     StorageCollectionEarlyQuit,
				Key:            StorageKeyEarlyQuit,
				UserId:         key.UserID,
				Value:          `{"matchmaking_tier":1}`,
				Version:        "v1",
				PermissionRead: int32(runtime.STORAGE_PERMISSION_NO_READ),
			}}, nil
		}
	}
	return nil, nil
}

func (m *reconnectTestNakamaModule) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	m.storageWrites = append(m.storageWrites, writes...)
	acks := make([]*api.StorageObjectAck, 0, len(writes))
	for _, write := range writes {
		acks = append(acks, &api.StorageObjectAck{Version: "v1", Collection: write.Collection, Key: write.Key, UserId: write.UserID})
	}
	return acks, nil
}

func (m *reconnectTestNakamaModule) StorageDelete(ctx context.Context, deletes []*runtime.StorageDelete) error {
	m.storageDelete = append(m.storageDelete, deletes...)
	return nil
}

type reconnectTestDispatcher struct {
	runtime.MatchDispatcher
}

func (d *reconnectTestDispatcher) BroadcastMessageDeferred(opCode int64, data []byte, presences []runtime.Presence, sender runtime.Presence, reliable bool) error {
	return nil
}

func (d *reconnectTestDispatcher) MatchLabelUpdate(label string) error {
	return nil
}

func reconnectTestState(mode evr.Symbol) *MatchLabel {
	serverSession := uuid.NewV5(uuid.Nil, "server-session")
	operatorID := uuid.NewV5(uuid.Nil, "server-operator")
	return &MatchLabel{
		ID:          MatchID{UUID: uuid.NewV5(uuid.Nil, "match-reconnect")},
		CreatedAt:   time.Now().UTC(),
		Open:        true,
		LobbyType:   PublicLobby,
		Mode:        mode,
		Level:       evr.LevelArena,
		MaxSize:     16,
		PlayerLimit: 8,
		TeamSize:    4,
		GameServer: &GameServerPresence{
			SessionID:  serverSession,
			OperatorID: operatorID,
			GroupIDs:   []uuid.UUID{uuid.NewV5(uuid.Nil, "group-reconnect")},
		},
		server: &Presence{
			ID: PresenceID{
				Node:      "test-node",
				SessionID: serverSession,
			},
			UserID: operatorID,
		},
		GameState:             &GameState{MatchOver: false},
		Players:               make([]PlayerInfo, 0, 16),
		presenceMap:           make(map[string]*EvrMatchPresence, 16),
		reservationMap:        make(map[string]*slotReservation),
		reconnectReservations: make(map[string]*reconnectReservation),
		presenceByEvrID:       make(map[evr.EvrId]*EvrMatchPresence, 16),
		TeamAlignments:        make(map[string]int, 16),
		joinTimestamps:        make(map[string]time.Time, 16),
		joinTimeMilliseconds:  make(map[string]int64, 16),
		participations:        make(map[string]*PlayerParticipation, 16),
		tickRate:              10,
	}
}

func reconnectTestPlayer(seed string, role int) *EvrMatchPresence {
	return &EvrMatchPresence{
		Node:           "test-node",
		SessionID:      uuid.NewV5(uuid.Nil, "session-"+seed),
		LoginSessionID: uuid.NewV5(uuid.Nil, "login-"+seed),
		UserID:         uuid.NewV5(uuid.Nil, "user-"+seed),
		EvrID:          evr.EvrId{PlatformCode: 4, AccountId: uint64(len(seed) + 100)},
		DiscordID:      "discord-" + seed,
		Username:       "user-" + seed,
		DisplayName:    "User " + seed,
		RoleAlignment:  role,
		EntrantID:      uuid.NewV5(uuid.Nil, "entrant-"+seed),
	}
}

func reconnectTestLogger() runtime.Logger {
	return NewRuntimeGoLogger(NewJSONLogger(os.Stdout, zapcore.ErrorLevel, JSONFormat))
}

func TestReconnectReservation_CreatedOnDisconnect(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "creates reservation with deferred penalty and expiry"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := ServiceSettings()
			if settings == nil {
				settings = &ServiceSettingsData{}
			}
			cloned := *settings
			cloned.Matchmaking.CrashRecoveryWindowSecs = 60
			ServiceSettingsUpdate(&cloned)
			t.Cleanup(func() { ServiceSettingsUpdate(settings) })

			state := reconnectTestState(evr.ModeArenaPublic)
			player := reconnectTestPlayer("reconnect", evr.TeamBlue)
			state.presenceMap[player.GetSessionId()] = player
			state.presenceByEvrID[player.EvrID] = player
			state.joinTimestamps[player.GetSessionId()] = time.Now().Add(-2 * time.Minute)
			state.rebuildCache()

			nk := &reconnectTestNakamaModule{}
			dispatcher := &reconnectTestDispatcher{}
			ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")
			leavePresence := reconnectTestPresence{EvrMatchPresence: player, reason: runtime.PresenceReasonDisconnect}

			m := &EvrMatch{}
			got := m.MatchLeave(ctx, reconnectTestLogger(), nil, nk, dispatcher, 1, state, []runtime.Presence{leavePresence})
			stateAfter := got.(*MatchLabel)

			rr, ok := stateAfter.reconnectReservations[player.GetUserId()]
			if !ok {
				t.Fatalf("expected reconnect reservation for user %s", player.GetUserId())
			}

			if diff := cmp.Diff(player.GetUserId(), rr.UserID); diff != "" {
				t.Fatalf("reservation user id mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(true, rr.DeferPenalty); diff != "" {
				t.Fatalf("reservation defer penalty mismatch (-want +got):\n%s", diff)
			}
			if d := time.Until(rr.Expiry); d < 50*time.Second || d > 70*time.Second {
				t.Fatalf("reservation expiry not within expected window: %s", d)
			}
		})
	}
}

func TestReconnectReservation_NotCreatedOnGracefulLeave(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "does not create reservation when leave still has entrant stream"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := reconnectTestState(evr.ModeSocialPublic)
			player := reconnectTestPlayer("leave", evr.TeamBlue)
			state.presenceMap[player.GetSessionId()] = player
			state.presenceByEvrID[player.EvrID] = player
			state.joinTimestamps[player.GetSessionId()] = time.Now().Add(-time.Minute)
			state.rebuildCache()

			nk := &reconnectTestNakamaModule{
				streamUsers: []runtime.Presence{reconnectTestPresence{EvrMatchPresence: player, reason: runtime.PresenceReasonUnknown}},
			}
			dispatcher := &reconnectTestDispatcher{}
			ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")
			leavePresence := reconnectTestPresence{EvrMatchPresence: player, reason: runtime.PresenceReasonLeave}

			m := &EvrMatch{}
			got := m.MatchLeave(ctx, reconnectTestLogger(), nil, nk, dispatcher, 1, state, []runtime.Presence{leavePresence})
			stateAfter := got.(*MatchLabel)

			if diff := cmp.Diff(0, len(stateAfter.reconnectReservations)); diff != "" {
				t.Fatalf("expected no reconnect reservations (-want +got):\n%s", diff)
			}
		})
	}
}

func TestReconnectReservation_CreatedOnCrashLikeLeave(t *testing.T) {
	t.Run("creates reservation when disconnect has no entrant stream", func(t *testing.T) {
		settings := ServiceSettings()
		if settings == nil {
			settings = &ServiceSettingsData{}
		}
		cloned := *settings
		cloned.Matchmaking.CrashRecoveryWindowSecs = 60
		ServiceSettingsUpdate(&cloned)
		t.Cleanup(func() { ServiceSettingsUpdate(settings) })

		state := reconnectTestState(evr.ModeSocialPublic)
		player := reconnectTestPlayer("leave-disconnected", evr.TeamBlue)
		state.presenceMap[player.GetSessionId()] = player
		state.presenceByEvrID[player.EvrID] = player
		state.joinTimestamps[player.GetSessionId()] = time.Now().Add(-time.Minute)
		state.rebuildCache()

		nk := &reconnectTestNakamaModule{}
		dispatcher := &reconnectTestDispatcher{}
		ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")
		// Use PresenceReasonDisconnect to simulate a crash (not a voluntary leave).
		leavePresence := reconnectTestPresence{EvrMatchPresence: player, reason: runtime.PresenceReasonDisconnect}

		m := &EvrMatch{}
		got := m.MatchLeave(ctx, reconnectTestLogger(), nil, nk, dispatcher, 1, state, []runtime.Presence{leavePresence})
		stateAfter := got.(*MatchLabel)

		rr, ok := stateAfter.reconnectReservations[player.GetUserId()]
		if !ok {
			t.Fatalf("expected reconnect reservation for disconnect without entrant stream")
		}
		if diff := cmp.Diff(player.GetUserId(), rr.UserID); diff != "" {
			t.Fatalf("reservation user id mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("no reservation when voluntary leave has no entrant stream", func(t *testing.T) {
		settings := ServiceSettings()
		if settings == nil {
			settings = &ServiceSettingsData{}
		}
		cloned := *settings
		cloned.Matchmaking.CrashRecoveryWindowSecs = 60
		ServiceSettingsUpdate(&cloned)
		t.Cleanup(func() { ServiceSettingsUpdate(settings) })

		state := reconnectTestState(evr.ModeSocialPublic)
		player := reconnectTestPlayer("leave-voluntary", evr.TeamBlue)
		state.presenceMap[player.GetSessionId()] = player
		state.presenceByEvrID[player.EvrID] = player
		state.joinTimestamps[player.GetSessionId()] = time.Now().Add(-time.Minute)
		state.rebuildCache()

		nk := &reconnectTestNakamaModule{}
		dispatcher := &reconnectTestDispatcher{}
		ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")
		// Use PresenceReasonLeave to simulate a voluntary leave.
		leavePresence := reconnectTestPresence{EvrMatchPresence: player, reason: runtime.PresenceReasonLeave}

		m := &EvrMatch{}
		got := m.MatchLeave(ctx, reconnectTestLogger(), nil, nk, dispatcher, 1, state, []runtime.Presence{leavePresence})
		stateAfter := got.(*MatchLabel)

		if diff := cmp.Diff(0, len(stateAfter.reconnectReservations)); diff != "" {
			t.Fatalf("expected no reconnect reservation for voluntary leave (-want +got):\n%s", diff)
		}
	})
}

func TestReconnectReservation_SlotCountPreserved(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "disconnected player still occupies slot through reservation"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := ServiceSettings()
			if settings == nil {
				settings = &ServiceSettingsData{}
			}
			cloned := *settings
			cloned.Matchmaking.CrashRecoveryWindowSecs = 60
			ServiceSettingsUpdate(&cloned)
			t.Cleanup(func() { ServiceSettingsUpdate(settings) })

			state := reconnectTestState(evr.ModeArenaPublic)
			first := reconnectTestPlayer("slot-one", evr.TeamBlue)
			second := reconnectTestPlayer("slot-two", evr.TeamOrange)
			state.presenceMap[first.GetSessionId()] = first
			state.presenceMap[second.GetSessionId()] = second
			state.presenceByEvrID[first.EvrID] = first
			state.presenceByEvrID[second.EvrID] = second
			state.joinTimestamps[first.GetSessionId()] = time.Now().Add(-2 * time.Minute)
			state.joinTimestamps[second.GetSessionId()] = time.Now().Add(-time.Minute)
			state.rebuildCache()

			nk := &reconnectTestNakamaModule{}
			dispatcher := &reconnectTestDispatcher{}
			ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")
			leavePresence := reconnectTestPresence{EvrMatchPresence: first, reason: runtime.PresenceReasonDisconnect}

			m := &EvrMatch{}
			got := m.MatchLeave(ctx, reconnectTestLogger(), nil, nk, dispatcher, 1, state, []runtime.Presence{leavePresence})
			stateAfter := got.(*MatchLabel)

			if diff := cmp.Diff(2, len(stateAfter.Players)); diff != "" {
				t.Fatalf("expected cached player slots to include reconnect hold (-want +got):\n%s", diff)
			}
		})
	}
}

func TestReconnectReservation_RejoinRestoresRole(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "rejoin restores role alignment from reservation"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := reconnectTestState(evr.ModeArenaPublic)
			state.Open = false

			userID := uuid.NewV5(uuid.Nil, "user-reconnect")
			oldPresence := reconnectTestPlayer("restore-old", evr.TeamOrange)
			oldPresence.UserID = userID
			state.reconnectReservations[userID.String()] = &reconnectReservation{
				Presence:     oldPresence,
				Expiry:       time.Now().Add(30 * time.Second),
				UserID:       userID.String(),
				DeferPenalty: true,
			}

			rejoined := reconnectTestPlayer("restore-new", evr.TeamBlue)
			rejoined.UserID = userID
			rejoined.SessionID = uuid.NewV5(uuid.Nil, "session-reconnect-new")

			nk := &reconnectTestNakamaModule{}
			m := &EvrMatch{}
			metadata := NewJoinMetadata(rejoined).ToMatchMetadata()
			gotState, allowed, reason := m.MatchJoinAttempt(context.Background(), reconnectTestLogger(), nil, nk, nil, 1, state, rejoined, metadata)

			if diff := cmp.Diff(true, allowed); diff != "" {
				t.Fatalf("expected reconnect join to be allowed (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(false, reason == ErrJoinRejectReasonMatchClosed.Error()); diff != "" {
				t.Fatalf("expected reconnect flow to bypass match-closed rejection (-want +got):\n%s", diff)
			}
			restored := &EvrMatchPresence{}
			if err := json.Unmarshal([]byte(reason), restored); err != nil {
				t.Fatalf("failed to parse restored reconnect metadata: %v", err)
			}
			if diff := cmp.Diff(evr.TeamOrange, restored.RoleAlignment); diff != "" {
				t.Fatalf("expected role alignment restored (-want +got):\n%s", diff)
			}

			finalState := gotState.(*MatchLabel)
			if _, ok := finalState.reconnectReservations[userID.String()]; ok {
				t.Fatalf("expected reconnect reservation to be removed after successful reconnect")
			}
		})
	}
}

func TestReconnectReservation_RejoinBypassesClosedMatch(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "reservation bypasses closed match rejection"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := reconnectTestState(evr.ModeSocialPublic)
			state.Open = false

			userID := uuid.NewV5(uuid.Nil, "user-reconnect")
			presence := reconnectTestPlayer("bypass", evr.TeamBlue)
			presence.UserID = userID
			metadata := NewJoinMetadata(presence).ToMatchMetadata()

			m := &EvrMatch{}
			_, allowedWithoutReservation, reasonWithoutReservation := m.MatchJoinAttempt(context.Background(), reconnectTestLogger(), nil, &reconnectTestNakamaModule{}, nil, 1, state, presence, metadata)
			if diff := cmp.Diff(false, allowedWithoutReservation); diff != "" {
				t.Fatalf("expected closed match join rejection without reservation (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(ErrJoinRejectReasonMatchClosed.Error(), reasonWithoutReservation); diff != "" {
				t.Fatalf("unexpected rejection reason without reservation (-want +got):\n%s", diff)
			}

			state.reconnectReservations[userID.String()] = &reconnectReservation{
				Presence:     reconnectTestPlayer("bypass-old", evr.TeamOrange),
				Expiry:       time.Now().Add(30 * time.Second),
				UserID:       userID.String(),
				DeferPenalty: false,
			}

			_, allowedWithReservation, reasonWithReservation := m.MatchJoinAttempt(context.Background(), reconnectTestLogger(), nil, &reconnectTestNakamaModule{}, nil, 1, state, presence, metadata)
			if diff := cmp.Diff(true, allowedWithReservation); diff != "" {
				t.Fatalf("expected reconnect join to bypass closed match (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(false, reasonWithReservation == ErrJoinRejectReasonMatchClosed.Error()); diff != "" {
				t.Fatalf("expected reservation path to bypass match-closed rejection (-want +got):\n%s", diff)
			}
		})
	}
}

func TestReconnectReservation_DuplicateEvrIDSameUserEvictsPhantom(t *testing.T) {
	state := reconnectTestState(evr.ModeArenaPublic)

	userID := uuid.NewV5(uuid.Nil, "user-duplicate-reconnect")
	oldPresence := reconnectTestPlayer("duplicate-old", evr.TeamBlue)
	oldPresence.UserID = userID
	oldPresence.EvrID = evr.EvrId{PlatformCode: 4, AccountId: 424242}
	state.presenceMap[oldPresence.GetSessionId()] = oldPresence
	state.presenceByEvrID[oldPresence.EvrID] = oldPresence
	state.joinTimestamps[oldPresence.GetSessionId()] = time.Now().Add(-30 * time.Second)

	state.reconnectReservations[userID.String()] = &reconnectReservation{
		Presence:     oldPresence,
		Expiry:       time.Now().Add(30 * time.Second),
		UserID:       userID.String(),
		DeferPenalty: true,
	}
	state.rebuildCache()

	rejoined := reconnectTestPlayer("duplicate-new", evr.TeamBlue)
	rejoined.UserID = userID
	rejoined.EvrID = oldPresence.EvrID
	rejoined.SessionID = uuid.NewV5(uuid.Nil, "session-duplicate-new")

	nk := &reconnectTestNakamaModule{}
	m := &EvrMatch{}
	metadata := NewJoinMetadata(rejoined).ToMatchMetadata()

	gotState, allowed, reason := m.MatchJoinAttempt(context.Background(), reconnectTestLogger(), nil, nk, &reconnectTestDispatcher{}, 1, state, rejoined, metadata)
	if !allowed {
		t.Fatalf("expected same-user duplicate EVR-ID to evict phantom and succeed, got rejected: %s", reason)
	}

	stateAfter := gotState.(*MatchLabel)

	// Old phantom session should be evicted
	if _, ok := stateAfter.presenceMap[oldPresence.GetSessionId()]; ok {
		t.Fatalf("expected old phantom session to be evicted from presenceMap")
	}

	// New session should be present
	if _, ok := stateAfter.presenceMap[rejoined.GetSessionId()]; !ok {
		t.Fatalf("expected new session to be in presenceMap")
	}

	// Reconnect reservation should be consumed
	if _, ok := stateAfter.reconnectReservations[userID.String()]; ok {
		t.Fatalf("expected reconnect reservation to be consumed after successful rejoin")
	}
}

func TestReconnectReservation_RejoinCanReclaimOwnReservedSlotWhenLobbyFull(t *testing.T) {
	state := reconnectTestState(evr.ModeArenaPublic)

	blueOne := reconnectTestPlayer("full-blue-one", evr.TeamBlue)
	blueTwo := reconnectTestPlayer("full-blue-two", evr.TeamBlue)
	orangeOne := reconnectTestPlayer("full-orange-one", evr.TeamOrange)
	orangeTwo := reconnectTestPlayer("full-orange-two", evr.TeamOrange)

	for _, p := range []*EvrMatchPresence{blueOne, blueTwo, orangeOne, orangeTwo} {
		state.presenceMap[p.GetSessionId()] = p
		state.presenceByEvrID[p.EvrID] = p
		state.joinTimestamps[p.GetSessionId()] = time.Now().Add(-1 * time.Minute)
	}

	userID := uuid.NewV5(uuid.Nil, "user-reclaim-slot")
	reserved := reconnectTestPlayer("reserved-old", evr.TeamBlue)
	reserved.UserID = userID
	reserved.RoleAlignment = evr.TeamBlue
	state.reconnectReservations[userID.String()] = &reconnectReservation{
		Presence:     reserved,
		Expiry:       time.Now().Add(30 * time.Second),
		UserID:       userID.String(),
		DeferPenalty: true,
	}
	state.rebuildCache()

	rejoined := reconnectTestPlayer("reserved-new", evr.TeamOrange)
	rejoined.UserID = userID
	rejoined.RoleAlignment = evr.TeamUnassigned
	rejoined.EvrID = reserved.EvrID
	rejoined.SessionID = uuid.NewV5(uuid.Nil, "session-reclaim-slot")

	metadata := NewJoinMetadata(rejoined).ToMatchMetadata()
	m := &EvrMatch{}
	nk := &reconnectTestNakamaModule{}

	gotState, allowed, reason := m.MatchJoinAttempt(context.Background(), reconnectTestLogger(), nil, nk, nil, 1, state, rejoined, metadata)
	if !allowed {
		t.Fatalf("expected reconnect to reclaim own reserved slot, got rejection: %s", reason)
	}

	parsed := &EvrMatchPresence{}
	if err := json.Unmarshal([]byte(reason), parsed); err != nil {
		t.Fatalf("failed to parse reconnect metadata: %v", err)
	}
	if diff := cmp.Diff(evr.TeamBlue, parsed.RoleAlignment); diff != "" {
		t.Fatalf("expected reconnect to restore reserved team role (-want +got):\n%s", diff)
	}

	stateAfter := gotState.(*MatchLabel)
	if _, ok := stateAfter.reconnectReservations[userID.String()]; ok {
		t.Fatalf("expected reconnect reservation to be consumed after successful reclaim")
	}
}

func TestReconnectReservation_ExpiryAppliesPenalty(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "expired reservation with deferred penalty is removed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := reconnectTestState(evr.ModeSocialPublic)
			userID := uuid.NewV5(uuid.Nil, "user-expire-penalty")
			state.reconnectReservations[userID.String()] = &reconnectReservation{
				Presence:     reconnectTestPlayer("expire-penalty", evr.TeamBlue),
				Expiry:       time.Now().Add(-time.Second),
				UserID:       userID.String(),
				DeferPenalty: true,
			}

			nk := &reconnectTestNakamaModule{}
			m := &EvrMatch{}
			got := m.MatchLoop(context.Background(), reconnectTestLogger(), nil, nk, nil, 1, state, nil)
			stateAfter := got.(*MatchLabel)

			if diff := cmp.Diff(0, len(stateAfter.reconnectReservations)); diff != "" {
				t.Fatalf("expected expired reconnect reservation to be removed (-want +got):\n%s", diff)
			}
		})
	}
}

func TestReconnectReservation_ExpiryNoPenaltyWhenFalse(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "expired reservation without deferred penalty skips early quit write"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := reconnectTestState(evr.ModeArenaPublic)
			userID := uuid.NewV5(uuid.Nil, "user-expire-nopenalty")
			state.reconnectReservations[userID.String()] = &reconnectReservation{
				Presence:     reconnectTestPlayer("expire-nopenalty", evr.TeamBlue),
				Expiry:       time.Now().Add(-time.Second),
				UserID:       userID.String(),
				DeferPenalty: false,
			}

			nk := &reconnectTestNakamaModule{}
			m := &EvrMatch{}
			got := m.MatchLoop(context.Background(), reconnectTestLogger(), nil, nk, nil, 1, state, nil)
			stateAfter := got.(*MatchLabel)

			if diff := cmp.Diff(0, len(stateAfter.reconnectReservations)); diff != "" {
				t.Fatalf("expected expired reconnect reservation to be removed (-want +got):\n%s", diff)
			}

			earlyQuitWrites := 0
			for _, write := range nk.storageWrites {
				if write.Collection == StorageCollectionEarlyQuit {
					earlyQuitWrites++
				}
			}
			if diff := cmp.Diff(0, earlyQuitWrites); diff != "" {
				t.Fatalf("expected no early quit writes when deferred penalty disabled (-want +got):\n%s", diff)
			}
		})
	}
}

func TestReconnectReservation_DisabledWhenConfigNegative(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "negative crash recovery window disables reservation"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := ServiceSettings()
			if settings == nil {
				settings = &ServiceSettingsData{}
			}
			cloned := *settings
			cloned.Matchmaking.CrashRecoveryWindowSecs = -1
			ServiceSettingsUpdate(&cloned)
			t.Cleanup(func() { ServiceSettingsUpdate(settings) })

			state := reconnectTestState(evr.ModeSocialPublic)
			player := reconnectTestPlayer("disabled", evr.TeamBlue)
			state.presenceMap[player.GetSessionId()] = player
			state.presenceByEvrID[player.EvrID] = player
			state.joinTimestamps[player.GetSessionId()] = time.Now().Add(-2 * time.Minute)
			state.rebuildCache()

			nk := &reconnectTestNakamaModule{}
			dispatcher := &reconnectTestDispatcher{}
			ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")
			leavePresence := reconnectTestPresence{EvrMatchPresence: player, reason: runtime.PresenceReasonDisconnect}

			m := &EvrMatch{}
			got := m.MatchLeave(ctx, reconnectTestLogger(), nil, nk, dispatcher, 1, state, []runtime.Presence{leavePresence})
			stateAfter := got.(*MatchLabel)

			if diff := cmp.Diff(0, len(stateAfter.reconnectReservations)); diff != "" {
				t.Fatalf("expected reconnect reservations to remain empty when crash recovery disabled (-want +got):\n%s", diff)
			}
		})
	}
}

func TestReconnectReservation_NotCreatedForSpectator(t *testing.T) {
	settings := ServiceSettings()
	if settings == nil {
		settings = &ServiceSettingsData{}
	}
	cloned := *settings
	cloned.Matchmaking.CrashRecoveryWindowSecs = 60
	ServiceSettingsUpdate(&cloned)
	t.Cleanup(func() { ServiceSettingsUpdate(settings) })

	state := reconnectTestState(evr.ModeArenaPublic)
	spectator := reconnectTestPlayer("spectator", evr.TeamSpectator)
	state.presenceMap[spectator.GetSessionId()] = spectator
	state.presenceByEvrID[spectator.EvrID] = spectator
	state.joinTimestamps[spectator.GetSessionId()] = time.Now().Add(-2 * time.Minute)
	state.rebuildCache()

	nk := &reconnectTestNakamaModule{}
	dispatcher := &reconnectTestDispatcher{}
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")
	leavePresence := reconnectTestPresence{EvrMatchPresence: spectator, reason: runtime.PresenceReasonDisconnect}

	m := &EvrMatch{}
	got := m.MatchLeave(ctx, reconnectTestLogger(), nil, nk, dispatcher, 1, state, []runtime.Presence{leavePresence})
	stateAfter := got.(*MatchLabel)

	if len(stateAfter.reconnectReservations) != 0 {
		t.Fatalf("expected no reconnect reservation for spectator, got %d", len(stateAfter.reconnectReservations))
	}

	if len(nk.storageWrites) != 0 {
		t.Fatalf("expected no join directive written for spectator, got %d writes", len(nk.storageWrites))
	}
}

// Test: When a player's old session is a phantom (in presenceMap but never fully tracked),
// and the same player tries to join again with a new session, the phantom should be evicted
// and the new join should succeed.
func TestMatchJoinAttempt_PhantomPresenceEvictedOnSameUserDuplicateEvrID(t *testing.T) {
	state := reconnectTestState(evr.ModeArenaPublic)

	// Add a phantom presence — the old session that never completed stream tracking
	phantom := reconnectTestPlayer("phantom", evr.TeamBlue)
	userID := uuid.NewV5(uuid.Nil, "user-phantom-evict")
	phantom.UserID = userID
	phantom.EvrID = evr.EvrId{PlatformCode: 4, AccountId: 777777}
	state.presenceMap[phantom.GetSessionId()] = phantom
	state.presenceByEvrID[phantom.EvrID] = phantom
	state.joinTimestamps[phantom.GetSessionId()] = time.Now().Add(-5 * time.Second)
	state.rebuildCache()

	// New session for the SAME user with the SAME EVR-ID (no reconnect reservation)
	newPresence := reconnectTestPlayer("phantom-new", evr.TeamBlue)
	newPresence.UserID = userID
	newPresence.EvrID = phantom.EvrID
	newPresence.SessionID = uuid.NewV5(uuid.Nil, "session-phantom-new")

	metadata := NewJoinMetadata(newPresence).ToMatchMetadata()
	nk := &reconnectTestNakamaModule{}
	m := &EvrMatch{}

	gotState, allowed, reason := m.MatchJoinAttempt(
		context.Background(), reconnectTestLogger(), nil, nk, &reconnectTestDispatcher{}, 1,
		state, reconnectTestPresence{newPresence, runtime.PresenceReasonJoin}, metadata,
	)
	if !allowed {
		t.Fatalf("expected join to succeed (phantom should be evicted), got rejected: %s", reason)
	}

	stateAfter := gotState.(*MatchLabel)

	// Phantom's old session should be gone
	if _, ok := stateAfter.presenceMap[phantom.GetSessionId()]; ok {
		t.Fatalf("expected phantom session %s to be evicted from presenceMap", phantom.GetSessionId())
	}

	// New session should be present
	if _, ok := stateAfter.presenceMap[newPresence.GetSessionId()]; !ok {
		t.Fatalf("expected new session %s to be in presenceMap", newPresence.GetSessionId())
	}

	// EVR-ID should now point to the new session
	if p, ok := stateAfter.presenceByEvrID[newPresence.EvrID]; !ok || p.GetSessionId() != newPresence.GetSessionId() {
		t.Fatalf("expected presenceByEvrID to point to new session")
	}
}

// Test: A different user with the same EVR-ID should still be rejected.
// This ensures we only evict phantoms for the SAME user, not different users.
func TestMatchJoinAttempt_DifferentUserDuplicateEvrIDStillRejected(t *testing.T) {
	state := reconnectTestState(evr.ModeArenaPublic)

	// Existing player in the match
	existing := reconnectTestPlayer("existing", evr.TeamBlue)
	existing.EvrID = evr.EvrId{PlatformCode: 4, AccountId: 888888}
	state.presenceMap[existing.GetSessionId()] = existing
	state.presenceByEvrID[existing.EvrID] = existing
	state.joinTimestamps[existing.GetSessionId()] = time.Now().Add(-30 * time.Second)
	state.rebuildCache()

	// DIFFERENT user tries to join with the same EVR-ID (should not happen, but must be rejected)
	imposter := reconnectTestPlayer("imposter", evr.TeamOrange)
	imposter.UserID = uuid.NewV5(uuid.Nil, "user-imposter-different")
	imposter.EvrID = existing.EvrID // Same EVR-ID, different user
	imposter.SessionID = uuid.NewV5(uuid.Nil, "session-imposter")

	metadata := NewJoinMetadata(imposter).ToMatchMetadata()
	nk := &reconnectTestNakamaModule{}
	m := &EvrMatch{}

	_, allowed, reason := m.MatchJoinAttempt(
		context.Background(), reconnectTestLogger(), nil, nk, nil, 1,
		state, reconnectTestPresence{imposter, runtime.PresenceReasonJoin}, metadata,
	)
	if allowed {
		t.Fatalf("expected different-user duplicate EVR-ID to be rejected")
	}
	if diff := cmp.Diff(ErrJoinRejectDuplicateEvrID.Error(), reason); diff != "" {
		t.Fatalf("unexpected rejection reason (-want +got):\n%s", diff)
	}

	// Original player should still be in the match
	if _, ok := state.presenceMap[existing.GetSessionId()]; !ok {
		t.Fatalf("expected original player to remain in presenceMap")
	}
}

// Test: Same-user phantom eviction should work correctly with a reconnect reservation.
// The phantom is evicted, the reconnect reservation is consumed, and the join succeeds.
func TestMatchJoinAttempt_ReconnectWithPhantomPresenceSucceeds(t *testing.T) {
	state := reconnectTestState(evr.ModeArenaPublic)

	userID := uuid.NewV5(uuid.Nil, "user-reconnect-phantom")
	phantom := reconnectTestPlayer("reconnect-phantom-old", evr.TeamBlue)
	phantom.UserID = userID
	phantom.EvrID = evr.EvrId{PlatformCode: 4, AccountId: 999999}
	state.presenceMap[phantom.GetSessionId()] = phantom
	state.presenceByEvrID[phantom.EvrID] = phantom
	state.joinTimestamps[phantom.GetSessionId()] = time.Now().Add(-10 * time.Second)

	// Add a reconnect reservation for this user
	state.reconnectReservations[userID.String()] = &reconnectReservation{
		Presence:     phantom,
		Expiry:       time.Now().Add(30 * time.Second),
		UserID:       userID.String(),
		DeferPenalty: true,
	}
	state.rebuildCache()

	// Same user reconnects with a new session
	rejoined := reconnectTestPlayer("reconnect-phantom-new", evr.TeamOrange)
	rejoined.UserID = userID
	rejoined.EvrID = phantom.EvrID
	rejoined.RoleAlignment = evr.TeamUnassigned
	rejoined.SessionID = uuid.NewV5(uuid.Nil, "session-reconnect-phantom-new")

	metadata := NewJoinMetadata(rejoined).ToMatchMetadata()
	nk := &reconnectTestNakamaModule{}
	m := &EvrMatch{}

	gotState, allowed, reason := m.MatchJoinAttempt(
		context.Background(), reconnectTestLogger(), nil, nk, &reconnectTestDispatcher{}, 1,
		state, reconnectTestPresence{rejoined, runtime.PresenceReasonJoin}, metadata,
	)
	if !allowed {
		t.Fatalf("expected reconnect with phantom eviction to succeed, got rejected: %s", reason)
	}

	stateAfter := gotState.(*MatchLabel)

	// Phantom should be evicted
	if _, ok := stateAfter.presenceMap[phantom.GetSessionId()]; ok {
		t.Fatalf("expected phantom session to be evicted")
	}

	// New session should be in the match
	if _, ok := stateAfter.presenceMap[rejoined.GetSessionId()]; !ok {
		t.Fatalf("expected new session to be in presenceMap")
	}

	// Reconnect reservation should be consumed
	if _, ok := stateAfter.reconnectReservations[userID.String()]; ok {
		t.Fatalf("expected reconnect reservation to be consumed")
	}

	// Team alignment should be restored from the reconnect reservation (TeamBlue, not TeamOrange)
	p := stateAfter.presenceMap[rejoined.GetSessionId()]
	if p.RoleAlignment != evr.TeamBlue {
		t.Fatalf("expected role alignment to be restored to TeamBlue from reconnect reservation, got %d", p.RoleAlignment)
	}
}

// TestArenaLocking_LocksOnPostMatch verifies that a public arena match locks
// when a GameStatusPostMatch update is received.
func TestArenaLocking_LocksOnPostMatch(t *testing.T) {
	state := &MatchLabel{
		Mode: evr.ModeArenaPublic,
		Open: true,
	}
	update := MatchGameStateUpdate{
		GameStatus: GameStatusPostMatch,
	}

	isPublicMatch := state.Mode == evr.ModeArenaPublic || state.Mode == evr.ModeCombatPublic
	if isPublicMatch && update.GameStatus == GameStatusPostMatch && state.Open {
		state.Open = false
		if state.LockedAt == nil {
			now := time.Now().UTC()
			state.LockedAt = &now
		}
	}

	if state.Open {
		t.Fatal("expected match to be closed after post-match update")
	}
	if state.LockedAt == nil {
		t.Fatal("expected LockedAt to be set after post-match update")
	}
}

// TestArenaLocking_DoesNotLockOnPlaying verifies that a GameStatusPlaying
// update does not lock a public arena match.
func TestArenaLocking_DoesNotLockOnPlaying(t *testing.T) {
	state := &MatchLabel{
		Mode: evr.ModeArenaPublic,
		Open: true,
	}
	update := MatchGameStateUpdate{
		GameStatus: GameStatusPlaying,
	}

	isPublicMatch := state.Mode == evr.ModeArenaPublic || state.Mode == evr.ModeCombatPublic
	if isPublicMatch && update.GameStatus == GameStatusPostMatch && state.Open {
		state.Open = false
		if state.LockedAt == nil {
			now := time.Now().UTC()
			state.LockedAt = &now
		}
	}

	if !state.Open {
		t.Fatal("expected match to remain open after playing update")
	}
	if state.LockedAt != nil {
		t.Fatal("expected LockedAt to remain nil after playing update")
	}
}

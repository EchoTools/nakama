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

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/gofrs/uuid/v5"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/heroiclabs/nakama-common/runtime"
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
		want string
	}{
		{
			name: "EvrMatchStateUnmarshal",
			args: args{
				data: `{"id":"7aab54ba-90ae-4e7f-abcf-69b30f5e8db7","open":true,"lobby_type":"public","endpoint":"","version_lock":14280634968751706381,"platform":"OVR","channel":"e8dd7736-32af-41f5-91e0-db591c6e8cfd","match_channels":["e8dd7736-32af-41f5-91e0-db591c6e8cfd","c016925b-3368-401c-8620-0c4ccd7e5c2e","f52129fb-d5c6-4c47-b644-f19981a933ee"],"mode":"social_2.0","level":"mpl_lobby_b2","session_settings":{"appid":"1369078409873402","gametype":301069346851901300},"max_size":15,"size":14,"max_team_size":15,"Presences":null}`,
			},
			want: `{"id":"7aab54ba-90ae-4e7f-abcf-69b30f5e8db7","open":true,"lobby_type":"public","endpoint":"","version_lock":14280634968751706381,"platform":"OVR","channel":"e8dd7736-32af-41f5-91e0-db591c6e8cfd","match_channels":["e8dd7736-32af-41f5-91e0-db591c6e8cfd","c016925b-3368-401c-8620-0c4ccd7e5c2e","f52129fb-d5c6-4c47-b644-f19981a933ee"],"mode":"social_2.0","level":"mpl_lobby_b2","session_settings":{"appid":"1369078409873402","gametype":301069346851901300},"max_size":15,"size":14,"max_team_size":15,"Presences":null}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// TODO protobuf's would be nice here.
			got := &MatchLabel{}
			err := json.Unmarshal([]byte(tt.args.data), got)
			if err != nil {
				t.Fatalf("error unmarshalling data: %v", err)
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("EvrMatch.MatchSignal() got = %s\n\nwant = %s", got.String(), tt.want)
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

	var state *MatchLabel

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
				tick: 15 * 60 * 10 * 2,
				state_: func() *MatchLabel {
					state := &MatchLabel{}
					state.sessionStartExpiry = 10 * 10
					state.server = &Presence{}
					return state
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
					state := &MatchLabel{}
					state.sessionStartExpiry = 10 * 10
					state.server = &Presence{}

					return state
				}(),

				messages: []runtime.MatchData{},
			},
			want: func() *MatchLabel {
				return state
			}(),
		},
		{
			name: "MatchLoop exits if empty for more than 20 seconds",
			m:    &EvrMatch{},
			args: args{
				tick: 0,
				state_: &MatchLabel{
					StartTime:  time.Now().Add(-30 * time.Minute),
					server:     &Presence{},
					emptyTicks: 10 * 30,
					tickRate:   10,
				},
				messages: []runtime.MatchData{},
			},
			want: nil,
		},
		{
			name: "MatchLoop exits if no broadcaster after 15 seconds.",
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
			want: nil,
		},
		{
			name: "MatchLoop tolerates being without broadcaster for 5 seconds.",
			m:    &EvrMatch{},
			args: args{
				tick: 5 * 10,
				state_: func() *MatchLabel {
					state := &MatchLabel{}
					state.sessionStartExpiry = 10 * 10
					state.presenceMap = map[string]*EvrMatchPresence{
						uuid.Must(uuid.NewV4()).String(): {},
					}

					return state
				}(),
				messages: []runtime.MatchData{},
			},
			want: func() *MatchLabel {
				return state
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &EvrMatch{}
			ctx := context.Background()
			var db *sql.DB
			var nk runtime.NakamaModule
			var dispatcher runtime.MatchDispatcher

			if got := m.MatchLoop(ctx, logger, db, nk, dispatcher, tt.args.tick, tt.args.state_, tt.args.messages); !reflect.DeepEqual(got, tt.want) {
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
			RankPercentile:       0.0,
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
							m[p.EntrantID(state.ID).String()] = p
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
						m[p.EntrantID(state.ID).String()] = p
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
							m[p.EntrantID(state.ID).String()] = p
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
						m[p.EntrantID(state.ID).String()] = p
					}
					return m
				}()
				state.presenceMap[presences[0].EntrantID(state.ID).String()] = presences[0]
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
							m[p.EntrantID(state.ID).String()] = p
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
						m[p.EntrantID(state.ID).String()] = p
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
							m[p.EntrantID(state.ID).String()] = p
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
						m[p.EntrantID(state.ID).String()] = p
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
			RankPercentile:       0.0,
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
							m[p.EntrantID(state.ID).String()] = p
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
							m[p.EntrantID(state.ID).String()] = p
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

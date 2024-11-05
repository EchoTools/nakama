package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/google/go-cmp/cmp"
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

	presences := make([]*EvrMatchPresence, 0)
	for i := 0; i < 10; i++ {
		s := strconv.FormatInt(int64(i), 10)
		presence := &EvrMatchPresence{
			Node:           "testnode",
			SessionID:      uuid.Must(uuid.NewV4()),
			LoginSessionID: uuid.Must(uuid.NewV4()),
			UserID:         uuid.Must(uuid.NewV4()),
			EvrID:          evr.EvrId{PlatformCode: 4, AccountId: uint64(i)},
			DiscordID:      "10000" + s,
			ClientIP:       "127.0.0." + s,
			ClientPort:     "100" + s,
			Username:       "Test username" + s,
			DisplayName:    "Test User" + s,
			PartyID:        uuid.Must(uuid.NewV4()),
			RoleAlignment:  evr.TeamBlue,
			Query:          "testquery",
			SessionExpiry:  1234567890,
		}
		presences = append(presences, presence)
	}

	type args struct {
		ctx        context.Context
		logger     runtime.Logger
		db         *sql.DB
		nk         runtime.NakamaModule
		dispatcher runtime.MatchDispatcher
		tick       int64
		state_     interface{}
		presence   runtime.Presence
		metadata   map[string]string
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
				ctx: context.Background(),
				logger: func() runtime.Logger {
					logger := NewRuntimeGoLogger(NewJSONLogger(os.Stdout, zapcore.ErrorLevel, JSONFormat))
					return logger
				}(),
				db:         nil,
				nk:         nil,
				dispatcher: nil,
				tick:       0,
				state_: func() *MatchLabel {
					state := &MatchLabel{}

					return state
				}(),
				presence: presences[0],
				metadata: EntrantMetadata{Presence: presences[0]}.ToMatchMetadata(),
			},
			want: func() *MatchLabel {
				state := &MatchLabel{}
				return state
			}(),
			want1: false,
			want2: ErrJoinRejectReasonUnassignedLobby.Error(),
		},
		{
			name: "MatchJoinAttempt returns nil if match is full.",
			m:    &EvrMatch{},
			args: args{
				ctx: context.Background(),
				logger: func() runtime.Logger {
					logger := NewRuntimeGoLogger(NewJSONLogger(os.Stdout, zapcore.ErrorLevel, JSONFormat))
					return logger
				}(),
				db:         nil,
				nk:         nil,
				dispatcher: nil,
				tick:       0,
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

				return state
			}(),
			want1: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &EvrMatch{}
			got, got1, got2 := m.MatchJoinAttempt(tt.args.ctx, tt.args.logger, tt.args.db, tt.args.nk, tt.args.dispatcher, tt.args.tick, tt.args.state_, tt.args.presence, tt.args.metadata)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("EvrMatch.MatchJoinAttempt() got = %v, want %v", got, tt.want)
			}
			if got1 != tt.want1 {
				t.Errorf("EvrMatch.MatchJoinAttempt() got1 = %v, want %v", got1, tt.want1)
			}
			if got2 != tt.want2 {
				t.Errorf("EvrMatch.MatchJoinAttempt() got2 = %v, want %v", got2, tt.want2)
			}
		})
	}
}

func TestEvrMatch_playerJoinAttempt(t *testing.T) {
	type args struct {
		state *MatchLabel
		mp    *EvrMatchPresence
	}
	tests := []struct {
		name  string
		m     *EvrMatch
		args  args
		want  *EvrMatchPresence
		want1 string
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &EvrMatch{}
			got := m.validateJoin(tt.args.state, tt.args.mp)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("EvrMatch.playerJoinAttempt() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchGameStateUpdate_FromGoal(t *testing.T) {
	type fields struct {
		GameState GameState
	}
	type args struct {
		goal evr.RemoteLogGoal
	}
	tests := []struct {
		name   string
		fields fields
		args   args
	}{
		{},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := &MatchGameStateUpdate{
				GameState: tt.fields.GameState,
			}
			u.FromGoal(tt.args.goal)
		})
	}
}

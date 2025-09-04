package backend

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
	"github.com/echotools/nakama/v3/service"
	"github.com/gofrs/uuid/v5"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server"
	"github.com/samber/lo"
	"go.uber.org/zap/zapcore"
)

func TestNEVRMatch_NEVRMatchState(t *testing.T) {
	type args struct {
		data string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "NEVRMatchStateUnmarshal",
			args: args{
				data: `{"id":"7aab54ba-90ae-4e7f-abcf-69b30f5e8db7","open":true,"lobby_type":"public","endpoint":"","version_lock":14280634968751706381,"platform":"OVR","channel":"e8dd7736-32af-41f5-91e0-db591c6e8cfd","match_channels":["e8dd7736-32af-41f5-91e0-db591c6e8cfd","c016925b-3368-401c-8620-0c4ccd7e5c2e","f52129fb-d5c6-4c47-b644-f19981a933ee"],"mode":"social_2.0","level":"mpl_lobby_b2","session_settings":{"appid":"1369078409873402","gametype":301069346851901300},"max_size":15,"size":14,"max_team_size":15,"Presences":null}`,
			},
			want: `{"id":"7aab54ba-90ae-4e7f-abcf-69b30f5e8db7","open":true,"lobby_type":"public","endpoint":"","version_lock":14280634968751706381,"platform":"OVR","channel":"e8dd7736-32af-41f5-91e0-db591c6e8cfd","match_channels":["e8dd7736-32af-41f5-91e0-db591c6e8cfd","c016925b-3368-401c-8620-0c4ccd7e5c2e","f52129fb-d5c6-4c47-b644-f19981a933ee"],"mode":"social_2.0","level":"mpl_lobby_b2","session_settings":{"appid":"1369078409873402","gametype":301069346851901300},"max_size":15,"size":14,"max_team_size":15,"Presences":null}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// TODO protobuf's would be nice here.
			got := &State{}
			err := json.Unmarshal([]byte(tt.args.data), got)
			if err != nil {
				t.Fatalf("error unmarshalling data: %v", err)
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("NEVRMatch.MatchSignal() got = %s\n\nwant = %s", got, tt.want)
			}

		})
	}
}

func TestSelectTeamForPlayer(t *testing.T) {
	presencesstr := map[string]*LobbyPresence{
		"player1": {RoleAlignment: BlueTeam},
		"player2": {RoleAlignment: OrangeTeam},
		"spec1":   {RoleAlignment: Spectator},
		"player4": {RoleAlignment: OrangeTeam},
		"player5": {RoleAlignment: OrangeTeam},
		"player6": {RoleAlignment: OrangeTeam},
		"player7": {RoleAlignment: OrangeTeam},
		"player8": {RoleAlignment: OrangeTeam},
		"spec2":   {RoleAlignment: Spectator},
	}

	presences := make(map[uuid.UUID]*LobbyPresence)
	for k, v := range presencesstr {
		u := uuid.NewV5(uuid.Nil, k)
		presences[u] = v
	}

	state := &State{
		Presences: presences,
	}

	tests := []struct {
		name           string
		preferred      Role
		lobbyType      LobbyType
		presences      map[string]*LobbyPresence
		expectedTeam   Role
		expectedResult bool
	}{
		{
			name:           "UnassignedPlayer",
			lobbyType:      service.PrivateLobby,
			preferred:      AnyTeam,
			presences:      map[string]*LobbyPresence{},
			expectedTeam:   BlueTeam,
			expectedResult: true,
		},
		{
			name:      "Public match, blue team full, puts the player on orange",
			lobbyType: service.PublicLobby,
			preferred: BlueTeam,
			presences: map[string]*LobbyPresence{
				"player1": {RoleAlignment: BlueTeam},
				"player2": {RoleAlignment: BlueTeam},
				"player3": {RoleAlignment: BlueTeam},
				"player4": {RoleAlignment: BlueTeam},
				"player5": {RoleAlignment: OrangeTeam},
				"player6": {RoleAlignment: Spectator},
				"player7": {RoleAlignment: OrangeTeam},
				"player8": {RoleAlignment: OrangeTeam},
			},
			expectedTeam:   OrangeTeam,
			expectedResult: true,
		},
		{
			name:      "Public match, orange team full, puts the player on blue",
			lobbyType: service.PublicLobby,
			preferred: OrangeTeam,
			presences: map[string]*LobbyPresence{
				"player1": {RoleAlignment: Spectator},
				"player2": {RoleAlignment: BlueTeam},
				"player3": {RoleAlignment: BlueTeam},
				"player4": {RoleAlignment: BlueTeam},
				"player5": {RoleAlignment: OrangeTeam},
				"player6": {RoleAlignment: OrangeTeam},
				"player7": {RoleAlignment: OrangeTeam},
				"player8": {RoleAlignment: OrangeTeam},
			},
			expectedTeam:   BlueTeam,
			expectedResult: true,
		},
		{
			name:      "Public match, teams equal, use preference",
			lobbyType: service.PublicLobby,
			preferred: OrangeTeam,
			presences: map[string]*LobbyPresence{
				"player1": {RoleAlignment: Spectator},
				"player2": {RoleAlignment: BlueTeam},
				"player3": {RoleAlignment: BlueTeam},
				"player4": {RoleAlignment: BlueTeam},
				"player5": {RoleAlignment: OrangeTeam},
				"player6": {RoleAlignment: OrangeTeam},
				"player7": {RoleAlignment: OrangeTeam},
				"player8": {RoleAlignment: Spectator},
			},
			expectedTeam:   OrangeTeam,
			expectedResult: true,
		},
		{
			name:      "Public match, full reject",
			lobbyType: service.PublicLobby,
			preferred: BlueTeam,
			presences: map[string]*LobbyPresence{
				"player1": {RoleAlignment: BlueTeam},
				"player2": {RoleAlignment: BlueTeam},
				"player3": {RoleAlignment: BlueTeam},
				"player4": {RoleAlignment: BlueTeam},
				"player5": {RoleAlignment: OrangeTeam},
				"player6": {RoleAlignment: OrangeTeam},
				"player7": {RoleAlignment: OrangeTeam},
				"player8": {RoleAlignment: OrangeTeam},
			},
			expectedTeam:   AnyTeam,
			expectedResult: false,
		},
		{
			name:      "Public match, spectators full, reject",
			lobbyType: service.PublicLobby,
			preferred: Spectator,
			presences: map[string]*LobbyPresence{
				"player1": {RoleAlignment: BlueTeam},
				"player2": {RoleAlignment: BlueTeam},
				"player3": {RoleAlignment: Spectator},
				"player4": {RoleAlignment: Spectator},
				"player5": {RoleAlignment: Spectator},
				"player6": {RoleAlignment: Spectator},
				"player7": {RoleAlignment: Spectator},
				"player8": {RoleAlignment: Spectator},
			},
			expectedTeam:   AnyTeam,
			expectedResult: false,
		},
		{
			name:      "Private match, use preference",
			lobbyType: service.PrivateLobby,
			preferred: OrangeTeam,
			presences: map[string]*LobbyPresence{
				"player1": {RoleAlignment: BlueTeam},
				"player2": {RoleAlignment: BlueTeam},
				"player3": {RoleAlignment: BlueTeam},
				"player4": {RoleAlignment: BlueTeam},
				"player5": {RoleAlignment: Spectator},
				"player6": {RoleAlignment: Spectator},
				"player7": {RoleAlignment: Spectator},
				"player8": {RoleAlignment: OrangeTeam},
			},
			expectedTeam:   OrangeTeam,
			expectedResult: true,
		},
		{
			name:      "Private match, use preference (5 player teams)",
			lobbyType: service.PrivateLobby,
			preferred: OrangeTeam,
			presences: map[string]*LobbyPresence{
				"player1": {RoleAlignment: Spectator},
				"player2": {RoleAlignment: BlueTeam},
				"player3": {RoleAlignment: BlueTeam},
				"player4": {RoleAlignment: BlueTeam},
				"player5": {RoleAlignment: OrangeTeam},
				"player6": {RoleAlignment: OrangeTeam},
				"player7": {RoleAlignment: OrangeTeam},
				"player8": {RoleAlignment: OrangeTeam},
			},
			expectedTeam:   OrangeTeam,
			expectedResult: true,
		},
		{
			name:      "Private match, preference full, put on other team",
			lobbyType: service.PrivateLobby,
			preferred: OrangeTeam,
			presences: map[string]*LobbyPresence{
				"player1":  {RoleAlignment: Spectator},
				"player2":  {RoleAlignment: BlueTeam},
				"player3":  {RoleAlignment: BlueTeam},
				"player4":  {RoleAlignment: BlueTeam},
				"player5":  {RoleAlignment: BlueTeam},
				"player6":  {RoleAlignment: OrangeTeam},
				"player7":  {RoleAlignment: OrangeTeam},
				"player8":  {RoleAlignment: OrangeTeam},
				"player9":  {RoleAlignment: OrangeTeam},
				"player10": {RoleAlignment: OrangeTeam},
			},
			expectedTeam:   BlueTeam,
			expectedResult: true,
		},
		{
			name:      "Full private match, puts the player on spectator",
			lobbyType: service.PrivateLobby,
			preferred: OrangeTeam,
			presences: map[string]*LobbyPresence{
				"player1":  {RoleAlignment: BlueTeam},
				"player2":  {RoleAlignment: BlueTeam},
				"player3":  {RoleAlignment: BlueTeam},
				"player4":  {RoleAlignment: BlueTeam},
				"player5":  {RoleAlignment: BlueTeam},
				"player6":  {RoleAlignment: OrangeTeam},
				"player7":  {RoleAlignment: OrangeTeam},
				"player8":  {RoleAlignment: OrangeTeam},
				"player9":  {RoleAlignment: OrangeTeam},
				"player10": {RoleAlignment: OrangeTeam},
			},
			expectedTeam:   Spectator,
			expectedResult: true,
		},
		{
			name:      "Private match, spectators full, reject",
			lobbyType: service.PrivateLobby,
			preferred: Spectator,
			presences: map[string]*LobbyPresence{
				"player1": {RoleAlignment: BlueTeam},
				"player2": {RoleAlignment: BlueTeam},
				"player3": {RoleAlignment: Spectator},
				"player4": {RoleAlignment: Spectator},
				"player5": {RoleAlignment: Spectator},
				"player6": {RoleAlignment: Spectator},
				"player7": {RoleAlignment: Spectator},
				"player8": {RoleAlignment: Spectator},
			},
			expectedTeam:   AnyTeam,
			expectedResult: false,
		},
		{
			name:      "full social lobby, reject",
			lobbyType: service.PublicLobby,
			preferred: Spectator,
			presences: map[string]*LobbyPresence{
				"player1":  {RoleAlignment: SocialLobbyParticipant},
				"player2":  {RoleAlignment: SocialLobbyParticipant},
				"player3":  {RoleAlignment: SocialLobbyParticipant},
				"player4":  {RoleAlignment: SocialLobbyParticipant},
				"player5":  {RoleAlignment: SocialLobbyParticipant},
				"player6":  {RoleAlignment: SocialLobbyParticipant},
				"player7":  {RoleAlignment: SocialLobbyParticipant},
				"player8":  {RoleAlignment: SocialLobbyParticipant},
				"player9":  {RoleAlignment: SocialLobbyParticipant},
				"player10": {RoleAlignment: SocialLobbyParticipant},
				"player11": {RoleAlignment: SocialLobbyParticipant},
				"player12": {RoleAlignment: SocialLobbyParticipant},
			},
			expectedTeam:   AnyTeam,
			expectedResult: false,
		},
		{
			name:      "social lobby, moderator, allow",
			lobbyType: service.PublicLobby,
			preferred: Moderator,
			presences: map[string]*LobbyPresence{
				"player1":  {RoleAlignment: SocialLobbyParticipant},
				"player2":  {RoleAlignment: SocialLobbyParticipant},
				"player3":  {RoleAlignment: SocialLobbyParticipant},
				"player4":  {RoleAlignment: SocialLobbyParticipant},
				"player5":  {RoleAlignment: SocialLobbyParticipant},
				"player6":  {RoleAlignment: SocialLobbyParticipant},
				"player7":  {RoleAlignment: SocialLobbyParticipant},
				"player8":  {RoleAlignment: SocialLobbyParticipant},
				"player9":  {RoleAlignment: SocialLobbyParticipant},
				"player10": {RoleAlignment: SocialLobbyParticipant},
				"player11": {RoleAlignment: SocialLobbyParticipant},
			},
			expectedTeam:   Moderator,
			expectedResult: true,
		},
	}

	for _, tt := range tests {
		presence := &LobbyPresence{
			RoleAlignment: tt.preferred,
		}
		presencestr := make(map[uuid.UUID]*LobbyPresence)
		for k, v := range tt.presences {
			u := uuid.NewV5(uuid.Nil, k)
			presencestr[u] = v
		}

		state.Presences = presencestr
		state.Metadata.MaxSize = service.SocialLobbyMaxSize
		state.Metadata.Visibility = tt.lobbyType
		if state.Metadata.Visibility == service.PublicLobby {
			state.Metadata.TeamSize = 4
		} else {
			state.Metadata.TeamSize = 5
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

		BlueTeam   = BlueTeam
		Orange     = OrangeTeam
		Spectator  = Spectator
		Unassigned = AnyTeam
	)
	alignments := map[string]Role{
		DMO_1:  BlueTeam,
		DMO_2:  BlueTeam,
		DMO_3:  BlueTeam,
		DMO_4:  BlueTeam,
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
		preferredTeam Role
		expectedTeam  Role
		allowed       bool
	}{
		{
			name:      "Follows alignment even when unbalanced",
			lobbyType: service.PublicLobby,
			players: []string{
				DMO_1,
				DMO_2,
				DMO_3,
			},
			newPlayer:     DMO_4,
			preferredTeam: OrangeTeam,
			expectedTeam:  BlueTeam,
			allowed:       true,
		},
	}

	for _, tt := range tests {
		// Existing players
		presences := make(map[uuid.UUID]*LobbyPresence)
		for _, player := range tt.players {
			u := uuid.NewV5(uuid.Nil, player)
			presences[u] = &LobbyPresence{
				RoleAlignment: alignments[player],
			}
		}

		// New Player
		presence := &LobbyPresence{
			XPID:          *lo.Must(evr.ParseEvrId(tt.newPlayer)),
			RoleAlignment: tt.preferredTeam,
		}

		// Match State
		state := &State{
			Presences: presences,
			Metadata: &service.LobbyMetadata{
				Visibility: tt.lobbyType,
				MaxSize:    service.SocialLobbyMaxSize,
				TeamSize: func() int {
					if tt.lobbyType == service.PublicLobby {
						return 4
					} else {
						return 5
					}
				}(),
			},
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

func TestNEVRMatch_MatchLoop(t *testing.T) {
	type args struct {
		tick     int64
		state_   interface{}
		messages []runtime.MatchData
	}

	consoleLogger := server.NewJSONLogger(os.Stdout, zapcore.ErrorLevel, server.JSONFormat)
	logger := server.NewRuntimeGoLogger(consoleLogger)

	var state *State

	tests := []struct {
		name string
		m    *NEVRMatch
		args args
		want interface{}
	}{
		{
			name: "Match did not start on time.",
			m:    &NEVRMatch{},
			args: args{
				tick: 15 * 60 * 10 * 2,
				state_: func() *State {
					state := &State{}
					state.SessionStartExpiry = 10 * 10
					state.Server = &service.GameServerPresence{}
					return state
				}(),

				messages: []runtime.MatchData{},
			},
			want: nil,
		},
		{
			name: "MatchLoop returns state.",
			m:    &NEVRMatch{},
			args: args{
				tick: 500,
				state_: func() *State {
					state := &State{}
					state.SessionStartExpiry = 10 * 10
					state.Server = &service.GameServerPresence{}

					return state
				}(),

				messages: []runtime.MatchData{},
			},
			want: func() *State {
				return state
			}(),
		},
		{
			name: "MatchLoop exits if empty for more than 20 seconds",
			m:    &NEVRMatch{},
			args: args{
				tick: 0,
				state_: &State{
					StartTime:  time.Now().Add(-30 * time.Minute),
					Server:     &service.GameServerPresence{},
					EmptyTicks: 10 * 30,
					TickRate:   10,
				},
				messages: []runtime.MatchData{},
			},
			want: nil,
		},
		{
			name: "MatchLoop exits if no broadcaster after 15 seconds.",
			m:    &NEVRMatch{},
			args: args{
				tick: 30 * 10,
				state_: &State{
					EmptyTicks: 0,
					TickRate:   10,
					Presences: map[uuid.UUID]*LobbyPresence{
						uuid.Must(uuid.NewV4()): {},
					},
				},
				messages: []runtime.MatchData{},
			},
			want: nil,
		},
		{
			name: "MatchLoop tolerates being without broadcaster for 5 seconds.",
			m:    &NEVRMatch{},
			args: args{
				tick: 5 * 10,
				state_: func() *State {
					state := &State{}
					state.SessionStartExpiry = 10 * 10
					state.Presences = map[uuid.UUID]*LobbyPresence{
						uuid.Must(uuid.NewV4()): {},
					}

					return state
				}(),
				messages: []runtime.MatchData{},
			},
			want: func() *State {
				return state
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &NEVRMatch{}
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

func TestNEVRMatch_MatchJoinAttempt(t *testing.T) {
	now := time.Now()
	ServerConfig := GameServerPresence{
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
	testStateFn := func() *State {

		state := &State{
			CreateTime: now,
			Server:     &ServerConfig,
			Metadata: &service.LobbyMetadata{
				Visibility:       service.UnassignedLobby,
				Mode:             evr.ModeUnloaded,
				Level:            evr.LevelUnloaded,
				RequiredFeatures: make([]string, 0),
				MaxSize:          service.SocialLobbyMaxSize,
			},
			Presences:          make(map[uuid.UUID]*LobbyPresence, service.SocialLobbyMaxSize),
			Reservations:       make(map[uuid.UUID]*Reservation, 2),
			Alignments:         make(map[uuid.UUID]Role, service.SocialLobbyMaxSize),
			JoinTimes:          make(map[uuid.UUID]time.Time, service.SocialLobbyMaxSize),
			Goals:              make([]*evr.MatchGoal, 0),
			EmptyTicks:         0,
			TickRate:           10,
			SessionStartExpiry: 0,
		}
		return state
	}

	presences := make([]*LobbyPresence, 0)
	for i := 0; i < 10; i++ {
		s := strconv.FormatInt(int64(i), 10)
		presence := &LobbyPresence{
			Node:           "testnode",
			SessionID:      uuid.NewV5(uuid.Nil, fmt.Sprintf("session-%d", i)),
			LoginSessionID: uuid.NewV5(uuid.Nil, fmt.Sprintf("login-%d", i)),
			UserID:         uuid.NewV5(uuid.Nil, fmt.Sprintf("user-%d", i)),
			XPID:           evr.XPID{PlatformCode: 4, AccountId: uint64(i)},
			DiscordID:      "10000" + s,
			ClientIP:       "127.0.0." + s,
			ClientPort:     "100" + s,
			Username:       "Test username" + s,
			DisplayName:    "Test User" + s,
			PartyID:        uuid.NewV5(uuid.Nil, fmt.Sprintf("party-%d", i)),
			RoleAlignment:  BlueTeam,
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
		m     *NEVRMatch
		args  args
		want  interface{}
		want1 bool
		want2 string
	}{
		{
			name: "MatchJoinAttempt rejects join to unassigned lobby.",
			m:    &NEVRMatch{},
			args: args{

				state_: func() *State {
					state := testStateFn()
					state.Open = true
					state.Metadata.Visibility = UnassignedLobby

					return state
				}(),
				presence: presences[0],
				metadata: EntrantMetadata{Presence: presences[0]}.ToMatchMetadata(),
			},
			want: func() *State {
				state := testStateFn()
				state.Open = true
				state.Metadata.Visibility = UnassignedLobby
				return state
			}(),
			want1: false,
			want2: ErrJoinRejectReasonUnassignedLobby.Error(),
		},
		{
			name: "MatchJoinAttempt allows spectators to join closed matches, that are not terminating.",
			m:    &NEVRMatch{},
			args: args{
				state_: func() *State {
					state := testStateFn()
					state.StartTime = time.Now()
					state.terminateTick = 0
					state.Open = false
					state.Metadata.Visibility = service.PublicLobby
					state.Mode = evr.ModeArenaPublic
					state.MaxSize = 3
					state.Size = 2
					state.PlayerLimit = 2
					state.PlayerCount = 2
					state.Presences = func() map[uuid.UUID]*LobbyPresence {
						m := make(map[uuid.UUID]*LobbyPresence)
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
					meta.Presence.RoleAlignment = Spectator
					return meta.ToMatchMetadata()
				}(),
			},
			want: func() *State {
				state := testStateFn()
				state.StartTime = time.Now()
				state.terminateTick = 0
				state.Open = false
				state.Metadata.Visibility = service.PublicLobby
				state.Mode = evr.ModeArenaPublic
				state.MaxSize = 3
				state.Size = 3
				state.PlayerLimit = 2
				state.PlayerCount = 2
				state.Presences = func() map[uuid.UUID]*LobbyPresence {
					m := make(map[uuid.UUID]*LobbyPresence)
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
			m:    &NEVRMatch{},
			args: args{
				state_: func() *State {
					state := testStateFn()

					state.StartTime = time.Now()
					state.terminateTick = 100
					state.Open = false
					state.Metadata.Visibility = service.PublicLobby
					state.Mode = evr.ModeArenaPublic
					state.MaxSize = 3
					state.Size = 2
					state.PlayerLimit = 2
					state.PlayerCount = 2
					state.Presences = func() map[uuid.UUID]*LobbyPresence {
						m := make(map[uuid.UUID]*LobbyPresence)
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
					meta.Presence.RoleAlignment = Spectator
					return meta.ToMatchMetadata()
				}(),
			},
			want: func() *State {
				state := testStateFn()
				state.MaxSize = 3
				state.Size = 2
				state.PlayerLimit = 2
				state.PlayerCount = 2
				state.Presences = func() map[uuid.UUID]*LobbyPresence {
					m := make(map[uuid.UUID]*LobbyPresence)
					for _, p := range presences[1:3] {
						m[p.EntrantID(state.ID).String()] = p
					}
					return m
				}()
				state.Presences[presences[0].EntrantID(state.ID).String()] = presences[0]
				state.rebuildCache()
				return state
			}(),
			want1: true,
			want2: presences[0].String(),
		},
		{
			name: "MatchJoinAttempt returns nil if match is full.",
			m:    &NEVRMatch{},
			args: args{
				state_: func() *State {
					state := &State{}
					state.Open = true
					state.Metadata.Visibility = service.PublicLobby
					state.Mode = evr.ModeArenaPublic
					state.MaxSize = 3
					state.Size = 2
					state.PlayerLimit = 2
					state.PlayerCount = 2
					state.Presences = func() map[uuid.UUID]*LobbyPresence {
						m := make(map[uuid.UUID]*LobbyPresence)
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
			want: func() *State {
				state := &State{}
				state.MaxSize = 3
				state.Size = 2
				state.PlayerLimit = 2
				state.PlayerCount = 2
				state.Presences = func() map[uuid.UUID]*LobbyPresence {
					m := make(map[uuid.UUID]*LobbyPresence)
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
			m:    &NEVRMatch{},
			args: args{
				state_: func() *State {
					state := &State{}
					state.Metadata.Visibility = service.PublicLobby
					state.Mode = evr.ModeArenaPublic
					state.MaxSize = 3
					state.Size = 2
					state.PlayerLimit = 2
					state.PlayerCount = 2
					state.Presences = func() map[uuid.UUID]*LobbyPresence {
						m := make(map[uuid.UUID]*LobbyPresence)
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
			want: func() *State {
				state := &State{}
				state.MaxSize = 3
				state.Size = 2
				state.PlayerLimit = 2
				state.PlayerCount = 2
				state.Presences = func() map[uuid.UUID]*LobbyPresence {
					m := make(map[uuid.UUID]*LobbyPresence)
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
				logger := server.NewRuntimeGoLogger(server.NewJSONLogger(os.Stdout, zapcore.ErrorLevel, server.JSONFormat))
				return logger
			}()

			m := &NEVRMatch{}
			got, got1, got2 := m.MatchJoinAttempt(ctx, logger, nil, nil, nil, 10, tt.args.state_, tt.args.presence, tt.args.metadata)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("NEVRMatch.MatchJoinAttempt() (- want/+ got): %s", cmp.Diff(got, tt.want, cmpopts.IgnoreUnexported(State{})))
			}
			if got1 != tt.want1 {
				t.Errorf("NEVRMatch.MatchJoinAttempt() (- want/+ got): %s", cmp.Diff(got, tt.want1, cmpopts.IgnoreUnexported(State{})))
			}
			if got2 != tt.want2 {
				t.Errorf("NEVRMatch.MatchJoinAttempt() (- want/+ got): %s", cmp.Diff(got, tt.want2, cmpopts.IgnoreUnexported(State{})))
			}
		})
	}
}
func TestNEVRMatch_MatchJoinAttempt_Counts(t *testing.T) {
	now := time.Now()
	ServerConfig := GameServerPresence{
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
	testStateFn := func() *State {

		state := &State{
			CreatedAt:        now,
			GameServer:       &ServerConfig,
			Open:             false,
			LobbyType:        UnassignedLobby,
			Mode:             evr.ModeUnloaded,
			Level:            evr.LevelUnloaded,
			RequiredFeatures: make([]string, 0),
			Players:          make([]PlayerInfo, 0, service.SocialLobbyMaxSize),
			Presences:        make(map[uuid.UUID]*LobbyPresence, service.SocialLobbyMaxSize),
			reservationMap:   make(map[string]*Reservation, 2),
			presenceByXPID:   make(map[evr.XPID]*LobbyPresence, service.SocialLobbyMaxSize),
			goals:            make([]*evr.MatchGoal, 0),

			TeamAlignments:       make(map[string]int, service.SocialLobbyMaxSize),
			joinTimestamps:       make(map[string]time.Time, service.SocialLobbyMaxSize),
			joinTimeMilliseconds: make(map[string]int64, service.SocialLobbyMaxSize),
			EmptyTicks:           0,
			TickRate:             10,
			RankPercentile:       0.0,
		}
		state.rebuildCache()
		return state
	}

	presences := make([]*LobbyPresence, 0)
	for i := 0; i < 10; i++ {
		s := strconv.FormatInt(int64(i), 10)
		presence := &LobbyPresence{
			Node:           "testnode",
			SessionID:      uuid.NewV5(uuid.Nil, fmt.Sprintf("session-%d", i)),
			LoginSessionID: uuid.NewV5(uuid.Nil, fmt.Sprintf("login-%d", i)),
			UserID:         uuid.NewV5(uuid.Nil, fmt.Sprintf("user-%d", i)),
			XPID:           evr.XPID{PlatformCode: 4, AccountId: uint64(i)},
			DiscordID:      "10000" + s,
			ClientIP:       "127.0.0." + s,
			ClientPort:     "100" + s,
			Username:       "Test username" + s,
			DisplayName:    "Test User" + s,
			PartyID:        uuid.NewV5(uuid.Nil, fmt.Sprintf("party-%d", i)),
			RoleAlignment:  BlueTeam,
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
		m           *NEVRMatch
		args        args
		wantAllowed bool
		wantReason  string
	}{
		{
			name: "MatchJoinAttempt returns full if both teams are full",
			m:    &NEVRMatch{},
			args: args{
				state_: func() *State {
					state := testStateFn()
					state.Open = true
					state.Metadata.Visibility = service.PublicLobby
					state.Mode = evr.ModeArenaPublic
					state.MaxSize = 16
					state.PlayerLimit = 8
					state.TeamSize = 4

					state.Presences = func() map[uuid.UUID]*LobbyPresence {
						m := make(map[uuid.UUID]*LobbyPresence)
						for i, p := range presences[1:9] {
							if i >= 4 {
								p.RoleAlignment = OrangeTeam
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
					presences[0].RoleAlignment = OrangeTeam
					return NewJoinMetadata(presences[0]).ToMatchMetadata()
				}(),
			},
			wantAllowed: false,
			wantReason:  ErrJoinRejectReasonLobbyFull.Error(),
		},
		{
			name: "MatchJoinAttempt returns full if both teams are full, and player has no team set",
			m:    &NEVRMatch{},
			args: args{
				state_: func() *State {
					state := testStateFn()
					state.Open = true
					state.Metadata.Visibility = service.PublicLobby
					state.Mode = evr.ModeArenaPublic
					state.MaxSize = 16
					state.PlayerLimit = 8
					state.TeamSize = 4

					state.Presences = func() map[uuid.UUID]*LobbyPresence {
						m := make(map[uuid.UUID]*LobbyPresence)
						for i, p := range presences[1:9] {
							if i >= 4 {
								p.RoleAlignment = OrangeTeam
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
					presences[0].RoleAlignment = AnyTeam
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
				logger := server.NewRuntimeGoLogger(server.NewJSONLogger(os.Stdout, zapcore.ErrorLevel, server.JSONFormat))
				return logger
			}()

			m := &NEVRMatch{}
			_, gotAllowed, gotResponse := m.MatchJoinAttempt(ctx, logger, nil, nil, nil, 10, tt.args.state_, tt.args.presence, tt.args.metadata)
			if gotAllowed != tt.wantAllowed {
				t.Errorf("NEVRMatch.MatchJoinAttempt() gotAllowed = %v, want %v", gotAllowed, tt.wantAllowed)
			}
			if gotResponse != tt.wantReason {
				t.Errorf("NEVRMatch.MatchJoinAttempt() gotResponse = %v, want %v", gotResponse, tt.wantReason)
			}
		})
	}
}

/*
func TestNEVRMatch_processJoin(t *testing.T) {
	session1 := uuid.Must(uuid.NewV4())
	session2 := uuid.Must(uuid.NewV4())
	session3 := uuid.Must(uuid.NewV4())
	session4 := uuid.Must(uuid.NewV4())
	session5 := uuid.Must(uuid.NewV4())

	tests := []struct {
		name           string
		state          *MatchState
		entrant        *NEVRLobbyPresence
		expectedResult bool
		expectedError  error
	}{
		{
			name: "Player with reservation joins successfully",
			state: &MatchState{
				Mode:        evr.ModeArenaPublic,
				MaxSize:     2,
				TeamSize:    1,
				PlayerLimit: 2,
				reservationMap: map[string]*slotReservation{
					session1.String(): {Presence: &NEVRLobbyPresence{RoleAlignment: 0, SessionID: session1, XPID: evr.XPID{PlatformCode: 1, AccountId: 1}}, Expiry: time.Now().Add(time.Minute)},
				},
				Presences: map[string]*NEVRLobbyPresence{
					session2.String(): {RoleAlignment: 1, SessionID: session2, XPID: evr.XPID{PlatformCode: 1, AccountId: 2}},
				},
			},
			entrant:        &NEVRLobbyPresence{SessionID: session1, RoleAlignment: -1, XPID: evr.XPID{PlatformCode: 1, AccountId: 1}},
			expectedResult: true,
			expectedError:  nil,
		},
		{
			name: "Lobby full rejected (due to full slot)",
			state: &MatchState{
				Mode:        evr.ModeArenaPublic,
				TeamSize:    2,
				MaxSize:     6,
				PlayerLimit: 4,
				Presences: map[string]*NEVRLobbyPresence{
					session1.String(): {SessionID: session1, RoleAlignment: 0, XPID: evr.XPID{PlatformCode: 1, AccountId: 1}},
					session2.String(): {SessionID: session2, RoleAlignment: 0, XPID: evr.XPID{PlatformCode: 1, AccountId: 2}},
					session3.String(): {SessionID: session3, RoleAlignment: 1, XPID: evr.XPID{PlatformCode: 1, AccountId: 3}},
				},
				reservationMap: map[string]*slotReservation{
					session4.String(): {
						Presence: &NEVRLobbyPresence{SessionID: session4, RoleAlignment: 1, XPID: evr.XPID{PlatformCode: 1, AccountId: 4}},
						Expiry:   time.Now().Add(time.Minute),
					},
				},
			},
			entrant:        &NEVRLobbyPresence{SessionID: session5, RoleAlignment: 1, XPID: evr.XPID{PlatformCode: 1, AccountId: 5}},
			expectedResult: false,
			expectedError:  ErrJoinRejectReasonLobbyFull,
		},
		{
			name: "Duplicate join rejected",
			state: &MatchState{
				Mode:        evr.ModeArenaPublic,
				TeamSize:    4,
				MaxSize:     8,
				PlayerLimit: 8,
				Presences: map[string]*NEVRLobbyPresence{
					session1.String(): {SessionID: session1, XPID: evr.XPID{PlatformCode: 1, AccountId: 1}},
				},
			},
			entrant:        &NEVRLobbyPresence{SessionID: session2, XPID: evr.XPID{PlatformCode: 1, AccountId: 1}},
			expectedResult: false,
			expectedError:  ErrJoinRejectReasonDuplicateJoin,
		},
		{
			name: "Feature mismatch rejected",
			state: &MatchState{
				Mode:             evr.ModeArenaPublic,
				TeamSize:         4,
				MaxSize:          8,
				PlayerLimit:      8,
				RequiredFeatures: []string{"feature1"},
				Presences:      map[string]*NEVRLobbyPresence{},
			},
			entrant:        &NEVRLobbyPresence{SessionID: session1, SupportedFeatures: []string{"feature2"}},
			expectedResult: false,
			expectedError:  ErrJoinRejectReasonFeatureMismatch,
		},
		{
			name: "Assign role and join successfully",
			state: &MatchState{
				MaxSize:     4,
				TeamSize:    2,
				PlayerLimit: 8,
				Mode:        evr.ModeArenaPublic,
				Presences: map[string]*NEVRLobbyPresence{},
			},
			entrant:        &NEVRLobbyPresence{SessionID: session1, RoleAlignment: AnyTeam, XPID: evr.XPID{PlatformCode: 1, AccountId: 1}},
			expectedResult: false,
			expectedError:  nil,
		},
		{
			name: "Arena match team full rejected",
			state: &MatchState{
				Mode:        evr.ModeArenaPublic,
				MaxSize:     8,
				PlayerLimit: 8,
				TeamSize:    4,
				Presences: map[string]*NEVRLobbyPresence{
					session1.String(): {SessionID: session1, RoleAlignment: OrangeTeam, XPID: evr.XPID{PlatformCode: 1, AccountId: 1}},
					session2.String(): {SessionID: session2, RoleAlignment: OrangeTeam, XPID: evr.XPID{PlatformCode: 1, AccountId: 2}},
					session3.String(): {SessionID: session3, RoleAlignment: OrangeTeam, XPID: evr.XPID{PlatformCode: 1, AccountId: 3}},
					session4.String(): {SessionID: session4, RoleAlignment: OrangeTeam, XPID: evr.XPID{PlatformCode: 1, AccountId: 4}},
				},
			},
			entrant:        &NEVRLobbyPresence{SessionID: session5, RoleAlignment: OrangeTeam, XPID: evr.XPID{PlatformCode: 1, AccountId: 5}},
			expectedResult: false,
			expectedError:  ErrJoinRejectReasonLobbyFull,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.state.rebuildCache()
			m := &NEVRMatch{}
			gotResult, gotError := m.processJoin(tt.state, server.NewRuntimeGoLogger(logger), tt.entrant)
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

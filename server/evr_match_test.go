package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"reflect"
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
			got := &EvrMatchState{}
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
		"player1": {TeamIndex: evr.TeamBlue},
		"player2": {TeamIndex: evr.TeamOrange},
		"player3": {TeamIndex: evr.TeamSpectator},
		"player4": {TeamIndex: evr.TeamOrange},
		"player5": {TeamIndex: evr.TeamOrange},
	}

	presences := make(map[string]*EvrMatchPresence)
	for k, v := range presencesstr {
		u := uuid.NewV5(uuid.Nil, k).String()
		presences[u] = v
	}

	state := &EvrMatchState{
		presences: presences,
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
				"player1": {TeamIndex: evr.TeamBlue},
				"player2": {TeamIndex: evr.TeamBlue},
				"player3": {TeamIndex: evr.TeamBlue},
				"player4": {TeamIndex: evr.TeamBlue},
				"player5": {TeamIndex: evr.TeamOrange},
				"player6": {TeamIndex: evr.TeamSpectator},
				"player7": {TeamIndex: evr.TeamOrange},
				"player8": {TeamIndex: evr.TeamOrange},
			},
			expectedTeam:   evr.TeamOrange,
			expectedResult: true,
		},
		{
			name:      "Public match, orange team full, puts the player on blue",
			lobbyType: PublicLobby,
			preferred: evr.TeamOrange,
			presences: map[string]*EvrMatchPresence{
				"player1": {TeamIndex: evr.TeamSpectator},
				"player2": {TeamIndex: evr.TeamBlue},
				"player3": {TeamIndex: evr.TeamBlue},
				"player4": {TeamIndex: evr.TeamBlue},
				"player5": {TeamIndex: evr.TeamOrange},
				"player6": {TeamIndex: evr.TeamOrange},
				"player7": {TeamIndex: evr.TeamOrange},
				"player8": {TeamIndex: evr.TeamOrange},
			},
			expectedTeam:   evr.TeamBlue,
			expectedResult: true,
		},
		{
			name:      "Public match, teams equal, use preference",
			lobbyType: PublicLobby,
			preferred: evr.TeamOrange,
			presences: map[string]*EvrMatchPresence{
				"player1": {TeamIndex: evr.TeamSpectator},
				"player2": {TeamIndex: evr.TeamBlue},
				"player3": {TeamIndex: evr.TeamBlue},
				"player4": {TeamIndex: evr.TeamBlue},
				"player5": {TeamIndex: evr.TeamOrange},
				"player6": {TeamIndex: evr.TeamOrange},
				"player7": {TeamIndex: evr.TeamOrange},
				"player8": {TeamIndex: evr.TeamSpectator},
			},
			expectedTeam:   evr.TeamOrange,
			expectedResult: true,
		},
		{
			name:      "Public match, full reject",
			lobbyType: PublicLobby,
			preferred: evr.TeamBlue,
			presences: map[string]*EvrMatchPresence{
				"player1": {TeamIndex: evr.TeamBlue},
				"player2": {TeamIndex: evr.TeamBlue},
				"player3": {TeamIndex: evr.TeamBlue},
				"player4": {TeamIndex: evr.TeamBlue},
				"player5": {TeamIndex: evr.TeamOrange},
				"player6": {TeamIndex: evr.TeamOrange},
				"player7": {TeamIndex: evr.TeamOrange},
				"player8": {TeamIndex: evr.TeamOrange},
			},
			expectedTeam:   evr.TeamUnassigned,
			expectedResult: false,
		},
		{
			name:      "Public match, spectators full, reject",
			lobbyType: PublicLobby,
			preferred: evr.TeamSpectator,
			presences: map[string]*EvrMatchPresence{
				"player1": {TeamIndex: evr.TeamBlue},
				"player2": {TeamIndex: evr.TeamBlue},
				"player3": {TeamIndex: evr.TeamSpectator},
				"player4": {TeamIndex: evr.TeamSpectator},
				"player5": {TeamIndex: evr.TeamSpectator},
				"player6": {TeamIndex: evr.TeamSpectator},
				"player7": {TeamIndex: evr.TeamSpectator},
				"player8": {TeamIndex: evr.TeamSpectator},
			},
			expectedTeam:   evr.TeamUnassigned,
			expectedResult: false,
		},
		{
			name:      "Private match, use preference",
			lobbyType: PrivateLobby,
			preferred: evr.TeamOrange,
			presences: map[string]*EvrMatchPresence{
				"player1": {TeamIndex: evr.TeamBlue},
				"player2": {TeamIndex: evr.TeamBlue},
				"player3": {TeamIndex: evr.TeamBlue},
				"player4": {TeamIndex: evr.TeamBlue},
				"player5": {TeamIndex: evr.TeamSpectator},
				"player6": {TeamIndex: evr.TeamSpectator},
				"player7": {TeamIndex: evr.TeamSpectator},
				"player8": {TeamIndex: evr.TeamOrange},
			},
			expectedTeam:   evr.TeamOrange,
			expectedResult: true,
		},
		{
			name:      "Private match, use preference (5 player teams)",
			lobbyType: PrivateLobby,
			preferred: evr.TeamOrange,
			presences: map[string]*EvrMatchPresence{
				"player1": {TeamIndex: evr.TeamSpectator},
				"player2": {TeamIndex: evr.TeamBlue},
				"player3": {TeamIndex: evr.TeamBlue},
				"player4": {TeamIndex: evr.TeamBlue},
				"player5": {TeamIndex: evr.TeamOrange},
				"player6": {TeamIndex: evr.TeamOrange},
				"player7": {TeamIndex: evr.TeamOrange},
				"player8": {TeamIndex: evr.TeamOrange},
			},
			expectedTeam:   evr.TeamOrange,
			expectedResult: true,
		},
		{
			name:      "Private match, preference full, put on other team",
			lobbyType: PrivateLobby,
			preferred: evr.TeamOrange,
			presences: map[string]*EvrMatchPresence{
				"player1":  {TeamIndex: evr.TeamSpectator},
				"player2":  {TeamIndex: evr.TeamBlue},
				"player3":  {TeamIndex: evr.TeamBlue},
				"player4":  {TeamIndex: evr.TeamBlue},
				"player5":  {TeamIndex: evr.TeamBlue},
				"player6":  {TeamIndex: evr.TeamOrange},
				"player7":  {TeamIndex: evr.TeamOrange},
				"player8":  {TeamIndex: evr.TeamOrange},
				"player9":  {TeamIndex: evr.TeamOrange},
				"player10": {TeamIndex: evr.TeamOrange},
			},
			expectedTeam:   evr.TeamBlue,
			expectedResult: true,
		},
		{
			name:      "Full private match, puts the player on spectator",
			lobbyType: PrivateLobby,
			preferred: evr.TeamOrange,
			presences: map[string]*EvrMatchPresence{
				"player1":  {TeamIndex: evr.TeamBlue},
				"player2":  {TeamIndex: evr.TeamBlue},
				"player3":  {TeamIndex: evr.TeamBlue},
				"player4":  {TeamIndex: evr.TeamBlue},
				"player5":  {TeamIndex: evr.TeamBlue},
				"player6":  {TeamIndex: evr.TeamOrange},
				"player7":  {TeamIndex: evr.TeamOrange},
				"player8":  {TeamIndex: evr.TeamOrange},
				"player9":  {TeamIndex: evr.TeamOrange},
				"player10": {TeamIndex: evr.TeamOrange},
			},
			expectedTeam:   evr.TeamSpectator,
			expectedResult: true,
		},
		{
			name:      "Private match, spectators full, reject",
			lobbyType: PrivateLobby,
			preferred: evr.TeamSpectator,
			presences: map[string]*EvrMatchPresence{
				"player1": {TeamIndex: evr.TeamBlue},
				"player2": {TeamIndex: evr.TeamBlue},
				"player3": {TeamIndex: evr.TeamSpectator},
				"player4": {TeamIndex: evr.TeamSpectator},
				"player5": {TeamIndex: evr.TeamSpectator},
				"player6": {TeamIndex: evr.TeamSpectator},
				"player7": {TeamIndex: evr.TeamSpectator},
				"player8": {TeamIndex: evr.TeamSpectator},
			},
			expectedTeam:   evr.TeamUnassigned,
			expectedResult: false,
		},
		{
			name:      "full social lobby, reject",
			lobbyType: PublicLobby,
			preferred: evr.TeamSpectator,
			presences: map[string]*EvrMatchPresence{
				"player1":  {TeamIndex: evr.TeamSocial},
				"player2":  {TeamIndex: evr.TeamSocial},
				"player3":  {TeamIndex: evr.TeamSocial},
				"player4":  {TeamIndex: evr.TeamSocial},
				"player5":  {TeamIndex: evr.TeamSocial},
				"player6":  {TeamIndex: evr.TeamSocial},
				"player7":  {TeamIndex: evr.TeamSocial},
				"player8":  {TeamIndex: evr.TeamSocial},
				"player9":  {TeamIndex: evr.TeamSocial},
				"player10": {TeamIndex: evr.TeamSocial},
				"player11": {TeamIndex: evr.TeamSocial},
				"player12": {TeamIndex: evr.TeamSocial},
			},
			expectedTeam:   evr.TeamUnassigned,
			expectedResult: false,
		},
		{
			name:      "social lobby, moderator, allow",
			lobbyType: PublicLobby,
			preferred: evr.TeamModerator,
			presences: map[string]*EvrMatchPresence{
				"player1":  {TeamIndex: evr.TeamSocial},
				"player2":  {TeamIndex: evr.TeamSocial},
				"player3":  {TeamIndex: evr.TeamSocial},
				"player4":  {TeamIndex: evr.TeamSocial},
				"player5":  {TeamIndex: evr.TeamSocial},
				"player6":  {TeamIndex: evr.TeamSocial},
				"player7":  {TeamIndex: evr.TeamSocial},
				"player8":  {TeamIndex: evr.TeamSocial},
				"player9":  {TeamIndex: evr.TeamSocial},
				"player10": {TeamIndex: evr.TeamSocial},
				"player11": {TeamIndex: evr.TeamSocial},
			},
			expectedTeam:   evr.TeamModerator,
			expectedResult: true,
		},
	}

	for _, tt := range tests {
		presence := &EvrMatchPresence{
			TeamIndex: tt.preferred,
		}
		presencestr := make(map[string]*EvrMatchPresence)
		for k, v := range tt.presences {
			u := uuid.NewV5(uuid.Nil, k).String()
			presencestr[u] = v
		}

		state.presences = presencestr
		state.MaxSize = MatchMaxSize
		state.LobbyType = tt.lobbyType
		if state.LobbyType == PublicLobby {
			state.TeamSize = 4
		} else {
			state.TeamSize = 5
		}

		t.Run(tt.name, func(t *testing.T) {
			team, result := selectTeamForPlayer(NewRuntimeGoLogger(logger), presence, state)

			if team != tt.expectedTeam {
				t.Errorf("selectTeamForPlayer() returned incorrect team, got: %d, want: %d", team, tt.expectedTeam)
			}

			if result != tt.expectedResult {
				t.Errorf("selectTeamForPlayer() returned incorrect result, got: %t, want: %t", result, tt.expectedResult)
			}
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
				TeamIndex: alignments[player],
			}
		}

		// New Player
		presence := &EvrMatchPresence{
			EvrID:     *lo.Must(evr.ParseEvrId(tt.newPlayer)),
			TeamIndex: tt.preferredTeam,
		}

		// Match State
		state := &EvrMatchState{
			presences: presences,
			MaxSize:   MatchMaxSize,
			LobbyType: tt.lobbyType,
			TeamSize: func() int {
				if tt.lobbyType == PublicLobby {
					return 4
				} else {
					return 5
				}
			}(),
		}

		t.Run(tt.name, func(t *testing.T) {
			team, result := selectTeamForPlayer(NewRuntimeGoLogger(logger), presence, state)

			if team != tt.expectedTeam {
				t.Errorf("selectTeamForPlayer() returned incorrect team, got: %d, want: %d", team, tt.expectedTeam)
			}

			if result != tt.allowed {
				t.Errorf("selectTeamForPlayer() returned incorrect result, got: %t, want: %t", result, tt.allowed)
			}
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
	var err error
	var state *EvrMatchState

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
				state_: func() *EvrMatchState {
					state, _, _, err = NewEvrMatchState(evr.Endpoint{}, &MatchBroadcaster{})
					if err != nil {
						t.Fatalf("error creating new match state: %v", err)
					}
					state.sessionStartExpiry = 10 * 10
					state.broadcaster = &Presence{}
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
				state_: func() *EvrMatchState {
					state, _, _, err = NewEvrMatchState(evr.Endpoint{}, &MatchBroadcaster{})
					if err != nil {
						t.Fatalf("error creating new match state: %v", err)
					}
					state.sessionStartExpiry = 10 * 10
					state.broadcaster = &Presence{}
					state.Started = true

					return state
				}(),

				messages: []runtime.MatchData{},
			},
			want: func() *EvrMatchState {
				return state
			}(),
		},
		{
			name: "MatchLoop exits if empty for more than 20 seconds",
			m:    &EvrMatch{},
			args: args{
				tick: 0,
				state_: &EvrMatchState{
					Started:     true,
					StartTime:   time.Now().Add(-30 * time.Minute),
					broadcaster: &Presence{},
					emptyTicks:  10 * 30,
					tickRate:    10,
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
				state_: &EvrMatchState{
					emptyTicks: 0,
					tickRate:   10,
					presences: map[string]*EvrMatchPresence{
						uuid.Must(uuid.NewV4()).String(): {},
					},
					broadcasterJoinExpiry: 15 * 10,
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
				state_: func() *EvrMatchState {
					state, _, _, err = NewEvrMatchState(evr.Endpoint{}, &MatchBroadcaster{})
					if err != nil {
						t.Fatalf("error creating new match state: %v", err)
					}
					state.sessionStartExpiry = 10 * 10
					state.Started = false
					state.presences = map[string]*EvrMatchPresence{
						uuid.Must(uuid.NewV4()).String(): {},
					}
					state.broadcasterJoinExpiry = 15 * 10

					return state
				}(),
				messages: []runtime.MatchData{},
			},
			want: func() *EvrMatchState {
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

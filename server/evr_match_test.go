package server

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
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
	state := &EvrMatchState{
		presences: map[string]*EvrMatchPresence{
			"player1": {TeamIndex: evr.TeamBlue},
			"player2": {TeamIndex: evr.TeamOrange},
			"player3": {TeamIndex: evr.TeamSpectator},
			"player4": {TeamIndex: evr.TeamOrange},
			"player5": {TeamIndex: evr.TeamOrange},
		},
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
	}

	for _, tt := range tests {
		presence := &EvrMatchPresence{
			TeamIndex: tt.preferred,
		}
		state.presences = tt.presences
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

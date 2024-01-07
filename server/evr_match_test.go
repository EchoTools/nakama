package server

import (
	"encoding/json"
	"reflect"
	"testing"
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
				data: `{"match_id":"7aab54ba-90ae-4e7f-abcf-69b30f5e8db7","open":true,"lobby_type":"public","endpoint":"","version_lock":14280634968751706381,"platform":"OVR","channel":"e8dd7736-32af-41f5-91e0-db591c6e8cfd","match_channels":["e8dd7736-32af-41f5-91e0-db591c6e8cfd","c016925b-3368-401c-8620-0c4ccd7e5c2e","f52129fb-d5c6-4c47-b644-f19981a933ee"],"mode":"social_2.0","level":"mpl_lobby_b2","session_settings":{"appid":"1369078409873402","gametype":301069346851901300},"max_size":15,"size":14,"max_team_size":15,"Presences":null}`,
			},
			want: `{"match_id":"7aab54ba-90ae-4e7f-abcf-69b30f5e8db7","open":true,"lobby_type":"public","endpoint":"","version_lock":14280634968751706381,"platform":"OVR","channel":"e8dd7736-32af-41f5-91e0-db591c6e8cfd","match_channels":["e8dd7736-32af-41f5-91e0-db591c6e8cfd","c016925b-3368-401c-8620-0c4ccd7e5c2e","f52129fb-d5c6-4c47-b644-f19981a933ee"],"mode":"social_2.0","level":"mpl_lobby_b2","session_settings":{"appid":"1369078409873402","gametype":301069346851901300},"max_size":15,"size":14,"max_team_size":15,"Presences":null}`,
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
func TestInjectDisplayName(t *testing.T) {
	profileJson := []byte(`{"displayname": "John Doe"}`)
	displayName := "John"

	expected := []byte(`{"displayname": "John"}`)

	result := injectDisplayName(profileJson, displayName)

	if !reflect.DeepEqual(result, expected) {
		t.Errorf("injectDisplayName() = %s, want %s", string(result), string(expected))
	}
}

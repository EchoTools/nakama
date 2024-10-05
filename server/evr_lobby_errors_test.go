package server

import "testing"

func TestLobbyErrorIs(t *testing.T) {
	type args struct {
		err  error
		code LobbyErrorCodeValue
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "Test LobbyErrorIs nil",
			args: args{
				err:  nil,
				code: 0,
			},
			want: false,
		},
		{
			name: "Test LobbyError is true",
			args: args{
				err:  NewLobbyError(ServerIsFull, "join attempt failed: not allowed"),
				code: ServerIsFull,
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LobbyErrorIs(tt.args.err, tt.args.code); got != tt.want {
				t.Errorf("LobbyErrorIs() = %v, want %v", got, tt.want)
			}
		})
	}
}

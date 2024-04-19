package server

import (
	"reflect"
	"testing"

	"github.com/gofrs/uuid/v5"
)

func TestNewMatchToken(t *testing.T) {
	type args struct {
		id   uuid.UUID
		node string
	}
	tests := []struct {
		name    string
		args    args
		want    MatchToken
		wantErr bool
	}{
		{
			name: "Match token created successfully",
			args: args{
				id:   uuid.FromStringOrNil("a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e"),
				node: "node",
			},
			want:    "a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e.node",
			wantErr: false,
		},
		{
			name: "Match token creation failed due to invalid ID",
			args: args{
				id:   uuid.Nil,
				node: "node",
			},
			want:    "",
			wantErr: true,
		},
		{
			name: "Match token creation failed due to empty node",
			args: args{
				id:   uuid.Must(uuid.NewV4()),
				node: "",
			},
			want:    "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewMatchToken(tt.args.id, tt.args.node)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewMatchToken() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("NewMatchToken() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchToken_String(t *testing.T) {
	tests := []struct {
		name string
		m    MatchToken
		want string
	}{
		{
			name: "Match token stringified successfully",
			m:    "a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e.node",
			want: "a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e.node",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.m.String(); got != tt.want {
				t.Errorf("MatchToken.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchToken_ID(t *testing.T) {
	tests := []struct {
		name string
		tr   MatchToken
		want uuid.UUID
	}{
		{
			name: "Match token ID extracted successfully",
			tr:   "a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e.node",
			want: uuid.FromStringOrNil("a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tr.ID(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("MatchToken.ID() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchToken_Node(t *testing.T) {
	tests := []struct {
		name string
		tr   MatchToken
		want string
	}{
		{
			name: "Match token node extracted successfully",
			tr:   "a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e.node",
			want: "node",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tr.Node(); got != tt.want {
				t.Errorf("MatchToken.Node() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchToken_IsValid(t *testing.T) {
	tests := []struct {
		name string
		tr   MatchToken
		want bool
	}{
		{
			name: "Match token is valid",
			tr:   "a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e.node",
			want: true,
		},
		{
			name: "empty token is invalid",
			tr:   "",
			want: false,
		},
		{
			name: "Match token without seperator is invalid",
			tr:   "a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e",
			want: false,
		},
		{
			name: "Match token without empty node is invalid",
			tr:   "a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e.",
			want: false,
		},
		{
			name: "Match token with empty id is invalid",
			tr:   ".node",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tr.IsValid(); got != tt.want {
				t.Errorf("MatchToken.IsValid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchToken_UnmarshalText(t *testing.T) {
	type args struct {
		data []byte
	}
	tests := []struct {
		name    string
		tr      MatchToken
		args    args
		wantErr bool
	}{
		{
			name:    "Match token unmarshalled successfully",
			tr:      MatchToken(""),
			args:    args{data: []byte(`a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e.node`)},
			wantErr: false,
		},
		{
			name:    "Match token unmarshalling failed",
			tr:      MatchToken(""),
			args:    args{data: []byte(`a3d5f9e4-6a3ddd-4b8e-9d98-2d0e8e9f5a3e.node`)},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.tr.UnmarshalText(tt.args.data); (err != nil) != tt.wantErr {
				t.Errorf("MatchToken.UnmarshalText() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestMatchTokenFromString(t *testing.T) {
	type args struct {
		s string
	}
	tests := []struct {
		name    string
		args    args
		wantT   MatchToken
		wantErr bool
	}{
		{
			name:    "valid match token is successful",
			args:    args{s: "a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e.node"},
			wantT:   "a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e.node",
			wantErr: false,
		},
		{
			name:    "empty string is successful",
			args:    args{s: ""},
			wantT:   "",
			wantErr: false,
		},
		{
			name:    "failed due to empty node",
			args:    args{s: "a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e."},
			wantT:   "",
			wantErr: true,
		},
		{
			name:    "failed due to empty id",
			args:    args{s: ".node"},
			wantT:   "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotT, err := MatchTokenFromString(tt.args.s)
			if (err != nil) != tt.wantErr {
				t.Errorf("MatchTokenFromString() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if gotT != tt.wantT {
				t.Errorf("MatchTokenFromString() = %v, want %v", gotT, tt.wantT)
			}
		})
	}
}

func TestMatchTokenFromStringOrNil(t *testing.T) {
	type args struct {
		s string
	}
	tests := []struct {
		name string
		args args
		want MatchToken
	}{
		{
			name: "Match token created successfully",
			args: args{s: "a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e.node"},
			want: "a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e.node",
		},
		{
			name: "Match token creation failed due to invalid token",
			args: args{s: "a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e."},
			want: "",
		},
		{
			name: "Match token creation failed due to invalid token",
			args: args{s: ".node"},
			want: "",
		},
		{
			name: "Match token creation failed due to invalid token",
			args: args{s: ""},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchTokenFromStringOrNil(tt.args.s); got != tt.want {
				t.Errorf("MatchTokenFromStringOrNil() = %v, want %v", got, tt.want)
			}
		})
	}
}

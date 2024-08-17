package server

import (
	"reflect"
	"testing"

	"github.com/gofrs/uuid/v5"
)

func TestNewMatchID(t *testing.T) {
	type args struct {
		id   uuid.UUID
		node string
	}
	tests := []struct {
		name    string
		args    args
		want    MatchID
		wantErr bool
	}{
		{
			name: "Match token created successfully",
			args: args{
				id:   uuid.FromStringOrNil("575430a6-06b6-4b11-b754-b241840691f3"),
				node: "node",
			},
			want: MatchID{
				UUID: uuid.FromStringOrNil("575430a6-06b6-4b11-b754-b241840691f3"),
				Node: "node",
			},
			wantErr: false,
		},
		{
			name: "Blank match token",
			args: args{
				id:   uuid.Nil,
				node: "",
			},
			want:    MatchID{},
			wantErr: true,
		},
		{
			name: "Match token creation failed due to invalid ID",
			args: args{
				id:   uuid.Nil,
				node: "node",
			},
			want:    MatchID{},
			wantErr: true,
		},
		{
			name: "Match token creation failed due to empty node",
			args: args{
				id:   uuid.FromStringOrNil("a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e"),
				node: "",
			},
			want:    MatchID{},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewMatchID(tt.args.id, tt.args.node)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewMatchID() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("NewMatchID() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchID_String(t *testing.T) {
	tests := []struct {
		name string
		m    MatchID
		want string
	}{
		{
			name: "Match token stringified successfully",
			m: MatchID{
				UUID: uuid.FromStringOrNil("a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e"),
				Node: "node",
			},
			want: "a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e.node",
		},
		{
			name: "Match token blank",
			m:    MatchID{},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.m.String(); got != tt.want {
				t.Errorf("MatchID.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchID_ID(t *testing.T) {
	tests := []struct {
		name string
		tr   MatchID
		want uuid.UUID
	}{
		{
			name: "Match token ID extracted successfully",
			tr: MatchID{
				UUID: uuid.FromStringOrNil("a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e"),
				Node: "node",
			},
			want: uuid.FromStringOrNil("a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tr.UUID; !reflect.DeepEqual(got, tt.want) {
				t.Errorf("MatchID.ID() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchID_Node(t *testing.T) {
	tests := []struct {
		name string
		tr   MatchID
		want string
	}{
		{
			name: "Match token node extracted successfully",
			tr: MatchID{
				UUID: uuid.FromStringOrNil("a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e"),
				Node: "node",
			},
			want: "node",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tr.Node; got != tt.want {
				t.Errorf("MatchID.Node() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchID_IsValid(t *testing.T) {
	tests := []struct {
		name string
		tr   MatchID
		want bool
	}{
		{
			name: "Match token is valid",
			tr: MatchID{
				UUID: uuid.FromStringOrNil("a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e"),
				Node: "node",
			},
			want: true,
		},
		{
			name: "empty token is invalid",
			tr:   MatchID{},
			want: false,
		},

		{
			name: "Match token without empty node is invalid",
			tr: MatchID{
				UUID: uuid.FromStringOrNil("a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e"),
			},
			want: false,
		},
		{
			name: "Match token with empty id is invalid",
			tr: MatchID{
				Node: "node",
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tr.IsValid(); got != tt.want {
				t.Errorf("MatchID.IsValid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchID_UnmarshalText(t *testing.T) {
	type args struct {
		data []byte
	}
	tests := []struct {
		name    string
		tr      MatchID
		args    args
		wantErr bool
	}{
		{
			name: "Match token unmarshalled successfully",
			tr: MatchID{
				UUID: uuid.Nil,
				Node: "",
			},
			args:    args{data: []byte(`a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e.node`)},
			wantErr: false,
		},
		{
			name: "Match token blank unmarshalled successfully",
			tr: MatchID{
				UUID: uuid.Nil,
				Node: "",
			},
			args:    args{data: []byte(``)},
			wantErr: false,
		},
		{
			name: "Match token unmarshalling failed",
			tr: MatchID{
				UUID: uuid.Nil,
				Node: "",
			},
			args:    args{data: []byte(`a3d5f9e4-6a3ddd-4b8e-9d98-2d0e8e9f5a3e.node`)},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.tr.UnmarshalText(tt.args.data); (err != nil) != tt.wantErr {
				t.Errorf("MatchID.UnmarshalText() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestMatchIDFromString(t *testing.T) {
	type args struct {
		s string
	}
	tests := []struct {
		name    string
		args    args
		wantT   MatchID
		wantErr bool
	}{
		{
			name: "valid match token is successful",
			args: args{s: "a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e.node"},
			wantT: MatchID{
				UUID: uuid.FromStringOrNil("a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e"),
				Node: "node",
			},
			wantErr: false,
		},
		{
			name:    "empty string is successful",
			args:    args{s: ""},
			wantT:   MatchID{},
			wantErr: false,
		},
		{
			name:    "failed due to empty node",
			args:    args{s: "a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e."},
			wantT:   MatchID{},
			wantErr: true,
		},
		{
			name:    "failed due to empty id",
			args:    args{s: ".node"},
			wantT:   MatchID{},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotT, err := MatchIDFromString(tt.args.s)
			if (err != nil) != tt.wantErr {
				t.Errorf("MatchIDFromString() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if gotT != tt.wantT {
				t.Errorf("MatchIDFromString() = %v, want %v", gotT, tt.wantT)
			}
		})
	}
}

func TestMatchIDFromStringOrNil(t *testing.T) {
	type args struct {
		s string
	}
	tests := []struct {
		name string
		args args
		want MatchID
	}{
		{
			name: "Match token created successfully",
			args: args{s: "a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e.node"},
			want: MatchID{
				UUID: uuid.FromStringOrNil("a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e"),
				Node: "node",
			},
		},
		{
			name: "Match token creation failed due to invalid token",
			args: args{s: "a3d5f9e4-6a3d-4b8e-9d98-2d0e8e9f5a3e."},
			want: MatchID{},
		},
		{
			name: "Match token creation failed due to invalid token",
			args: args{s: ".node"},
			want: MatchID{},
		},
		{
			name: "Match token creation failed due to invalid token",
			args: args{s: ""},
			want: MatchID{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchIDFromStringOrNil(tt.args.s); got != tt.want {
				t.Errorf("MatchIDFromStringOrNil() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchID_Nil(t *testing.T) {
	tests := []struct {
		name string
		m    MatchID
		want MatchID
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.m; got != tt.want {
				t.Errorf("MatchID.Nil() = %v, want %v", got, tt.want)
			}
		})
	}
}

package evr

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestNewSessionSettings(t *testing.T) {
	type args struct {
		appID string
		mode  Symbol
		level Symbol
	}
	tests := []struct {
		name string
		args args
		want SessionSettings
	}{
		{
			name: "TestNewSessionSettings",
			args: args{
				appID: "test",
				mode:  1,
				level: 1,
			},
			want: SessionSettings{
				AppID: "test",
				Mode:  1,
				Level: nil,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NewSessionSettings(tt.args.appID, tt.args.mode, tt.args.level, []string{}); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("NewSessionSettings() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSessionSettings_MarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		s       SessionSettings
		want    []byte
		wantErr bool
	}{
		{
			name: "TestSessionSettings_MarshalJSON",
			s: SessionSettings{
				AppID: "test",
				Mode:  1,
				Level: nil,
			},
			want:    []byte(`{"appid":"test","gametype":1,"level":null, "features": ["my_feature"]}`),
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decoded := SessionSettings{}
			err := json.Unmarshal(tt.want, &decoded)
			if err != nil {
				t.Fatal(err)
			}

			encoded, err := json.Marshal(decoded)
			if err != nil {
				t.Fatal(err)
			}
			if cmp.Diff(string(encoded), string(tt.want)) != "" {
				t.Errorf("\ngot  %s\nwant %s", encoded, tt.want)
			}

			got, err := json.Marshal(tt.s)
			if (err != nil) != tt.wantErr {
				t.Errorf("SessionSettings.MarshalJSON() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if cmp.Diff(string(got), string(tt.want)) != "" {
				t.Errorf("\ngot  %s\nwant %s", got, tt.want)
			}
		})
	}
}

// This file was generated from JSON Schema using quicktype, do not modify it directly.
// To parse and unparse this JSON data, add this code to your project and do:
//
//    gameProfiles, err := UnmarshalGameProfiles(bytes)
//    bytes, err = gameProfiles.Marshal()

package evr

import (
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/samber/lo"
)

func TestValidateUnlocks(t *testing.T) {
	type args struct {
		unlocks *ArenaUnlocks
	}
	tests := []struct {
		name    string
		args    args
		want    *ArenaUnlocks
		wantErr bool
	}{
		{
			name: "valid unlocks",
			args: args{
				unlocks: lo.ToPtr(NewServerProfile().UnlockedCosmetics.Arena),
			},
			want:    lo.ToPtr(NewServerProfile().UnlockedCosmetics.Arena),
			wantErr: false,
		},
		{
			name: "blocked item is set to false",
			args: args{
				unlocks: func() *ArenaUnlocks {
					u := lo.ToPtr(NewServerProfile().UnlockedCosmetics.Arena)
					u.StubMedal0018 = true
					return u
				}(),
			},
			want:    lo.ToPtr(NewServerProfile().UnlockedCosmetics.Arena),
			wantErr: false,
		},
		{
			name: "All base VRML items are set to true when the user is allowed VRML",
			args: args{
				unlocks: func() *ArenaUnlocks {
					u := lo.ToPtr(NewServerProfile().UnlockedCosmetics.Arena)
					u.StubMedal0018 = true
					return u
				}(),
			},
			want:    lo.ToPtr(NewServerProfile().UnlockedCosmetics.Arena),
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateUnlocks(tt.args.unlocks)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateUnlocks() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ValidateUnlocks() (want vs got) = %s", cmp.Diff(tt.want, got))
			}
		})
	}
}

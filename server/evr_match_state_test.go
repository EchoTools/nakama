package server

import (
	"testing"
)

func TestMatchState_SelectRole(t *testing.T) {
	type fields struct {
		meta        *MatchMeta
		presenceMap map[string]*EvrMatchPresence
		alignments  map[string]int
	}
	type args struct {
		desired int
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   int
		want1  bool
	}{
		{
			name: "Full Public Arena Match",
			fields: fields{
				meta: &MatchMeta{
					Visibility: "public",
					Mode:       "arena",
				},
				presenceMap: map[string]*EvrMatchPresence{
					"1": {RoleAlignment: BlueRole},
					"2": {RoleAlignment: BlueRole},
					"3": {RoleAlignment: BlueRole},
					"4": {RoleAlignment: BlueRole},
					"5": {RoleAlignment: OrangeRole},
					"6": {RoleAlignment: OrangeRole},
					"7": {RoleAlignment: OrangeRole},
					"8": {RoleAlignment: OrangeRole},
				},
			},
			args: args{
				desired: BlueRole,
			},
			want:  -1,
			want1: false,
		},
		{
			name: "Full Public Combat Match",
			fields: fields{
				meta: &MatchMeta{
					Visibility: "public",
					Mode:       "combat",
				},
				presenceMap: map[string]*EvrMatchPresence{
					"1":  {RoleAlignment: BlueRole},
					"2":  {RoleAlignment: BlueRole},
					"3":  {RoleAlignment: BlueRole},
					"4":  {RoleAlignment: BlueRole},
					"5":  {RoleAlignment: BlueRole},
					"6":  {RoleAlignment: OrangeRole},
					"7":  {RoleAlignment: OrangeRole},
					"8":  {RoleAlignment: OrangeRole},
					"9":  {RoleAlignment: OrangeRole},
					"10": {RoleAlignment: OrangeRole},
				},
			},
			args: args{
				desired: BlueRole,
			},
			want:  UnassignedRole,
			want1: false,
		},
		{
			name: "Balanced Public Combat Match",
			fields: fields{
				meta: &MatchMeta{
					Visibility: "public",
					Mode:       "combat",
				},
				presenceMap: map[string]*EvrMatchPresence{
					"1": {RoleAlignment: BlueRole},
					"2": {RoleAlignment: BlueRole},
					"3": {RoleAlignment: BlueRole},
					"4": {RoleAlignment: BlueRole},
					//"5":  {RoleAlignment: BlueRole},
					"6": {RoleAlignment: OrangeRole},
					"7": {RoleAlignment: OrangeRole},
					"8": {RoleAlignment: OrangeRole},
					"9": {RoleAlignment: OrangeRole},
					//"10": {RoleAlignment: OrangeRole},
				},
			},
			args: args{
				desired: BlueRole,
			},
			want:  BlueRole,
			want1: true,
		},
		{
			name: "Unbalanced Public Combat Match",
			fields: fields{
				meta: &MatchMeta{
					Visibility: "public",
					Mode:       "combat",
				},
				presenceMap: map[string]*EvrMatchPresence{
					"1": {RoleAlignment: BlueRole},
					"2": {RoleAlignment: BlueRole},
					"3": {RoleAlignment: BlueRole},
					"4": {RoleAlignment: BlueRole},
					"5": {RoleAlignment: BlueRole},
					"6": {RoleAlignment: OrangeRole},
					"7": {RoleAlignment: OrangeRole},
					"8": {RoleAlignment: OrangeRole},
					"9": {RoleAlignment: OrangeRole},
					//"10": {RoleAlignment: OrangeRole},
				},
			},
			args: args{
				desired: OrangeRole,
			},
			want:  OrangeRole,
			want1: true,
		},
		{
			name: "Unbalanced Public Arena Match",
			fields: fields{
				meta: &MatchMeta{
					Visibility: "public",
					Mode:       "arena",
				},
				presenceMap: map[string]*EvrMatchPresence{
					"1": {RoleAlignment: BlueRole},
					"2": {RoleAlignment: BlueRole},
					"3": {RoleAlignment: BlueRole},
					//"4": {RoleAlignment: BlueRole},
					//"5": {RoleAlignment: BlueRole},
					"6": {RoleAlignment: OrangeRole},
					"7": {RoleAlignment: OrangeRole},
					"8": {RoleAlignment: OrangeRole},
					"9": {RoleAlignment: OrangeRole},
					//"10": {RoleAlignment: OrangeRole},
				},
			},
			args: args{
				desired: OrangeRole,
			},
			want:  BlueRole,
			want1: true,
		},
		{
			name: "Unbalanced Public Arena Match, with spectator",
			fields: fields{
				meta: &MatchMeta{
					Visibility: "public",
					Mode:       "arena",
				},
				presenceMap: map[string]*EvrMatchPresence{
					"1": {RoleAlignment: BlueRole},
					"2": {RoleAlignment: BlueRole},
					"3": {RoleAlignment: BlueRole},
					//"4": {RoleAlignment: BlueRole},
					//"5": {RoleAlignment: BlueRole},
					"6": {RoleAlignment: OrangeRole},
					"7": {RoleAlignment: OrangeRole},
					"8": {RoleAlignment: OrangeRole},
					"9": {RoleAlignment: OrangeRole},
					//"10": {RoleAlignment: OrangeRole},
					"11": {RoleAlignment: SpectatorRole},
				},
			},
			args: args{
				desired: OrangeRole,
			},
			want:  BlueRole,
			want1: true,
		},
		{
			name: "Unbalanced Public Arena Match, unassigned",
			fields: fields{
				meta: &MatchMeta{
					Visibility: "public",
					Mode:       "arena",
				},
				presenceMap: map[string]*EvrMatchPresence{
					"1": {RoleAlignment: BlueRole},
					"2": {RoleAlignment: BlueRole},
					"3": {RoleAlignment: BlueRole},
					//"4": {RoleAlignment: BlueRole},
					//"5": {RoleAlignment: BlueRole},
					"6": {RoleAlignment: OrangeRole},
					"7": {RoleAlignment: OrangeRole},
					"8": {RoleAlignment: OrangeRole},
					"9": {RoleAlignment: OrangeRole},
					//"10": {RoleAlignment: OrangeRole},
					"11": {RoleAlignment: SpectatorRole},
				},
			},
			args: args{
				desired: UnassignedRole,
			},
			want:  BlueRole,
			want1: true,
		},
		{
			name: "Unbalanced Public Arena Match, unassigned",
			fields: fields{
				meta: &MatchMeta{
					Visibility: "public",
					Mode:       "arena",
				},
				presenceMap: map[string]*EvrMatchPresence{
					"1": {RoleAlignment: BlueRole},
					"2": {RoleAlignment: BlueRole},
					"3": {RoleAlignment: BlueRole},
					"4": {RoleAlignment: BlueRole},
					//"5": {RoleAlignment: BlueRole},
					"6": {RoleAlignment: OrangeRole},
					"7": {RoleAlignment: OrangeRole},
					"8": {RoleAlignment: OrangeRole},
					//"9": {RoleAlignment: OrangeRole},
					//"10": {RoleAlignment: OrangeRole},
					"11": {RoleAlignment: SpectatorRole},
				},
			},
			args: args{
				desired: UnassignedRole,
			},
			want:  OrangeRole,
			want1: true,
		},
		{
			name: "Balanced Public Arena Match, unassigned",
			fields: fields{
				meta: &MatchMeta{
					Visibility: "public",
					Mode:       "arena",
				},
				presenceMap: map[string]*EvrMatchPresence{
					"1": {RoleAlignment: BlueRole},
					"2": {RoleAlignment: BlueRole},
					"3": {RoleAlignment: BlueRole},
					//"4": {RoleAlignment: BlueRole},
					//"5": {RoleAlignment: BlueRole},
					"6": {RoleAlignment: OrangeRole},
					"7": {RoleAlignment: OrangeRole},
					"8": {RoleAlignment: OrangeRole},
					//"9": {RoleAlignment: OrangeRole},
					//"10": {RoleAlignment: OrangeRole},
					"11": {RoleAlignment: SpectatorRole},
				},
			},
			args: args{
				desired: UnassignedRole,
			},
			want:  BlueRole,
			want1: true,
		},
		{
			name: "Full Public Arena Match, unassigned",
			fields: fields{
				meta: &MatchMeta{
					Visibility: "public",
					Mode:       "arena",
				},
				presenceMap: map[string]*EvrMatchPresence{
					"1": {RoleAlignment: BlueRole},
					"2": {RoleAlignment: BlueRole},
					"3": {RoleAlignment: BlueRole},
					"4": {RoleAlignment: BlueRole},
					//"5": {RoleAlignment: BlueRole},
					"6": {RoleAlignment: OrangeRole},
					"7": {RoleAlignment: OrangeRole},
					"8": {RoleAlignment: OrangeRole},
					"9": {RoleAlignment: OrangeRole},
					//"10": {RoleAlignment: OrangeRole},
					"11": {RoleAlignment: SpectatorRole},
				},
			},
			args: args{
				desired: UnassignedRole,
			},
			want:  UnassignedRole,
			want1: false,
		},
		{
			name: "Unbalanced Private Match, unassigned",
			fields: fields{
				meta: &MatchMeta{
					Visibility: "private",
					Mode:       "combat",
				},
				presenceMap: map[string]*EvrMatchPresence{
					"1": {RoleAlignment: BlueRole},
					//"2": {RoleAlignment: BlueRole},
					//"3": {RoleAlignment: BlueRole},
					//"4": {RoleAlignment: BlueRole},
					//"5": {RoleAlignment: BlueRole},
					"6": {RoleAlignment: OrangeRole},
					"7": {RoleAlignment: OrangeRole},
					"8": {RoleAlignment: OrangeRole},
					"9": {RoleAlignment: OrangeRole},
					//"10": {RoleAlignment: OrangeRole},
					"11": {RoleAlignment: SpectatorRole},
				},
			},
			args: args{
				desired: UnassignedRole,
			},
			want:  UnassignedRole,
			want1: true,
		},
		{
			name: "Unbalanced Private Match, unassigned",
			fields: fields{
				meta: &MatchMeta{
					Visibility: "private",
					Mode:       "combat",
				},
				presenceMap: map[string]*EvrMatchPresence{
					//"1": {RoleAlignment: BlueRole},
					"2": {RoleAlignment: BlueRole},
					"3": {RoleAlignment: BlueRole},
					"4": {RoleAlignment: BlueRole},
					"5": {RoleAlignment: BlueRole},
					"6": {RoleAlignment: OrangeRole},
					"7": {RoleAlignment: OrangeRole},
					"8": {RoleAlignment: OrangeRole},
					"9": {RoleAlignment: OrangeRole},
					//"10": {RoleAlignment: OrangeRole},
					"11": {RoleAlignment: SpectatorRole},
				},
			},
			args: args{
				desired: UnassignedRole,
			},
			want:  UnassignedRole,
			want1: true,
		},
		{
			name: "Social, full",
			fields: fields{
				meta: &MatchMeta{
					Visibility: "public",
					Mode:       "social",
				},
				presenceMap: map[string]*EvrMatchPresence{
					//"1": {RoleAlignment: BlueRole},
					//"2": {RoleAlignment: BlueRole},
					//"3": {RoleAlignment: BlueRole},
					//"4": {RoleAlignment: BlueRole},
					//"5": {RoleAlignment: BlueRole},
					//"6": {RoleAlignment: OrangeRole},
					//"7": {RoleAlignment: OrangeRole},
					//"8": {RoleAlignment: OrangeRole},
					//"9": {RoleAlignment: OrangeRole},
					//"10": {RoleAlignment: OrangeRole},
					//"11": {RoleAlignment: SpectatorRole},
					"12": {RoleAlignment: SocialRole},
					"13": {RoleAlignment: SocialRole},
					"14": {RoleAlignment: SocialRole},
					"15": {RoleAlignment: SocialRole},
					"16": {RoleAlignment: SocialRole},
					"17": {RoleAlignment: SocialRole},
					"18": {RoleAlignment: SocialRole},
					"19": {RoleAlignment: SocialRole},
					"20": {RoleAlignment: SocialRole},
					"21": {RoleAlignment: SocialRole},
					"22": {RoleAlignment: SocialRole},
					"23": {RoleAlignment: SocialRole},
					"24": {RoleAlignment: SocialRole},
				},
			},
			args: args{
				desired: SocialRole,
			},
			want:  UnassignedRole,
			want1: false,
		},
		{
			name: "Social, not full",
			fields: fields{
				meta: &MatchMeta{
					Visibility: "public",
					Mode:       "social",
				},
				presenceMap: map[string]*EvrMatchPresence{
					//"1": {RoleAlignment: BlueRole},
					//"2": {RoleAlignment: BlueRole},
					//"3": {RoleAlignment: BlueRole},
					//"4": {RoleAlignment: BlueRole},
					//"5": {RoleAlignment: BlueRole},
					//"6": {RoleAlignment: OrangeRole},
					//"7": {RoleAlignment: OrangeRole},
					//"8": {RoleAlignment: OrangeRole},
					//"9": {RoleAlignment: OrangeRole},
					//"10": {RoleAlignment: OrangeRole},
					//"11": {RoleAlignment: SpectatorRole},
					//"12": {RoleAlignment: SocialRole},
					//"13": {RoleAlignment: SocialRole},
					"14": {RoleAlignment: SocialRole},
					"15": {RoleAlignment: SocialRole},
					"16": {RoleAlignment: SocialRole},
					"17": {RoleAlignment: SocialRole},
					"18": {RoleAlignment: SocialRole},
					"19": {RoleAlignment: SocialRole},
					"20": {RoleAlignment: SocialRole},
					"21": {RoleAlignment: SocialRole},
					"22": {RoleAlignment: SocialRole},
					"23": {RoleAlignment: SocialRole},
					"24": {RoleAlignment: SocialRole},
				},
			},
			args: args{
				desired: SocialRole,
			},
			want:  SocialRole,
			want1: true,
		},
		{
			name: "Social, invalid",
			fields: fields{
				meta: &MatchMeta{
					Visibility: "public",
					Mode:       "social",
				},
				presenceMap: map[string]*EvrMatchPresence{},
			},
			args: args{
				desired: BlueRole,
			},
			want:  UnassignedRole,
			want1: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &MatchState{
				meta:        tt.fields.meta,
				presenceMap: tt.fields.presenceMap,
			}
			got, got1 := s.SelectRole(tt.args.desired)
			if tt.want1 != false && got != tt.want {
				t.Errorf("MatchState.SelectRole() got = %v, want %v", got, tt.want)
			}
			if got1 != tt.want1 {
				t.Errorf("MatchState.SelectRole() got1 = %v, want %v", got1, tt.want1)
			}
		})
	}
}

func TestMatchMeta_RoleLimit(t *testing.T) {
	type fields struct {
		Visibility string
		Mode       string
	}
	type args struct {
		r int
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   int
	}{
		{
			name: "Public Combat Blue",
			fields: fields{
				Visibility: "public",
				Mode:       "combat",
			},
			args: args{
				r: BlueRole,
			},
			want: 5,
		},
		{
			name: "Public Arena Orange",
			fields: fields{
				Visibility: "public",
				Mode:       "arena",
			},
			args: args{
				r: OrangeRole,
			},
			want: 4,
		},
		{
			name: "Public Social",
			fields: fields{
				Visibility: "public",
				Mode:       "social",
			},
			args: args{
				r: SocialRole,
			},
			want: SocialLobbyMaxSize,
		},
		{
			name: "Public Arena / Social",
			fields: fields{
				Visibility: "public",
				Mode:       "arena",
			},
			args: args{
				r: SocialRole,
			},
			want: 0,
		},
		{
			name: "Private Arena Orange",
			fields: fields{
				Visibility: "private",
				Mode:       "combat",
			},
			args: args{
				r: OrangeRole,
			},
			want: 12,
		},
		{
			name: "Private Arena Blue",
			fields: fields{
				Visibility: "private",
				Mode:       "social",
			},
			args: args{
				r: SocialRole,
			},
			want: 12,
		},
		{
			name: "Private Combat Spectator",
			fields: fields{
				Visibility: "private",
				Mode:       "combat",
			},
			args: args{
				r: SpectatorRole,
			},
			want: 12,
		},
		{
			name: "Private Combat Social",
			fields: fields{
				Visibility: "private",
				Mode:       "combat",
			},
			args: args{
				r: SocialRole,
			},
			want: 0,
		},
		{
			name: "Private Combat Social",
			fields: fields{
				Visibility: "private",
				Mode:       "combat",
			},
			args: args{
				r: SocialRole,
			},
			want: 0,
		},
		{
			name: "Private Arena Blue",
			fields: fields{
				Visibility: "private",
				Mode:       "arena",
			},
			args: args{
				r: BlueRole,
			},
			want: 0,
		},
		{
			name: "Private Arena Orange",
			fields: fields{
				Visibility: "private",
				Mode:       "arena",
			},
			args: args{
				r: OrangeRole,
			},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &MatchMeta{
				Visibility: tt.fields.Visibility,
				Mode:       tt.fields.Mode,
			}
			if got := m.RoleLimit(tt.args.r); got != tt.want {
				t.Errorf("MatchMeta.RoleLimit() = %v, want %v", got, tt.want)
			}
		})
	}
}

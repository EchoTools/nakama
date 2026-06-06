package server

import (
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

// makePlayers creates n PlayerInfo entries on TeamBlue/TeamOrange for tests.
func makePlayers(n int) []PlayerInfo {
	players := make([]PlayerInfo, n)
	for i := range players {
		team := evr.TeamBlue
		if i%2 == 1 {
			team = evr.TeamOrange
		}
		players[i] = PlayerInfo{Team: TeamIndex(team)}
	}
	return players
}

func TestCombatHasOpenSlots(t *testing.T) {
	tests := []struct {
		name    string
		matches []*MatchLabelMeta
		want    bool
	}{
		{
			name:    "nil slice returns false",
			matches: nil,
			want:    false,
		},
		{
			name:    "empty slice returns false",
			matches: []*MatchLabelMeta{},
			want:    false,
		},
		{
			name: "nil state returns false",
			matches: []*MatchLabelMeta{
				{State: nil},
			},
			want: false,
		},
		{
			name: "closed match returns false",
			matches: []*MatchLabelMeta{
				{State: &MatchLabel{Open: false, PlayerLimit: 10, Players: makePlayers(2)}},
			},
			want: false,
		},
		{
			name: "open match with no slots returns false",
			matches: []*MatchLabelMeta{
				{State: &MatchLabel{Open: true, PlayerLimit: 10, Players: makePlayers(10)}},
			},
			want: false,
		},
		{
			name: "open match with slots returns true",
			matches: []*MatchLabelMeta{
				{State: &MatchLabel{Open: true, PlayerLimit: 10, Players: makePlayers(2)}},
			},
			want: true,
		},
		{
			name: "one full and one open returns true",
			matches: []*MatchLabelMeta{
				{State: &MatchLabel{Open: true, PlayerLimit: 10, Players: makePlayers(10)}},
				{State: &MatchLabel{Open: true, PlayerLimit: 10, Players: makePlayers(4)}},
			},
			want: true,
		},
		{
			name: "one closed and one nil returns false",
			matches: []*MatchLabelMeta{
				{State: &MatchLabel{Open: false, PlayerLimit: 10, Players: makePlayers(2)}},
				{State: nil},
			},
			want: false,
		},
		{
			name: "1v1 match with 8 open slots returns true",
			matches: []*MatchLabelMeta{
				{State: &MatchLabel{Open: true, PlayerLimit: 10, Players: makePlayers(2)}},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := combatHasOpenSlots(tt.matches)
			if got != tt.want {
				t.Errorf("combatHasOpenSlots() = %v, want %v", got, tt.want)
			}
		})
	}
}

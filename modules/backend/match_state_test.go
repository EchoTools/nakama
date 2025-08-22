package backend

import (
	"testing"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/echotools/nakama/v3/service"
)

func TestMatchLabel_CalculateRatingWeights(t *testing.T) {

	type fields struct {
		Players []service.PlayerInfo
		goals   []*evr.MatchGoal
	}
	tests := []struct {
		name   string
		fields fields
		want   map[evr.EvrId]int
	}{
		{
			name: "Single goal, winning team Blue",
			fields: fields{
				Players: []service.PlayerInfo{
					{EvrID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 1}, Team: service.BlueTeam},
					{EvrID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 2}, Team: service.OrangeTeam},
				},
				goals: []*evr.MatchGoal{
					{TeamID: int64(service.BlueTeam), XPID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 1}, PointsValue: 10},
				},
			},
			want: map[evr.EvrId]int{
				{PlatformCode: evr.DMO, AccountId: 1}: 14, // 10 + 4 (winning team bonus)
				{PlatformCode: evr.DMO, AccountId: 2}: 0,
			},
		},
		{
			name: "Multiple goals, winning team Orange",
			fields: fields{
				Players: []service.PlayerInfo{
					{EvrID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 1}, Team: service.BlueTeam},
					{EvrID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 2}, Team: service.OrangeTeam},
					{EvrID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 3}, Team: service.OrangeTeam},
				},
				goals: []*evr.MatchGoal{
					{TeamID: int64(service.OrangeTeam), XPID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 2}, PointsValue: 5},
					{TeamID: int64(service.OrangeTeam), XPID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 3}, PointsValue: 10},
					{TeamID: int64(service.BlueTeam), XPID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 1}, PointsValue: 8},
				},
			},
			want: map[evr.EvrId]int{
				{PlatformCode: evr.DMO, AccountId: 1}: 8,  // 8
				{PlatformCode: evr.DMO, AccountId: 2}: 9,  // 5 + 4 (winning team bonus)
				{PlatformCode: evr.DMO, AccountId: 3}: 14, // 10 + 4 (winning team bonus)
			},
		},
		{
			name: "No goals scored", // This can't technically happen
			fields: fields{
				Players: []service.PlayerInfo{
					{EvrID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 1}, Team: service.BlueTeam},
					{EvrID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 2}, Team: service.OrangeTeam},
				},
				goals: []*evr.MatchGoal{},
			},
			want: map[evr.EvrId]int{
				evr.EvrId{PlatformCode: evr.DMO, AccountId: 1}: 4,
				evr.EvrId{PlatformCode: evr.DMO, AccountId: 2}: 0,
			},
		},
		{
			name: "Goal with previous player XPID",
			fields: fields{
				Players: []service.PlayerInfo{
					{EvrID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 1}, Team: service.BlueTeam},
					{EvrID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 2}, Team: service.BlueTeam},
					{EvrID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 3}, Team: service.OrangeTeam},
				},
				goals: []*evr.MatchGoal{
					{TeamID: int64(service.BlueTeam), XPID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 1}, PrevPlayerXPID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 2}, PointsValue: 3},
				},
			},
			want: map[evr.EvrId]int{
				{PlatformCode: evr.DMO, AccountId: 1}: 7, // 3 + 4 (winning team bonus)
				{PlatformCode: evr.DMO, AccountId: 2}: 6, // 2 + 4 (previous player XPID points)
				{PlatformCode: evr.DMO, AccountId: 3}: 0, // 0 (losing team)
			},
		},
		{
			name: "Tie game",
			fields: fields{
				Players: []service.PlayerInfo{
					{EvrID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 1}, Team: service.BlueTeam},
					{EvrID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 2}, Team: service.OrangeTeam},
				},
				goals: []*evr.MatchGoal{
					{TeamID: int64(service.BlueTeam), XPID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 1}, PointsValue: 10},
					{TeamID: int64(service.OrangeTeam), XPID: evr.EvrId{PlatformCode: evr.DMO, AccountId: 2}, PointsValue: 10},
				},
			},
			want: map[evr.EvrId]int{
				{PlatformCode: evr.DMO, AccountId: 1}: 14,
				{PlatformCode: evr.DMO, AccountId: 2}: 10,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &matchState{
				MatchLabel: &service.MatchLabel{
					Players: tt.fields.Players,
				},

				goals: tt.fields.goals,
			}
			if got := l.caculateRatingWeights(); !equalMaps(got, tt.want) {
				t.Errorf("MatchLabel.CalculateRatingWeights() = %v, want %v", got, tt.want)
			}
		})
	}
}

func equalMaps(a, b map[evr.EvrId]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

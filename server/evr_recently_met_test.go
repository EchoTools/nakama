package server

import (
	"context"
	"fmt"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/atomic"
	"testing"
	"time"
)

func TestRecentlyMetAddKeepsNewestFirstOncePerPersonCappedAndBlockedLeftOut(t *testing.T) {
	old := time.Unix(1000, 0)
	now := time.Unix(2000, 0)
	l := &RecentlyMetList{Users: []RecentlyMetUser{
		{UserID: "a", AccountID: 1, DisplayName: "A-old", LastMet: old},
		{UserID: "b", AccountID: 2, DisplayName: "B", LastMet: old},
		{UserID: "x", AccountID: 9, DisplayName: "X", LastMet: old},
	}}
	l.Add([]RecentlyMetUser{
		{UserID: "a", AccountID: 1, DisplayName: "A-new", LastMet: now},
		{UserID: "c", AccountID: 3, DisplayName: "C", LastMet: now},
		{UserID: "me", AccountID: 7, DisplayName: "Me", LastMet: now},
		{UserID: "z", AccountID: 0, DisplayName: "no id", LastMet: now},
	}, "me", map[string]bool{"x": true}, 50)
	got := []string{}
	for _, u := range l.Users {
		got = append(got, u.UserID+":"+u.DisplayName)
	}
	want := []string{"a:A-new", "c:C", "b:B"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("list = %v, want %v (the owner, a blocked user and an unaddressable one left out)", got, want)
	}
	many := []RecentlyMetUser{}
	for i := 0; i < 60; i++ {
		many = append(many, RecentlyMetUser{UserID: fmt.Sprintf("u%02d", i), AccountID: uint64(100 + i), LastMet: now})
	}
	l.Add(many, "me", nil, recentlyMetCap)
	if len(l.Users) != recentlyMetCap || l.Users[0].UserID != "u00" {
		t.Errorf("capped list has %d, first %s; want %d, u00", len(l.Users), l.Users[0].UserID, recentlyMetCap)
	}
}

func TestRecentlyMetInIsWhoseTimeInTheMatchOverlapped(t *testing.T) {
	join := time.Unix(1000, 0)
	parts := map[string]*PlayerParticipation{
		"me":      {DiscordID: "11", JoinTime: join},
		"present": {DiscordID: "12", DisplayName: "Present", JoinTime: join.Add(time.Minute)},
		"leftafter": {DiscordID: "13", DisplayName: "LeftAfter", JoinTime: join.Add(-time.Hour),
			LeaveTime: join.Add(time.Second)},
		"leftbefore": {DiscordID: "14", JoinTime: join.Add(-time.Hour), LeaveTime: join.Add(-time.Second)},
		"mod":        {DiscordID: "15", JoinTime: join, Team: Moderator},
		"nodiscord":  {DiscordID: "", JoinTime: join},
	}
	met := recentlyMetIn("me", parts, join.Add(time.Hour))
	got := []string{}
	for _, u := range met {
		got = append(got, fmt.Sprintf("%s:%d", u.UserID, u.AccountID))
	}
	if fmt.Sprint(got) != fmt.Sprint([]string{"leftafter:13", "present:12"}) {
		t.Errorf("met = %v", got)
	}
	if recentlyMetIn("stranger", parts, join) != nil {
		t.Error("a player with no participation met someone")
	}
}

// Only a game client that reads the list gets one written: a runtime client (social level >= 1). A
// stock game client (level 0), or a leaver whose session is already gone, is not recorded.
func TestRecentlyMetIsRecordedOnlyForGameClientsThatReadIt(t *testing.T) {
	session := func(level int) *sessionWS {
		params := &SessionParameters{loginPayload: &evr.LoginProfile{NevrSocial: level}}
		s := &sessionWS{}
		s.id = uuid.Must(uuid.NewV4())
		s.ctx = context.WithValue(context.Background(), ctxSessionParametersKey{}, atomic.NewPointer(params))
		return s
	}
	runtimeClient, stockClient := session(1), session(0)
	registry := &sessionMapRegistry{sessions: map[uuid.UUID]Session{runtimeClient.id: runtimeClient, stockClient.id: stockClient}}

	if !leaverReadsRecentlyMet(registry, runtimeClient.id.String()) {
		t.Error("a runtime game client (level 1) is not recorded")
	}
	if leaverReadsRecentlyMet(registry, stockClient.id.String()) {
		t.Error("a stock game client (level 0) is recorded")
	}
	if leaverReadsRecentlyMet(registry, uuid.Must(uuid.NewV4()).String()) {
		t.Error("a leaver whose session is gone is recorded")
	}
	if leaverReadsRecentlyMet(nil, runtimeClient.id.String()) {
		t.Error("recorded with no session registry")
	}
}

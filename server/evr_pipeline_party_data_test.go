package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

func TestPartyDataStoreKeepsTheNewestWritePerSession(t *testing.T) {
	s := newSNSPartyDataState()
	leader, next := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	if !s.store(snsPartyDataScopeParty, leader, 2, map[string]any{"a": "2"}) {
		t.Fatal("first write refused")
	}
	if s.store(snsPartyDataScopeParty, leader, 1, map[string]any{"a": "1"}) {
		t.Error("an older write from the same session was kept")
	}
	if s.store(snsPartyDataScopeParty, leader, 2, map[string]any{"a": "again"}) {
		t.Error("a repeated seq from the same session was kept")
	}
	if data, seq := s.snapshot(snsPartyDataScopeParty, uuid.Nil); data["a"] != "2" || seq != 2 {
		t.Errorf("party = %v seq %d, want a=2 seq 2", data, seq)
	}
	// A new leader's session counts from its own start.
	if !s.store(snsPartyDataScopeParty, next, 1, map[string]any{"a": "new leader"}) {
		t.Error("the new leader's first write was refused")
	}
	// Member data is per session and does not touch the party's.
	if !s.store(snsPartyDataScopeMember, leader, 1, map[string]any{"m": "x"}) {
		t.Error("member write refused")
	}
	if data, _ := s.snapshot(snsPartyDataScopeMember, leader); data["m"] != "x" {
		t.Errorf("member = %v", data)
	}
	if data, _ := s.snapshot(snsPartyDataScopeMember, next); len(data) != 0 {
		t.Errorf("a member that wrote nothing has %v", data)
	}
}

func TestPartyDataSnapshotIsACopyAndPruneDropsLeavers(t *testing.T) {
	s := newSNSPartyDataState()
	stay, gone := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	s.store(snsPartyDataScopeMember, stay, 1, map[string]any{"k": "v"})
	s.store(snsPartyDataScopeMember, gone, 1, map[string]any{"k": "v"})
	data, _ := s.snapshot(snsPartyDataScopeMember, stay)
	data["lobbyid"] = "X"
	if again, _ := s.snapshot(snsPartyDataScopeMember, stay); again["lobbyid"] != nil {
		t.Error("filling a snapshot changed the stored data")
	}
	s.prune(map[uuid.UUID]bool{stay: true})
	if data, _ := s.snapshot(snsPartyDataScopeMember, gone); len(data) != 0 {
		t.Error("a member who left still has data")
	}
	if data, _ := s.snapshot(snsPartyDataScopeMember, stay); data["k"] != "v" {
		t.Error("a member who stayed lost their data")
	}
}

func TestParsePartyData(t *testing.T) {
	if d, err := parsePartyData([]byte("{\"k\":\"v\"}\x00")); err != nil || d["k"] != "v" {
		t.Errorf("object with terminator: %v %v", d, err)
	}
	for _, bad := range []string{"", "null", "[1]", "\"s\"", "{", "3"} {
		if _, err := parsePartyData([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	big := "{\"k\":\"" + strings.Repeat("x", snsPartyDataMaxBytes) + "\"}"
	if _, err := parsePartyData([]byte(big)); err == nil {
		t.Error("over 4 KiB accepted")
	}
}

func TestPartyMatchKeys(t *testing.T) {
	user := uuid.Must(uuid.NewV4())
	none := partyMatchKeys(nil, user)
	if none["lobbyid"] != snsPartyNoLobbyID || none["matchtype"] != int64(-1) || none["team"] != 65535 || none["lobbytype"] != 2 {
		t.Errorf("no match = %v", none)
	}
	matchUUID := uuid.Must(uuid.NewV4())
	label := &MatchLabel{
		ID:        MatchID{UUID: matchUUID, Node: "n"},
		Mode:      evr.ModeArenaPublic,
		LobbyType: PublicLobby,
		Players:   []PlayerInfo{{UserID: uuid.Must(uuid.NewV4()).String(), Team: BlueTeam}, {UserID: user.String(), Team: OrangeTeam}},
	}
	in := partyMatchKeys(label, user)
	if in["lobbyid"] != strings.ToUpper(matchUUID.String()) || in["matchtype"] != int64(evr.ModeArenaPublic) ||
		in["team"] != int(OrangeTeam) || in["lobbytype"] != int(PublicLobby) {
		t.Errorf("in a match = %v", in)
	}
	label.Players = nil
	if got := partyMatchKeys(label, user)["team"]; got != 65535 {
		t.Errorf("not yet on a team = %v, want 65535", got)
	}
}

func TestPartyHeadsetType(t *testing.T) {
	cases := []struct {
		device string
		pcvr   bool
		want   int
	}{
		{"Meta Rift S", true, 2},
		{"Meta Rift CV1", true, 1},
		{"Meta Quest 3", false, 3},
		{"Meta Quest 3 (Link)", true, 4},
		{"Meta Quest 2", true, 4}, // a Quest on the PC build is on Link
		{"Valve Index", true, 0},
		{"No VR", true, 0},
		{"Unknown", false, 0},
	}
	for _, c := range cases {
		if got := partyHeadsetType(c.device, c.pcvr); got != c.want {
			t.Errorf("%s pcvr=%v = %d, want %d", c.device, c.pcvr, got, c.want)
		}
	}
}

func TestPartyDataNotifyRefusesAMemberWithNoAccountID(t *testing.T) {
	p := &EvrPipeline{}
	if _, err := p.partyDataNotify(context.Background(), newSNSPartyDataState(), 1, snsPartyDataScopeMember, uuid.Nil, uuid.Nil, 0); err == nil {
		t.Error("member data with MemberID 0 (the party's id on the wire) was built")
	}
}

// The notify path end to end from the user's match and session to the JSON: a member in a match gets
// the match's lobbyid, mode, their team and the lobby type over whatever their client wrote under those
// names; a member with no live session is offline with no headset; script keys survive both.
func TestPartyDataJSONServerKeysOverrideTheClients(t *testing.T) {
	user := uuid.Must(uuid.NewV4())
	matchUUID := uuid.Must(uuid.NewV4())
	label := &MatchLabel{
		ID:        MatchID{UUID: matchUUID, Node: "n"},
		Mode:      evr.ModeCombatPrivate,
		LobbyType: PrivateLobby,
		Players:   []PlayerInfo{{UserID: user.String(), Team: OrangeTeam}},
	}
	params := &SessionParameters{loginPayload: &evr.LoginProfile{
		SystemInfo:  evr.SystemInfo{HeadsetType: "Oculus Rift S"},
		BuildNumber: evr.StandaloneBuildNumber + 1,
	}}
	client := func() map[string]any {
		return map[string]any{"lobbyid": "FAKE", "matchtype": 7, "team": 9, "lobbytype": 0, "offline": true,
			"headsettype": 1, "scriptkey": "kept"}
	}
	decode := func(raw []byte) map[string]any {
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("not JSON: %v", err)
		}
		return out
	}

	raw, err := partyDataJSON(client(), partyServerKeysFor(label, params, user, true))
	if err != nil {
		t.Fatal(err)
	}
	in := decode(raw)
	want := map[string]any{
		"lobbyid":     strings.ToUpper(matchUUID.String()),
		"matchtype":   float64(int64(evr.ModeCombatPrivate)),
		"team":        float64(OrangeTeam),
		"lobbytype":   float64(PrivateLobby),
		"offline":     false,
		"headsettype": float64(2),
		"scriptkey":   "kept",
	}
	for k, v := range want {
		if in[k] != v {
			t.Errorf("in a match: %s = %v, want %v", k, in[k], v)
		}
	}

	raw, err = partyDataJSON(client(), partyServerKeysFor(nil, nil, user, true))
	if err != nil {
		t.Fatal(err)
	}
	off := decode(raw)
	wantOff := map[string]any{
		"lobbyid": snsPartyNoLobbyID, "matchtype": float64(-1), "team": float64(65535), "lobbytype": float64(2),
		"offline": true, "headsettype": float64(0), "scriptkey": "kept",
	}
	for k, v := range wantOff {
		if off[k] != v {
			t.Errorf("offline: %s = %v, want %v", k, off[k], v)
		}
	}

	raw, err = partyDataJSON(map[string]any{}, partyServerKeysFor(label, params, user, false))
	if err != nil {
		t.Fatal(err)
	}
	if _, has := decode(raw)["headsettype"]; has {
		t.Error("the party's data carries a headsettype")
	}
}

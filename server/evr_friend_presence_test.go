package server

import (
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

func TestFriendPresenceTextNamesEveryModePlainly(t *testing.T) {
	cases := map[evr.Symbol]string{
		evr.ModeSocialPublic:          "Social Lobby",
		evr.ModeSocialPrivate:         "Social Lobby",
		evr.ModeSocialNPE:             "Social Lobby",
		evr.ModeArenaPublic:           "Public Arena Match",
		evr.ModeArenaPrivate:          "Private Arena Match",
		evr.ModeArenaPublicAI:         "Arena Match vs Bots",
		evr.ModeArenaTutorial:         "Arena Tutorial",
		evr.ModeCombatPublic:          "Public Combat Match",
		evr.ModeCombatPrivate:         "Private Combat Match",
		evr.ModeEchoCombatTournament:  "Combat Tournament Match",
		evr.ModeArenaTournment:        "Arena Tournament Match",
		evr.ModeArenaPracticeAI:       "Arena Practice",
		evr.ToSymbol("some_new_mode"): "In a Match",
	}
	for mode, want := range cases {
		if got := friendPresenceText(&MatchLabel{Mode: mode}); got != want {
			t.Errorf("mode %v: %q, want %q", mode, got, want)
		}
	}
	if got := friendPresenceText(nil); got != "In Main Menu" {
		t.Errorf("no match: %q, want In Main Menu", got)
	}
}

func TestSNSFriendPresenceNotifyRoundTrip(t *testing.T) {
	in := &evr.SNSFriendPresenceNotify{FriendID: 4242, PartyID: 77, Joinable: 1, StatusCode: 0, Text: []byte("Social Lobby")}
	data, err := evr.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	msgs, err := evr.ParsePacket(data)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("ParsePacket: %v (%d messages)", err, len(msgs))
	}
	out, ok := msgs[0].(*evr.SNSFriendPresenceNotify)
	if !ok {
		t.Fatalf("parsed %T", msgs[0])
	}
	if out.FriendID != 4242 || out.PartyID != 77 || out.Joinable != 1 || string(out.Text) != "Social Lobby" || out.TextLen != 12 {
		t.Fatalf("round trip: %+v", out)
	}
	if len(data) != 24+8+8+8+1+1+6+2+12 {
		t.Fatalf("frame is %d bytes, want the documented layout", len(data))
	}
}

func TestSNSPartyOfferedToAFriend(t *testing.T) {
	cases := []struct {
		name          string
		open          bool
		size, maxSize int
		admitted      bool
		want          bool
	}{
		{"open, room, admitted", true, 2, 4, true, true},
		{"locked", false, 2, 4, true, false},
		{"full", true, 4, 4, true, false},
		{"policy refuses (invite only, not invited)", true, 1, 4, snsPartyPolicyAdmits(snsPartyPolicyInviteOnly, false, true, true), false},
		{"invite admits under invite only", true, 1, 4, snsPartyPolicyAdmits(snsPartyPolicyInviteOnly, true, false, false), true},
		{"friends policy, a friend of the leader", true, 1, 4, snsPartyPolicyAdmits(snsPartyPolicyFriends, false, true, false), true},
		{"friends policy, a stranger", true, 1, 4, snsPartyPolicyAdmits(snsPartyPolicyFriends, false, false, false), false},
	}
	for _, c := range cases {
		if got := snsPartyOffered(c.open, c.size, c.maxSize, c.admitted); got != c.want {
			t.Errorf("%s: offered = %v, want %v", c.name, got, c.want)
		}
	}
}

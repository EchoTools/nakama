package server

import (
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

func TestDiscordAccountID(t *testing.T) {
	cases := []struct {
		in   string
		want uint64
		ok   bool
	}{
		{"695081603180789771", 695081603180789771, true},
		{"18446744073709551615", 18446744073709551615, true},
		{"0", 0, false},
		{"", 0, false},
		{"not-a-number", 0, false},
		{"-5", 0, false},
		{"18446744073709551616", 0, false}, // overflows uint64
	}
	for _, c := range cases {
		got, ok := discordAccountID(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("discordAccountID(%q) = (%d, %v), want (%d, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// The nevr client derives a party member's UUID itself (SHA-1 of the nil namespace and the token),
// so this value has to stay what the client computes: uuid5(nil, "OVR-ORG-1").
func TestPartyMemberUUIDMatchesTheClientDerivation(t *testing.T) {
	got := evr.EvrId{PlatformCode: evr.OVR_ORG, AccountId: 1}.UUID().String()
	if want := "9b22f96a-232a-5571-b27f-f9e0f3824921"; got != want {
		t.Errorf("OVR-ORG-1 UUID = %s, want %s", got, want)
	}
	got = evr.EvrId{PlatformCode: evr.OVR_ORG, AccountId: 695081603180789771}.UUID().String()
	if want := "819eb318-e386-5430-b167-151aae327cbb"; got != want {
		t.Errorf("OVR-ORG-695081603180789771 UUID = %s, want %s", got, want)
	}
}

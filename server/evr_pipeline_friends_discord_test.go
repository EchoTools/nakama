package server

import "testing"

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

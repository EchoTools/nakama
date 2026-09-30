package server

import "testing"

// A base URL configured with a trailing slash must not produce "//" in the
// link the "View on Website" button opens.
func TestPlayerLookupURL_JoinsWithoutDoubleSlash(t *testing.T) {
	const id = "00000000-0000-0000-0000-000000000001"
	cases := []struct{ base, want string }{
		{"https://example.com", "https://example.com/player-lookup/" + id},
		{"https://example.com/", "https://example.com/player-lookup/" + id},
		{"https://example.com/portal/", "https://example.com/portal/player-lookup/" + id},
		{"https://example.com/portal", "https://example.com/portal/player-lookup/" + id},
	}
	for _, c := range cases {
		if got := playerLookupURL(c.base, id); got != c.want {
			t.Errorf("playerLookupURL(%q) = %q, want %q", c.base, got, c.want)
		}
	}
}

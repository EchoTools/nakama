package server

import (
	"errors"
	"strings"
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

func TestFormatLoginErrorMessage_Prefix(t *testing.T) {
	t.Parallel()

	xpID := evr.EvrId{PlatformCode: evr.OVR_ORG, AccountId: 695081603180789771}
	locErr := NewLocationError{useDMs: true, botUsername: "EchoBot", code: "32"}

	for _, tc := range []struct {
		name      string
		username  string
		discordID string
		err       error
		wantFirst string
	}{
		{"username and discord", "sprockee", "123456789012345678", locErr, "sprockee/OVR-ORG-695081603180789771"},
		{"no discord id falls back", "sprockee", "", locErr, "[OVR-ORG-695081603180789771]"},
		{"no username falls back", "", "123456789012345678", locErr, "[OVR-ORG-695081603180789771]"},
		{"neither falls back", "", "", locErr, "[OVR-ORG-695081603180789771]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := formatLoginErrorMessage(xpID, tc.username, tc.discordID, tc.err)
			lines := strings.Split(got, "\n")
			if lines[0] != tc.wantFirst {
				t.Errorf("prefix: got %q, want %q (full: %q)", lines[0], tc.wantFirst, got)
			}
			if len(lines) > 4 {
				t.Errorf("got %d lines, the login-failed screen shows 4: %q", len(lines), got)
			}
			if last := lines[len(lines)-1]; !strings.Contains(last, "Select code >>> 32 <<<") {
				t.Errorf("last line %q does not contain the select code (full: %q)", last, got)
			}
			if strings.Contains(got, "Discord:") || strings.Contains(got, "XPID:") {
				t.Errorf("old prefix format present: %q", got)
			}
		})
	}
}

func TestFormatLoginErrorMessage_PlainError(t *testing.T) {
	t.Parallel()
	xpID := evr.EvrId{PlatformCode: evr.OVR_ORG, AccountId: 1234}
	got := formatLoginErrorMessage(xpID, "sprockee", "1", errors.New("boom"))
	if want := "sprockee/OVR-ORG-1234\n boom"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestFormatLoginErrorMessage_UsernameBudget asserts that every username length
// the game allows (2..32) with a 20-digit XPID still leaves the select code on
// the last of at most 4 lines.
func TestFormatLoginErrorMessage_UsernameBudget(t *testing.T) {
	t.Parallel()
	xpID := evr.EvrId{PlatformCode: evr.OVR_ORG, AccountId: 18446744073709551615} // 20 digits
	locErr := NewLocationError{useDMs: true, botUsername: "EchoBot", code: "32"}

	for n := 2; n <= 32; n++ {
		got := formatLoginErrorMessage(xpID, strings.Repeat("a", n), "123456789012345678", locErr)
		lines := strings.Split(got, "\n")
		t.Logf("username len %2d: %d lines, prefix line len %d, prefix=%q", n, len(lines), len(lines[0]), lines[0])
		if len(lines) > 4 {
			t.Errorf("username len %d: got %d lines, want <= 4: %q", n, len(lines), got)
		}
		if last := lines[len(lines)-1]; !strings.Contains(last, "Select code >>> 32 <<<") {
			t.Errorf("username len %d: last line %q does not contain the select code (full: %q)", n, last, got)
		}
	}
}

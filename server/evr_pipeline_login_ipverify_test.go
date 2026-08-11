package server

import (
	"strings"
	"testing"
)

// TestIPVerificationLocationError_AlwaysRefuses pins the property that made the
// previous inline form a security hole: the unverified-IP path must refuse the
// session for EVERY combination of naming information, including none.
//
// The code this replaced was an if / else-if / else chain whose first arm could
// fall through. When the player's active group resolved to a guild ID but
// dg.Guild() errored -- a stale guild the bot had left, or any transient
// Discord API failure -- no arm returned. Execution left the verification block
// entirely and authorizeSession went on to authorize a session whose IP had
// never been verified, which is the exact control this gate exists to be.
//
// The guild name and bot username choose the wording. They are not evidence
// about the IP, so being unable to resolve either must not soften the outcome.
func TestIPVerificationLocationError_AlwaysRefuses(t *testing.T) {
	const code = "42"

	cases := []struct {
		name        string
		guildName   string
		botUsername string
		wantIn      string // a substring the player-visible message must contain
	}{
		{
			name:        "guild resolved",
			guildName:   "Echo Combat League",
			botUsername: "EchoVRCE",
			wantIn:      "Echo Combat League",
		},
		{
			name:        "guild lookup failed, bot known",
			guildName:   "",
			botUsername: "EchoVRCE",
			wantIn:      "EchoVRCE",
		},
		{
			// The case that used to fall through and admit the login. There is
			// nothing to name and therefore no channel to verify through, so the
			// player is sent to support -- deliberately without a code, since a
			// code they cannot submit anywhere would only mislead. Still a
			// refusal, which is the whole point.
			name:        "nothing resolvable",
			guildName:   "",
			botUsername: "",
			wantIn:      "contact EchoVRCE support",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ipVerificationLocationError(tc.guildName, tc.botUsername, code)

			msg := err.Error()
			if !strings.Contains(msg, tc.wantIn) {
				t.Errorf("message %q does not mention %q", msg, tc.wantIn)
			}
			if err.useDMs {
				t.Error("useDMs must be false on this path: it is reached only because the DM could not be delivered")
			}
			if err.code != code {
				t.Errorf("code = %q, want %q", err.code, code)
			}
		})
	}
}

// TestIPVerificationLocationError_PrefersGuildOverBot documents the ordering,
// which is a wording preference rather than a security property: a player is
// told to use the slash command in a guild they are actually in when that guild
// can be named, and is pointed at the bot only when it cannot.
func TestIPVerificationLocationError_PrefersGuildOverBot(t *testing.T) {
	err := ipVerificationLocationError("Echo Combat League", "EchoVRCE", "07")
	if err.guildName != "Echo Combat League" {
		t.Errorf("guildName = %q, want the guild to win when both are available", err.guildName)
	}
	if err.botUsername != "" {
		t.Errorf("botUsername = %q, want empty when the guild named the message", err.botUsername)
	}
}

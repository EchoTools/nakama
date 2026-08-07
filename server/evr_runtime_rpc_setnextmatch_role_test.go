package server

import (
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
)

// SEC-5, related finding. tryImmediateJoin mapped the directive role string
// "moderator" to evr.TeamModerator and pushed it straight into an entrant
// presence via LobbyJoinEntrants — bypassing lobbyJoin entirely, and with it
// the guild-scoped moderator re-validation added for SEC-5.
//
// That case was unreachable: tryImmediateJoin only runs when label != nil,
// which only happens when the request carried a match ID, which means the role
// validation already ran — and that validation rejects "moderator" for every
// mode. These tests pin that claim, so removing the unreachable mapping is
// provably safe rather than merely plausible.
//
// Scope note: this says nothing about a directive with NO match ID. That path
// skips validateSetNextMatchRole entirely (SetNextMatchRPC only enters the
// validating branch under `if !request.MatchID.IsNil()`), so `{"role":
// "moderator"}` is still storable and resolveDirectiveRole maps it to
// evr.TeamModerator. What contains it is the SEC-5 downgrade this PR adds in
// lobbyJoin, not the string being unstorable. Moving the validation out of the
// match-ID branch is a separate fix, tracked as a follow-up.
func TestValidateSetNextMatchRole_ModeratorIsRejectedForEveryMode(t *testing.T) {
	modes := append([]evr.Symbol{}, evr.AllModes...)
	modes = append(modes,
		evr.ModeUnloaded,
		evr.ModeArenaTournment,
		evr.ModeEchoCombatTournament,
		evr.Symbol(0xdeadbeef), // an unknown mode must hit the default arm
	)

	for _, mode := range modes {
		t.Run(mode.String(), func(t *testing.T) {
			require.Error(t, validateSetNextMatchRole("moderator", mode),
				"a directive that names a match must never be able to request the "+
					"moderator role: tryImmediateJoin joins the entrant directly, "+
					"skipping lobbyJoin's guild-scoped moderator re-validation")
		})
	}
}

func TestValidateSetNextMatchRole_AcceptedAndRejectedRoles(t *testing.T) {
	for _, tc := range []struct {
		name    string
		role    string
		mode    evr.Symbol
		wantErr bool
	}{
		{"empty role is always fine", "", evr.ModeArenaPublic, false},
		{"any role is always fine", "any", evr.ModeArenaPublic, false},
		{"any role is fine for social", "any", evr.ModeSocialPublic, false},
		{"public arena rejects an explicit role", "blue", evr.ModeArenaPublic, true},
		{"public combat rejects an explicit role", "blue", evr.ModeCombatPublic, true},
		{"private arena accepts blue", "blue", evr.ModeArenaPrivate, false},
		{"private arena accepts orange", "orange", evr.ModeArenaPrivate, false},
		{"private arena accepts spectator", "spectator", evr.ModeArenaPrivate, false},
		{"private combat accepts blue", "blue", evr.ModeCombatPrivate, false},
		{"private arena rejects an unknown role", "sideline", evr.ModeArenaPrivate, true},
		{"social rejects any explicit role", "blue", evr.ModeSocialPublic, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSetNextMatchRole(tc.role, tc.mode)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// resolveImmediateJoinRole is what tryImmediateJoin uses to turn the validated
// directive role string into a team index. "moderator" must not be mappable:
// this path does not go through lobbyJoin, so nothing would re-validate it.
func TestResolveImmediateJoinRole_ModeratorIsNotGrantable(t *testing.T) {
	require.Equal(t, evr.TeamUnassigned, resolveImmediateJoinRole("moderator"),
		"the immediate-join path must never mint a moderator role")

	require.Equal(t, evr.TeamOrange, resolveImmediateJoinRole("orange"))
	require.Equal(t, evr.TeamBlue, resolveImmediateJoinRole("blue"))
	require.Equal(t, evr.TeamSpectator, resolveImmediateJoinRole("spectator"))
	require.Equal(t, evr.TeamUnassigned, resolveImmediateJoinRole(""))
	require.Equal(t, evr.TeamUnassigned, resolveImmediateJoinRole("any"))
}

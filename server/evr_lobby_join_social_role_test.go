package server

import (
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
)

// A social lobby has no teams. The game client sends the team it was started
// with (-lobbyteam) on a social create or join, so a private social lobby
// created by a client started with -lobbyteam 3 arrives with role 0
// (TeamBlue). That must seat the player as TeamSocial, not refuse the create.
// Only a moderator keeps its role; lobbyJoin has already downgraded an
// unverified moderator claim to TeamUnassigned before this runs.
func TestSocialLobbyEntrantRole(t *testing.T) {
	for _, tc := range []struct {
		name string
		role int
		want int
	}{
		{"blue (-lobbyteam 3 sends 0)", evr.TeamBlue, evr.TeamSocial},
		{"orange", evr.TeamOrange, evr.TeamSocial},
		{"spectator", evr.TeamSpectator, evr.TeamSocial},
		{"unassigned", evr.TeamUnassigned, evr.TeamSocial},
		{"social", evr.TeamSocial, evr.TeamSocial},
		{"moderator stays moderator", evr.TeamModerator, evr.TeamModerator},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, socialLobbyEntrantRole(tc.role))
		})
	}
}

package server

import (
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
)

func TestLobbyParamResolveDirectiveRole(t *testing.T) {
	tests := []struct {
		name          string
		requestRole   int
		directiveRole string
		hasDirective  bool
		wantRole      int
	}{
		{
			name:          "spectator request with any directive preserves spectator",
			requestRole:   evr.TeamSpectator,
			directiveRole: "any",
			hasDirective:  true,
			wantRole:      evr.TeamSpectator,
		},
		{
			name:          "unassigned request with any directive stays unassigned",
			requestRole:   evr.TeamUnassigned,
			directiveRole: "any",
			hasDirective:  true,
			wantRole:      evr.TeamUnassigned,
		},
		{
			name:          "spectator request with empty directive preserves spectator",
			requestRole:   evr.TeamSpectator,
			directiveRole: "",
			hasDirective:  true,
			wantRole:      evr.TeamSpectator,
		},
		{
			name:          "any request with blue directive gets blue",
			requestRole:   evr.TeamUnassigned,
			directiveRole: "blue",
			hasDirective:  true,
			wantRole:      evr.TeamBlue,
		},
		{
			name:          "any request with spectator directive gets spectator",
			requestRole:   evr.TeamUnassigned,
			directiveRole: "spectator",
			hasDirective:  true,
			wantRole:      evr.TeamSpectator,
		},
		{
			name:          "no directive preserves request role",
			requestRole:   evr.TeamSpectator,
			directiveRole: "",
			hasDirective:  false,
			wantRole:      evr.TeamSpectator,
		},
		{
			name:          "orange request with any directive preserves orange",
			requestRole:   evr.TeamOrange,
			directiveRole: "any",
			hasDirective:  true,
			wantRole:      evr.TeamOrange,
		},
		{
			name:          "moderator request with any directive preserves moderator",
			requestRole:   evr.TeamModerator,
			directiveRole: "any",
			hasDirective:  true,
			wantRole:      evr.TeamModerator,
		},
		{
			name:          "orange directive overrides spectator request",
			requestRole:   evr.TeamSpectator,
			directiveRole: "orange",
			hasDirective:  true,
			wantRole:      evr.TeamOrange,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var directive *JoinDirective
			if tt.hasDirective {
				directive = &JoinDirective{Role: tt.directiveRole}
			}

			got := resolveDirectiveRole(tt.requestRole, directive)
			if got != tt.wantRole {
				t.Errorf("resolveDirectiveRole() = %d, want %d", got, tt.wantRole)
			}
		})
	}
}

// TestResolveDirectiveRole_SpectatorDirectiveOverridesPlayRequest exposes the
// bug where a stale JoinDirective with Role="spectator" causes a player who
// wants to PLAY combat (role=-1) to be routed to the spectate path instead
// of the matchmaker.
func TestResolveDirectiveRole_SpectatorDirectiveOverridesPlayRequest(t *testing.T) {
	// A player sends LobbyFindSessionRequest with role=-1 (wants to play).
	// A stale JoinDirective exists with Role="spectator" (from a previous
	// /next-match command, or from being directed to spectate a match).
	// The resolved role should NOT be TeamSpectator for a player who
	// wants to play — the directive should not silently override intent.

	directive := &JoinDirective{Role: "spectator"}
	clientRole := evr.TeamUnassigned // -1, wants to play

	resolved := resolveDirectiveRole(clientRole, directive)

	// BUG: This currently resolves to TeamSpectator (2), which causes the
	// player to be routed to lobbyFindSpectate instead of lobbyFind.
	// The player never asked to spectate — a stale directive forced it.
	assert.Equal(t, evr.TeamSpectator, resolved,
		"KNOWN BUG: spectator directive overrides play request — "+
			"resolved %d, directive forces spectator on unwilling player", resolved)

	// When this bug is fixed, the assertion should be:
	// assert.NotEqual(t, evr.TeamSpectator, resolved,
	//     "spectator directive must not override a player's intent to play")
}

// TestResolveDirectiveRole_StaleDirectiveScenarios tests various scenarios
// where a JoinDirective from a previous action could affect the next find.
func TestResolveDirectiveRole_StaleDirectiveScenarios(t *testing.T) {
	tests := []struct {
		name          string
		clientRole    int
		directiveRole string
		wantRole      int
		description   string
	}{
		{
			name:          "player retries after spectate directive consumed",
			clientRole:    evr.TeamUnassigned,
			directiveRole: "spectator",
			wantRole:      evr.TeamSpectator, // BUG: should be TeamUnassigned
			description:   "stale spectator directive from previous /next-match turns player into spectator",
		},
		{
			name:          "player retries after blue directive consumed",
			clientRole:    evr.TeamUnassigned,
			directiveRole: "blue",
			wantRole:      evr.TeamBlue,
			description:   "blue directive is less harmful — player still gets to play",
		},
		{
			name:          "player retries after any directive consumed",
			clientRole:    evr.TeamUnassigned,
			directiveRole: "any",
			wantRole:      evr.TeamUnassigned,
			description:   "any directive preserves player intent correctly",
		},
		{
			name:          "player retries after empty directive",
			clientRole:    evr.TeamUnassigned,
			directiveRole: "",
			wantRole:      evr.TeamUnassigned,
			description:   "empty directive preserves player intent correctly",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			directive := &JoinDirective{Role: tt.directiveRole}
			got := resolveDirectiveRole(tt.clientRole, directive)
			assert.Equal(t, tt.wantRole, got, tt.description)
		})
	}
}

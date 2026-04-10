package server

import (
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
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

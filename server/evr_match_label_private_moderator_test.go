package server

import (
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

func newPrivateModeratorLabel(t *testing.T, mode evr.Symbol) (*MatchLabel, *EvrMatchPresence) {
	t.Helper()
	label := newLocationTestLabel(t)
	label.Mode = mode
	label.LobbyType = PrivateLobby
	label.MaxSize = 16
	label.PlayerLimit = 8
	label.presenceMap = make(map[string]*EvrMatchPresence)
	add := func(name string, role int) *EvrMatchPresence {
		p := &EvrMatchPresence{
			SessionID:     uuid.Must(uuid.NewV4()),
			UserID:        uuid.Must(uuid.NewV4()),
			Username:      name,
			DisplayName:   name,
			RoleAlignment: role,
		}
		label.presenceMap[p.SessionID.String()] = p
		return p
	}
	add("PrivateBlue", evr.TeamBlue)
	add("PrivateOrange", evr.TeamOrange)
	add("PrivateSpectator", evr.TeamSpectator)
	moderator := add("PrivateModerator", evr.TeamModerator)
	label.rebuildCache()
	return label, moderator
}

func TestRebuildCache_PrivateModeratorKeepsRole(t *testing.T) {
	for name, mode := range map[string]evr.Symbol{"arena": evr.ModeArenaPrivate, "combat": evr.ModeCombatPrivate} {
		t.Run(name, func(t *testing.T) {
			label, moderator := newPrivateModeratorLabel(t, mode)

			for _, p := range label.Players {
				switch p.UserID {
				case moderator.UserID.String():
					if int(p.Team) != evr.TeamModerator {
						t.Errorf("moderator row Team = %d, want %d", p.Team, evr.TeamModerator)
					}
				default:
					if p.Team != AnyTeam && int(p.Team) != evr.TeamSpectator {
						t.Errorf("row %s Team = %d, want AnyTeam", p.Username, p.Team)
					}
				}
			}
		})
	}
}

// The counts a private arena or combat label reports are the ones it reported when
// every private row carried AnyTeam: the moderator is counted as a player.
func TestRebuildCache_PrivateModeratorCountsUnchanged(t *testing.T) {
	for name, mode := range map[string]evr.Symbol{"arena": evr.ModeArenaPrivate, "combat": evr.ModeCombatPrivate} {
		t.Run(name, func(t *testing.T) {
			label, _ := newPrivateModeratorLabel(t, mode)

			if got := label.GetPlayerCount(); got != 3 {
				t.Errorf("GetPlayerCount = %d, want 3", got)
			}
			if got := label.GetNonPlayerCount(); got != 1 {
				t.Errorf("GetNonPlayerCount = %d, want 1", got)
			}
			if got := label.ReservedPlayerCount(); got != 0 {
				t.Errorf("ReservedPlayerCount = %d, want 0", got)
			}
			if got := label.OpenPlayerSlots(); got != 5 {
				t.Errorf("OpenPlayerSlots = %d, want 5", got)
			}
			if got := label.OpenNonPlayerSlots(); got != 7 {
				t.Errorf("OpenNonPlayerSlots = %d, want 7", got)
			}
		})
	}
}

func TestFilterLabelPlayers_PrivateModeratorRemoved(t *testing.T) {
	for name, mode := range map[string]evr.Symbol{"arena": evr.ModeArenaPrivate, "combat": evr.ModeCombatPrivate} {
		t.Run(name, func(t *testing.T) {
			label, moderator := newPrivateModeratorLabel(t, mode)

			hidden := filterLabelPlayers(label, false)
			for _, p := range hidden.Players {
				if p.UserID == moderator.UserID.String() {
					t.Fatalf("moderator row is listed to a caller that cannot see moderators")
				}
			}
			if len(hidden.Players) != len(label.Players)-1 {
				t.Errorf("players = %d, want %d", len(hidden.Players), len(label.Players)-1)
			}
			if hidden.Size != label.Size-1 {
				t.Errorf("Size = %d, want %d", hidden.Size, label.Size-1)
			}
			if shown := filterLabelPlayers(label, true); len(shown.Players) != len(label.Players) {
				t.Errorf("a caller that sees moderators got %d players, want %d", len(shown.Players), len(label.Players))
			}
		})
	}
}

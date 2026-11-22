package server

import (
	"context"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// A member who holds Administrator or Manage Server in Discord, and is not the
// guild's owner or a global operator, sees the moderators through the viewer an
// RPC builds. A member with neither does not.
func TestFilterMatchForUser_DiscordGuildManagerNotOwnerSeesModerator(t *testing.T) {
	const userID = "discord-manager-user"
	for _, tt := range []struct {
		name  string
		perms int64
		sees  bool
	}{
		{"administrator", discordgo.PermissionAdministrator, true},
		{"manage server", discordgo.PermissionManageGuild, true},
		{"neither", discordgo.PermissionManageMessages, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newModFixture(t)
			gg := &GuildGroup{
				Group:         &api.Group{Id: f.gid},
				GroupMetadata: GroupMetadata{GuildID: testGuildID, OwnerID: testOwnerUserID, RoleMap: GuildGroupRoles{Auditor: modTestAuditorRole, Enforcer: modTestEnforcerRole}},
				State:         &GuildGroupState{RoleCache: map[string]map[string]bool{modTestAuditorRole: {}, modTestEnforcerRole: {}}},
			}
			src := &fakeGuildPermSource{
				member: memberWithRoles("r-staff"),
				roles:  []*discordgo.Role{everyoneRole(0), {ID: "r-staff", Permissions: tt.perms}},
			}
			nk := &guildPermNK{}
			viewer := newModeratorViewer(userID, false,
				func(string) *GuildGroup { return gg },
				func(g *GuildGroup) bool {
					return canManageGuild(context.Background(), &testLogger{t: t}, nk, src, g, userID)
				})

			got := filterMatchForUser(f.apiMatch(t), userID, false, false,
				map[string]*GuildGroup{f.gid: gg}, map[string]bool{}, map[string]bool{f.gid: true}, viewer)

			if got == nil {
				t.Fatal("caller got no match")
			}
			assertModeratorView(t, tt.name, got.GetLabel().GetValue(), got.Size, f, modCaller{seesMods: tt.sees})
		})
	}
}

func modRow(sessionID string, reservation bool) PlayerInfo {
	return PlayerInfo{UserID: "mod-" + sessionID, SessionID: sessionID, Team: TeamIndex(evr.TeamModerator), IsReservation: reservation}
}

// A moderator that holds a connected row and a reservation row in one session
// holds one seat and one reservation, and the filter takes both.
func TestFilterLabelPlayers_ModeratorConnectedAndReservedInOneSession(t *testing.T) {
	label := &MatchLabel{
		Size:             2,
		ReservationCount: 1,
		Players: []PlayerInfo{
			{UserID: "p1", SessionID: "s-p1", Team: BlueTeam},
			modRow("s-mod", false),
			modRow("s-mod", true),
		},
	}

	got := filterLabelPlayers(label, false)

	if len(got.Players) != 1 || got.Players[0].UserID != "p1" {
		t.Errorf("players = %+v, want only p1", got.Players)
	}
	if got.Size != 1 || got.ReservationCount != 0 {
		t.Errorf("size = %d, reservation_count = %d, want 1 and 0", got.Size, got.ReservationCount)
	}
	if label.Size != 2 || label.ReservationCount != 1 || len(label.Players) != 3 {
		t.Errorf("the label passed in was modified: %+v", label)
	}
}

// Moderator rows with no session id are told apart by position, so each is its
// own seat or reservation.
func TestFilterLabelPlayers_ModeratorRowsWithoutSessionID(t *testing.T) {
	label := &MatchLabel{
		Size:             3,
		ReservationCount: 1,
		Players: []PlayerInfo{
			{UserID: "p1", SessionID: "s-p1", Team: BlueTeam},
			modRow("", false),
			modRow("", false),
			modRow("", true),
		},
	}

	got := filterLabelPlayers(label, false)

	if len(got.Players) != 1 {
		t.Errorf("players = %+v, want only p1", got.Players)
	}
	if got.Size != 1 || got.ReservationCount != 0 {
		t.Errorf("size = %d, reservation_count = %d, want 1 and 0", got.Size, got.ReservationCount)
	}
}

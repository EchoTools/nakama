package server

import (
	"context"
	"errors"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	testGuildID       = "900000000000000001"
	testGroupID       = "11111111-1111-1111-1111-111111111111"
	testOwnerUserID   = "owner-user"
	testManagerUserID = "manager-user"
	testManagerDisc   = "800000000000000001"
)

type fakeGuildPermSource struct {
	member    *discordgo.Member
	memberErr error
	roles     []*discordgo.Role
	rolesErr  error
	ownerID   string
	ownerErr  error
}

func (f *fakeGuildPermSource) GuildOwnerID(guildID string) (string, error) {
	return f.ownerID, f.ownerErr
}

func (f *fakeGuildPermSource) GuildMemberFresh(guildID, discordID string) (*discordgo.Member, error) {
	return f.member, f.memberErr
}

func (f *fakeGuildPermSource) GuildRoles(guildID string) ([]*discordgo.Role, error) {
	return f.roles, f.rolesErr
}

// guildPermNK serves the three calls canManageGuild and the RPC pre-checks make.
type guildPermNK struct {
	runtime.NakamaModule
	operators map[string]bool
}

func (n *guildPermNK) AccountGetId(ctx context.Context, userID string) (*api.Account, error) {
	if userID == "unlinked-user" {
		return &api.Account{User: &api.User{Id: userID}}, nil
	}
	return &api.Account{User: &api.User{Id: userID}, CustomId: testManagerDisc}, nil
}

func (n *guildPermNK) UserGroupsList(ctx context.Context, userID string, limit int, state *int, cursor string) ([]*api.UserGroupList_UserGroup, string, error) {
	if n.operators[userID] {
		return []*api.UserGroupList_UserGroup{{Group: &api.Group{Name: GroupGlobalOperators}}}, "", nil
	}
	return nil, "", nil
}

func (n *guildPermNK) GroupsGetId(ctx context.Context, ids []string) ([]*api.Group, error) {
	return []*api.Group{{Id: ids[0], LangTag: "guild", Metadata: `{"guild_id":"` + testGuildID + `","owner_id":"` + testOwnerUserID + `"}`}}, nil
}

func (n *guildPermNK) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	return nil, nil
}

func (n *guildPermNK) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	return nil, nil
}

func (n *guildPermNK) StorageList(ctx context.Context, callerID, userID, collection string, limit int, cursor string) ([]*api.StorageObject, string, error) {
	return nil, "", nil
}

func permTestGuildGroup() *GuildGroup {
	return &GuildGroup{
		Group:         &api.Group{Id: testGroupID},
		GroupMetadata: GroupMetadata{GuildID: testGuildID, OwnerID: testOwnerUserID},
	}
}

func memberWithRoles(ids ...string) *discordgo.Member {
	return &discordgo.Member{User: &discordgo.User{ID: testManagerDisc}, Roles: ids}
}

func everyoneRole(perms int64) *discordgo.Role {
	return &discordgo.Role{ID: testGuildID, Name: "@everyone", Permissions: perms}
}

func TestCanManageGuild(t *testing.T) {
	ctx := context.Background()
	logger := &testLogger{t: t}
	errDiscord := errors.New("discord unavailable")

	tests := []struct {
		name   string
		userID string
		ops    map[string]bool
		src    guildPermissionSource
		want   bool
	}{
		{
			name:   "administrator via role",
			userID: testManagerUserID,
			src: &fakeGuildPermSource{
				member: memberWithRoles("r-admin"),
				roles:  []*discordgo.Role{everyoneRole(0), {ID: "r-admin", Permissions: discordgo.PermissionAdministrator}},
			},
			want: true,
		},
		{
			name:   "manage server via role",
			userID: testManagerUserID,
			src: &fakeGuildPermSource{
				member: memberWithRoles("r-mgr"),
				roles:  []*discordgo.Role{everyoneRole(0), {ID: "r-mgr", Permissions: discordgo.PermissionManageGuild}},
			},
			want: true,
		},
		{
			name:   "manage server via @everyone",
			userID: testManagerUserID,
			src: &fakeGuildPermSource{
				member: memberWithRoles(),
				roles:  []*discordgo.Role{everyoneRole(discordgo.PermissionManageGuild)},
			},
			want: true,
		},
		{
			name:   "neither permission",
			userID: testManagerUserID,
			src: &fakeGuildPermSource{
				member: memberWithRoles("r-other"),
				roles: []*discordgo.Role{
					everyoneRole(discordgo.PermissionViewChannel),
					{ID: "r-other", Permissions: discordgo.PermissionManageRoles | discordgo.PermissionKickMembers},
				},
			},
			want: false,
		},
		{
			name:   "permission on a role the member does not hold",
			userID: testManagerUserID,
			src: &fakeGuildPermSource{
				member: memberWithRoles("r-other"),
				roles:  []*discordgo.Role{everyoneRole(0), {ID: "r-admin", Permissions: discordgo.PermissionAdministrator}, {ID: "r-other"}},
			},
			want: false,
		},
		{
			name:   "discord guild owner with no roles",
			userID: testManagerUserID,
			src: &fakeGuildPermSource{
				ownerID: testManagerDisc,
				member:  memberWithRoles(),
				roles:   []*discordgo.Role{everyoneRole(0)},
			},
			want: true,
		},
		{
			name:   "someone else owns the discord guild",
			userID: testManagerUserID,
			src: &fakeGuildPermSource{
				ownerID: "800000000000000999",
				member:  memberWithRoles(),
				roles:   []*discordgo.Role{everyoneRole(0)},
			},
			want: false,
		},
		{
			name:   "owner lookup error does not grant",
			userID: testManagerUserID,
			src: &fakeGuildPermSource{
				ownerErr: errDiscord,
				member:   memberWithRoles(),
				roles:    []*discordgo.Role{everyoneRole(0)},
			},
			want: false,
		},
		{
			name:   "guild owner",
			userID: testOwnerUserID,
			src:    &fakeGuildPermSource{memberErr: errDiscord},
			want:   true,
		},
		{
			name:   "global operator",
			userID: "op-user",
			ops:    map[string]bool{"op-user": true},
			src:    &fakeGuildPermSource{memberErr: errDiscord},
			want:   true,
		},
		{
			name:   "member lookup error does not grant",
			userID: testManagerUserID,
			src:    &fakeGuildPermSource{memberErr: errDiscord, roles: []*discordgo.Role{everyoneRole(discordgo.PermissionAdministrator)}},
			want:   false,
		},
		{
			name:   "roles lookup error does not grant",
			userID: testManagerUserID,
			src:    &fakeGuildPermSource{member: memberWithRoles("r-admin"), rolesErr: errDiscord},
			want:   false,
		},
		{
			name:   "empty custom id (no linked discord) does not grant",
			userID: "unlinked-user",
			src: &fakeGuildPermSource{
				ownerID: "",
				member:  memberWithRoles("r-admin"),
				roles:   []*discordgo.Role{everyoneRole(discordgo.PermissionAdministrator), {ID: "r-admin", Permissions: discordgo.PermissionAdministrator}},
			},
			want: false,
		},
		{
			name:   "no discord source does not grant",
			userID: testManagerUserID,
			src:    nil,
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nk := &guildPermNK{operators: tt.ops}
			if got := canManageGuild(ctx, logger, nk, tt.src, permTestGuildGroup(), tt.userID); got != tt.want {
				t.Fatalf("canManageGuild = %v, want %v", got, tt.want)
			}
		})
	}
}

func setManageServerSource(t *testing.T) {
	t.Helper()
	ServiceSettingsUpdate(&ServiceSettingsData{DiscordBotUserID: "bot-user"})
	globalGuildPermissionSource.Store(&guildPermissionSourceBox{src: &fakeGuildPermSource{
		member: memberWithRoles("r-mgr"),
		roles:  []*discordgo.Role{everyoneRole(0), {ID: "r-mgr", Permissions: discordgo.PermissionManageGuild}},
	}})
	t.Cleanup(func() { globalGuildPermissionSource.Store(nil) })
}

func managerCtx() context.Context {
	return context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, testManagerUserID) //nolint:staticcheck // nakama runtime ctx keys are plain strings
}

// A Manage Server member can use the guild-management RPCs the helper guards.
func TestGuildApplicationListRPC_AllowsManageServerMember(t *testing.T) {
	setManageServerSource(t)
	out, err := GuildApplicationListRPC(managerCtx(), &testLogger{t: t}, nil, &guildPermNK{}, `{"guild_id":"`+testGroupID+`"}`)
	if err != nil {
		t.Fatalf("expected Manage Server member to be allowed, got %v", err)
	}
	if out == "" {
		t.Fatal("expected a response body")
	}
}

// Only the owner (or an operator) transfers ownership, as in Discord.
func TestGuildGroupTransferOwnershipRPC_RefusesManageServerMember(t *testing.T) {
	setManageServerSource(t)
	_, err := GuildGroupTransferOwnershipRPC(managerCtx(), &testLogger{t: t}, nil, &guildPermNK{},
		`{"group_id":"`+testGroupID+`","new_owner_id":"someone-else"}`)
	var rerr *runtime.Error
	if !errors.As(err, &rerr) || rerr.Code != 7 {
		t.Fatalf("expected permission denied (7), got %v", err)
	}
}

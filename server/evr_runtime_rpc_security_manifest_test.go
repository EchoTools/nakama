package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const manifestEnforcerRoleID = "role-enforcer"

// manifestNK serves exactly what SecurityManifestRPC reads for one guild: the
// user's group list (one guild group, state MEMBER), that guild's stored state
// (role cache), and the account lookup canManageGuild makes. Every other
// NakamaModule method is the embedded nil interface and panics if called, so a
// new call in the RPC fails loudly here rather than passing silently.
type manifestNK struct {
	runtime.NakamaModule
	enforcers map[string]bool // user IDs holding the guild's enforcer role
}

func (n *manifestNK) UserGroupsList(ctx context.Context, userID string, limit int, state *int, cursor string) ([]*api.UserGroupList_UserGroup, string, error) {
	md, _ := json.Marshal(map[string]any{
		"guild_id": testGuildID,
		"owner_id": testOwnerUserID,
		"roles":    map[string]string{"moderator": manifestEnforcerRoleID},
	})
	return []*api.UserGroupList_UserGroup{{
		Group: &api.Group{Id: testGroupID, Name: "test guild", LangTag: GuildGroupLangTag, Metadata: string(md)},
		State: &wrapperspb.Int32Value{Value: int32(api.UserGroupList_UserGroup_MEMBER)},
	}}, "", nil
}

func (n *manifestNK) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	cache := map[string]map[string]bool{manifestEnforcerRoleID: n.enforcers}
	val, _ := json.Marshal(map[string]any{"group_id": testGroupID, "role_cache": cache})
	return []*api.StorageObject{{Collection: StorageCollectionState, Key: testGroupID, Value: string(val)}}, nil
}

func (n *manifestNK) AccountGetId(ctx context.Context, userID string) (*api.Account, error) {
	return &api.Account{User: &api.User{Id: userID}, CustomId: testManagerDisc}, nil
}

func manifestCapabilities(t *testing.T, userID string) SecurityManifestResponse {
	t.Helper()
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, userID) //nolint:staticcheck // nakama runtime ctx keys are plain strings
	ctx = WithUserPermissions(ctx, &UserPermissions{})
	nk := &manifestNK{enforcers: map[string]bool{"enforcer-user": true}}
	out, err := SecurityManifestRPC(ctx, &testLogger{t: t}, nil, nk, `{"guild_id":"`+testGroupID+`"}`)
	if err != nil {
		t.Fatalf("SecurityManifestRPC: %v", err)
	}
	var resp SecurityManifestResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	return resp
}

// guild.manage must mirror canManageGuild (owner / operator / Administrator /
// Manage Server). The guild-management RPCs gate on canManageGuild, so an
// enforcer without it must not be shown the settings UI.
func TestSecurityManifest_GuildManage(t *testing.T) {
	tests := []struct {
		name         string
		userID       string
		src          guildPermissionSource
		wantEnforcer bool
		want         bool
	}{
		{
			name:         "enforcer without Manage Server",
			userID:       "enforcer-user",
			src:          &fakeGuildPermSource{member: memberWithRoles("r-other"), roles: []*discordgo.Role{everyoneRole(discordgo.PermissionViewChannel)}},
			wantEnforcer: true,
			want:         false,
		},
		{
			name:   "Manage Server holder",
			userID: testManagerUserID,
			src:    &fakeGuildPermSource{member: memberWithRoles("r-mgr"), roles: []*discordgo.Role{everyoneRole(0), {ID: "r-mgr", Permissions: discordgo.PermissionManageGuild}}},
			want:   true,
		},
		{
			name:   "guild owner",
			userID: testOwnerUserID,
			src:    &fakeGuildPermSource{member: memberWithRoles(), roles: []*discordgo.Role{everyoneRole(0)}},
			want:   true,
		},
		{
			name:         "enforcer who also holds Manage Server",
			userID:       "enforcer-user",
			src:          &fakeGuildPermSource{member: memberWithRoles("r-mgr"), roles: []*discordgo.Role{everyoneRole(0), {ID: "r-mgr", Permissions: discordgo.PermissionManageGuild}}},
			wantEnforcer: true,
			want:         true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			globalGuildPermissionSource.Store(&guildPermissionSourceBox{src: tt.src})
			t.Cleanup(func() { globalGuildPermissionSource.Store(nil) })

			resp := manifestCapabilities(t, tt.userID)
			isEnforcer := false
			for _, r := range resp.Roles {
				if r == "enforcer" {
					isEnforcer = true
				}
			}
			if isEnforcer != tt.wantEnforcer {
				t.Fatalf("enforcer role = %v, want %v (roles=%v)", isEnforcer, tt.wantEnforcer, resp.Roles)
			}
			if got := resp.Capabilities["guild.manage"]; got != tt.want {
				t.Fatalf("guild.manage = %v, want %v", got, tt.want)
			}
			// Unchanged by this fix; pinned so the two capabilities cannot drift apart again.
			if got := resp.Capabilities["guild.roles.update"]; got != tt.want {
				t.Fatalf("guild.roles.update = %v, want %v", got, tt.want)
			}
		})
	}
}

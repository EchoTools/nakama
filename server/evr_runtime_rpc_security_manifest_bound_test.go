package server

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const manifestManyGuilds = 12

func manifestManyGroupID(i int) string { return fmt.Sprintf("00000000-0000-0000-0000-%012d", i) }
func manifestManyGuildID(i int) string { return fmt.Sprintf("9000000000000%05d", i) }

// manifestManyNK serves SecurityManifestRPC for a user who is a member of
// manifestManyGuilds guilds, numbered from 1 in group-id order.
type manifestManyNK struct {
	runtime.NakamaModule
	owners map[string]string
}

func (n *manifestManyNK) UserGroupsList(ctx context.Context, userID string, limit int, state *int, cursor string) ([]*api.UserGroupList_UserGroup, string, error) {
	out := make([]*api.UserGroupList_UserGroup, 0, manifestManyGuilds)
	for i := 1; i <= manifestManyGuilds; i++ {
		md, _ := json.Marshal(map[string]any{
			"guild_id": manifestManyGuildID(i),
			"owner_id": n.owners[manifestManyGroupID(i)],
			"roles":    map[string]string{"moderator": manifestMultiRoleID},
		})
		out = append(out, &api.UserGroupList_UserGroup{
			Group: &api.Group{Id: manifestManyGroupID(i), Name: "guild", LangTag: GuildGroupLangTag, Metadata: string(md)},
			State: &wrapperspb.Int32Value{Value: int32(api.UserGroupList_UserGroup_MEMBER)},
		})
	}
	return out, "", nil
}

func (n *manifestManyNK) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	objs := make([]*api.StorageObject, 0, len(reads))
	for _, r := range reads {
		val, _ := json.Marshal(map[string]any{"group_id": r.Key, "role_cache": map[string]map[string]bool{}})
		objs = append(objs, &api.StorageObject{Collection: StorageCollectionState, Key: r.Key, Value: string(val)})
	}
	return objs, nil
}

func (n *manifestManyNK) AccountGetId(ctx context.Context, userID string) (*api.Account, error) {
	return &api.Account{User: &api.User{Id: userID}, CustomId: testManagerDisc}, nil
}

// countingPermSource records each guild canManageGuild asks Discord about, and
// grants Manage Server in the guilds in managed.
type countingPermSource struct {
	managed map[string]bool
	asked   []string
}

func (c *countingPermSource) GuildOwnerID(guildID string) (string, error) {
	c.asked = append(c.asked, guildID)
	return "", nil
}

func (c *countingPermSource) GuildMemberFresh(guildID, discordID string) (*discordgo.Member, error) {
	return memberWithRoles("r-mgr"), nil
}

func (c *countingPermSource) GuildRoles(guildID string) ([]*discordgo.Role, error) {
	perms := int64(0)
	if c.managed[guildID] {
		perms = discordgo.PermissionManageGuild
	}
	return []*discordgo.Role{{ID: guildID, Name: "@everyone"}, {ID: "r-mgr", Permissions: perms}}, nil
}

func manifestForMany(t *testing.T, nk *manifestManyNK, src *countingPermSource) SecurityManifestResponse {
	t.Helper()
	globalGuildPermissionSource.Store(&guildPermissionSourceBox{src: src})
	t.Cleanup(func() { globalGuildPermissionSource.Store(nil) })
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, "multi-user") //nolint:staticcheck // nakama runtime ctx keys are plain strings
	ctx = WithUserPermissions(ctx, &UserPermissions{})
	out, err := SecurityManifestRPC(ctx, &testLogger{t: t}, nil, nk, "")
	if err != nil {
		t.Fatalf("SecurityManifestRPC: %v", err)
	}
	var resp SecurityManifestResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	return resp
}

func TestSecurityManifest_ManageCheckPassIsBounded(t *testing.T) {
	tests := []struct {
		name      string
		owners    map[string]string
		managed   map[string]bool
		wantGuild string
		wantAsked int
	}{
		{
			name:      "manages none: ten guilds asked, the first by id selected",
			wantGuild: manifestManyGroupID(1),
			wantAsked: securityManifestManageChecks,
		},
		{
			name:      "manages the third: asks stop there",
			managed:   map[string]bool{manifestManyGuildID(3): true},
			wantGuild: manifestManyGroupID(3),
			wantAsked: 3,
		},
		{
			name:      "manages only the eleventh: it is not asked about",
			managed:   map[string]bool{manifestManyGuildID(11): true},
			wantGuild: manifestManyGroupID(1),
			wantAsked: securityManifestManageChecks,
		},
		{
			name:      "owns the last, manages the first: owner chosen, nobody asked",
			owners:    map[string]string{manifestManyGroupID(manifestManyGuilds): "multi-user"},
			managed:   map[string]bool{manifestManyGuildID(1): true},
			wantGuild: manifestManyGroupID(manifestManyGuilds),
			wantAsked: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := &countingPermSource{managed: tt.managed}
			nk := &manifestManyNK{owners: tt.owners}

			resp := manifestForMany(t, nk, src)

			if resp.GuildID != tt.wantGuild {
				t.Errorf("selected guild = %q, want %q", resp.GuildID, tt.wantGuild)
			}
			if len(src.asked) != tt.wantAsked {
				t.Errorf("canManageGuild asked Discord about %d guilds %v, want %d", len(src.asked), src.asked, tt.wantAsked)
			}
		})
	}
}

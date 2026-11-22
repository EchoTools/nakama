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

const (
	// Sorted: manifestLowGroupID comes first, which is the guild the old
	// first-by-id selection picked.
	manifestLowGroupID  = "00000000-0000-0000-0000-00000000000a"
	manifestHighGroupID = "ffffffff-ffff-ffff-ffff-fffffffffffe"

	manifestLowGuildID  = "900000000000000010"
	manifestHighGuildID = "900000000000000020"

	manifestMultiRoleID = "role-enforcer-multi"
)

// manifestMultiNK serves SecurityManifestRPC for a user who is a member of two
// guilds. owners maps group ID to the owner's user ID; enforcers maps group ID to
// the user IDs holding the enforcer role in it.
type manifestMultiNK struct {
	runtime.NakamaModule
	owners    map[string]string
	enforcers map[string]map[string]bool
}

func (n *manifestMultiNK) UserGroupsList(ctx context.Context, userID string, limit int, state *int, cursor string) ([]*api.UserGroupList_UserGroup, string, error) {
	guildIDs := map[string]string{manifestLowGroupID: manifestLowGuildID, manifestHighGroupID: manifestHighGuildID}
	out := make([]*api.UserGroupList_UserGroup, 0, len(guildIDs))
	for groupID, guildID := range guildIDs {
		md, _ := json.Marshal(map[string]any{
			"guild_id": guildID,
			"owner_id": n.owners[groupID],
			"roles":    map[string]string{"moderator": manifestMultiRoleID},
		})
		out = append(out, &api.UserGroupList_UserGroup{
			Group: &api.Group{Id: groupID, Name: "guild " + groupID, LangTag: GuildGroupLangTag, Metadata: string(md)},
			State: &wrapperspb.Int32Value{Value: int32(api.UserGroupList_UserGroup_MEMBER)},
		})
	}
	return out, "", nil
}

func (n *manifestMultiNK) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	objs := make([]*api.StorageObject, 0, len(reads))
	for _, r := range reads {
		cache := map[string]map[string]bool{manifestMultiRoleID: n.enforcers[r.Key]}
		val, _ := json.Marshal(map[string]any{"group_id": r.Key, "role_cache": cache})
		objs = append(objs, &api.StorageObject{Collection: StorageCollectionState, Key: r.Key, Value: string(val)})
	}
	return objs, nil
}

func (n *manifestMultiNK) AccountGetId(ctx context.Context, userID string) (*api.Account, error) {
	return &api.Account{User: &api.User{Id: userID}, CustomId: testManagerDisc}, nil
}

func manifestFor(t *testing.T, nk *manifestMultiNK, userID, payload string) SecurityManifestResponse {
	t.Helper()
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, userID) //nolint:staticcheck // nakama runtime ctx keys are plain strings
	ctx = WithUserPermissions(ctx, &UserPermissions{})
	out, err := SecurityManifestRPC(ctx, &testLogger{t: t}, nil, nk, payload)
	if err != nil {
		t.Fatalf("SecurityManifestRPC: %v", err)
	}
	var resp SecurityManifestResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	return resp
}

// An owner of one guild who is an enforcer of a guild with a lower id gets the
// owned guild, with guild.manage.
func TestSecurityManifest_NoGuildID_OwnerOfHigherIDGuildSelectedOverEnforcerOfLowerID(t *testing.T) {
	nk := &manifestMultiNK{
		owners:    map[string]string{manifestHighGroupID: "multi-user"},
		enforcers: map[string]map[string]bool{manifestLowGroupID: {"multi-user": true}},
	}
	resp := manifestFor(t, nk, "multi-user", "")
	if resp.GuildID != manifestHighGroupID {
		t.Fatalf("selected guild = %q, want the owned guild %q", resp.GuildID, manifestHighGroupID)
	}
	if !resp.Capabilities["guild.manage"] {
		t.Fatalf("guild.manage = false for the owner of the selected guild (roles=%v)", resp.Roles)
	}
}

// A member who can manage a guild through Discord (Manage Server) and holds no
// role in it is selected over an enforcer role in a lower-id guild.
func TestSecurityManifest_NoGuildID_DiscordManagerSelectedOverEnforcerOfLowerID(t *testing.T) {
	src := &fakeGuildPermSource{
		member: memberWithRoles("r-mgr"),
		roles:  []*discordgo.Role{{ID: manifestHighGuildID, Name: "@everyone"}, {ID: "r-mgr", Permissions: discordgo.PermissionManageGuild}},
	}
	// Manage Server only in the high guild: the low guild's lookups find no such role.
	globalGuildPermissionSource.Store(&guildPermissionSourceBox{src: &perGuildPermSource{byGuild: map[string]guildPermissionSource{manifestHighGuildID: src}}})
	t.Cleanup(func() { globalGuildPermissionSource.Store(nil) })

	nk := &manifestMultiNK{enforcers: map[string]map[string]bool{manifestLowGroupID: {"multi-user": true}}}
	resp := manifestFor(t, nk, "multi-user", "")
	if resp.GuildID != manifestHighGroupID {
		t.Fatalf("selected guild = %q, want the guild the user can manage %q", resp.GuildID, manifestHighGroupID)
	}
	if !resp.Capabilities["guild.manage"] {
		t.Fatalf("guild.manage = false for a Manage Server holder in the selected guild")
	}
}

// With only enforcer roles the first guild by id is still selected.
func TestSecurityManifest_NoGuildID_EnforcerOnlyKeepsLowestID(t *testing.T) {
	nk := &manifestMultiNK{
		enforcers: map[string]map[string]bool{
			manifestLowGroupID:  {"multi-user": true},
			manifestHighGroupID: {"multi-user": true},
		},
	}
	resp := manifestFor(t, nk, "multi-user", "")
	if resp.GuildID != manifestLowGroupID {
		t.Fatalf("selected guild = %q, want the lowest id %q", resp.GuildID, manifestLowGroupID)
	}
	if resp.Capabilities["guild.manage"] {
		t.Fatalf("guild.manage = true for an enforcer who cannot manage the guild")
	}
}

// A named guild_id is honoured even when the user owns another guild.
func TestSecurityManifest_NamedGuildID_UnchangedWhenUserOwnsAnotherGuild(t *testing.T) {
	nk := &manifestMultiNK{
		owners:    map[string]string{manifestHighGroupID: "multi-user"},
		enforcers: map[string]map[string]bool{manifestLowGroupID: {"multi-user": true}},
	}
	resp := manifestFor(t, nk, "multi-user", `{"guild_id":"`+manifestLowGroupID+`"}`)
	if resp.GuildID != manifestLowGroupID {
		t.Fatalf("selected guild = %q, want the named guild %q", resp.GuildID, manifestLowGroupID)
	}
	if resp.Capabilities["guild.manage"] {
		t.Fatalf("guild.manage = true in a guild where the user is only an enforcer")
	}
}

// perGuildPermSource routes the Discord lookups canManageGuild makes to the
// source configured for that guild, and answers "no permissions" for any other.
type perGuildPermSource struct {
	byGuild map[string]guildPermissionSource
}

func (p *perGuildPermSource) GuildMemberFresh(guildID, discordID string) (*discordgo.Member, error) {
	if s, ok := p.byGuild[guildID]; ok {
		return s.GuildMemberFresh(guildID, discordID)
	}
	return memberWithRoles(), nil
}

func (p *perGuildPermSource) GuildRoles(guildID string) ([]*discordgo.Role, error) {
	if s, ok := p.byGuild[guildID]; ok {
		return s.GuildRoles(guildID)
	}
	return []*discordgo.Role{{ID: guildID, Name: "@everyone"}}, nil
}

func (p *perGuildPermSource) GuildOwnerID(guildID string) (string, error) {
	if s, ok := p.byGuild[guildID]; ok {
		return s.GuildOwnerID(guildID)
	}
	return "", nil
}

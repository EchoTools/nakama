package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// memberUpdateTestModule counts the account writes handleMemberUpdate issues.
//
// Storage comes from the OCC double; only the three surfaces handleMemberUpdate
// needs beyond it are added here. accountUpdateCalls is the measurement the S3b
// falsifier is stated in.
type memberUpdateTestModule struct {
	*occTestNakamaModule

	account *api.Account
	groups  []*api.UserGroupList_UserGroup

	accountUpdateCalls int
	groupAddCalls      int
}

func (m *memberUpdateTestModule) AccountGetId(ctx context.Context, userID string) (*api.Account, error) {
	return m.account, nil
}

func (m *memberUpdateTestModule) AccountUpdateId(ctx context.Context, userID, username string, _ map[string]any, displayName, timezone, location, langTag, avatarURL string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accountUpdateCalls++
	return nil
}

func (m *memberUpdateTestModule) UserGroupsList(ctx context.Context, userID string, limit int, state *int, cursor string) ([]*api.UserGroupList_UserGroup, string, error) {
	return m.groups, "", nil
}

func (m *memberUpdateTestModule) GroupUsersAdd(ctx context.Context, callerID, groupID string, userIDs []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.groupAddCalls++
	return nil
}

func (m *memberUpdateTestModule) accountWrites() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.accountUpdateCalls
}

func (m *memberUpdateTestModule) groupAdds() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.groupAddCalls
}

const (
	memberWriteUserID    = "7b1e3c4d-0000-4000-8000-00000000e1e1"
	memberWriteGroupID   = "8c2f4d5e-0000-4000-8000-00000000e1e2"
	memberWriteOtherID   = "9d3a5e6f-0000-4000-8000-00000000e1e3"
	memberWriteGuildID   = "guild-777"
	memberWriteDiscordID = "discord-777"
	memberWriteUsername  = "tester"
	memberWriteIGN       = "SteadyName"
)

// memberUpdateFixture wires handleMemberUpdate's dependencies so that NOTHING
// about the member has changed. Every field is deliberately matched to the
// stored profile; each test below perturbs exactly one of them.
type memberUpdateFixture struct {
	m     *memberUpdateTestModule
	d     *DiscordIntegrator
	event *discordgo.GuildMemberUpdate
	group *GuildGroup
}

func newMemberUpdateFixture(t *testing.T, activeGroupID string) *memberUpdateFixture {
	t.Helper()
	ctx := context.Background()

	account := &api.Account{User: &api.User{
		Id:       memberWriteUserID,
		Username: memberWriteUsername,
	}}

	m := &memberUpdateTestModule{
		occTestNakamaModule: newOCCTestNakamaModule(),
		account:             account,
	}

	// The stored profile already carries the member's current IGN, so the
	// display-name branch is a no-op and syncMembersIGN is never reached.
	profile := &EVRProfile{
		ActiveGroupID: activeGroupID,
		InGameNames: map[string]GroupInGameName{
			memberWriteGroupID: {GroupID: memberWriteGroupID, DisplayName: memberWriteIGN},
		},
	}
	b, err := json.Marshal(profile)
	require.NoError(t, err)
	m.seedObject(memberWriteUserID, StorageCollectionEVRProfile, StorageKeyEVRProfile, string(b))

	// HasLoggedIntoEcho gates the whole handler on the presence of this row.
	m.seedObject(memberWriteUserID, LoginStorageCollection, LoginHistoryStorageKey, "{}")

	// An all-empty RoleMap keeps RoleCacheUpdate false (so GuildGroupStore, which
	// hard-casts to the production module, is never reached) and keeps
	// updateMemberRole off the Discord API, so a nil dg session is safe.
	group := &GuildGroup{
		GroupMetadata: GroupMetadata{GuildID: memberWriteGuildID},
		State:         &GuildGroupState{GroupID: memberWriteGroupID},
		Group:         &api.Group{Id: memberWriteGroupID, LangTag: GuildGroupLangTag},
	}

	// The user is already a member of the group, so no GroupUsersAdd.
	m.groups = []*api.UserGroupList_UserGroup{{
		Group: group.Group,
		State: wrapperspb.Int32(int32(api.UserGroupList_UserGroup_MEMBER)),
	}}

	groups := map[string]*GuildGroup{memberWriteGroupID: group}
	inheritance := map[string][]string{}

	d := &DiscordIntegrator{
		ctx:    ctx,
		logger: zap.NewNop(),
		nk:     m,
		guildGroupRegistry: &GuildGroupRegistry{
			guildGroups:    atomic.NewPointer(&groups),
			inheritanceMap: atomic.NewPointer(&inheritance),
		},
		idcache:     &MapOf[string, string]{},
		memberCache: &MapOf[string, cachedMember]{},
	}
	// Pre-seed both lookups so neither touches the (nil) database.
	d.idcache.Store(memberWriteGuildID, memberWriteGroupID)
	d.idcache.Store(memberWriteDiscordID, memberWriteUserID)

	event := &discordgo.GuildMemberUpdate{
		Member: &discordgo.Member{
			GuildID: memberWriteGuildID,
			Nick:    memberWriteIGN,
			User: &discordgo.User{
				ID:       memberWriteDiscordID,
				Username: memberWriteUsername,
			},
		},
	}

	return &memberUpdateFixture{m: m, d: d, event: event, group: group}
}

// TestHandleMemberUpdate_UnchangedMemberIssuesNoAccountWrite is the S3b
// falsifier's first half.
//
// updateMemberRole returns nil on three no-op paths -- empty role ID, nil
// member, and "the member already has exactly the role they should have" --
// which is indistinguishable from "I changed something". handleMemberUpdate
// read that nil as success and set accountUpdate = true, so a member-update
// event in which literally nothing changed still issued a full account write.
// handleMemberUpdate completes 3,652 times/day.
//
// The event here is a pure no-op: same nickname, same username, already in the
// group, roles already correct. It must write nothing.
func TestHandleMemberUpdate_UnchangedMemberIssuesNoAccountWrite(t *testing.T) {
	// A group that is NOT the user's active group: the avatar branch is a
	// separate trigger and is pinned on its own below.
	f := newMemberUpdateFixture(t, memberWriteOtherID)

	require.NoError(t, f.d.handleMemberUpdate(zap.NewNop(), nil, f.event))

	require.Zero(t, f.m.accountWrites(),
		"a member update in which nothing changed must issue zero account writes")
}

// TestHandleMemberUpdate_ChangedUsernameIssuesExactlyOneAccountWrite is the
// falsifier's second half, and it is what makes the test above non-trivial.
//
// Asserting only "zero writes" passes just as well if the write path has been
// broken outright. This pins that a real change still produces exactly one
// write, so the two tests can only both pass if the gating is right.
func TestHandleMemberUpdate_ChangedUsernameIssuesExactlyOneAccountWrite(t *testing.T) {
	f := newMemberUpdateFixture(t, memberWriteOtherID)

	// The one perturbation: Discord reports a username the stored account does
	// not have. That is a genuine change and must be persisted.
	f.event.Member.User.Username = "renamed"

	require.NoError(t, f.d.handleMemberUpdate(zap.NewNop(), nil, f.event))

	require.Equal(t, 1, f.m.accountWrites(),
		"a real change must still issue exactly one account write")
}

// TestHandleMemberUpdate_JoinedGroupIssuesExactlyOneAccountWrite is a second
// positive control on a different trigger: the user is not yet in the group, so
// the handler adds them and must persist that.
func TestHandleMemberUpdate_JoinedGroupIssuesExactlyOneAccountWrite(t *testing.T) {
	f := newMemberUpdateFixture(t, memberWriteOtherID)

	// Not a member of any group yet, so GuildUserGroupsList comes back empty and
	// the handler calls GroupUsersAdd.
	f.m.groups = nil

	require.NoError(t, f.d.handleMemberUpdate(zap.NewNop(), nil, f.event))

	require.Equal(t, 1, f.m.groupAdds(),
		"precondition: the handler actually added the user to the group")
	require.Equal(t, 1, f.m.accountWrites(),
		"joining the group is a real change and must issue exactly one account write")
}

// TestHandleMemberUpdate_ActiveGroupStillWritesForTheAvatar characterizes a
// SECOND, independent trigger that the updateMemberRole change does not and
// cannot address.
//
// When the event's group is the user's active group, handleMemberUpdate sets
// accountUpdate = true unconditionally in order to push e.Member.AvatarURL("512")
// through AccountUpdateId. That is not a change test -- it fires on every
// member-update event for the active guild, whether or not the avatar moved.
//
// This test pins the current behavior rather than asserting the behavior we
// want, so the remaining trigger is recorded and measured instead of being
// quietly hidden by the no-op test above choosing a non-active group. Closing it
// means gating on the avatar actually differing (EVRProfile.AvatarURL() exists
// and reads the stored value), which is a behavior change beyond this stage.
func TestHandleMemberUpdate_ActiveGroupStillWritesForTheAvatar(t *testing.T) {
	f := newMemberUpdateFixture(t, memberWriteGroupID)

	require.NoError(t, f.d.handleMemberUpdate(zap.NewNop(), nil, f.event))

	require.Equal(t, 1, f.m.accountWrites(),
		"KNOWN GAP: the active-group avatar push is unconditional, so an otherwise "+
			"unchanged event still writes once. Not addressed by the updateMemberRole "+
			"change; see the commit message.")
}

// TestUpdateMemberRole_NoOpPathsReportNoChange pins the unit-level contract the
// gating above depends on. The two no-op paths reachable without a Discord
// session must report changed=false, not merely err=nil.
func TestUpdateMemberRole_NoOpPathsReportNoChange(t *testing.T) {
	d := &DiscordIntegrator{logger: zap.NewNop()}
	member := &discordgo.Member{
		GuildID: memberWriteGuildID,
		Roles:   []string{"role-a"},
		User:    &discordgo.User{ID: memberWriteDiscordID},
	}

	t.Run("empty role id", func(t *testing.T) {
		changed, err := d.updateMemberRole(member, "", true)
		require.NoError(t, err)
		require.False(t, changed, "an unconfigured role is not a change")
	})

	t.Run("nil member", func(t *testing.T) {
		changed, err := d.updateMemberRole(nil, "role-a", true)
		require.NoError(t, err)
		require.False(t, changed, "a nil member is not a change")
	})

	t.Run("member already has the role", func(t *testing.T) {
		changed, err := d.updateMemberRole(member, "role-a", true)
		require.NoError(t, err)
		require.False(t, changed, "granting a role the member already has is not a change")
	})

	t.Run("member already lacks the role", func(t *testing.T) {
		changed, err := d.updateMemberRole(member, "role-b", false)
		require.NoError(t, err)
		require.False(t, changed, "removing a role the member does not have is not a change")
	})
}

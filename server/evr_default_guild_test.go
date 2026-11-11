package server

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// defaultGuildTestModule is a user whose group list starts empty and gains the
// groups added to it, which is what a brand-new /device account looks like.
//
// The embedded base is *occTestNakamaModule, which no embedder of this type
// overrides GroupUsersAdd/GroupUserLeave/UserGroupsList on, so the calls below
// reach these methods directly (defect class 1 does not apply).
type defaultGuildTestModule struct {
	*occTestNakamaModule

	gmu    sync.Mutex
	member map[string]bool // groupID -> member
	adds   []string
	leaves []string
}

func newDefaultGuildTestModule() *defaultGuildTestModule {
	return &defaultGuildTestModule{occTestNakamaModule: newOCCTestNakamaModule(), member: map[string]bool{}}
}

func (m *defaultGuildTestModule) UserGroupsList(ctx context.Context, userID string, limit int, state *int, cursor string) ([]*api.UserGroupList_UserGroup, string, error) {
	m.gmu.Lock()
	defer m.gmu.Unlock()
	var out []*api.UserGroupList_UserGroup
	for gid, in := range m.member {
		if in {
			out = append(out, &api.UserGroupList_UserGroup{
				Group: &api.Group{Id: gid, LangTag: GuildGroupLangTag},
				State: wrapperspb.Int32(int32(api.UserGroupList_UserGroup_MEMBER)),
			})
		}
	}
	return out, "", nil
}

func (m *defaultGuildTestModule) GroupUsersAdd(ctx context.Context, callerID, groupID string, userIDs []string) error {
	m.gmu.Lock()
	defer m.gmu.Unlock()
	m.adds = append(m.adds, groupID)
	m.member[groupID] = true
	return nil
}

func (m *defaultGuildTestModule) GroupUserLeave(ctx context.Context, groupID, userID, username string) error {
	m.gmu.Lock()
	defer m.gmu.Unlock()
	m.leaves = append(m.leaves, groupID)
	m.member[groupID] = false
	return nil
}

func (m *defaultGuildTestModule) AccountGetId(ctx context.Context, userID string) (*api.Account, error) {
	return &api.Account{User: &api.User{Id: userID, Username: "tester"}}, nil
}

func (m *defaultGuildTestModule) snapshot() (adds, leaves []string, isMember func(string) bool) {
	m.gmu.Lock()
	defer m.gmu.Unlock()
	member := map[string]bool{}
	for k, v := range m.member {
		member[k] = v
	}
	return append([]string(nil), m.adds...), append([]string(nil), m.leaves...), func(g string) bool { return member[g] }
}

func registryWith(groupIDs ...string) *GuildGroupRegistry {
	groups := map[string]*GuildGroup{}
	for _, id := range groupIDs {
		groups[id] = &GuildGroup{
			GroupMetadata: GroupMetadata{GuildID: "guild-" + id},
			State:         &GuildGroupState{GroupID: id},
			Group:         &api.Group{Id: id, LangTag: GuildGroupLangTag},
		}
	}
	inheritance := map[string][]string{}
	return &GuildGroupRegistry{
		guildGroups:    atomic.NewPointer(&groups),
		inheritanceMap: atomic.NewPointer(&inheritance),
	}
}

const (
	defaultGuildTestUserID    = "7b1e3c4d-0000-4000-8000-00000000d701"
	defaultGuildTestDiscordID = "discord-749"
	otherGuildTestGroupID     = "8c2f4d5e-0000-4000-8000-00000000d702"
	otherGuildTestGuildID     = "guild-749-other"
	loungeTestGuildID         = "guild-749-lounge"
)

// A new account in no guild is added to the Lounge group, and the groups the
// login then sees contain it (the len(guildGroups)==0 refusal is not taken).
func TestEnsureDefaultGuildMembership_NewAccountLandsInLounge(t *testing.T) {
	ctx := context.Background()
	nk := newDefaultGuildTestModule()
	reg := registryWith(DefaultGuildGroupID)

	groups, err := GuildUserGroupsList(ctx, nk, reg, defaultGuildTestUserID)
	require.NoError(t, err)
	require.Empty(t, groups, "precondition: a new account has no guild groups")

	groups, err = ensureDefaultGuildMembership(ctx, zap.NewNop(), nk, reg, defaultGuildTestUserID, groups)
	require.NoError(t, err)
	require.Contains(t, groups, DefaultGuildGroupID)

	adds, _, _ := nk.snapshot()
	require.Equal(t, []string{DefaultGuildGroupID}, adds)
}

// Control: a user who already has a guild is left alone.
func TestEnsureDefaultGuildMembership_ExistingMemberUntouched(t *testing.T) {
	ctx := context.Background()
	nk := newDefaultGuildTestModule()
	nk.member[otherGuildTestGroupID] = true
	reg := registryWith(DefaultGuildGroupID, otherGuildTestGroupID)

	groups, err := GuildUserGroupsList(ctx, nk, reg, defaultGuildTestUserID)
	require.NoError(t, err)
	require.Contains(t, groups, otherGuildTestGroupID)

	groups, err = ensureDefaultGuildMembership(ctx, zap.NewNop(), nk, reg, defaultGuildTestUserID, groups)
	require.NoError(t, err)
	require.NotContains(t, groups, DefaultGuildGroupID)
	adds, _, _ := nk.snapshot()
	require.Empty(t, adds)
}

func TestIsSyncableGuildID(t *testing.T) {
	require.False(t, isSyncableGuildID(""))
	require.False(t, isSyncableGuildID(uuid.Nil.String()))
	require.True(t, isSyncableGuildID("123456789012345678"))
}

func newQueueTestIntegrator(nk *defaultGuildTestModule) *DiscordIntegrator {
	d := &DiscordIntegrator{
		ctx:            context.Background(),
		logger:         zap.NewNop(),
		nk:             nk,
		idcache:        &MapOf[string, string]{},
		queueCh:        make(chan QueueEntry, 8),
		queueCooldowns: &MapOf[QueueEntry, time.Time]{},
	}
	d.idcache.Store(otherGuildTestGuildID, otherGuildTestGroupID)
	d.idcache.Store(loungeTestGuildID, DefaultGuildGroupID)
	d.idcache.Store(defaultGuildTestDiscordID, defaultGuildTestUserID)
	return d
}

// The zero-guild sync (guild_id all-zero, the shape in #749) is never queued.
// The control proves the queue works: a real guild is.
func TestQueueSyncMember_SkipsZeroGuild(t *testing.T) {
	d := newQueueTestIntegrator(newDefaultGuildTestModule())

	d.QueueSyncMember(uuid.Nil.String(), defaultGuildTestDiscordID, true)
	d.QueueSyncMember("", defaultGuildTestDiscordID, true)
	require.Len(t, d.queueCh, 0, "a sync with no guild must not be queued")

	d.QueueSyncMember(otherGuildTestGuildID, defaultGuildTestDiscordID, true)
	require.Len(t, d.queueCh, 1, "control: a real guild is queued")
}

// A Discord-driven removal (member left the Discord server, or the sync found
// them absent) must not undo the default-group membership; the same removal for
// any other guild still applies; an explicit moderator kick still applies.
func TestGuildGroupMemberRemove_DiscordSyncKeepsLoungeMembership(t *testing.T) {
	ctx := context.Background()
	nk := newDefaultGuildTestModule()
	nk.member[DefaultGuildGroupID] = true
	nk.member[otherGuildTestGroupID] = true
	d := newQueueTestIntegrator(nk)

	// Sync-driven removal from the Lounge: membership survives.
	require.NoError(t, d.GuildGroupMemberRemove(ctx, loungeTestGuildID, defaultGuildTestDiscordID, ""))
	_, leaves, isMember := nk.snapshot()
	require.Empty(t, leaves, "the Lounge must not be left by a Discord sync")
	require.True(t, isMember(DefaultGuildGroupID))

	// Control: the same sync-driven removal from another guild is honoured.
	_ = d.GuildGroupMemberRemove(ctx, otherGuildTestGuildID, defaultGuildTestDiscordID, "")
	_, leaves, isMember = nk.snapshot()
	require.Equal(t, []string{otherGuildTestGroupID}, leaves)
	require.False(t, isMember(otherGuildTestGroupID))
	require.True(t, isMember(DefaultGuildGroupID))
}

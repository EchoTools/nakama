package server

import (
	"context"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// ignSyncTestModule adds the StorageIndexList surface DisplayNameOwnerSearch
// needs on top of the OCC-correct profile module. An empty index result means
// "nobody else owns this display name", which is the branch that carries
// syncMembersIGN through to the profile write.
type ignSyncTestModule struct {
	*profileUpdateTestModule
}

func (m *ignSyncTestModule) StorageIndexList(ctx context.Context, callerID, indexName, query string, limit int, order []string, cursor string) (*api.StorageObjects, string, error) {
	return &api.StorageObjects{}, "", nil
}

func newIGNSyncTestModule() *ignSyncTestModule {
	return &ignSyncTestModule{profileUpdateTestModule: newProfileUpdateTestModule()}
}

// ignSyncFixture is the minimal wiring syncMembersIGN touches: an nk module, a
// guild group with an empty RoleMap, and a member whose nickname is the new IGN.
type ignSyncFixture struct {
	m      *ignSyncTestModule
	d      *DiscordIntegrator
	caller *EVRProfile
	member *discordgo.Member
	group  *GuildGroup
}

const (
	ignRebindUserID  = "5f9c1a2b-0000-4000-8000-00000000d1d1"
	ignRebindGroupID = "6a0d2b3c-0000-4000-8000-00000000d1d2"
	ignRebindNewName = "ReboundName"
)

func newIGNSyncFixture(t *testing.T) *ignSyncFixture {
	t.Helper()
	ctx := context.Background()

	m := newIGNSyncTestModule()

	// The stored profile carries a field the caller's in-hand copy knows nothing
	// about. It stands in for whatever a concurrent writer committed between the
	// caller's read and this write.
	seedStoredProfile(t, m.profileUpdateTestModule, ignRebindUserID, &EVRProfile{MatchmakingDivision: "gold"})

	// The caller's own profile object: a DIFFERENT pointer, without that field.
	// InGameNames is non-nil but empty so GetGroupIGNData yields "", which
	// differs from the member's nickname and drives the sync.
	caller := &EVRProfile{InGameNames: map[string]GroupInGameName{}}
	caller.account = &api.Account{User: &api.User{Id: ignRebindUserID, Username: "tester"}}

	return &ignSyncFixture{
		m:      m,
		d:      &DiscordIntegrator{ctx: ctx, logger: zap.NewNop(), nk: m},
		caller: caller,
		member: &discordgo.Member{
			GuildID: "guild-1",
			Nick:    ignRebindNewName,
			User:    &discordgo.User{ID: "discord-1", Username: "tester"},
		},
		// An empty RoleMap keeps RoleCacheUpdate out of it; State is required
		// because GuildGroup methods lock it.
		group: &GuildGroup{State: &GuildGroupState{}, Group: &api.Group{Id: ignRebindGroupID}},
	}
}

// TestSyncMembersIGN_CallerObservesReboundProfile is the S3a falsifier.
//
// syncMembersIGN retries its profile write on a version conflict, and the retry
// RE-READS the stored profile into its `profile` local (evr_discord_integrator.go,
// the `profile, err = EVRProfileLoad(...)` line). That rebinding is invisible to
// the caller, which goes on using the pre-reload object it still holds -- in
// handleMemberUpdate's case to build the account metadata it then writes. So the
// account write carries a profile that predates the very reload performed to
// avoid clobbering a concurrent writer, which is the failure the retry exists to
// prevent.
//
// Production signal: "Version conflict updating display name" fires 62x/day
// against 711 IGN syncs/day, so this retry branch is taken routinely.
//
// The test forces exactly one conflict, so the retry branch is guaranteed, then
// asserts the caller can see BOTH the concurrent writer's field and the display
// name that was actually written.
func TestSyncMembersIGN_CallerObservesReboundProfile(t *testing.T) {
	f := newIGNSyncFixture(t)

	// Exactly one injected conflict: attempt 0 is rejected, attempt 1 reloads
	// and succeeds. Guarantees the rebinding happens.
	f.m.conflictsRemaining = 1

	updated, err := f.d.syncMembersIGN(f.d.ctx, zap.NewNop(), f.caller, f.member, f.group)
	require.NoError(t, err)

	// Precondition: the retry branch really was taken. Without this the
	// assertions below could pass for the wrong reason.
	require.Equal(t, 2, f.m.calls(),
		"precondition: one rejected write then one successful write")

	require.NotSame(t, f.caller, updated,
		"precondition: the retry reloaded the profile, so the callee rebound its local")

	require.Equal(t, "gold", updated.MatchmakingDivision,
		"the caller must observe the reloaded profile, which carries the concurrent writer's field")
	require.Equal(t, ignRebindNewName, updated.GetGroupIGNData(ignRebindGroupID).DisplayName,
		"the caller must observe the display name that was actually written")
}

// TestSyncMembersIGN_NoConflictKeepsCallerPointer is the control for the test
// above: with no conflict there is no reload, so the caller's own object is the
// one that was written and must come back unchanged. Without this, returning a
// freshly loaded profile unconditionally would also pass the rebind test while
// quietly discarding caller state on the common path.
func TestSyncMembersIGN_NoConflictKeepsCallerPointer(t *testing.T) {
	f := newIGNSyncFixture(t)

	updated, err := f.d.syncMembersIGN(f.d.ctx, zap.NewNop(), f.caller, f.member, f.group)
	require.NoError(t, err)

	require.Equal(t, 1, f.m.calls(), "precondition: exactly one write, no retry")
	require.Same(t, f.caller, updated,
		"no conflict means no reload: the caller's own object is what was written")
	require.Equal(t, ignRebindNewName, updated.GetGroupIGNData(ignRebindGroupID).DisplayName)
}

// TestSyncMembersIGN_NoOpPathsReturnTheCallersProfile pins that the early
// returns hand back a usable profile rather than nil. A caller that adopts the
// return value would otherwise nil out its own profile whenever the display name
// was locked or unchanged -- the two most common outcomes on this path.
func TestSyncMembersIGN_NoOpPathsReturnTheCallersProfile(t *testing.T) {
	t.Run("locked display name", func(t *testing.T) {
		f := newIGNSyncFixture(t)
		f.caller.InGameNames[ignRebindGroupID] = GroupInGameName{
			GroupID:     ignRebindGroupID,
			DisplayName: "SomethingElse",
			IsLocked:    true,
		}

		updated, err := f.d.syncMembersIGN(f.d.ctx, zap.NewNop(), f.caller, f.member, f.group)
		require.NoError(t, err)
		require.Same(t, f.caller, updated, "a locked IGN is a no-op, not a nil profile")
		require.Zero(t, f.m.calls(), "a locked IGN must issue no profile write")
	})

	t.Run("display name unchanged", func(t *testing.T) {
		f := newIGNSyncFixture(t)
		f.caller.InGameNames[ignRebindGroupID] = GroupInGameName{
			GroupID:     ignRebindGroupID,
			DisplayName: ignRebindNewName,
		}

		updated, err := f.d.syncMembersIGN(f.d.ctx, zap.NewNop(), f.caller, f.member, f.group)
		require.NoError(t, err)
		require.Same(t, f.caller, updated, "an unchanged IGN is a no-op, not a nil profile")
		require.Zero(t, f.m.calls(), "an unchanged IGN must issue no profile write")
	})
}

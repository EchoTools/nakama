package server

import (
	"context"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	"go.uber.org/zap"
)

// groupDeleteRecorderModule records GroupDelete calls. handleGuildDelete's only
// real side effect on the Nakama side is the group deletion, so that is all the
// double needs to observe.
type groupDeleteRecorderModule struct {
	runtime.NakamaModule
	deleted []string
}

func (m *groupDeleteRecorderModule) GroupDelete(ctx context.Context, groupID string) error {
	m.deleted = append(m.deleted, groupID)
	return nil
}

// newGuildDeleteTestIntegrator builds a DiscordIntegrator wired only with the
// pieces handleGuildDelete touches. The registry is constructed as a struct
// literal rather than via NewGuildGroupRegistry so no background database poller
// is started.
func newGuildDeleteTestIntegrator(t *testing.T, nk runtime.NakamaModule) *DiscordIntegrator {
	t.Helper()
	return &DiscordIntegrator{
		ctx:    context.Background(),
		logger: zap.NewNop(),
		nk:     nk,
		guildGroupRegistry: &GuildGroupRegistry{
			guildGroups:    atomic.NewPointer(&map[string]*GuildGroup{}),
			inheritanceMap: atomic.NewPointer(&map[string][]string{}),
		},
		idcache:        &MapOf[string, string]{},
		memberCache:    &MapOf[string, cachedMember]{},
		queueCooldowns: &MapOf[QueueEntry, time.Time]{},
	}
}

// TestHandleGuildDelete_UnregisteredGroupDoesNotPanic pins the crash-safety of
// the GUILD_DELETE handler.
//
// GuildGroupRegistry.Get returns nil for a group that is in the database but not
// (yet) in the registry — the registry rebuild filters on GuildGroupLangTag, and
// any failure between GroupCreate and registry.Add in guildSync leaves that
// window open. handleGuildDelete then logged gg.GroupMetadata. GroupMetadata is
// an EMBEDDED VALUE in GuildGroup, so that expression dereferences the nil
// pointer. discordgo dispatches handlers on bare goroutines with no recover, so
// the panic takes down the whole process.
//
// evr_lobby_joinentrant.go already nil-checks the same lookup defensively; this
// test holds handleGuildDelete to the same standard: no panic, and the group is
// still deleted.
func TestHandleGuildDelete_UnregisteredGroupDoesNotPanic(t *testing.T) {
	const guildID = "123456789012345678"
	groupID := uuid.Must(uuid.NewV4()).String()

	nk := &groupDeleteRecorderModule{}
	d := newGuildDeleteTestIntegrator(t, nk)

	// Seed the guild->group mapping so GuildIDToGroupID resolves without a DB.
	d.idcache.Store(guildID, groupID)
	d.idcache.Store(groupID, guildID)

	// Deliberately do NOT add the group to the registry: this is the window the
	// bug lives in.
	require.Nil(t, d.guildGroupRegistry.Get(groupID), "precondition: group is absent from the registry")

	require.NotPanics(t, func() {
		err := d.handleGuildDelete(zap.NewNop(), nil, &discordgo.GuildDelete{
			Guild: &discordgo.Guild{ID: guildID, OwnerID: "987654321098765432"},
		})
		require.NoError(t, err)
	})

	require.Equal(t, []string{groupID}, nk.deleted,
		"the group must still be deleted even when it was never registered")
}

// TestHandleGuildDelete_RegisteredGroupStillDeletes is the positive control: the
// nil guard must not short-circuit the normal path.
func TestHandleGuildDelete_RegisteredGroupStillDeletes(t *testing.T) {
	const guildID = "223456789012345678"
	groupID := uuid.Must(uuid.NewV4()).String()

	nk := &groupDeleteRecorderModule{}
	d := newGuildDeleteTestIntegrator(t, nk)

	d.idcache.Store(guildID, groupID)
	d.idcache.Store(groupID, guildID)

	gg := &GuildGroup{
		GroupMetadata: GroupMetadata{GuildID: guildID},
		Group:         &api.Group{Id: groupID, Name: "Test Guild"},
	}
	d.guildGroupRegistry.Add(gg)
	require.NotNil(t, d.guildGroupRegistry.Get(groupID))

	require.NotPanics(t, func() {
		err := d.handleGuildDelete(zap.NewNop(), nil, &discordgo.GuildDelete{
			Guild: &discordgo.Guild{ID: guildID, OwnerID: "987654321098765432"},
		})
		require.NoError(t, err)
	})

	require.Equal(t, []string{groupID}, nk.deleted)
}

// TestHandleGuildDelete_UnknownGuildIsNoOp covers the third branch: a guild with
// no group mapping at all must return early without deleting anything.
func TestHandleGuildDelete_UnknownGuildIsNoOp(t *testing.T) {
	nk := &groupDeleteRecorderModule{}
	d := newGuildDeleteTestIntegrator(t, nk)
	d.idcache.Store("323456789012345678", "") // resolves to "" => unknown guild

	require.NotPanics(t, func() {
		err := d.handleGuildDelete(zap.NewNop(), nil, &discordgo.GuildDelete{
			Guild: &discordgo.Guild{ID: "323456789012345678", OwnerID: "987654321098765432"},
		})
		require.NoError(t, err)
	})

	require.Empty(t, nk.deleted)
}

// TestEarlyQuitEnforcementEnabled_NilGuildGroupEntry pins the same nil-*GuildGroup
// class of bug on the other site the review flagged.
//
// GetEnforceEarlyQuitPenalty opens with `if g == nil` — but its receiver is
// *GroupMetadata, and GroupMetadata is embedded by VALUE in GuildGroup. The call
// `gg.GetEnforceEarlyQuitPenalty()` is shorthand for
// `(&gg.GroupMetadata).GetEnforceEarlyQuitPenalty()`, so the receiver address is
// computed from gg BEFORE the method body runs. A nil gg therefore panics and the
// guard never fires: it is decorative at this call site.
//
// GuildUserGroupsList currently filters nil registry lookups out of the map, so
// the panic is not reachable through that path today. The guard is cheap, matches
// the defensive nil check evr_lobby_joinentrant.go already applies to the same
// map, and stops a future populator from turning a map entry into a crash.
func TestEarlyQuitEnforcementEnabled_NilGuildGroupEntry(t *testing.T) {
	groupID := uuid.Must(uuid.NewV4()).String()

	params := &SessionParameters{
		guildGroups: map[string]*GuildGroup{groupID: nil},
	}
	ctx := context.WithValue(context.Background(), ctxSessionParametersKey{}, atomic.NewPointer(params))

	var enabled bool
	require.NotPanics(t, func() {
		enabled = earlyQuitEnforcementEnabled(ctx, groupID)
	})
	require.True(t, enabled,
		"an unusable guild-group entry must fall through to the lenient default, like an absent one")
}

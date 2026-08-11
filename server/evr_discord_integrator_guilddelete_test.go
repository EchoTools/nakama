package server

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
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

// TestEarlyQuitEnforcementEnabled_FailsClosedWhenGuildUnresolvable pins the
// direction this gate errs in.
//
// It used to read the guild from session parameters and return TRUE when they
// were absent. ctxSessionParametersKey is planted only on the WebSocket session
// context; the context Nakama hands a match callback never carries it, so every
// match-side caller took that fail-open branch on every real invocation and the
// opt-in flag suppressed nothing. Players in guilds that never opted in accrued
// quit counters, lockouts, tier degradation and Discord DMs.
//
// It now resolves through nk, and when that fails it returns FALSE. The setting
// is opt-in, so the absence of an explicit yes is a no, and the costs are
// asymmetric: a missed charge is recoverable, while a wrongly-charged player
// takes a lifetime NumEarlyQuits increment that never decays.
func TestEarlyQuitEnforcementEnabled_FailsClosedWhenGuildUnresolvable(t *testing.T) {
	logger := NewRuntimeGoLogger(NewJSONLogger(os.Stdout, zapcore.ErrorLevel, JSONFormat))

	t.Run("empty group id does not charge", func(t *testing.T) {
		require.False(t, earlyQuitEnforcementEnabled(context.Background(), &guildLookupFailsNakamaModule{}, logger, ""),
			"no guild means no opt-in; charging would punish a player for a lookup we never made")
	})

	t.Run("group lookup error does not charge", func(t *testing.T) {
		groupID := uuid.Must(uuid.NewV4()).String()
		var enabled bool
		require.NotPanics(t, func() {
			enabled = earlyQuitEnforcementEnabled(context.Background(), &guildLookupFailsNakamaModule{}, logger, groupID)
		})
		require.False(t, enabled,
			"an unreadable guild configuration is not evidence the guild wanted enforcement")
	})

	t.Run("guild that opted in does charge", func(t *testing.T) {
		groupID := uuid.Must(uuid.NewV4()).String()
		nk := newEvrTestNakamaModule()
		optInGuildToEarlyQuitEnforcement(t, nk, groupID)
		require.True(t, earlyQuitEnforcementEnabled(context.Background(), nk, logger, groupID),
			"a guild with enforce_early_quit_penalty set must enforce")
	})

	t.Run("guild present but not opted in does not charge", func(t *testing.T) {
		groupID := uuid.Must(uuid.NewV4()).String()
		nk := newEvrTestNakamaModule()
		nk.groups[groupID] = &api.Group{Id: groupID, Metadata: `{}`}
		require.False(t, earlyQuitEnforcementEnabled(context.Background(), nk, logger, groupID),
			"an existing guild that never set the flag has not opted in")
	})

	t.Run("group not found does not charge", func(t *testing.T) {
		groupID := uuid.Must(uuid.NewV4()).String()
		require.False(t, earlyQuitEnforcementEnabled(context.Background(), &guildMissingNakamaModule{}, logger, groupID),
			"a group that does not exist cannot have opted in")
	})
}

// guildLookupFailsNakamaModule fails the group read GuildGroupLoad starts with.
type guildLookupFailsNakamaModule struct {
	runtime.NakamaModule
}

func (m *guildLookupFailsNakamaModule) GroupsGetId(ctx context.Context, groupIDs []string) ([]*api.Group, error) {
	return nil, errors.New("simulated group read failure")
}

// guildMissingNakamaModule returns no groups, as happens for an unknown ID.
type guildMissingNakamaModule struct {
	runtime.NakamaModule
}

func (m *guildMissingNakamaModule) GroupsGetId(ctx context.Context, groupIDs []string) ([]*api.Group, error) {
	return nil, nil
}

// TestHandleGuildDelete_ReportOnlyFreezesTheDelete pins report_only against the
// promise its own documentation makes.
//
// PruneSettings.ReportOnly is described as making a pass "perform NO writes at
// all ... the operator's single freeze switch during an incident". It gated the
// prune pass and nothing else, so this event path deleted guild groups straight
// through the freeze.
//
// That matters because of when it fires. An operator sets report_only during a
// Discord incident, which is exactly when Discord's reads are least
// trustworthy -- the same premise the prune pass was hardened against. A
// GUILD_DELETE arriving then destroyed the group's role mappings, channel IDs
// and suspension inheritance, and a re-add returned a fresh group with default
// metadata. There is no undo.
func TestHandleGuildDelete_ReportOnlyFreezesTheDelete(t *testing.T) {
	const guildID = "323456789012345678"
	groupID := uuid.Must(uuid.NewV4()).String()

	restore := ServiceSettings()
	t.Cleanup(func() { ServiceSettingsUpdate(restore) })

	frozen := &ServiceSettingsData{}
	frozen.PruneSettings.ReportOnly = true
	frozen.PruneSettings.DeleteOrphanedGroups = true // armed, and still must not fire
	ServiceSettingsUpdate(frozen)

	nk := &groupDeleteRecorderModule{}
	d := newGuildDeleteTestIntegrator(t, nk)

	d.idcache.Store(guildID, groupID)
	d.idcache.Store(groupID, guildID)
	d.guildGroupRegistry.Add(&GuildGroup{
		GroupMetadata: GroupMetadata{GuildID: guildID},
		Group:         &api.Group{Id: groupID, Name: "Frozen Guild"},
	})

	err := d.handleGuildDelete(zap.NewNop(), nil, &discordgo.GuildDelete{
		Guild: &discordgo.Guild{ID: guildID, OwnerID: "987654321098765432"},
	})
	require.NoError(t, err)

	require.Empty(t, nk.deleted,
		"report_only must freeze the event-driven group delete, not only the prune pass; "+
			"the group can be collected later, but a deleted one cannot be recovered")
}

// TestPruneDeletesAllowed_SuppressedAfterBoot pins the post-restart grace on
// destructive prunes.
//
// unavailableGuilds -- the record of which guilds are merely dark rather than
// departed -- lives only in memory. A restart erases it, and it is the only
// thing between a Discord read anomaly and an unrecoverable GroupDelete.
// Normally READY lists unavailable guilds as stubs and they are protected
// anyway, but a READY that omits a member guild is precisely the "Discord is
// lying" case this pass was hardened against, and it is most likely during the
// incident that caused the restart.
//
// Leaves and the non-destructive repair pass are deliberately unaffected:
// re-inviting the bot undoes a leave, nothing undoes a delete.
func TestPruneDeletesAllowed_SuppressedAfterBoot(t *testing.T) {
	cases := []struct {
		name       string
		configured bool
		uptime     time.Duration
		want       bool
	}{
		{"armed, just booted", true, 0, false},
		{"armed, one prune interval in", true, 15 * time.Minute, false},
		{"armed, one second short of the grace", true, pruneDeleteStartupGrace - time.Second, false},
		{"armed, grace elapsed", true, pruneDeleteStartupGrace, true},
		{"armed, long-running process", true, 72 * time.Hour, true},
		{"not configured, grace elapsed", false, 72 * time.Hour, false},
		{"not configured, just booted", false, 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pruneDeletesAllowed(tc.configured, tc.uptime); got != tc.want {
				t.Errorf("pruneDeletesAllowed(%t, %v) = %t, want %t", tc.configured, tc.uptime, got, tc.want)
			}
		})
	}
}

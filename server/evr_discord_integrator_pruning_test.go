package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// pruneRecorder records every side effect executePrunePlan attempts, so tests
// can assert on what actually ran rather than on what was merely identified.
type pruneRecorder struct {
	synced      []string
	left        []string
	deleted     []string
	purged      []string
	syncFails   map[string]bool
	leaveFails  map[string]bool
	deleteFails map[string]bool
}

func (r *pruneRecorder) actions() pruneActions {
	return pruneActions{
		syncGuild: func(g *discordgo.Guild) error {
			r.synced = append(r.synced, g.ID)
			if r.syncFails[g.ID] {
				return errors.New("sync failed")
			}
			return nil
		},
		leaveGuild: func(guildID string) error {
			r.left = append(r.left, guildID)
			if r.leaveFails[guildID] {
				return errors.New("leave failed")
			}
			return nil
		},
		deleteGroup: func(groupID string) error {
			r.deleted = append(r.deleted, groupID)
			if r.deleteFails[groupID] {
				return errors.New("delete failed")
			}
			return nil
		},
		purgeGuild: func(guildID string) {
			r.purged = append(r.purged, guildID)
		},
	}
}

func newPruneRecorder() *pruneRecorder {
	return &pruneRecorder{
		syncFails:   map[string]bool{},
		leaveFails:  map[string]bool{},
		deleteFails: map[string]bool{},
	}
}

// observedLogger returns a runtime.Logger whose emitted entries are captured.
func observedLogger() (runtime.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(zapcore.DebugLevel)
	return NewRuntimeGoLogger(zap.New(core)), logs
}

func testOrphanGroup(guildID, groupID string) orphanGroup {
	return orphanGroup{
		guildID: guildID,
		group: &api.Group{
			Id:       groupID,
			Name:     "group-" + guildID,
			Metadata: fmt.Sprintf(`{"guild_id":%q,"audit_channel_id":%q}`, guildID, sentinelAuditChannelID),
		},
	}
}

// sentinelChannelTopic is a marker buried deep inside a guild's object graph.
// It can only appear in a log line if the whole struct was logged.
const sentinelChannelTopic = "SENTINEL_CHANNEL_TOPIC"

// sentinelAuditChannelID is a marker inside a group's GroupMetadata. Unlike a
// *discordgo.Guild, GroupMetadata is a small bounded config struct, and it is
// exactly what a group delete destroys -- so it MUST appear in the pre-delete
// log.
const sentinelAuditChannelID = "SENTINEL_AUDIT_CHANNEL"

func testOrphanGuild(id string) *discordgo.Guild {
	return &discordgo.Guild{ID: id, Name: "guild-" + id}
}

func TestReconcileOrphanGuilds(t *testing.T) {
	guildA := &discordgo.Guild{ID: "1111", Name: "Guild A"}
	guildB := &discordgo.Guild{ID: "2222", Name: "Guild B"}
	guildC := &discordgo.Guild{ID: "3333", Name: "Guild C"}

	tests := []struct {
		name          string
		orphans       []*discordgo.Guild
		failIDs       map[string]bool
		wantRemaining []string
	}{
		{
			name:          "all syncs succeed leaves no orphan candidates",
			orphans:       []*discordgo.Guild{guildA, guildB},
			failIDs:       map[string]bool{},
			wantRemaining: nil,
		},
		{
			name:          "all syncs fail leaves all orphan candidates",
			orphans:       []*discordgo.Guild{guildA, guildB},
			failIDs:       map[string]bool{"1111": true, "2222": true},
			wantRemaining: []string{"1111", "2222"},
		},
		{
			name:          "only failed syncs remain orphan candidates",
			orphans:       []*discordgo.Guild{guildA, guildB, guildC},
			failIDs:       map[string]bool{"2222": true},
			wantRemaining: []string{"2222"},
		},
		{
			name:          "no orphans means no sync attempts and no candidates",
			orphans:       nil,
			failIDs:       map[string]bool{},
			wantRemaining: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := NewRuntimeGoLogger(zap.NewNop())

			syncCalls := make(map[string]int)
			syncFn := func(guild *discordgo.Guild) error {
				syncCalls[guild.ID]++
				if tt.failIDs[guild.ID] {
					return errors.New("sync failed")
				}
				return nil
			}

			remaining := reconcileOrphanGuilds(logger, tt.orphans, syncFn)

			// Every orphan gets exactly one sync attempt.
			for _, g := range tt.orphans {
				if syncCalls[g.ID] != 1 {
					t.Errorf("guild %s: got %d sync attempts, want 1", g.ID, syncCalls[g.ID])
				}
			}
			if len(syncCalls) != len(tt.orphans) {
				t.Errorf("got sync attempts for %d guilds, want %d", len(syncCalls), len(tt.orphans))
			}

			var remainingIDs []string
			for _, g := range remaining {
				remainingIDs = append(remainingIDs, g.ID)
			}
			if len(remainingIDs) != len(tt.wantRemaining) {
				t.Fatalf("got remaining %v, want %v", remainingIDs, tt.wantRemaining)
			}
			for i, id := range tt.wantRemaining {
				if remainingIDs[i] != id {
					t.Errorf("remaining[%d] = %s, want %s", i, remainingIDs[i], id)
				}
			}
		})
	}
}

// prunePolicy is a terse constructor for pruneConfig in tests.
func prunePolicy(doGuildLeaves, doGroupDeletes bool, safetyThreshold int) pruneConfig {
	return pruneConfig{
		doGuildLeaves:   doGuildLeaves,
		doGroupDeletes:  doGroupDeletes,
		safetyThreshold: safetyThreshold,
	}
}

// TestExecutePrunePlanHealsOrphanGuildAtShippedDefaults is the regression test
// for the incident this whole path exists for. Guild 1522261692355055849 is a
// guild the bot is a member of whose join-time guildSync failed with SQLSTATE
// 22001, so it has no Nakama group and its players cannot matchmake. Gateway
// Guild Create events are not replayed on reconnect, so the 15-minute prune
// tick is the ONLY thing that can ever repair it.
//
// It must therefore repair it at the SHIPPED DEFAULTS: LeaveOrphanedGuilds
// false, DeleteOrphanedGroups false, SafetyLimit 0 (nothing in the tree ever
// assigns SafetyLimit a default -- FixDefaultServiceSettings does not mention
// it -- so an unconfigured deployment runs with 0).
func TestExecutePrunePlanHealsOrphanGuildAtShippedDefaults(t *testing.T) {
	incidentGuild := &discordgo.Guild{ID: "1522261692355055849", Name: "Jett's Hangout"}

	for _, tt := range []struct {
		name string
		cfg  pruneConfig
	}{
		{"shipped defaults (leaves=false deletes=false SafetyLimit=0)", prunePolicy(false, false, 0)},
		{"deletes only, SafetyLimit=0", prunePolicy(false, true, 0)},
		{"leaves+deletes, SafetyLimit=0", prunePolicy(true, true, 0)},
		{"leaves+deletes, SafetyLimit=100", prunePolicy(true, true, 100)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			logger, _ := observedLogger()
			rec := newPruneRecorder()
			plan := prunePlan{orphanGuilds: []*discordgo.Guild{incidentGuild}}

			outcome, err := executePrunePlan(logger, plan, tt.cfg, rec.actions())
			if err != nil {
				t.Fatalf("executePrunePlan returned error: %v", err)
			}
			if len(rec.synced) != 1 || rec.synced[0] != incidentGuild.ID {
				t.Fatalf("reconciliation syncs = %v; want exactly [%s] -- the guild can never self-heal otherwise",
					rec.synced, incidentGuild.ID)
			}
			if outcome.reconciledGuilds != 1 {
				t.Errorf("outcome.reconciledGuilds = %d; want 1", outcome.reconciledGuilds)
			}
			// The repair succeeded, so it is no longer a leave candidate --
			// even with leaves enabled and a zero threshold.
			if len(rec.left) != 0 {
				t.Errorf("left guilds %v after a successful repair; want none", rec.left)
			}
		})
	}
}

// TestExecutePrunePlanDisableReconciliationSuppressesAllWrites pins the
// operator's escape hatch. Reconciliation writes (it creates Nakama groups,
// writes GuildGroupState and mutates the guild group registry), so an operator
// freezing writes during an incident sets DisableReconciliation. That must also
// suppress guild leaves: the bot may never leave a guild it did not try to
// repair.
func TestExecutePrunePlanDisableReconciliationSuppressesAllWrites(t *testing.T) {
	logger, _ := observedLogger()
	rec := newPruneRecorder()

	plan := prunePlan{
		orphanGroups: []orphanGroup{testOrphanGroup("g1", "grp1")},
		orphanGuilds: []*discordgo.Guild{testOrphanGuild("d1"), testOrphanGuild("d2")},
	}
	cfg := pruneConfig{doGuildLeaves: true, safetyThreshold: 100, disableReconciliation: true}

	outcome, err := executePrunePlan(logger, plan, cfg, rec.actions())
	if err != nil {
		t.Fatalf("executePrunePlan returned error: %v", err)
	}
	if len(rec.synced) != 0 {
		t.Errorf("ran %d reconciliation syncs with reconciliation disabled (%v); want 0", len(rec.synced), rec.synced)
	}
	if len(rec.left) != 0 {
		t.Errorf("left guilds %v without attempting a repair; want none", rec.left)
	}
	if outcome.guildsLeft != 0 || outcome.reconciledGuilds != 0 {
		t.Errorf("outcome = %+v; want guildsLeft=0 reconciledGuilds=0", outcome)
	}
}

// TestExecutePrunePlanSkipsReconcileAndLeavesOnImplausibleOrphanCount pins the
// bound on the repair pass. A partial GroupsList result that makes 200 guilds
// look orphaned is a symptom of a bad read, not 200 broken guilds. It must not
// cost 200 guildSync calls (a Discord REST call plus several DB writes each),
// and because no guild was vetted, none may be left either.
func TestExecutePrunePlanSkipsReconcileAndLeavesOnImplausibleOrphanCount(t *testing.T) {
	logger, _ := observedLogger()
	rec := newPruneRecorder()

	var guilds []*discordgo.Guild
	for i := 0; i < 200; i++ {
		guilds = append(guilds, testOrphanGuild(fmt.Sprintf("d%d", i)))
	}
	plan := prunePlan{orphanGuilds: guilds}

	outcome, err := executePrunePlan(logger, plan, prunePolicy(true, true, 500), rec.actions())
	if err != nil {
		t.Fatalf("executePrunePlan returned error: %v", err)
	}
	if len(rec.synced) != 0 {
		t.Errorf("ran %d reconciliation syncs for %d orphan guilds; want 0 (limit %d)",
			len(rec.synced), len(guilds), maxReconcileGuildsPerPass)
	}
	// The threshold is 500, well above 200, so only the "no repair was
	// attempted" rule can stop these leaves.
	if len(rec.left) != 0 {
		t.Errorf("left %d guilds without attempting a repair; want 0", len(rec.left))
	}
	if outcome.reconciledGuilds != 0 || outcome.guildsLeft != 0 {
		t.Errorf("outcome = %+v; want reconciledGuilds=0 guildsLeft=0", outcome)
	}
}

// TestExecutePrunePlanReconcilesUpToTheLimit is the other side of the bound: a
// plausible orphan count is repaired normally.
func TestExecutePrunePlanReconcilesUpToTheLimit(t *testing.T) {
	logger, _ := observedLogger()
	rec := newPruneRecorder()

	var guilds []*discordgo.Guild
	for i := 0; i < maxReconcileGuildsPerPass; i++ {
		guilds = append(guilds, testOrphanGuild(fmt.Sprintf("d%d", i)))
	}

	outcome, err := executePrunePlan(logger, prunePlan{orphanGuilds: guilds}, prunePolicy(false, false, 0), rec.actions())
	if err != nil {
		t.Fatalf("executePrunePlan returned error: %v", err)
	}
	if len(rec.synced) != maxReconcileGuildsPerPass {
		t.Errorf("ran %d reconciliation syncs; want %d", len(rec.synced), maxReconcileGuildsPerPass)
	}
	if outcome.reconciledGuilds != maxReconcileGuildsPerPass {
		t.Errorf("outcome.reconciledGuilds = %d; want %d", outcome.reconciledGuilds, maxReconcileGuildsPerPass)
	}
}

// TestExecutePrunePlanSafetyValveAbortsLeavesAfterFailedRepair pins that the
// mass-leave threshold is evaluated against the POST-repair candidates and
// aborts before any leave. Every sync here fails, so all six guilds survive as
// leave candidates and the threshold of 5 must stop them.
func TestExecutePrunePlanSafetyValveAbortsLeavesAfterFailedRepair(t *testing.T) {
	logger, _ := observedLogger()
	rec := newPruneRecorder()

	var guilds []*discordgo.Guild
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("d%d", i)
		guilds = append(guilds, testOrphanGuild(id))
		rec.syncFails[id] = true
	}

	outcome, err := executePrunePlan(logger, prunePlan{orphanGuilds: guilds}, prunePolicy(true, true, 5), rec.actions())
	if err == nil {
		t.Fatal("executePrunePlan accepted a mass leave; want an error")
	}
	if len(rec.left) != 0 || len(rec.deleted) != 0 {
		t.Errorf("safety valve left %v and deleted %v; want none", rec.left, rec.deleted)
	}
	if outcome.guildsLeft != 0 {
		t.Errorf("outcome.guildsLeft = %d; want 0", outcome.guildsLeft)
	}
}

// TestExecutePrunePlanSafetyValveDoesNotTripOnRepairedGuilds is the companion:
// the same six guilds, but the repairs succeed. Nothing would be destroyed, so
// the valve must NOT trip -- this is precisely what the pre-review ordering
// (valve before repair) got wrong.
func TestExecutePrunePlanSafetyValveDoesNotTripOnRepairedGuilds(t *testing.T) {
	logger, _ := observedLogger()
	rec := newPruneRecorder()

	var guilds []*discordgo.Guild
	for i := 0; i < 6; i++ {
		guilds = append(guilds, testOrphanGuild(fmt.Sprintf("d%d", i)))
	}

	outcome, err := executePrunePlan(logger, prunePlan{orphanGuilds: guilds}, prunePolicy(true, true, 5), rec.actions())
	if err != nil {
		t.Fatalf("executePrunePlan returned error after repairing every guild: %v", err)
	}
	if len(rec.synced) != 6 {
		t.Errorf("reconciliation syncs = %v; want all 6", rec.synced)
	}
	if len(rec.left) != 0 {
		t.Errorf("left guilds %v after repairing them all; want none", rec.left)
	}
	if outcome.reconciledGuilds != 6 {
		t.Errorf("outcome.reconciledGuilds = %d; want 6", outcome.reconciledGuilds)
	}
}

// TestExecutePrunePlanSafetyValveTripsOnOrphanGroups covers the group side of
// the threshold, which guards irreplaceable configuration (role maps, channel
// IDs, suspension inheritance, memberships).
func TestExecutePrunePlanSafetyValveTripsOnOrphanGroups(t *testing.T) {
	logger, _ := observedLogger()
	rec := newPruneRecorder()

	var groups []orphanGroup
	for i := 0; i < 10; i++ {
		groups = append(groups, testOrphanGroup(fmt.Sprintf("g%d", i), fmt.Sprintf("grp%d", i)))
	}

	if _, err := executePrunePlan(logger, prunePlan{orphanGroups: groups}, prunePolicy(true, true, 3), rec.actions()); err == nil {
		t.Fatal("executePrunePlan accepted a mass group delete; want an error")
	}
	if len(rec.deleted) != 0 {
		t.Errorf("deleted groups %v after tripping the safety valve; want none", rec.deleted)
	}
}

// TestExecutePrunePlanValvesAreIndependent pins that a tripped group-delete
// valve does not suppress the guild-leave side, and vice versa. Conflating them
// meant one orphaned group could block the repair of an unrelated guild.
func TestExecutePrunePlanValvesAreIndependent(t *testing.T) {
	logger, _ := observedLogger()
	rec := newPruneRecorder()

	// Ten orphan groups (over the threshold of 3) and one unrepairable orphan
	// guild (under it).
	var groups []orphanGroup
	for i := 0; i < 10; i++ {
		groups = append(groups, testOrphanGroup(fmt.Sprintf("g%d", i), fmt.Sprintf("grp%d", i)))
	}
	rec.syncFails["d1"] = true
	plan := prunePlan{orphanGroups: groups, orphanGuilds: []*discordgo.Guild{testOrphanGuild("d1")}}

	outcome, err := executePrunePlan(logger, plan, prunePolicy(true, true, 3), rec.actions())
	if err == nil {
		t.Fatal("want an error: the group-delete valve tripped")
	}
	if len(rec.deleted) != 0 {
		t.Errorf("deleted groups %v with the group valve tripped; want none", rec.deleted)
	}
	if len(rec.left) != 1 || rec.left[0] != "d1" {
		t.Errorf("left = %v; want [d1] -- the guild valve did not trip and must still act", rec.left)
	}
	if outcome.guildsLeft != 1 {
		t.Errorf("outcome.guildsLeft = %d; want 1", outcome.guildsLeft)
	}
}

// TestExecutePrunePlanCompletionLogReportsOnlyRealWork pins finding 5: the
// completion log must report work that actually happened, not the size of the
// candidate lists. Here leaves are disabled entirely and one of two deletes
// fails, so exactly one group was deleted and zero guilds were left.
func TestExecutePrunePlanCompletionLogReportsOnlyRealWork(t *testing.T) {
	logger, logs := observedLogger()
	rec := newPruneRecorder()
	rec.deleteFails["grp2"] = true

	plan := prunePlan{
		orphanGroups: []orphanGroup{testOrphanGroup("g1", "grp1"), testOrphanGroup("g2", "grp2")},
		orphanGuilds: []*discordgo.Guild{testOrphanGuild("d1"), testOrphanGuild("d2"), testOrphanGuild("d3")},
	}

	outcome, err := executePrunePlan(logger, plan, prunePolicy(false, true, 100), rec.actions())
	if err != nil {
		t.Fatalf("executePrunePlan returned error: %v", err)
	}

	if outcome.groupsDeleted != 1 || outcome.groupDeleteErrors != 1 {
		t.Errorf("outcome = %+v; want groupsDeleted=1 groupDeleteErrors=1", outcome)
	}
	if outcome.guildsLeft != 0 {
		t.Errorf("outcome.guildsLeft = %d; want 0 (leaves disabled)", outcome.guildsLeft)
	}

	entry := findLogEntry(t, logs, "Pruned unused groups and guilds")
	fields := entry.ContextMap()
	if got := fields["deleted_groups"]; got != int64(1) {
		t.Errorf("log deleted_groups = %v; want 1 (one delete failed)", got)
	}
	if got := fields["left_guilds"]; got != int64(0) {
		t.Errorf("log left_guilds = %v; want 0 (guild leaves were disabled)", got)
	}
}

// TestExecutePrunePlanCompletionLogCountsPartialLeaveFailures is the guild-side
// twin: three leave candidates, one leave call fails.
func TestExecutePrunePlanCompletionLogCountsPartialLeaveFailures(t *testing.T) {
	logger, logs := observedLogger()
	rec := newPruneRecorder()
	rec.leaveFails["d2"] = true

	plan := prunePlan{orphanGuilds: []*discordgo.Guild{
		testOrphanGuild("d1"), testOrphanGuild("d2"), testOrphanGuild("d3"),
	}}
	rec.syncFails["d1"] = true
	rec.syncFails["d2"] = true
	rec.syncFails["d3"] = true

	outcome, err := executePrunePlan(logger, plan, prunePolicy(true, false, 100), rec.actions())
	if err != nil {
		t.Fatalf("executePrunePlan returned error: %v", err)
	}
	if outcome.guildsLeft != 2 || outcome.guildLeaveErrors != 1 {
		t.Errorf("outcome = %+v; want guildsLeft=2 guildLeaveErrors=1", outcome)
	}

	fields := findLogEntry(t, logs, "Pruned unused groups and guilds").ContextMap()
	if got := fields["left_guilds"]; got != int64(2) {
		t.Errorf("log left_guilds = %v; want 2 (one leave failed)", got)
	}
}

// TestExecutePrunePlanPurgesIDCacheAfterGroupDelete pins finding 2: the prune
// path must purge the guild ID -> group ID cache exactly as handleGuildDelete
// does. Otherwise the guild's return produces a GUILD_CREATE whose guildSync
// resolves the deleted group ID from cache, takes the UPDATE branch, matches
// zero rows, and fails with runtime.ErrGroupNotUpdated forever -- after which
// the next prune tick leaves the healthy guild.
func TestExecutePrunePlanPurgesIDCacheAfterGroupDelete(t *testing.T) {
	logger, _ := observedLogger()
	rec := newPruneRecorder()
	rec.deleteFails["grp2"] = true

	plan := prunePlan{orphanGroups: []orphanGroup{
		testOrphanGroup("g1", "grp1"),
		testOrphanGroup("g2", "grp2"),
	}}

	if _, err := executePrunePlan(logger, plan, prunePolicy(false, true, 100), rec.actions()); err != nil {
		t.Fatalf("executePrunePlan returned error: %v", err)
	}

	if len(rec.purged) != 1 || rec.purged[0] != "g1" {
		t.Fatalf("purged = %v; want exactly [g1] (g2's delete failed, so its cache entry is still valid)", rec.purged)
	}
}

// TestExecutePrunePlanLogsGroupMetadataBeforeDeleting pins that the pre-delete
// log records the configuration the delete is about to destroy. GroupMetadata
// carries the role map, matchmaking and audit channel IDs and the suspension
// inheritance list; none of it can be rebuilt from Discord, and this log line
// is the only forensic record of it. It is a small bounded struct, unlike the
// *discordgo.Guild that TestExecutePrunePlanLogsIdentifiersNotWholeStructs
// keeps out of the logs.
func TestExecutePrunePlanLogsGroupMetadataBeforeDeleting(t *testing.T) {
	logger, logs := observedLogger()
	rec := newPruneRecorder()

	plan := prunePlan{orphanGroups: []orphanGroup{testOrphanGroup("g1", "grp1")}}

	if _, err := executePrunePlan(logger, plan, prunePolicy(false, true, 100), rec.actions()); err != nil {
		t.Fatalf("executePrunePlan returned error: %v", err)
	}

	fields := findLogEntry(t, logs, "Deleting orphaned group from Nakama").ContextMap()
	metadata, _ := fields["metadata"].(string)
	if !strings.Contains(metadata, sentinelAuditChannelID) {
		t.Fatalf("pre-delete log metadata = %q; want it to carry the group configuration being destroyed (%s)",
			metadata, sentinelAuditChannelID)
	}
}

// TestExecutePrunePlanLogsIdentifiersNotWholeStructs pins finding 6: logging a
// *discordgo.Guild drags in members, channels, presences and emojis -- the
// largest possible log line at the worst possible moment.
//
// Every log site that receives a guild must be exercised. The abort log and the
// per-guild "Leaving orphaned discord guild" log are on mutually exclusive
// branches, so each needs its own scenario; a test that only trips the valve
// leaves the leave-loop log completely unguarded.
func TestExecutePrunePlanLogsIdentifiersNotWholeStructs(t *testing.T) {
	newFatPlan := func() (prunePlan, *discordgo.Guild) {
		fat := testOrphanGuild("d1")
		fat.Channels = []*discordgo.Channel{{ID: "c1", Topic: sentinelChannelTopic}}
		return prunePlan{
			orphanGuilds: []*discordgo.Guild{fat, testOrphanGuild("d2")},
			orphanGroups: []orphanGroup{testOrphanGroup("g1", "grp1")},
		}, fat
	}

	for _, tt := range []struct {
		name string
		// wantMsg is a log message the scenario must actually produce, so a
		// silent branch change cannot make this test vacuously pass.
		wantMsg string
		run     func(t *testing.T, logger runtime.Logger, rec *pruneRecorder, plan prunePlan)
	}{
		{
			name:    "safety valve abort log",
			wantMsg: "Pruning Discord guilds and groups will leave more than 1, skipping to avoid mass leave",
			run: func(t *testing.T, logger runtime.Logger, rec *pruneRecorder, plan prunePlan) {
				// Both repairs fail, so both guilds survive as leave
				// candidates and a threshold of 1 trips.
				rec.syncFails["d1"] = true
				rec.syncFails["d2"] = true
				if _, err := executePrunePlan(logger, plan, prunePolicy(true, true, 1), rec.actions()); err == nil {
					t.Fatal("expected the safety valve to trip")
				}
			},
		},
		{
			name:    "per-guild leave log",
			wantMsg: "Leaving orphaned discord guild",
			run: func(t *testing.T, logger runtime.Logger, rec *pruneRecorder, plan prunePlan) {
				// Repairs fail but the threshold is generous, so the leave
				// loop actually runs and logs each guild.
				rec.syncFails["d1"] = true
				rec.syncFails["d2"] = true
				if _, err := executePrunePlan(logger, plan, prunePolicy(true, false, 100), rec.actions()); err != nil {
					t.Fatalf("executePrunePlan returned error: %v", err)
				}
				if len(rec.left) != 2 {
					t.Fatalf("left = %v; want both guilds left so the leave log is exercised", rec.left)
				}
			},
		},
		{
			name:    "reconciliation log",
			wantMsg: "Attempting to reconcile orphan guild via guild sync",
			run: func(t *testing.T, logger runtime.Logger, rec *pruneRecorder, plan prunePlan) {
				if _, err := executePrunePlan(logger, plan, prunePolicy(false, false, 100), rec.actions()); err != nil {
					t.Fatalf("executePrunePlan returned error: %v", err)
				}
			},
		},
		{
			name:    "implausible-orphan-count log",
			wantMsg: "skipping reconciliation and guild leaves",
			run: func(t *testing.T, logger runtime.Logger, rec *pruneRecorder, plan prunePlan) {
				_, fat := newFatPlan()
				guilds := []*discordgo.Guild{fat}
				for i := 0; i < maxReconcileGuildsPerPass; i++ {
					guilds = append(guilds, testOrphanGuild(fmt.Sprintf("x%d", i)))
				}
				if _, err := executePrunePlan(logger, prunePlan{orphanGuilds: guilds}, prunePolicy(true, true, 1000), rec.actions()); err != nil {
					t.Fatalf("executePrunePlan returned error: %v", err)
				}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			logger, logs := observedLogger()
			rec := newPruneRecorder()
			plan, _ := newFatPlan()

			tt.run(t, logger, rec, plan)

			var sawWanted bool
			for _, entry := range logs.All() {
				if strings.Contains(entry.Message, tt.wantMsg) {
					sawWanted = true
				}
				// Render each field the way a log encoder does. Formatting
				// with %v is NOT good enough: a []*discordgo.Guild formats as
				// a slice of pointer addresses ("[0xc000123456]"), so a leak
				// would be invisible while the JSON the encoder actually
				// writes carries the whole object graph.
				for k, v := range entry.ContextMap() {
					encoded, err := json.Marshal(v)
					if err != nil {
						// Not encodable as JSON; fall back to the Go rendering.
						encoded = []byte(fmt.Sprintf("%+v", v))
					}
					if strings.Contains(string(encoded), sentinelChannelTopic) {
						t.Fatalf("log entry %q field %q leaked a whole struct (%d bytes): %.200s",
							entry.Message, k, len(encoded), encoded)
					}
				}
			}
			if !sawWanted {
				var got []string
				for _, e := range logs.All() {
					got = append(got, e.Message)
				}
				t.Fatalf("scenario never emitted %q, so it guards nothing; entries: %v", tt.wantMsg, got)
			}
		})
	}
}

func findLogEntry(t *testing.T, logs *observer.ObservedLogs, msg string) observer.LoggedEntry {
	t.Helper()
	entries := logs.FilterMessage(msg).All()
	if len(entries) != 1 {
		var got []string
		for _, e := range logs.All() {
			got = append(got, e.Message)
		}
		t.Fatalf("want exactly one %q log entry, got %d; all entries: %v", msg, len(entries), got)
	}
	return entries[0]
}

// TestComputePrunePlanIgnoresUnavailableStubGuilds pins that a guild present
// in session state only as an unavailable stub is never a leave candidate.
// On READY, and while a Discord shard is down, State.Guilds holds stubs with
// Unavailable=true and no Name, OwnerID, or Channels. Reconciliation cannot
// sync such a stub, so treating it as an orphan candidate means the bot leaves
// a healthy guild purely because Discord was having an outage.
func TestComputePrunePlanIgnoresUnavailableStubGuilds(t *testing.T) {
	stub := &discordgo.Guild{ID: "d_stub", Unavailable: true}
	healthy := testOrphanGuild("d_healthy")

	plan := computePrunePlan(map[string]*api.Group{}, []*discordgo.Guild{stub, healthy}, nil)

	if got := orphanGuildIDs(plan.orphanGuilds); len(got) != 1 || got[0] != "d_healthy" {
		t.Fatalf("orphan guilds = %v; want only [d_healthy] -- an unavailable stub is not a prune candidate", got)
	}
}

// TestComputePrunePlanKeepsGroupsOfUnavailableGuilds pins the outage-safety
// invariant with the largest blast radius in this file.
//
// discordgo calls State.GuildRemove on EVERY GUILD_DELETE, including
// Unavailable=true (discordgo@v0.29.0 state.go:973-981), and state is updated
// before any handler runs (event.go:188-200). So when a shard goes down, the
// affected guilds simply vanish from State.Guilds -- the integrator's own
// "ignore unavailable" check in Start() runs too late to matter. Their groups
// then look orphaned, and GroupDelete destroys role maps, channel IDs,
// suspension-inheritance configuration and every membership, none of which is
// recoverable from Discord.
//
// The integrator therefore remembers guilds it saw go unavailable, and those
// guilds count as present for the purpose of orphan-group detection.
func TestComputePrunePlanKeepsGroupsOfUnavailableGuilds(t *testing.T) {
	groups := map[string]*api.Group{
		"g_down":     {Id: "grp_down", Name: "down"},
		"g_up":       {Id: "grp_up", Name: "up"},
		"g_departed": {Id: "grp_departed", Name: "departed"},
	}
	// The shard hosting g_down died: discordgo already dropped it from state.
	// g_departed is a real departure -- the bot was kicked or the guild was
	// deleted -- so it is genuinely orphaned.
	stateGuilds := []*discordgo.Guild{{ID: "g_up", Name: "up"}}
	unavailable := map[string]struct{}{"g_down": {}}

	plan := computePrunePlan(groups, stateGuilds, unavailable)

	got := orphanGroupIDs(plan.orphanGroups)
	if len(got) != 1 || got[0] != "grp_departed" {
		t.Fatalf("orphan groups = %v; want only [grp_departed] -- an unavailable guild's group must never be deleted", got)
	}
	// An unavailable guild is not a leave candidate either: it is not in state
	// at all, so it cannot be, but assert it explicitly.
	if ids := orphanGuildIDs(plan.orphanGuilds); len(ids) != 0 {
		t.Errorf("orphan guilds = %v; want none", ids)
	}
}

// TestUnavailableGuildTracking pins the bookkeeping the prune pass depends on:
// a guild marked unavailable is remembered, GUILD_CREATE clears it, and an
// entry that has sat unavailable past the grace window expires so a guild the
// bot silently left while a shard was down can eventually be pruned.
func TestUnavailableGuildTracking(t *testing.T) {
	d := &DiscordIntegrator{unavailableGuilds: &MapOf[string, time.Time]{}}
	now := time.Now()

	d.markGuildUnavailable("g1", now)
	d.markGuildUnavailable("g2", now.Add(-unavailableGuildGraceTTL-time.Minute))
	d.markGuildUnavailable("g3", now)
	d.markGuildAvailable("g3")

	ids := d.unavailableGuildIDsAsOf(now)

	if _, ok := ids["g1"]; !ok {
		t.Errorf("g1 missing; a freshly unavailable guild must be remembered")
	}
	if _, ok := ids["g2"]; ok {
		t.Errorf("g2 present; an entry older than the %s grace window must expire", unavailableGuildGraceTTL)
	}
	if _, ok := ids["g3"]; ok {
		t.Errorf("g3 present; a guild that came back must be cleared")
	}
	if len(ids) != 1 {
		t.Errorf("unavailable IDs = %v; want exactly {g1}", ids)
	}

	// The expired entry must actually be dropped, not merely filtered, so the
	// map cannot grow without bound.
	if _, loaded := d.unavailableGuilds.Load("g2"); loaded {
		t.Errorf("expired entry g2 was filtered but not deleted")
	}
}

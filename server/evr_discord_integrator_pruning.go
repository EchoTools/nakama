package server

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

// maxReconcileGuildsPerPass bounds the non-destructive repair pass. Each
// reconciliation is a Discord REST call plus several DB writes, so a partial
// GroupsList result that makes hundreds of guilds look orphaned must not turn
// into hundreds of syncs. A count this large is a symptom of a bad read rather
// than that many genuinely broken guilds, so the pass is skipped entirely
// rather than truncated -- and, because a guild is only ever left after a
// failed repair attempt, skipping it also suppresses every guild leave.
const maxReconcileGuildsPerPass = 25

// orphanGroup pairs an orphaned Nakama group with the Discord guild ID taken
// from its metadata, so the prune path can act on both without re-parsing.
type orphanGroup struct {
	guildID string
	group   *api.Group
}

// prunePlan is the read-only result of a prune pass: what looks orphaned.
// Computing it performs no writes, so it is safe to build even in report-only
// mode.
type prunePlan struct {
	// orphanGroups are Nakama groups whose Discord guild the bot is no longer
	// a member of.
	orphanGroups []orphanGroup
	// orphanGuilds are Discord guilds the bot is a member of that have no
	// Nakama group.
	orphanGuilds []*discordgo.Guild
}

// pruneActions are the side-effecting operations executePrunePlan may perform.
// They are injected so the gating and accounting logic is testable without a
// Discord session or a database.
type pruneActions struct {
	syncGuild   func(*discordgo.Guild) error
	leaveGuild  func(guildID string) error
	deleteGroup func(groupID string) error
	purgeGuild  func(guildID string)
}

// pruneConfig is the operator-controlled policy for a prune pass.
type pruneConfig struct {
	// doGuildLeaves permits leaving Discord guilds that have no Nakama group
	// and could not be repaired.
	doGuildLeaves bool
	// doGroupDeletes permits deleting Nakama groups whose Discord guild the
	// bot is no longer in.
	doGroupDeletes bool
	// safetyThreshold is the maximum number of guilds that may be left, or
	// groups that may be deleted, in one pass. Exceeding it aborts that side
	// of the prune.
	safetyThreshold int
	// reportOnly suppresses every write the pass could make: the repair pass,
	// guild leaves and group deletes. It overrides doGuildLeaves and
	// doGroupDeletes.
	reportOnly bool
}

// pruneOutcome records what a prune pass actually did, as opposed to what it
// identified as a candidate.
type pruneOutcome struct {
	reconciledGuilds  int
	guildsLeft        int
	guildLeaveErrors  int
	groupsDeleted     int
	groupDeleteErrors int
}

// reconcileOrphanGuilds attempts to repair each orphan guild (bot is a member,
// but no matching Nakama group exists) by invoking syncFn — the same guildSync
// that runs on guild join — before the guild is considered for prune-leave.
// This covers guilds whose sync failed at join time (e.g. an oversize guild
// description failing GroupCreate with SQLSTATE 22001, as with guild
// 1522261692355055849), since gateway Guild Create events are not replayed
// on reconnect.
// Guilds whose sync attempt fails are returned as the remaining orphan
// candidates for the leave/safety-threshold logic. syncFn must be
// non-destructive: it must not leave the guild, so that every guild leave is
// performed by the caller's safety-checked prune path.
func reconcileOrphanGuilds(logger runtime.Logger, orphanGuilds []*discordgo.Guild, syncFn func(*discordgo.Guild) error) []*discordgo.Guild {
	var remaining []*discordgo.Guild
	for _, guild := range orphanGuilds {
		fields := map[string]any{
			"guild_id":   guild.ID,
			"guild_name": guild.Name,
		}
		logger.WithFields(fields).Info("Attempting to reconcile orphan guild via guild sync")
		if err := syncFn(guild); err != nil {
			logger.WithFields(fields).WithField("error", err.Error()).Warn("Failed to reconcile orphan guild; it remains an orphan candidate")
			remaining = append(remaining, guild)
			continue
		}
		logger.WithFields(fields).Info("Orphan guild sync succeeded")
	}
	return remaining
}

// snapshotStateGuilds returns a copy of the guilds the bot is a member of,
// taken under the discordgo state's read lock.
//
// discordgo owns Session.State and mutates it from the gateway goroutine under
// State.Lock(): GuildAdd appends to State.Guilds and overwrites an existing
// *Guild in place (`*g = *guild`), ChannelAdd appends to guild.Channels and
// overwrites an existing *Channel in place, and ChannelRemove compacts
// guild.Channels. The prune pass runs on the prune ticker goroutine, not the
// gateway goroutine, so reading State.Guilds unlocked and passing the LIVE
// *Guild pointers on to a deep reader — guildSync ranges guild.Channels, and
// the leave path logs the whole guild, which zap reflect-walks it — races the
// gateway. In production that surfaces as a torn slice header (index out of
// range) or a read of an already-removed channel.
//
// The copy goes exactly as deep as the consumers read: the Guild struct and its
// Channels. Every other reference-typed field is CLEARED rather than left
// aliasing live state, so a future deep read cannot silently reach back into
// gateway-mutated memory; a caller that needs one of them must extend the
// copy here.
//
// The Unavailable flag is a scalar and so rides along in the struct copy:
// computePrunePlan relies on it to skip READY's unavailable-guild stubs.
func snapshotStateGuilds(state *discordgo.State) []*discordgo.Guild {
	if state == nil {
		return nil
	}

	state.RLock()
	defer state.RUnlock()

	guilds := make([]*discordgo.Guild, 0, len(state.Guilds))
	for _, g := range state.Guilds {
		if g == nil {
			continue
		}
		guilds = append(guilds, copyStateGuild(g))
	}
	return guilds
}

// copyStateGuild copies a guild out of the discordgo state. It must be called
// with the state's read lock held. See snapshotStateGuilds for why.
func copyStateGuild(g *discordgo.Guild) *discordgo.Guild {
	// discordgo.Guild carries no lock, so the struct copy is safe; it takes the
	// scalar fields guildSync reads (ID, Name, OwnerID, Description, Icon) and
	// the Unavailable flag the prune plan filters on.
	c := *g

	// Channels is the one collection read deeply: guildSync scans it for the
	// #rules channel, and ChannelAdd overwrites *Channel values in place.
	c.Channels = make([]*discordgo.Channel, 0, len(g.Channels))
	for _, ch := range g.Channels {
		if ch == nil {
			continue
		}
		chCopy := *ch
		// The channel's own collections still alias live state (MessageAdd
		// appends to Channel.Messages under State.Lock()). Nothing reads them
		// here, so drop them rather than hand out an aliased header.
		chCopy.Recipients = nil
		chCopy.Messages = nil
		chCopy.PermissionOverwrites = nil
		chCopy.ThreadMetadata = nil
		chCopy.Member = nil
		chCopy.Members = nil
		chCopy.AvailableTags = nil
		chCopy.AppliedTags = nil
		// Pointer fields nothing here reads. ChannelAdd replaces the pointer
		// (`*c = *channel`) rather than writing through it, so aliasing them
		// would not actually race — but "cleared unless copied" has to hold
		// for every reference-typed field or the contract is unenforceable.
		chCopy.LastPinTimestamp = nil
		chCopy.DefaultSortOrder = nil
		c.Channels = append(c.Channels, &chCopy)
	}
	c.Features = slices.Clone(g.Features)

	// Collections no consumer of this snapshot reads. Cleared, not aliased.
	c.Roles = nil
	c.Emojis = nil
	c.Stickers = nil
	c.Members = nil
	c.Presences = nil
	c.Threads = nil
	c.VoiceStates = nil
	c.StageInstances = nil

	return &c
}

// computePrunePlan identifies orphaned groups and guilds by comparing the
// Nakama groups keyed by Discord guild ID against the guilds present in the
// Discord session state.
//
// unavailableGuildIDs are guilds the bot saw go unavailable. discordgo removes
// a guild from State.Guilds on EVERY GUILD_DELETE, including Unavailable=true
// (discordgo@v0.29.0 state.go:973-981), and it does so before any handler runs
// (event.go:188-200) -- so a shard outage silently shrinks State.Guilds. Those
// guilds count as present here: their groups hold role maps, channel IDs,
// suspension-inheritance configuration and every membership, none of which can
// be rebuilt from Discord if GroupDelete removes them.
//
// The record is best-effort, not a hard guarantee. discordgo dispatches typed
// handlers on a goroutine unless Session.SyncEvents is set (event.go:166-173),
// which this codebase does not set, so there is a brief window in which a
// guild is neither in State.Guilds nor recorded as unavailable. That window is
// microseconds against a 15-minute prune tick.
func computePrunePlan(groupByGuildID map[string]*api.Group, stateGuilds []*discordgo.Guild, unavailableGuildIDs map[string]struct{}) prunePlan {
	botGuildMap := make(map[string]*discordgo.Guild, len(stateGuilds))
	for _, g := range stateGuilds {
		botGuildMap[g.ID] = g
	}

	var plan prunePlan

	for id, g := range groupByGuildID {
		if _, ok := botGuildMap[id]; ok {
			continue
		}
		if _, unavailable := unavailableGuildIDs[id]; unavailable {
			continue
		}
		plan.orphanGroups = append(plan.orphanGroups, orphanGroup{guildID: id, group: g})
	}
	// Map iteration order is random; sort so logs and behaviour are stable.
	sort.Slice(plan.orphanGroups, func(i, j int) bool {
		return plan.orphanGroups[i].guildID < plan.orphanGroups[j].guildID
	})

	for _, g := range stateGuilds {
		if g.Unavailable {
			// On READY, and whenever a shard is down, State.Guilds holds
			// stubs with no Name, OwnerID or Channels. guildSync cannot
			// reconcile one, so treating it as a leave candidate would make
			// the bot leave a healthy guild because Discord had an outage.
			continue
		}
		if _, ok := groupByGuildID[g.ID]; !ok {
			plan.orphanGuilds = append(plan.orphanGuilds, g)
		}
	}

	return plan
}

// executePrunePlan carries out a prune plan, subject to cfg.
//
// Ordering and gating are load-bearing:
//
//   - Reconciliation is a REPAIR, not a prune action, and runs on every pass
//     regardless of the do* flags -- exactly as it did before this file was
//     split out. It creates the Nakama group a member guild should already
//     have had, the condition guild 1522261692355055849 was stuck in after its
//     join-time guildSync failed with SQLSTATE 22001. Gating it on the
//     destructive flags, or behind the mass-leave valve, would make that
//     incident unhealable at the shipped defaults, where both flags are false
//     and SafetyLimit is 0. Operators who need a genuinely write-free pass set
//     reportOnly, which suppresses every write below.
//
//   - Reconciliation is bounded by maxReconcileGuildsPerPass instead, so a
//     partial GroupsList result still cannot cost hundreds of REST calls.
//
//   - A guild is only ever LEFT after a failed repair attempt. If the repair
//     pass did not run, guild leaves are suppressed for the whole pass: the
//     bot must never leave a guild it did not try to fix.
//
//   - Each safety valve guards its own side, is evaluated against what would
//     actually be destroyed -- guild leaves against the POST-repair
//     candidates, group deletes against the orphaned groups -- and is only
//     evaluated at all when that side can act. A valve that "trips" for writes
//     that are already switched off is a false alarm, and because the pass
//     runs every 15 minutes it is a false alarm forever. Tripping a live valve
//     skips that side's writes and returns an error; it does not suppress the
//     other side, nor the repair that already ran.
//
//   - The returned outcome counts work that actually succeeded, so the
//     completion log never claims guilds were left or groups deleted when they
//     were not.
func executePrunePlan(logger runtime.Logger, plan prunePlan, cfg pruneConfig, actions pruneActions) (pruneOutcome, error) {
	var outcome pruneOutcome

	orphanGroupCount := len(plan.orphanGroups)
	orphanGuildCount := len(plan.orphanGuilds)

	// Repair pass.
	leaveCandidates := plan.orphanGuilds
	reconciled := false
	switch {
	case cfg.reportOnly:
		if orphanGroupCount+orphanGuildCount > 0 {
			logger.WithFields(map[string]any{
				"orphan_group_count": orphanGroupCount,
				"orphan_guild_count": orphanGuildCount,
			}).Warn("Prune is in report-only mode; no repairs, guild leaves or group deletes will be performed")
		}
	case orphanGuildCount > maxReconcileGuildsPerPass:
		// Log identifiers only: a *discordgo.Guild carries members, channels,
		// presences and emojis.
		logger.WithFields(map[string]any{
			"orphan_guild_count": orphanGuildCount,
			"reconcile_limit":    maxReconcileGuildsPerPass,
			"orphan_guild_ids":   orphanGuildIDs(plan.orphanGuilds),
		}).Error(fmt.Sprintf("More than %d orphaned guilds; skipping reconciliation and guild leaves (suspect a bad read, not that many broken guilds)", maxReconcileGuildsPerPass))
	default:
		leaveCandidates = reconcileOrphanGuilds(logger, plan.orphanGuilds, actions.syncGuild)
		outcome.reconciledGuilds = orphanGuildCount - len(leaveCandidates)
		reconciled = true
	}

	var pruneErr error

	// Which sides of the prune can act at all. reconciled is false whenever
	// the repair pass was skipped, including in report-only mode, so it
	// already covers the leave side.
	leavesEnabled := cfg.doGuildLeaves && reconciled
	deletesEnabled := cfg.doGroupDeletes && !cfg.reportOnly

	// Safety valves, evaluated against what would actually be destroyed, and
	// only for a side that could destroy something.
	leaveTripped := leavesEnabled && len(leaveCandidates) > cfg.safetyThreshold
	deleteTripped := deletesEnabled && orphanGroupCount > cfg.safetyThreshold
	if leaveTripped || deleteTripped {
		// Log identifiers only: a *discordgo.Guild carries members, channels,
		// presences and emojis, which would make this the largest log line in
		// the process at the worst possible moment.
		logger.WithFields(map[string]any{
			"orphan_group_count":    orphanGroupCount,
			"orphan_guild_count":    orphanGuildCount,
			"leave_candidate_count": len(leaveCandidates),
			"orphan_group_ids":      orphanGroupIDs(plan.orphanGroups),
			"leave_candidate_ids":   orphanGuildIDs(leaveCandidates),
			"guild_leaves_skipped":  leaveTripped,
			"group_deletes_skipped": deleteTripped,
		}).Error(fmt.Sprintf("Pruning Discord guilds and groups will leave more than %d, skipping to avoid mass leave", cfg.safetyThreshold))
		pruneErr = fmt.Errorf("pruning Discord guilds and groups will leave more than %d, skipping to avoid mass leave", cfg.safetyThreshold)
	}

	// Remove any guilds that are not in Nakama and could not be repaired.
	if leavesEnabled && !leaveTripped {
		for _, guild := range leaveCandidates {
			logger.WithFields(map[string]any{
				"guild_name": guild.Name,
				"guild_id":   guild.ID,
			}).Info("Leaving orphaned discord guild")
			if err := actions.leaveGuild(guild.ID); err != nil {
				logger.WithField("error", err).Warn("Failed to leave orphaned discord guild")
				outcome.guildLeaveErrors++
				continue
			}
			outcome.guildsLeft++
		}
	}

	// Remove any Nakama groups of guilds that the bot is not a member of
	if deletesEnabled && !deleteTripped {
		for _, og := range plan.orphanGroups {
			logger.WithFields(map[string]any{
				"group_id":   og.group.GetId(),
				"group_name": og.group.GetName(),
				"guild_id":   og.guildID,
				// GroupMetadata (role map, matchmaking/audit channel IDs,
				// suspension inheritance) is exactly what this delete
				// destroys and cannot be rebuilt from Discord. This log line
				// is the only forensic record of it.
				"metadata": og.group.GetMetadata(),
			}).Info("Deleting orphaned group from Nakama")
			if err := actions.deleteGroup(og.group.GetId()); err != nil {
				logger.WithField("error", err).Warn("Failed to delete orphaned group from Nakama")
				outcome.groupDeleteErrors++
				continue
			}
			outcome.groupsDeleted++
			// Drop the guild ID -> group ID mapping, exactly as
			// handleGuildDelete does. A stale entry would make the guild's
			// eventual return resolve the deleted group ID, take guildSync's
			// UPDATE branch, match zero rows, and fail with
			// runtime.ErrGroupNotUpdated for the life of the process -- after
			// which the next prune tick would leave the healthy guild.
			actions.purgeGuild(og.guildID)
		}
	}

	// Log the results. These counts are what happened, not what was
	// identified: an operator reading this log must not believe guilds were
	// left or groups deleted when the do* flag was off or the call failed.
	if orphanGroupCount+orphanGuildCount > 0 {
		logger.WithFields(map[string]any{
			"orphan_groups":        orphanGroupCount,
			"orphan_guilds":        orphanGuildCount,
			"reconciled_guilds":    outcome.reconciledGuilds,
			"deleted_groups":       outcome.groupsDeleted,
			"failed_group_deletes": outcome.groupDeleteErrors,
			"left_guilds":          outcome.guildsLeft,
			"failed_guild_leaves":  outcome.guildLeaveErrors,
			"do_guild_leaves":      cfg.doGuildLeaves,
			"do_group_deletes":     cfg.doGroupDeletes,
			"report_only":          cfg.reportOnly,
		}).Info("Pruned unused groups and guilds")
	}

	return outcome, pruneErr
}

func orphanGroupIDs(orphans []orphanGroup) []string {
	ids := make([]string, 0, len(orphans))
	for _, og := range orphans {
		ids = append(ids, og.group.GetId())
	}
	return ids
}

func orphanGuildIDs(guilds []*discordgo.Guild) []string {
	ids := make([]string, 0, len(guilds))
	for _, g := range guilds {
		ids = append(ids, g.ID)
	}
	return ids
}

// prunePageSize is the GroupsList page size used to enumerate guild groups.
const prunePageSize = 100

// prunePassDeps are the integrator-bound inputs and side effects of one prune
// pass. Bundling them makes the pass itself a pure function of its
// dependencies, so the wiring (newPrunePassDeps) and the policy (runPrunePass,
// executePrunePlan) can each be tested without a database or a live Discord
// session. Every write this package can perform passes through
// prunePassDeps.actions.
type prunePassDeps struct {
	// listGroups returns one page of Nakama groups plus the next cursor.
	listGroups func(ctx context.Context, cursor string) ([]*api.Group, string, error)
	// stateGuilds returns the guilds currently in the Discord session state.
	stateGuilds func() []*discordgo.Guild
	// unavailableGuildIDs returns the guilds that are absent from state only
	// because they went unavailable. See computePrunePlan.
	unavailableGuildIDs func(now time.Time) map[string]struct{}
	actions             pruneActions
}

// collectGuildGroups enumerates every Nakama guild group, keyed by the Discord
// guild ID in its metadata. Groups with unreadable or guild-less metadata are
// skipped rather than treated as orphans: a group that cannot be keyed must
// never become a delete candidate.
func collectGuildGroups(ctx context.Context, logger runtime.Logger, listGroups func(context.Context, string) ([]*api.Group, string, error)) (map[string]*api.Group, error) {
	groupByGuildID := make(map[string]*api.Group)
	var cursor string
	for {
		groups, next, err := listGroups(ctx, cursor)
		if err != nil {
			logger.WithField("error", err).Error("Failed to list groups")
			return nil, err
		}
		for _, group := range groups {
			metadata := GroupMetadata{}
			if err := json.Unmarshal([]byte(group.Metadata), &metadata); err != nil {
				logger.WithField("error", err).Error("Failed to unmarshal group metadata")
				continue
			}
			if metadata.GuildID == "" {
				logger.WithFields(map[string]any{
					"group_id":   group.GetId(),
					"group_name": group.GetName(),
				}).Warn("Group metadata does not contain GuildID, skipping")
				continue
			}
			groupByGuildID[metadata.GuildID] = group
		}
		if next == "" {
			return groupByGuildID, nil
		}
		cursor = next
	}
}

// runPrunePass is one whole prune tick: enumerate groups, compare them against
// Discord state, then act. It is separated from newPrunePassDeps so the
// sequencing here -- in particular that the unavailable-guild record is
// consulted, and that an empty state short-circuits before anything can be
// destroyed -- is testable.
func runPrunePass(ctx context.Context, logger runtime.Logger, deps prunePassDeps, cfg pruneConfig, now time.Time) (pruneOutcome, error) {
	groupByGuildID, err := collectGuildGroups(ctx, logger, deps.listGroups)
	if err != nil {
		return pruneOutcome{}, err
	}

	stateGuilds := deps.stateGuilds()
	if len(stateGuilds) == 0 {
		// Every group would look orphaned. This is a bad read, not an empty
		// deployment.
		logger.Warn("No guilds found in Discord state, skipping pruning operation")
		return pruneOutcome{}, nil
	}

	plan := computePrunePlan(groupByGuildID, stateGuilds, deps.unavailableGuildIDs(now))

	return executePrunePlan(logger, plan, cfg, deps.actions)
}

// newPrunePassDeps binds a prune pass to this integrator. syncGuild is passed
// in rather than read off d so a test can observe how the repair pass calls it;
// production always supplies d.guildSync.
func (d *DiscordIntegrator) newPrunePassDeps(ctx context.Context, syncGuild func(context.Context, *zap.Logger, *discordgo.Guild, bool) error) prunePassDeps {
	return prunePassDeps{
		listGroups: func(ctx context.Context, cursor string) ([]*api.Group, string, error) {
			return d.nk.GroupsList(ctx, "", GuildGroupLangTag, nil, nil, prunePageSize, cursor)
		},
		// The prune pass runs on the prune-ticker goroutine while discordgo
		// mutates State from the gateway goroutine, so it must never see the
		// live State.Guilds slice or the live *Guild pointers in it.
		// snapshotStateGuilds takes the copy under State.RLock().
		stateGuilds:         func() []*discordgo.Guild { return snapshotStateGuilds(d.dg.State) },
		unavailableGuildIDs: d.unavailableGuildIDsAsOf,
		actions: pruneActions{
			// Reconciliation must be non-destructive
			// (leaveOnBannedOwner=false): a guild that cannot be synced stays
			// an orphan candidate and is only ever left by the
			// safety-threshold-checked prune path in executePrunePlan.
			syncGuild: func(guild *discordgo.Guild) error {
				return syncGuild(ctx, d.logger, guild, false)
			},
			leaveGuild: func(guildID string) error {
				return d.dg.GuildLeave(guildID)
			},
			deleteGroup: func(groupID string) error {
				return d.nk.GroupDelete(ctx, groupID)
			},
			// Deleting a group without dropping its guild ID -> group ID cache
			// entry strands the guild forever. See executePrunePlan.
			purgeGuild: func(guildID string) {
				d.Purge(guildID)
			},
		},
	}
}

func (d *DiscordIntegrator) pruneGuildGroups(ctx context.Context, logger runtime.Logger, cfg pruneConfig) error {
	_, err := runPrunePass(ctx, logger, d.newPrunePassDeps(ctx, d.guildSync), cfg, time.Now())
	return err
}

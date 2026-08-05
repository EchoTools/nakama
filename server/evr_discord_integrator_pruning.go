package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// pruneSafetyThreshold is the maximum number of orphaned groups or guilds that can be deleted/left before the pruning operation is aborted.

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

// computePrunePlan identifies orphaned groups and guilds by comparing the
// Nakama groups keyed by Discord guild ID against the guilds present in the
// Discord session state.
func computePrunePlan(groupByGuildID map[string]*api.Group, stateGuilds []*discordgo.Guild) prunePlan {
	botGuildMap := make(map[string]*discordgo.Guild, len(stateGuilds))
	for _, g := range stateGuilds {
		botGuildMap[g.ID] = g
	}

	var plan prunePlan

	for id, g := range groupByGuildID {
		if _, ok := botGuildMap[id]; !ok {
			plan.orphanGroups = append(plan.orphanGroups, orphanGroup{guildID: id, group: g})
		}
	}
	// Map iteration order is random; sort so logs and behaviour are stable.
	sort.Slice(plan.orphanGroups, func(i, j int) bool {
		return plan.orphanGroups[i].guildID < plan.orphanGroups[j].guildID
	})

	for _, g := range stateGuilds {
		if _, ok := groupByGuildID[g.ID]; !ok {
			plan.orphanGuilds = append(plan.orphanGuilds, g)
		}
	}

	return plan
}

// executePrunePlan carries out a prune plan, subject to the do* flags and the
// safety threshold.
//
// Ordering and gating are load-bearing:
//   - The mass-leave safety threshold is checked before ANY side effect. It
//     cannot guard work that runs in front of it, so nothing -- including the
//     write-heavy reconciliation pass -- may precede it.
//   - Every write is gated on a do* flag. With both flags false the caller is
//     running this as a report, and a report must not create Nakama groups,
//     write GuildGroupState, or mutate the guild group registry.
//   - The returned outcome counts work that actually succeeded, so the
//     completion log never claims guilds were left or groups deleted when they
//     were not.
func executePrunePlan(logger runtime.Logger, plan prunePlan, doGuildLeaves, doGroupDeletes bool, pruneSafetyThreshold int, actions pruneActions) (pruneOutcome, error) {
	var outcome pruneOutcome

	orphanGroupCount := len(plan.orphanGroups)
	orphanGuildCount := len(plan.orphanGuilds)

	// Safety check to ensure this is not a mass leave operation. This runs
	// first: a partial GroupsList result that makes hundreds of guilds look
	// orphaned must abort here rather than after hundreds of reconciliation
	// syncs (a Discord REST call plus several DB writes each).
	if orphanGroupCount > pruneSafetyThreshold || orphanGuildCount > pruneSafetyThreshold {
		// Log identifiers only: a *discordgo.Guild carries members, channels,
		// presences and emojis, which would make this the largest log line in
		// the process at the worst possible moment.
		logger.WithFields(map[string]any{
			"orphan_group_count": orphanGroupCount,
			"orphan_guild_count": orphanGuildCount,
			"orphan_group_ids":   orphanGroupIDs(plan.orphanGroups),
			"orphan_guild_ids":   orphanGuildIDs(plan.orphanGuilds),
		}).Error(fmt.Sprintf("Pruning Discord guilds and groups will leave more than %d, skipping to avoid mass leave", pruneSafetyThreshold))
		return outcome, fmt.Errorf("pruning Discord guilds and groups will leave more than %d, skipping to avoid mass leave", pruneSafetyThreshold)
	}

	// Remove any guilds that are not in Nakama
	if doGuildLeaves {
		// Try to repair orphan guilds before treating them as prune
		// candidates: re-run guildSync so a guild whose join-time sync failed
		// gets its group created instead of the bot leaving a healthy
		// community. This is gated on doGuildLeaves because a leave is the
		// only thing it protects against -- and because it writes, so it must
		// not run when the caller only wants a report.
		leaveCandidates := reconcileOrphanGuilds(logger, plan.orphanGuilds, actions.syncGuild)
		outcome.reconciledGuilds = orphanGuildCount - len(leaveCandidates)

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
	if doGroupDeletes {
		for _, og := range plan.orphanGroups {
			logger.WithFields(map[string]any{
				"group_id":   og.group.GetId(),
				"group_name": og.group.GetName(),
				"guild_id":   og.guildID,
			}).Info("Deleting orphaned group from Nakama")
			if err := actions.deleteGroup(og.group.GetId()); err != nil {
				logger.WithField("error", err).Warn("Failed to delete orphaned group from Nakama")
				outcome.groupDeleteErrors++
				continue
			}
			outcome.groupsDeleted++
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
			"do_guild_leaves":      doGuildLeaves,
			"do_group_deletes":     doGroupDeletes,
		}).Info("Pruned unused groups and guilds")
	}

	return outcome, nil
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

func (d *DiscordIntegrator) pruneGuildGroups(ctx context.Context, logger runtime.Logger, doGuildLeaves, doGroupDeletes bool, pruneSafetyThreshold int) error {
	var (
		groupByGuildID = make(map[string]*api.Group)
		cursor         string
		err            error
		groups         []*api.Group
	)
	// Collect the guild groups from Nakama
	for {
		groups, cursor, err = d.nk.GroupsList(ctx, "", GuildGroupLangTag, nil, nil, 100, cursor)
		if err != nil {
			logger.WithField("error", err).Error("Failed to list groups")
			return err
		}
		// Iterate over the groups and extract the guild ID from the metadata
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
		if cursor == "" {
			break
		}
	}

	if len(d.dg.State.Guilds) == 0 {
		logger.Warn("No guilds found in Discord state, skipping pruning operation")
		return nil
	}

	plan := computePrunePlan(groupByGuildID, d.dg.State.Guilds)

	actions := pruneActions{
		// Reconciliation must be non-destructive (leaveOnBannedOwner=false):
		// a guild that cannot be synced stays an orphan candidate and is only
		// left by the safety-threshold-checked prune path below.
		syncGuild: func(guild *discordgo.Guild) error {
			return d.guildSync(ctx, d.logger, guild, false)
		},
		leaveGuild: func(guildID string) error {
			return d.dg.GuildLeave(guildID)
		},
		deleteGroup: func(groupID string) error {
			return d.nk.GroupDelete(ctx, groupID)
		},
		purgeGuild: func(guildID string) {
			d.Purge(guildID)
		},
	}

	_, err = executePrunePlan(logger, plan, doGuildLeaves, doGroupDeletes, pruneSafetyThreshold, actions)
	return err
}

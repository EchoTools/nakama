package server

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// pruneSafetyThreshold is the maximum number of orphaned groups or guilds that can be deleted/left before the pruning operation is aborted.

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
// guild.Channels. pruneGuildGroups runs on the prune ticker goroutine, not the
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
	// scalar fields guildSync reads (ID, Name, OwnerID, Description, Icon).
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

	stateGuilds := snapshotStateGuilds(d.dg.State)
	if len(stateGuilds) == 0 {
		logger.Warn("No guilds found in Discord state, skipping pruning operation")
		return nil
	}
	// Get the guilds where the bot is a member
	botGuildMap := make(map[string]*discordgo.Guild, len(stateGuilds))
	for _, g := range stateGuilds {
		botGuildMap[g.ID] = g
	}

	// Collect orphan groups to delete
	var orphanGroups []*api.Group
	for id, g := range groupByGuildID {
		if _, ok := botGuildMap[id]; !ok {
			orphanGroups = append(orphanGroups, g)
		}
	}

	// Collect orphan guilds to leave
	var orphanGuilds []*discordgo.Guild
	for id, g := range botGuildMap {
		if _, ok := groupByGuildID[id]; !ok {
			orphanGuilds = append(orphanGuilds, g)
		}
	}

	// Try to repair orphan guilds before treating them as prune candidates:
	// re-run guildSync so a guild whose join-time sync failed gets its group
	// created instead of the bot leaving a healthy community. The sync runs
	// non-destructively (leaveOnBannedOwner=false): a guild that cannot be
	// synced (e.g. a globally banned owner) stays an orphan candidate and is
	// only left by the safety-threshold-checked prune path below.
	orphanGuilds = reconcileOrphanGuilds(logger, orphanGuilds, func(guild *discordgo.Guild) error {
		return d.guildSync(ctx, d.logger, guild, false)
	})

	// Safety check to ensure this is not a mass leave operation
	if len(orphanGroups) > pruneSafetyThreshold || len(orphanGuilds) > pruneSafetyThreshold {
		logger.WithFields(map[string]any{
			"orphan_groups": orphanGroups,
			"orphan_guilds": orphanGuilds,
		}).Error(fmt.Sprintf("Pruning Discord guilds and groups will leave more than %d, skipping to avoid mass leave", pruneSafetyThreshold))
		return fmt.Errorf("Pruning Discord guilds and groups will leave more than %d, skipping to avoid mass leave", pruneSafetyThreshold)
	}

	// Remove any guilds that are not in Nakama
	if doGuildLeaves {
		for _, guild := range orphanGuilds {
			logger.WithFields(map[string]any{
				"guild_name":     guild.Name,
				"guild_id":       guild.ID,
				"guild_metadata": guild,
			}).Info("Leaving orphaned discord guild")
			if err := d.dg.GuildLeave(guild.ID); err != nil {
				logger.WithField("error", err).Warn("Failed to leave orphaned discord guild")
				continue
			}
		}
	}

	// Remove any Nakama groups of guilds that the bot is not a member of
	if doGroupDeletes {
		for _, g := range orphanGroups {
			logger.WithFields(map[string]any{
				"group_id":   g.GetId(),
				"group_name": g.GetName(),
				"metadata":   g.GetMetadata(),
			}).Info("Deleting orphaned group from Nakama")
			if err := d.nk.GroupDelete(ctx, g.GetId()); err != nil {
				logger.WithField("error", err).Warn("Failed to delete orphaned group from Nakama")
				continue
			}
		}
	}

	// Log the results
	if len(orphanGroups)+len(orphanGuilds) > 0 {
		logger.WithFields(map[string]any{
			"deleted_groups": len(orphanGroups),
			"left_guilds":    len(orphanGuilds),
		}).Info("Pruned unused groups and guilds")
	}

	return nil
}

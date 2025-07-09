package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// pruneSafetyThreshold is the maximum number of orphaned groups or guilds that can be deleted/left before the pruning operation is aborted.

func (d *DiscordIntegrator) pruneGuildGroups(ctx context.Context, logger runtime.Logger, doGuildLeaves, doGroupDeletes bool, pruneSafetyThreshold int) error {
	var (
		groupByGuildID = make(map[string]*api.Group)
		cursor         string
		err            error
		groups         []*api.Group
	)
	// Collect the guild groups from Nakama
	for {
		groups, cursor, err = d.nk.GroupsList(ctx, "", "guild", nil, nil, 100, cursor)
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
	// Get the guilds where the bot is a member
	botGuildMap := make(map[string]*discordgo.Guild)
	for _, g := range d.dg.State.Guilds {
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

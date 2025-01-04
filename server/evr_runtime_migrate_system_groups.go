package server

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
)

var _ = SystemMigrator(&PruneSystemGroups{})

type PruneSystemGroups struct{}

func (m *PruneSystemGroups) MigrateSystem(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) error {
	_nk := nk.(*RuntimeGoNakamaModule)

	vars := _nk.config.GetRuntime().Environment

	botToken, ok := vars["DISCORD_BOT_TOKEN"]
	if !ok {
		return fmt.Errorf("Bot token is not set in context.")
	}

	var dg *discordgo.Session
	var err error
	if botToken != "" {
		dg, err = discordgo.New("Bot " + botToken)
		if err != nil {
			logger.Error("Unable to create bot")
		}
		dg.StateEnabled = true
	}

	for _, langType := range []string{"role", "entitlement"} {

		// Delete entitlement groups
		if groups, _, err := nk.GroupsList(ctx, "", langType, nil, nil, 100, ""); err != nil {
			return fmt.Errorf("error listing groups: %w", err)
		} else {
			for _, group := range groups {
				if err := nk.GroupDelete(ctx, group.Id); err != nil {
					return fmt.Errorf("error deleting group: %w", err)
				}
			}
		}
	}

	guildIDs := make(map[string]struct{})

	cursor := ""
	for {

		guilds, err := dg.UserGuilds(100, "", cursor, false)
		if err != nil {
			return fmt.Errorf("error getting guilds: %w", err)
		}

		for _, g := range guilds {
			guildIDs[g.ID] = struct{}{}
		}
		if len(guilds) == 0 {
			break
		}
		if len(guilds) < 100 {
			break
		}
		cursor = guilds[len(guilds)-1].ID
	}

	// Delete guild groups that are not in the guilds collection
	if groups, _, err := nk.GroupsList(ctx, "", "guild", nil, nil, 100, ""); err != nil {
		return fmt.Errorf("error listing groups: %w", err)
	} else {
		for _, g := range groups {
			gg, err := NewGuildGroup(g)
			if err != nil {
				return fmt.Errorf("error getting guild group: %w", err)
			}

			// Delete it if the bot is not in the guild
			if _, ok := guildIDs[gg.GuildID]; !ok {
				if err := nk.GroupDelete(ctx, gg.ID().String()); err != nil {
					return fmt.Errorf("error deleting group: %w", err)
				}
				continue
			}

			// Prune the guild cache
			roles := gg.Roles.AsSlice()
			updated := false
			for r, _ := range gg.RoleCache {
				if !slices.Contains(roles, r) {
					delete(gg.RoleCache, r)
					updated = true
				}
			}

			if updated {
				if err := nk.GroupUpdate(ctx, gg.ID().String(), "", "", "", "", "", "", false, gg.MarshalMap(), 0); err != nil {
					return fmt.Errorf("error updating group: %w", err)
				}
			}
		}
	}

	return nil
}

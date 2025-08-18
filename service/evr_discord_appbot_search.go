package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

type CommandOption interface {
	int | bool | float64 | ~string | *discordgo.User | *discordgo.Role | *discordgo.MessageAttachment
}

func parseCommandOption[E CommandOption](s *discordgo.Session, i *discordgo.InteractionCreate, name string, dst *E) error {
	if i == nil {
		return errors.New("interaction is nil")
	}

	// Make sure to check that the Type of the interaction is InteractionApplicationCommand before calling.
	if i.Type != discordgo.InteractionApplicationCommand {
		return fmt.Errorf("unexpected interaction type: %d", i.Type)
	}

	for _, o := range i.ApplicationCommandData().Options {
		if o == nil || o.Name != name {
			continue
		}

		var expectedType discordgo.ApplicationCommandOptionType

		switch any(dst).(type) {
		case *string:
			expectedType = discordgo.ApplicationCommandOptionString
		case *int:
			expectedType = discordgo.ApplicationCommandOptionInteger
		case *bool:
			expectedType = discordgo.ApplicationCommandOptionBoolean
		case *float64:
			expectedType = discordgo.ApplicationCommandOptionNumber
		case *discordgo.User:
			expectedType = discordgo.ApplicationCommandOptionUser
		case *discordgo.Role:
			expectedType = discordgo.ApplicationCommandOptionRole
		case *discordgo.MessageAttachment:
			expectedType = discordgo.ApplicationCommandOptionAttachment
		default:
			return fmt.Errorf("unsupported flag type for option %s", o.Name)
		}

		if o.Type != expectedType {
			return fmt.Errorf("unexpected type for option %s: expected %d, got %d", o.Name, expectedType, o.Type)
		}

		switch v := any(dst).(type) {
		case *string:
			*v = o.StringValue()
		case *int64:
			*v = o.IntValue()
		case *float64:
			*v = o.FloatValue()
		case *bool:
			*v = o.BoolValue()
		case *discordgo.User:
			user, err := s.User(o.Value.(string))
			if err != nil {
				return fmt.Errorf("failed to get user for option %s: %w", o.Name, err)
			}
			*v = *user
		case *discordgo.Role:
			role, err := s.State.Role(i.GuildID, o.Value.(string))
			if err != nil {
				return fmt.Errorf("failed to get role for option %s: %w", o.Name, err)
			}
			*v = *role
		}
	}
	return nil
}

func (d *DiscordAppBot) handleSearch(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userIDStr string, groupID string) error {

	var (
		nk      = d.nk
		db      = d.db
		partial string
	)

	callerGuildGroups, err := GuildUserGroupsList(ctx, d.nk, d.guildGroupRegistry, userIDStr)
	if err != nil {
		return fmt.Errorf("failed to get guild groups: %w", err)
	}

	isGuildAuditor := false
	if isGlobalOperator, err := CheckSystemGroupMembership(ctx, db, userIDStr, GroupGlobalOperators); err != nil {
		return fmt.Errorf("error checking global operator status: %w", err)
	} else if isGlobalOperator {
		isGuildAuditor = true
	} else if gg, ok := callerGuildGroups[groupID]; ok && gg.IsAuditor(userIDStr) {
		isGuildAuditor = true
	}

	if err := parseCommandOption(s, i, "pattern", &partial); err != nil {
		return err
	}

	partial = strings.ToLower(partial)
	if partial == "" {
		return simpleInteractionResponse(s, i, "Please provide a search pattern")
	}

	type result struct {
		account *api.Account
		updated time.Time
		matches map[string]time.Time
	}

	var (
		results           = make([]result, 0)
		embeds            = make([]*discordgo.MessageEmbed, 0)
		useWildcardPrefix bool
		useWildcardSuffix bool
	)

	// Check if the display name has a wildcard prefix or suffix
	// Remove it from the search string
	if strings.HasPrefix(partial, "*") {
		partial = partial[1:]
		useWildcardPrefix = true
	}
	if strings.HasSuffix(partial, "*") {
		partial = partial[:len(partial)-1]
		useWildcardSuffix = true
	}

	// Sanitize the display name
	if displayName := strings.ToLower(sanitizeDisplayName(partial)); displayName == "" && !isGuildAuditor {
		return simpleInteractionResponse(s, i, "Invalid search pattern")
	} else if len(partial) < 3 && (useWildcardPrefix || useWildcardSuffix) {
		return fmt.Errorf("search string is too short for wildcards")
	} else {
		pattern := Query.QuoteStringValue(displayName)

		// Check if the display name is a partial match
		if useWildcardPrefix {
			pattern = fmt.Sprintf(".*%s", pattern)
		}
		if useWildcardSuffix {
			pattern = fmt.Sprintf("%s.*", pattern)
		}

		displayNameMatches, err := DisplayNameCacheRegexSearch(ctx, nk, pattern, 5)
		if err != nil {
			logger.WithFields(map[string]any{
				"partial":   partial,
				"sanitized": displayName,
				"pattern":   pattern,
				"error":     err,
			}).Error("Failed to search display name history")
			return fmt.Errorf("failed to search display name history: %w", err)
		}
		for userID, byGroup := range displayNameMatches {

			account, err := nk.AccountGetId(ctx, userID)
			if err != nil {
				logger.WithFields(map[string]interface{}{}).Warn("Failed to get account")
				continue
			}

			result := result{
				account: account,
				matches: make(map[string]time.Time, len(byGroup)),
			}

			for _, names := range byGroup {
				for dn, ts := range names {

					dn = strings.ToLower(dn)
					include := false
					if useWildcardPrefix && useWildcardSuffix {
						include = strings.Contains(dn, partial)
					} else if useWildcardPrefix {
						include = strings.HasSuffix(dn, partial)
					} else if useWildcardSuffix {
						include = strings.HasPrefix(dn, partial)
					} else {
						include = dn == partial
					}

					if !include {
						continue
					}

					if ts.After(result.matches[dn]) {
						result.matches[dn] = ts

						if ts.After(result.updated) {
							result.updated = ts
						}
					}
				}
			}

			if len(result.matches) == 0 {
				continue
			}

			results = append(results, result)
		}
	}

	if isGuildAuditor {
		var pattern string

		if len(partial) > 2 && partial[0] == '/' && partial[len(partial)-1] == '/' {
			pattern = partial[1 : len(partial)-1]
		} else {
			pattern = Query.QuoteStringValue(partial)
			if useWildcardPrefix {
				pattern = fmt.Sprintf(".*%s", pattern)
			}
			if useWildcardSuffix {
				pattern = fmt.Sprintf("%s.*", pattern)
			}
		}
		// Search for Login data
		loginHistoryResults, err := LoginHistoryRegexSearch(ctx, nk, pattern, 5)
		if err != nil {
			logger.WithField("error", err).Error("Failed to search login history")
			return fmt.Errorf("failed to search login history: %w", err)
		}

		if len(loginHistoryResults) > 0 {
			pattern := strings.ToLower(partial)
			for _, userID := range loginHistoryResults {
				account, err := nk.AccountGetId(ctx, userID)
				if err != nil {
					logger.WithFields(map[string]any{}).Warn("Failed to get account")
					continue
				}
				r := result{
					account: account,
					updated: account.User.GetUpdateTime().AsTime(),
					matches: make(map[string]time.Time),
				}

				loginHistory := NewLoginHistory(userID)
				if err := StorableRead(ctx, nk, userID, loginHistory, false); err != nil {
					logger.WithField("error", err).Error("Failed to read login history")
				}
				for _, e := range loginHistory.History {
					for _, item := range e.Items() {
						if strings.Contains(item, pattern) {
							r.matches[item] = e.UpdatedAt
						}
					}
				}

				if len(r.matches) == 0 {
					continue
				}

				results = append(results, r)

			}
		}
	}

	if len(results) == 0 {
		return simpleInteractionResponse(s, i, "No results found")
	}

	// Sort the results by last updated
	sort.Slice(results, func(i, j int) bool {
		return results[i].updated.Before(results[j].updated)
	})

	for _, r := range results {

		if r.account.User.AvatarUrl != "" && !strings.HasPrefix(r.account.User.AvatarUrl, "https://") {
			r.account.User.AvatarUrl = discordgo.EndpointUserAvatar(r.account.CustomId, r.account.User.AvatarUrl)
		}
		// Discord-ish green
		color := 0x43b581
		footer := ""
		if r.account.DisableTime != nil {
			// Discord-ish red
			color = 0xf04747
			footer = "Account disabled"
		} else if len(r.account.Devices) == 0 {
			// Discord-ish grey
			color = 0x747f8d
			footer = "Account is inactive (no linked devices)"
		}

		embed := &discordgo.MessageEmbed{
			Author: &discordgo.MessageEmbedAuthor{
				IconURL: r.account.User.AvatarUrl,
				Name:    r.account.User.DisplayName,
			},
			Description: fmt.Sprintf("<@%s>", r.account.CustomId),
			Color:       color,
			Fields:      make([]*discordgo.MessageEmbedField, 0, 2),
			Footer: &discordgo.MessageEmbedFooter{
				Text: footer,
			},
		}

		embeds = append(embeds, embed)

		names := &discordgo.MessageEmbedField{
			Name:   "Match",
			Inline: true,
		}

		lastActive := &discordgo.MessageEmbedField{
			Name:   "Updated At",
			Inline: true,
		}

		embed.Fields = append(embed.Fields,
			names,
			lastActive,
		)

		type item struct {
			displayName string
			lastActive  time.Time
		}

		displayNames := make([]item, 0, len(r.matches))
		reserved := make([]string, 0, 1)
		for dn, ts := range r.matches {
			if ts.IsZero() {
				reserved = append(reserved, dn)
			} else {
				displayNames = append(displayNames, item{dn, ts})
			}
		}

		sort.Slice(displayNames, func(i, j int) bool {
			return displayNames[i].lastActive.Before(displayNames[j].lastActive)
		})

		if len(displayNames) > 5 {
			displayNames = displayNames[:5]
		}
		for _, n := range displayNames {
			names.Value += fmt.Sprintf("%s\n", EscapeDiscordMarkdown(n.displayName))
			lastActive.Value += fmt.Sprintf("<t:%d:R>\n", n.lastActive.UTC().Unix())
		}
		for _, n := range reserved {
			names.Value += fmt.Sprintf("%s\n", EscapeDiscordMarkdown(n))
			lastActive.Value += "*reserved*\n"
		}
	}

	if len(embeds) == 0 {
		return simpleInteractionResponse(s, i, "No results found")
	}

	// Send the response
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags:  discordgo.MessageFlagsEphemeral,
			Embeds: embeds,
		},
	})
}

package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// enforcementNoticeInput is everything the guild's enforcement-notice channel
// ("suspension log") card is built from. The Discord /kick-player command and the
// portal kick RPC both fill it in, so the two surfaces cannot drift apart.
type enforcementNoticeInput struct {
	ActorName      string // display name of the moderator
	ActorUsername  string // Discord username of the moderator
	ActorIconURL   string // moderator avatar; empty when unknown
	ActorDiscordID string // moderator Discord ID, mentioned in the kick details

	TargetIGN       string // the target's in-game name in this guild (unescaped)
	TargetDiscordID string // the target's Discord ID

	UserNotice          string
	Notes               string
	RestrictNotes       bool // the guild hides enforcer notes from the notice channel
	AllowPrivateLobbies bool

	GuildID          string // Discord guild ID, carried by the Edit/Void buttons
	GroupID          string
	RecordsByGroupID map[string][]GuildEnforcementRecord
	Voids            map[string]GuildEnforcementRecordVoid
	GroupName        func(groupID string) string // display name for a group; nil falls back to the ID
	Now              time.Time
}

// buildEnforcementNotice renders the enforcement-notice card and, for a fresh
// suspension, its Edit/Void buttons.
func buildEnforcementNotice(in enforcementNoticeInput) (*discordgo.MessageEmbed, []discordgo.MessageComponent) {
	title := "Kicked Player"
	if len(in.Voids) > 0 {
		title = "Voided Suspension(s)"
	} else if len(in.RecordsByGroupID) > 0 {
		title = fmt.Sprintf("Suspension: *%s*", Query.QuoteStringValue(in.TargetIGN))
	}

	descParts := []string{fmt.Sprintf("%s (<@!%s>)", EscapeDiscordMarkdown(in.TargetIGN), in.TargetDiscordID)}
	if in.AllowPrivateLobbies {
		descParts = append(descParts, "✅ **Private lobbies: allowed**")
	} else {
		descParts = append(descParts, "🚫 **Private lobbies: not allowed**")
	}

	embed := &discordgo.MessageEmbed{
		Author: &discordgo.MessageEmbedAuthor{
			Name:    fmt.Sprintf("%s (%s)", in.ActorName, in.ActorUsername),
			IconURL: in.ActorIconURL,
		},
		Title:       title,
		Description: strings.Join(descParts, "\n"),
		Color:       0x9656ce,
		Fields:      []*discordgo.MessageEmbedField{},
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Confidential. Do not share.",
		},
	}

	if len(in.RecordsByGroupID) == 0 {
		// this is just a kick.
		parts := []string{
			fmt.Sprintf("<t:%d:R> by <@!%s>:", in.Now.UTC().Unix(), in.ActorDiscordID),
			fmt.Sprintf("- `%s`", in.UserNotice),
		}
		if in.Notes != "" && !in.RestrictNotes {
			parts = append(parts, fmt.Sprintf("- *%s*", in.Notes))
		}
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Kick Details",
			Value:  strings.Join(parts, "\n"),
			Inline: true,
		})
	}

	for gID, records := range in.RecordsByGroupID {
		if len(records) == 0 {
			continue
		}
		gn := gID
		if in.GroupName != nil {
			if name := in.GroupName(gID); name != "" {
				gn = name
			}
		}
		// Show enforcer ID in the notice channel; hide notes when the toggle restricts visibility.
		embed.Fields = append(embed.Fields, createSuspensionDetailsEmbedField(gn, records, in.Voids, true, !in.RestrictNotes, true, gID, "", false))
	}

	var components []discordgo.MessageComponent
	if len(in.RecordsByGroupID) > 0 && len(in.Voids) == 0 {
		// Only add buttons if there are active suspensions (not voids), for the first record
		// of the first group, to avoid clutter.
		for _, records := range in.RecordsByGroupID {
			for _, record := range records {
				components = append(components, &discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						&discordgo.Button{
							Label:    "Edit",
							Style:    discordgo.PrimaryButton,
							CustomID: fmt.Sprintf("enf:e:%s:%s:%s", record.ID, in.GuildID, in.TargetDiscordID),
						},
						&discordgo.Button{
							Label:    "Void",
							Style:    discordgo.DangerButton,
							CustomID: fmt.Sprintf("enf:v:%s:%s:%s", record.ID, in.GuildID, in.TargetDiscordID),
						},
					},
				})
				break
			}
			break
		}
	}

	return embed, components
}

// enforcementNoticeSender is the one Discord call the notice needs.
type enforcementNoticeSender interface {
	ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend, options ...discordgo.RequestOption) (*discordgo.Message, error)
}

// sendEnforcementNotice posts the card to the guild's enforcement-notice channel.
// A guild with no channel configured is not an error: there is nowhere to post.
// The post goes through the bot, so no webhook URL is ever handled here.
func sendEnforcementNotice(ctx context.Context, s enforcementNoticeSender, channelID string, embed *discordgo.MessageEmbed, components []discordgo.MessageComponent) error {
	if channelID == "" {
		return nil
	}
	if _, err := s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Embed:           embed,
		Components:      components,
		AllowedMentions: &discordgo.MessageAllowedMentions{},
	}, discordgo.WithContext(ctx)); err != nil {
		return fmt.Errorf("sendEnforcementNotice: %w", err)
	}
	return nil
}

// PostsToSuspensionLog reports whether the portal kick should post to the guild's
// suspension log. The field is optional and defaults to true; a caller opts out by
// sending "post_to_suspension_log": false.
func (r EnforcementKickRequest) PostsToSuspensionLog() bool {
	return r.PostToSuspensionLog == nil || *r.PostToSuspensionLog
}

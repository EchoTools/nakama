package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

// inheritedNoticeBudget bounds all inherited notices for one enforcement, from
// the first lookup to the last post. A slow Discord is cut off at this point and
// the notices not yet sent are dropped (logged), never held against the caller.
var inheritedNoticeBudget = 15 * time.Second

// inheritedNoticeSendFunc posts one message to a channel.
type inheritedNoticeSendFunc func(channelID string, msg *discordgo.MessageSend) error

// inheritedNoticeTarget is a guild that inherits an enforcement and the channel
// its notice goes to.
type inheritedNoticeTarget struct {
	Guild     *GuildGroup
	ChannelID string
}

// inheritedNoticeChannel is where a guild receives enforcement notices: its
// enforcement notice channel, or its audit channel when it has none.
func inheritedNoticeChannel(gg *GuildGroup) string {
	if gg.EnforcementNoticeChannelID != "" {
		return gg.EnforcementNoticeChannelID
	}
	return gg.AuditChannelID
}

// inheritedNoticeTargets returns the guilds that should be told about a new
// enforcement record issued in srcGroupID: those that inherit from it AND whose
// filter on that link admits this record, which is the set CheckEnforcementSuspensions
// applies the record to. A record the guild's filter excludes yields no target.
//
// A guild with no channel to post in is skipped, as is any channel in
// postedChannelIDs: the channels the source actually posted this enforcement to,
// on whatever path issued it (its notice, its audit line). Empty when it posted
// nothing, in which case nothing is skipped on its account; two guilds configured
// with the same channel would otherwise see it twice. A channel the source did not
// post to is never skipped, however it is configured. A channel listed by more
// than one inheriting guild is returned once.
func inheritedNoticeTargets(registry *GuildGroupRegistry, srcGroupID string, record GuildEnforcementRecord, postedChannelIDs []string) []inheritedNoticeTarget {
	if registry == nil {
		return nil
	}
	filters := registry.InheritanceFilters()
	skip := map[string]struct{}{}
	for _, ch := range postedChannelIDs {
		if ch != "" {
			skip[ch] = struct{}{}
		}
	}

	var targets []inheritedNoticeTarget
	for _, childID := range registry.InheritanceByParentGroupID()[srcGroupID] {
		if childID == srcGroupID {
			continue
		}
		if !filters.For(srcGroupID, childID).Accepts(record) {
			continue
		}
		child := registry.Get(childID)
		if child == nil {
			continue
		}
		ch := inheritedNoticeChannel(child)
		if ch == "" {
			continue
		}
		if _, dup := skip[ch]; dup {
			continue
		}
		skip[ch] = struct{}{}
		targets = append(targets, inheritedNoticeTarget{Guild: child, ChannelID: ch})
	}
	return targets
}

// inheritedNoticeSubject is the player an inherited notice is about.
type inheritedNoticeSubject struct {
	DiscordID string
	// NameIn returns the player's in-guild name (IGN) in a group, "" if unknown.
	NameIn func(groupID string) string
}

// line renders the subject for the inheriting guild: the player's name in that
// guild and the mention. Names are per guild, so another guild's name is never
// shown here; with no name in the inheriting guild the line is the mention alone,
// and with neither it is empty. It never renders a placeholder.
func (s inheritedNoticeSubject) line(inheriting *GuildGroup) string {
	var name string
	if s.NameIn != nil {
		name = s.NameIn(inheriting.IDStr())
	}
	var parts []string
	if name != "" {
		parts = append(parts, EscapeDiscordMarkdown(name))
	}
	if s.DiscordID != "" {
		parts = append(parts, fmt.Sprintf("(<@!%s>)", s.DiscordID))
	}
	return strings.Join(parts, " ")
}

// inheritedEnforcementNoticeEmbed builds the notice an inheriting guild receives:
// the one the source guild gets, marked with the source guild. It carries no
// Edit/Void buttons (the record belongs to the source guild, and those buttons
// act on the clicking guild's own records), and the source's moderator notes and
// enforcer attribution are left out, as createSuspensionDetailsEmbedField already
// does for a record that is not the viewing guild's own.
func inheritedEnforcementNoticeEmbed(src, inheriting *GuildGroup, record GuildEnforcementRecord, subject inheritedNoticeSubject) *discordgo.MessageEmbed {
	var descParts []string
	if line := subject.line(inheriting); line != "" {
		descParts = append(descParts, line)
	}
	if record.AllowPrivateLobbies {
		descParts = append(descParts, "✅ **Private lobbies: allowed**")
	} else {
		descParts = append(descParts, "🚫 **Private lobbies: not allowed**")
	}
	descParts = append(descParts, fmt.Sprintf("**Source guild:** %s", EscapeDiscordMarkdown(src.Name())))

	embed := &discordgo.MessageEmbed{
		Title:       "Inherited Suspension",
		Description: strings.Join(descParts, "\n"),
		Color:       0x9656ce,
		Fields:      []*discordgo.MessageEmbedField{},
		Footer: &discordgo.MessageEmbedFooter{
			Text: fmt.Sprintf("Inherited from %s. Confidential. Do not share.", src.Name()),
		},
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	if field := createSuspensionDetailsEmbedField(src.Name(), []GuildEnforcementRecord{record}, nil, true, false, true, inheriting.IDStr(), "", false); field != nil {
		embed.Fields = append(embed.Fields, field)
	}
	return embed
}

// sendInheritedEnforcementNotices posts the inherited notice to each guild that
// inherits record from srcGroupID, in order, until ctx ends. A failed post is
// logged and does not stop the others, and none of it can fail the enforcement.
// Each post runs in its own goroutine so a send that never returns is abandoned
// when ctx ends instead of holding the rest.
func sendInheritedEnforcementNotices(ctx context.Context, logger *zap.Logger, send inheritedNoticeSendFunc, registry *GuildGroupRegistry, src *GuildGroup, record GuildEnforcementRecord, subject inheritedNoticeSubject, postedChannelIDs []string) {
	if src == nil || send == nil {
		return
	}
	for _, t := range inheritedNoticeTargets(registry, src.IDStr(), record, postedChannelIDs) {
		msg := &discordgo.MessageSend{
			Embed:           inheritedEnforcementNoticeEmbed(src, t.Guild, record, subject),
			AllowedMentions: &discordgo.MessageAllowedMentions{},
		}
		done := make(chan error, 1)
		go func(channelID string) { done <- send(channelID, msg) }(t.ChannelID)

		var err error
		select {
		case err = <-done:
		case <-ctx.Done():
			err = ctx.Err()
		}
		if err != nil {
			logger.Error("Failed to send inherited enforcement notice",
				zap.Error(err),
				zap.String("source_group", src.IDStr()),
				zap.String("group_id", t.Guild.IDStr()),
				zap.String("channel_id", t.ChannelID))
			if ctx.Err() != nil {
				return
			}
		}
	}
}

// notifyInheritingGuildsAsync tells every guild that inherits a newly issued
// enforcement record from src, subject to each guild's inheritance filters. It
// returns at once: the lookups and the posts run in a goroutine bounded by
// inheritedNoticeBudget and detached from ctx (which ends when the RPC or
// interaction does), tracked by wg when given. Call it after the record is saved
// and after the source guild's own posts. postedChannelIDs are the channels those
// posts actually went to, none if they posted nothing. targetDiscordID may be empty, in which case
// it is resolved from the target's account.
func notifyInheritingGuildsAsync(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, registry *GuildGroupRegistry, send inheritedNoticeSendFunc, wg *sync.WaitGroup, src *GuildGroup, record GuildEnforcementRecord, targetUserID, targetDiscordID string, postedChannelIDs []string) {
	if registry == nil || send == nil || src == nil || len(inheritedNoticeTargets(registry, src.IDStr(), record, postedChannelIDs)) == 0 {
		return
	}
	if wg != nil {
		wg.Add(1)
	}
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), inheritedNoticeBudget)
	go func() {
		defer cancel()
		if wg != nil {
			defer wg.Done()
		}
		subject := inheritedNoticeSubject{DiscordID: targetDiscordID}
		if subject.DiscordID == "" {
			if account, err := nk.AccountGetId(bg, targetUserID); err != nil {
				logger.Warn("Failed to load target account for inherited enforcement notice", zap.Error(err))
			} else if account != nil {
				subject.DiscordID = account.CustomId
			}
		}
		if profile, err := EVRProfileLoad(bg, nk, targetUserID); err != nil {
			logger.Warn("Failed to load target profile for inherited enforcement notice", zap.Error(err))
		} else {
			// The player's name in exactly that guild. GetGroupIGN would fall back to
			// the active guild's name or the username for any guild, which would
			// present another guild's name as the name in this one.
			subject.NameIn = func(groupID string) string {
				return sanitizeDisplayName(profile.InGameNames[groupID].DisplayName)
			}
		}
		sendInheritedEnforcementNotices(bg, logger, send, registry, src, record, subject, postedChannelIDs)
	}()
}

// notifyInheritingGuilds is notifyInheritingGuildsAsync for the app bot.
func (d *DiscordAppBot) notifyInheritingGuilds(ctx context.Context, logger *zap.Logger, src *GuildGroup, record GuildEnforcementRecord, targetUserID, targetDiscordID string, postedChannelIDs []string) {
	if d == nil {
		return
	}
	notifyInheritingGuildsAsync(ctx, logger, d.nk, d.guildGroupRegistry, func(channelID string, msg *discordgo.MessageSend) error {
		return postChannelMessage(d.dg, d.sendChannelMessageFn, channelID, msg)
	}, &d.noticeWG, src, record, targetUserID, targetDiscordID, postedChannelIDs)
}

// postChannelMessage posts to a Discord channel through dg; override, when set,
// replaces the post (tests).
func postChannelMessage(dg *discordgo.Session, override func(channelID string, msg *discordgo.MessageSend) error, channelID string, msg *discordgo.MessageSend) error {
	if override != nil {
		return override(channelID, msg)
	}
	if dg == nil {
		return fmt.Errorf("discord session is nil")
	}
	_, err := dg.ChannelMessageSendComplex(channelID, msg)
	return err
}

// channelPoster adapts postChannelMessage to enforcementNoticeSender, so the
// kick path posts through the same test seam as the inherited notices.
type channelPoster struct {
	dg       *discordgo.Session
	override func(channelID string, msg *discordgo.MessageSend) error
}

func (c channelPoster) ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	return nil, postChannelMessage(c.dg, c.override, channelID, data)
}

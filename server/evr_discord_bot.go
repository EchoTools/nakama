package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
	"gopkg.in/yaml.v3"
)

func (d *DiscordAppBot) LogYAMLtoChannel(data any, channelID string) error {
	if d.dg == nil {
		return fmt.Errorf("discord session is not initialized")
	}
	if channelID == "" {
		return fmt.Errorf("channelID is empty")
	}
	yamlData, err := yaml.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal data: %w", err)
	}

	lines := strings.Split(string(yamlData), "\n")
	chunks := make([]string, 0)

	var str strings.Builder

	for _, line := range lines {
		if str.Len()+len(line) > 1900 {
			chunks = append(chunks, str.String())
			str.Reset()
		}
		str.WriteString(line + "\n")
	}
	if str.Len() > 0 {
		chunks = append(chunks, fmt.Sprintf("```yaml\n%s\n```", str.String()))
	}
	for _, chunk := range chunks {
		_, err := d.dg.ChannelMessageSend(channelID, chunk)
		if err != nil {
			return fmt.Errorf("failed to send message: %w", err)
		}
	}
	return nil
}

func (d *DiscordAppBot) interactionToSignature(prefix string, options []*discordgo.ApplicationCommandInteractionDataOption) string {
	args := make([]string, 0, len(options))
	sep := ": "

	for _, opt := range options {
		strval := ""
		switch opt.Type {
		case discordgo.ApplicationCommandOptionSubCommand:
			strval = d.interactionToSignature(opt.Name, opt.Options)
		case discordgo.ApplicationCommandOptionSubCommandGroup:
			strval = d.interactionToSignature(opt.Name, opt.Options)
		case discordgo.ApplicationCommandOptionString:
			strval = opt.StringValue()
		case discordgo.ApplicationCommandOptionInteger:
			strval = fmt.Sprintf("%d", opt.IntValue())
		case discordgo.ApplicationCommandOptionBoolean:
			strval = fmt.Sprintf("%t", opt.BoolValue())
		case discordgo.ApplicationCommandOptionUser:
			strval = fmt.Sprintf("<@%s>", opt.Value)
		case discordgo.ApplicationCommandOptionChannel:
			strval = fmt.Sprintf("<#%s>", opt.Value)
		case discordgo.ApplicationCommandOptionRole:
			strval = fmt.Sprintf("<@&%s>", opt.Value)
		case discordgo.ApplicationCommandOptionMentionable:
			strval = fmt.Sprintf("<@%s>", opt.Value)
		default:
			strval = fmt.Sprintf("unknown type %d", opt.Type)
		}
		if strval != "" {
			args = append(args, opt.Name+sep+strval)
		}
	}
	return fmt.Sprintf("`/%s` { %s }", prefix, strings.Join(args, ", "))
}

func (d *DiscordAppBot) LogInteractionToChannel(i *discordgo.InteractionCreate, channelID string) error {
	data := i.ApplicationCommandData()
	signature := d.interactionToSignature(data.Name, data.Options)

	content := fmt.Sprintf("<@%s> used %s", i.Member.User.ID, signature)
	d.dg.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content:         content,
		AllowedMentions: &discordgo.MessageAllowedMentions{},
	})
	return nil
}

func (d *DiscordAppBot) LogMessageToChannel(message string, channelID string) error {
	if d.dg == nil {
		return fmt.Errorf("discord session is not initialized")
	}
	if channelID == "" {
		return fmt.Errorf("channelID is empty")
	}
	_, err := d.dg.ChannelMessageSend(channelID, message)
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}
	return nil
}

func (d *DiscordAppBot) LogAuditMessage(ctx context.Context, groupID string, message string, replaceMentions bool) (*discordgo.Message, error) {
	// replace all <@uuid> mentions with <@discordID>
	if replaceMentions {
		message = d.cache.ReplaceMentions(message)
	}

	groupMetadata, err := GetGuildGroupMetadata(ctx, d.db, groupID)
	if err != nil {
		return nil, err
	}

	if groupMetadata.AuditChannelID != "" {
		return d.dg.ChannelMessageSend(groupMetadata.AuditChannelID, message)
	}
	return nil, nil
}

func (d *DiscordAppBot) LogUserErrorMessage(ctx context.Context, groupID string, message string, replaceMentions bool) (*discordgo.Message, error) {
	// replace all <@uuid> mentions with <@discordID>
	if replaceMentions {
		message = d.cache.ReplaceMentions(message)
	}

	groupMetadata, err := GetGuildGroupMetadata(ctx, d.db, groupID)
	if err != nil {
		return nil, err
	}

	if groupMetadata.ErrorChannelID != "" {
		return d.dg.ChannelMessageSend(groupMetadata.ErrorChannelID, message)
	}
	return nil, nil
}

func (d *DiscordAppBot) SendErrorToUser(userID string, userErr error) error {

	if userErr == nil {
		return nil
	}
	var err error
	if d.dg == nil {
		return fmt.Errorf("discord session is not initialized")
	}

	discordID := d.cache.UserIDToDiscordID(userID)
	if discordID == "" {
		return fmt.Errorf("discordID not found for user %s", userID)
	}

	_, err = d.dg.UserChannelCreate(discordID)
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}

	_, err = d.dg.ChannelMessageSend(discordID, userErr.Error())
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}

	return nil
}

func (d *DiscordAppBot) SendIPAuthorizationNotification(userID string, ip string) error {
	if d == nil {
		return fmt.Errorf("discord bot is not initialized")
	}

	if d.dg == nil {
		return fmt.Errorf("discord session is not initialized")
	}

	discordID := d.cache.UserIDToDiscordID(userID)
	if discordID == "" {
		return fmt.Errorf("discordID not found for user %s", userID)
	}

	channel, err := d.dg.UserChannelCreate(discordID)
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}

	if _, err = d.dg.ChannelMessageSend(channel.ID, fmt.Sprintf("IP `%s` has been automatically authorized, because you used discordID/password authentication.", ip)); err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}

	return nil
}

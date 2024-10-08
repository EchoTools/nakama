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

func (d *DiscordAppBot) LogAuditMessage(ctx context.Context, groupID string, message string, replaceMentions bool) error {
	// replace all <@uuid> mentions with <@discordID>
	if replaceMentions {
		message = d.cache.ReplaceMentions(message)
	}

	groupMetadata, err := GetGuildGroupMetadata(ctx, d.db, groupID)
	if err != nil {
		return err
	}

	if groupMetadata.AuditChannelID != "" {
		if _, err := d.dg.ChannelMessageSend(groupMetadata.AuditChannelID, message); err != nil {
			return err
		}
	}
	return nil
}

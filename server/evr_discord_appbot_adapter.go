package server

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
)

// DiscordAdapter adapts DiscordGo objects to our abstractions
type DiscordAdapter struct {
	session       *discordgo.Session
	cache         *DiscordIntegrator
	guildRegistry *GuildGroupRegistry
	nk            runtime.NakamaModule
	db            *sql.DB
}

// NewDiscordAdapter creates a new Discord adapter
func NewDiscordAdapter(session *discordgo.Session, cache *DiscordIntegrator, guildRegistry *GuildGroupRegistry, nk runtime.NakamaModule, db *sql.DB) *DiscordAdapter {
	return &DiscordAdapter{
		session:       session,
		cache:         cache,
		guildRegistry: guildRegistry,
		nk:            nk,
		db:            db,
	}
}

// ConvertToCommandData converts DiscordGo interaction to our CommandData DTO
func (a *DiscordAdapter) ConvertToCommandData(i *discordgo.InteractionCreate) (*CommandData, error) {
	user, member := getScopedUserMember(i)
	
	if user == nil {
		return nil, fmt.Errorf("user is nil")
	}
	
	data := &CommandData{
		CommandName: i.ApplicationCommandData().Name,
		UserID:      user.ID,
		Username:    user.Username,
		Options:     convertOptions(i.ApplicationCommandData().Options),
	}
	
	// Convert member data if available
	if member != nil {
		data.Member = &MemberData{
			UserID:      member.User.ID,
			Nick:        member.Nick,
			Roles:       member.Roles,
			JoinedAt:    member.JoinedAt.Format("2006-01-02T15:04:05Z07:00"),
			Permissions: int64(member.Permissions),
		}
	}
	
	// Convert guild data if available
	if i.GuildID != "" {
		data.Guild = &GuildData{
			ID: i.GuildID,
			// Name can be filled in if needed
		}
	}
	
	return data, nil
}

// ConvertToDiscordResponse converts our CommandResponse back to DiscordGo format
func (a *DiscordAdapter) ConvertToDiscordResponse(response *CommandResponse) *discordgo.InteractionResponse {
	responseData := &discordgo.InteractionResponseData{
		Content: response.Content,
	}
	
	// Set ephemeral flag
	if response.Ephemeral {
		responseData.Flags = discordgo.MessageFlagsEphemeral
	}
	
	// Convert embeds
	if len(response.Embeds) > 0 {
		responseData.Embeds = make([]*discordgo.MessageEmbed, len(response.Embeds))
		for i, embed := range response.Embeds {
			responseData.Embeds[i] = convertEmbed(embed)
		}
	}
	
	// Convert components (basic support for now)
	if len(response.Components) > 0 {
		responseData.Components = convertComponents(response.Components)
	}
	
	return &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: responseData,
	}
}

// CreateCommandContext creates a CommandContext from the adapter's dependencies
func (a *DiscordAdapter) CreateCommandContext(ctx context.Context, logger runtime.Logger, data *CommandData) CommandContext {
	return CommandContext{
		Ctx:         ctx,
		Logger:      logger,
		Nakama:      a.nk,
		Database:    &DatabaseAdapter{db: a.db},
		Cache:       &CacheAdapter{cache: a.cache},
		GuildGroups: &GuildGroupAdapter{registry: a.guildRegistry},
		Discord:     &SessionAdapter{session: a.session},
		Data:        *data,
	}
}

// convertOptions converts DiscordGo options to our CommandDataOption format
func convertOptions(options []*discordgo.ApplicationCommandInteractionDataOption) []CommandDataOption {
	if options == nil {
		return nil
	}
	
	result := make([]CommandDataOption, len(options))
	for i, opt := range options {
		result[i] = CommandDataOption{
			Name:  opt.Name,
			Type:  int(opt.Type),
			Value: opt.Value,
		}
	}
	return result
}

// convertEmbed converts our EmbedData to DiscordGo MessageEmbed
func convertEmbed(embed *EmbedData) *discordgo.MessageEmbed {
	result := &discordgo.MessageEmbed{
		Title:       embed.Title,
		Description: embed.Description,
		Color:       embed.Color,
	}
	
	if len(embed.Fields) > 0 {
		result.Fields = make([]*discordgo.MessageEmbedField, len(embed.Fields))
		for i, field := range embed.Fields {
			result.Fields[i] = &discordgo.MessageEmbedField{
				Name:   field.Name,
				Value:  field.Value,
				Inline: field.Inline,
			}
		}
	}
	
	return result
}

// convertComponents converts our ComponentData to DiscordGo components
func convertComponents(components []ComponentData) []discordgo.MessageComponent {
	// Basic implementation - can be expanded as needed
	return []discordgo.MessageComponent{}
}

// CacheAdapter implements DiscordCache interface
type CacheAdapter struct {
	cache *DiscordIntegrator
}

func (c *CacheAdapter) DiscordIDToUserID(discordID string) string {
	return c.cache.DiscordIDToUserID(discordID)
}

func (c *CacheAdapter) GuildIDToGroupID(guildID string) string {
	return c.cache.GuildIDToGroupID(guildID)
}

// DatabaseAdapter implements DatabaseService interface
type DatabaseAdapter struct {
	db *sql.DB
}

func (d *DatabaseAdapter) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return d.db.QueryContext(ctx, query, args...)
}

func (d *DatabaseAdapter) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return d.db.ExecContext(ctx, query, args...)
}

// GuildGroupAdapter implements GuildGroupService interface
type GuildGroupAdapter struct {
	registry *GuildGroupRegistry
}

func (g *GuildGroupAdapter) GetGuildGroup(guildID string) (*GuildGroup, bool) {
	gg := g.registry.Get(guildID)
	return gg, gg != nil
}

// SessionAdapter implements DiscordSessionService interface
type SessionAdapter struct {
	session *discordgo.Session
}

func (s *SessionAdapter) InteractionRespond(interaction *discordgo.Interaction, resp *discordgo.InteractionResponse) error {
	return s.session.InteractionRespond(interaction, resp)
}
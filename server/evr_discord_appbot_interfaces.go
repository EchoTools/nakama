package server

import (
	"context"
	"database/sql"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
)

// DiscordCache abstracts Discord ID/UserID mapping functionality
type DiscordCache interface {
	DiscordIDToUserID(discordID string) string
	GuildIDToGroupID(guildID string) string
}

// NakamaService abstracts Nakama operations
type NakamaService interface {
	runtime.NakamaModule
}

// DatabaseService abstracts database operations
type DatabaseService interface {
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}

// GuildGroupService abstracts guild group registry functionality
type GuildGroupService interface {
	GetGuildGroup(guildID string) (*GuildGroup, bool)
}

// DiscordSessionService abstracts Discord session operations
type DiscordSessionService interface {
	InteractionRespond(interaction *discordgo.Interaction, resp *discordgo.InteractionResponse) error
}

// CommandData represents the essential data from a Discord interaction
type CommandData struct {
	CommandName string
	UserID      string // Discord user ID
	Username    string // Discord username
	Member      *MemberData
	Guild       *GuildData
	Options     []CommandDataOption
}

// MemberData represents Discord member information
type MemberData struct {
	UserID      string
	Nick        string
	Roles       []string
	JoinedAt    string
	Permissions int64
}

// GuildData represents Discord guild information
type GuildData struct {
	ID   string
	Name string
}

// CommandDataOption represents a command option
type CommandDataOption struct {
	Name  string
	Type  int
	Value interface{}
}

// CommandResponse represents the response to send back to Discord
type CommandResponse struct {
	Content    string
	Embeds     []*EmbedData
	Components []ComponentData
	Ephemeral  bool
}

// EmbedData represents a Discord embed
type EmbedData struct {
	Title       string
	Description string
	Color       int
	Fields      []EmbedField
}

// EmbedField represents an embed field
type EmbedField struct {
	Name   string
	Value  string
	Inline bool
}

// ComponentData represents Discord components (buttons, etc.)
type ComponentData struct {
	Type     int
	CustomID string
	Label    string
	Style    int
}

// CommandContext contains all the services and data needed for command processing
type CommandContext struct {
	Ctx         context.Context
	Logger      runtime.Logger
	Nakama      NakamaService
	Database    DatabaseService
	Cache       DiscordCache
	GuildGroups GuildGroupService
	Discord     DiscordSessionService
	Data        CommandData
}

// CommandProcessor defines the interface for processing commands
type CommandProcessor interface {
	ProcessCommand(ctx CommandContext) (*CommandResponse, error)
	CanHandleCommand(commandName string) bool
}

// CommandHandler is the function signature for individual command handlers
type CommandHandler func(ctx CommandContext) (*CommandResponse, error)

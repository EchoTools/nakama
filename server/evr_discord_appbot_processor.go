package server

import (
	"fmt"
	"strconv"
	"strings"
)

// ModernCommandProcessor implements CommandProcessor with dependency injection
type ModernCommandProcessor struct {
	handlers map[string]CommandHandler
}

// NewModernCommandProcessor creates a new command processor
func NewModernCommandProcessor() *ModernCommandProcessor {
	processor := &ModernCommandProcessor{
		handlers: make(map[string]CommandHandler),
	}

	// Register command handlers
	processor.registerHandlers()
	return processor
}

// ProcessCommand processes a command using the modern architecture
func (p *ModernCommandProcessor) ProcessCommand(ctx CommandContext) (*CommandResponse, error) {
	// Validate input
	if err := p.validateCommandContext(ctx); err != nil {
		return nil, fmt.Errorf("invalid command context: %w", err)
	}

	handler, exists := p.handlers[ctx.Data.CommandName]
	if !exists {
		return nil, fmt.Errorf("command handler not found for: %s", ctx.Data.CommandName)
	}

	return handler(ctx)
}

// CanHandleCommand returns true if the processor can handle the given command
func (p *ModernCommandProcessor) CanHandleCommand(commandName string) bool {
	_, exists := p.handlers[commandName]
	return exists
}

// validateCommandContext validates the command context for nil safety
func (p *ModernCommandProcessor) validateCommandContext(ctx CommandContext) error {
	if ctx.Ctx == nil {
		return fmt.Errorf("context is nil")
	}
	if ctx.Logger == nil {
		return fmt.Errorf("logger is nil")
	}
	if ctx.Nakama == nil {
		return fmt.Errorf("nakama service is nil")
	}
	if ctx.Database == nil {
		return fmt.Errorf("database service is nil")
	}
	if ctx.Cache == nil {
		return fmt.Errorf("cache service is nil")
	}
	if ctx.Data.CommandName == "" {
		return fmt.Errorf("command name is empty")
	}
	if ctx.Data.UserID == "" {
		return fmt.Errorf("user ID is empty")
	}

	return nil
}

// registerHandlers registers all the command handlers
func (p *ModernCommandProcessor) registerHandlers() {
	p.handlers["throw-settings"] = p.handleThrowSettings
	p.handlers["whoami"] = p.handleWhoami
	// Add more commands as they are refactored
}

// handleThrowSettings handles the throw-settings command with proper nil safety
func (p *ModernCommandProcessor) handleThrowSettings(ctx CommandContext) (*CommandResponse, error) {
	// Get the Nakama user ID from cache
	nakamaUserID := ctx.Cache.DiscordIDToUserID(ctx.Data.UserID)
	if nakamaUserID == "" {
		return &CommandResponse{
			Content:   "You need to link your Discord account with EchoVRCE first.",
			Ephemeral: true,
		}, nil
	}

	// Load user profile
	metadata, err := EVRProfileLoad(ctx.Ctx, ctx.Nakama, nakamaUserID)
	if err != nil {
		ctx.Logger.Error("Failed to load user profile", "error", err)
		return &CommandResponse{
			Content:   "Failed to load your game settings. Please try again later.",
			Ephemeral: true,
		}, nil
	}

	// Check for nil GamePauseSettings with detailed error
	if metadata == nil {
		ctx.Logger.Error("User metadata is nil")
		return &CommandResponse{
			Content:   "Your game settings are not available. Please check your account setup.",
			Ephemeral: true,
		}, nil
	}

	if metadata.GamePauseSettings == nil {
		ctx.Logger.Warn("GamePauseSettings is nil for user", "userID", nakamaUserID)
		return &CommandResponse{
			Content:   "Your throw settings are not configured. Please set them up in the game first.",
			Ephemeral: true,
		}, nil
	}

	// Build the response embed with safe field access
	embed := &EmbedData{
		Title: "Game Settings",
		Color: 5814783,
		Fields: []EmbedField{
			{
				Name:   "Grab Deadzone",
				Value:  strconv.FormatFloat(metadata.GamePauseSettings.GrabDeadZone, 'f', -1, 64),
				Inline: true,
			},
			{
				Name:   "Release Distance",
				Value:  strconv.FormatFloat(metadata.GamePauseSettings.ReleaseDistance, 'f', -1, 64),
				Inline: true,
			},
			{
				Name:   "Wrist Angle Offset",
				Value:  strconv.FormatFloat(metadata.GamePauseSettings.WristAngleOffset, 'f', -1, 64),
				Inline: true,
			},
		},
	}

	return &CommandResponse{
		Embeds:    []*EmbedData{embed},
		Ephemeral: true,
	}, nil
}

// handleWhoami handles the whoami command
func (p *ModernCommandProcessor) handleWhoami(ctx CommandContext) (*CommandResponse, error) {
	// Get the Nakama user ID from cache
	nakamaUserID := ctx.Cache.DiscordIDToUserID(ctx.Data.UserID)
	if nakamaUserID == "" {
		return &CommandResponse{
			Content:   "You are not linked to any EchoVRCE account.",
			Ephemeral: true,
		}, nil
	}

	// Get user account info
	account, err := ctx.Nakama.AccountGetId(ctx.Ctx, nakamaUserID)
	if err != nil {
		ctx.Logger.Error("Failed to get account", "error", err)
		return &CommandResponse{
			Content:   "Failed to retrieve account information.",
			Ephemeral: true,
		}, nil
	}

	if account == nil {
		return &CommandResponse{
			Content:   "Account not found.",
			Ephemeral: true,
		}, nil
	}

	// Build response with account information
	username := account.GetUser().GetUsername()
	displayName := account.GetUser().GetDisplayName()

	content := fmt.Sprintf("**Discord User:** %s\n**Nakama User ID:** %s\n**Username:** %s",
		ctx.Data.Username, nakamaUserID, username)

	if displayName != "" && displayName != username {
		content += fmt.Sprintf("\n**Display Name:** %s", displayName)
	}

	return &CommandResponse{
		Content:   content,
		Ephemeral: true,
	}, nil
}

// extractStringOption safely extracts a string option from command options
func extractStringOption(options []CommandDataOption, name string) string {
	for _, opt := range options {
		if strings.EqualFold(opt.Name, name) {
			if str, ok := opt.Value.(string); ok {
				return str
			}
		}
	}
	return ""
}

// extractIntOption safely extracts an int option from command options
func extractIntOption(options []CommandDataOption, name string) int64 {
	for _, opt := range options {
		if strings.EqualFold(opt.Name, name) {
			switch v := opt.Value.(type) {
			case int64:
				return v
			case int:
				return int64(v)
			case float64:
				return int64(v)
			}
		}
	}
	return 0
}

// extractBoolOption safely extracts a bool option from command options
func extractBoolOption(options []CommandDataOption, name string) bool {
	for _, opt := range options {
		if strings.EqualFold(opt.Name, name) {
			if b, ok := opt.Value.(bool); ok {
				return b
			}
		}
	}
	return false
}

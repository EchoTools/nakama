package server

import (
	"context"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
)

// ModernDiscordHandler provides a gradual migration path for command handling
type ModernDiscordHandler struct {
	processor *ModernCommandProcessor
	adapter   *DiscordAdapter
}

// NewModernDiscordHandler creates a new modern handler for gradual migration
func NewModernDiscordHandler(adapter *DiscordAdapter) *ModernDiscordHandler {
	return &ModernDiscordHandler{
		processor: NewModernCommandProcessor(),
		adapter:   adapter,
	}
}

// HandleInteractionApplicationCommandModern handles commands using the modern architecture
func (h *ModernDiscordHandler) HandleInteractionApplicationCommandModern(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, commandName string) error {
	// Convert Discord interaction to our DTO
	commandData, err := h.adapter.ConvertToCommandData(i)
	if err != nil {
		logger.Error("Failed to convert command data", "error", err)
		return simpleInteractionResponse(s, i, "Failed to process command.")
	}
	
	// Create command context
	commandCtx := h.adapter.CreateCommandContext(ctx, logger, commandData)
	
	// Process the command
	response, err := h.processor.ProcessCommand(commandCtx)
	if err != nil {
		logger.Error("Failed to process command", "error", err)
		return simpleInteractionResponse(s, i, "Command processing failed.")
	}
	
	// Convert response back to Discord format
	discordResponse := h.adapter.ConvertToDiscordResponse(response)
	
	// Send the response
	return s.InteractionRespond(i.Interaction, discordResponse)
}

// CanHandleCommand returns true if this handler can process the given command
func (h *ModernDiscordHandler) CanHandleCommand(commandName string) bool {
	return h.processor.CanHandleCommand(commandName)
}

// GetSupportedCommands returns a list of commands supported by the modern handler
func (h *ModernDiscordHandler) GetSupportedCommands() []string {
	return []string{"throw-settings", "whoami"}
}

// Integration helper for the existing DiscordAppBot
func (d *DiscordAppBot) CreateModernHandler() *ModernDiscordHandler {
	adapter := NewDiscordAdapter(d.dg, d.cache, d.guildGroupRegistry, d.nk, d.db)
	return NewModernDiscordHandler(adapter)
}
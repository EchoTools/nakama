package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

// ReservationSlashCommandHandler handles all /reserve slash command interactions
type ReservationSlashCommandHandler struct {
	nk             runtime.NakamaModule
	logger         runtime.Logger
	reservationMgr *ReservationManager
	preemptionMgr  *MatchPreemptionManager
}

// NewReservationSlashCommandHandler creates a new reservation command handler
func NewReservationSlashCommandHandler(nk runtime.NakamaModule, logger runtime.Logger, reservationMgr *ReservationManager, preemptionMgr *MatchPreemptionManager) *ReservationSlashCommandHandler {
	return &ReservationSlashCommandHandler{
		nk:             nk,
		logger:         logger,
		reservationMgr: reservationMgr,
		preemptionMgr:  preemptionMgr,
	}
}

// HandleReserveCommand handles the /reserve slash command
func (h *ReservationSlashCommandHandler) HandleReserveCommand(ctx context.Context, dg *discordgo.Session, i *discordgo.InteractionCreate) error {
	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		return h.respondError(dg, i, "No subcommand provided")
	}

	subcommand := options[0]

	// Get the user's information
	userID, guildID, err := h.getUserInfo(ctx, i)
	if err != nil {
		return h.respondError(dg, i, fmt.Sprintf("Failed to get user info: %v", err))
	}

	switch subcommand.Name {
	case "add":
		return h.handleAddReservation(ctx, dg, i, userID, guildID, subcommand.Options)
	case "check":
		return h.handleCheckReservation(ctx, dg, i, userID, guildID, subcommand.Options)
	case "remove":
		return h.handleRemoveReservation(ctx, dg, i, userID, guildID, subcommand.Options)
	case "list":
		return h.handleListReservations(ctx, dg, i, userID, guildID, subcommand.Options)
	case "status":
		return h.handleReservationStatus(ctx, dg, i, userID, guildID, subcommand.Options)
	case "dashboard":
		return h.handleDashboard(ctx, dg, i, userID, guildID, subcommand.Options)
	default:
		return h.respondError(dg, i, "Unknown subcommand")
	}
}

// handleAddReservation handles the /reserve add command
func (h *ReservationSlashCommandHandler) handleAddReservation(ctx context.Context, dg *discordgo.Session, i *discordgo.InteractionCreate, userID, guildID string, options []*discordgo.ApplicationCommandInteractionDataOption) error {
	// Parse options
	params := make(map[string]*discordgo.ApplicationCommandInteractionDataOption)
	for _, opt := range options {
		params[opt.Name] = opt
	}

	// Get required parameters
	startTimeStr := params["start_time"].StringValue()
	durationMinutes := int(params["duration"].IntValue())
	classificationStr := params["classification"].StringValue()

	// Optional parameters
	var ownerID string
	if owner, exists := params["owner"]; exists {
		ownerID = owner.StringValue()
	} else {
		ownerID = userID
	}

	var force bool
	if forceOpt, exists := params["force"]; exists {
		force = forceOpt.BoolValue()
	}

	// Parse start time (expecting format like "2023-12-25T15:30")
	startTime, err := time.Parse("2006-01-02T15:04", startTimeStr)
	if err != nil {
		return h.respondError(dg, i, "Invalid start time format. Use YYYY-MM-DDTHH:MM (e.g., 2023-12-25T15:30)")
	}

	endTime := startTime.Add(time.Duration(durationMinutes) * time.Minute)
	classification := ParseSessionClassification(classificationStr)

	// Get guild group ID
	groupID, err := GetGroupIDByGuildIDNK(ctx, h.nk, guildID)
	if err != nil {
		return h.respondError(dg, i, "Failed to get guild group information")
	}

	// Create reservation request
	req := &CreateReservationRequest{
		GroupID:        uuid.FromStringOrNil(groupID),
		Owner:          ownerID,
		Requester:      userID,
		StartTime:      startTime,
		EndTime:        endTime,
		Classification: classification,
		Force:          force,
	}

	// Create the reservation
	reservation, err := h.reservationMgr.CreateReservation(ctx, req)
	if err != nil {
		if conflictErr, ok := err.(*ReservationConflictError); ok {
			return h.handleReservationConflicts(dg, i, conflictErr.Conflicts)
		}
		return h.respondError(dg, i, fmt.Sprintf("Failed to create reservation: %v", err))
	}

	// Success response
	embed := &discordgo.MessageEmbed{
		Title: "✅ Reservation Created",
		Color: 0x00ff00,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Reservation ID", Value: reservation.ID, Inline: true},
			{Name: "Start Time", Value: fmt.Sprintf("<t:%d:F>", reservation.StartTime.Unix()), Inline: true},
			{Name: "Duration", Value: fmt.Sprintf("%d minutes", int(reservation.Duration.Minutes())), Inline: true},
			{Name: "Classification", Value: reservation.Classification.String(), Inline: true},
			{Name: "Owner", Value: fmt.Sprintf("<@%s>", reservation.Owner), Inline: true},
		},
		Timestamp: time.Now().Format(time.RFC3339),
	}

	return dg.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embed},
		},
	})
}

// handleCheckReservation handles the /reserve check command
func (h *ReservationSlashCommandHandler) handleCheckReservation(ctx context.Context, dg *discordgo.Session, i *discordgo.InteractionCreate, userID, guildID string, options []*discordgo.ApplicationCommandInteractionDataOption) error {
	if len(options) == 0 {
		return h.respondError(dg, i, "Reservation ID required")
	}

	reservationID := options[0].StringValue()

	reservation, err := h.reservationMgr.GetReservation(ctx, reservationID)
	if err != nil {
		return h.respondError(dg, i, "Reservation not found")
	}

	// Build status embed
	embed := h.buildReservationEmbed(reservation)

	return dg.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embed},
		},
	})
}

// handleListReservations handles the /reserve list command
func (h *ReservationSlashCommandHandler) handleListReservations(ctx context.Context, dg *discordgo.Session, i *discordgo.InteractionCreate, userID, guildID string, options []*discordgo.ApplicationCommandInteractionDataOption) error {
	// Get time range for listing (default to next 24 hours)
	startTime := time.Now()
	endTime := startTime.Add(24 * time.Hour)

	// Parse optional time range
	for _, opt := range options {
		switch opt.Name {
		case "hours":
			hours := int(opt.IntValue())
			endTime = startTime.Add(time.Duration(hours) * time.Hour)
		}
	}

	// Get guild group ID
	groupID, err := GetGroupIDByGuildIDNK(ctx, h.nk, guildID)
	if err != nil {
		return h.respondError(dg, i, "Failed to get guild group information")
	}

	// Get reservations
	reservations, err := h.reservationMgr.ListReservations(ctx, uuid.FromStringOrNil(groupID), startTime, endTime)
	if err != nil {
		return h.respondError(dg, i, fmt.Sprintf("Failed to list reservations: %v", err))
	}

	// Build response
	embed := &discordgo.MessageEmbed{
		Title:       "📅 Upcoming Reservations",
		Description: fmt.Sprintf("Next %d hours", int(endTime.Sub(startTime).Hours())),
		Color:       0x0066cc,
		Timestamp:   time.Now().Format(time.RFC3339),
	}

	if len(reservations) == 0 {
		embed.Description += "\n*No reservations found*"
	} else {
		for i, res := range reservations {
			if i >= 10 { // Limit to first 10
				embed.Footer = &discordgo.MessageEmbedFooter{
					Text: fmt.Sprintf("... and %d more", len(reservations)-10),
				}
				break
			}

			statusIcon := h.getStateIcon(res.State)
			timeStr := fmt.Sprintf("<t:%d:t>", res.StartTime.Unix())
			durationStr := fmt.Sprintf("(%dm)", int(res.Duration.Minutes()))

			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
				Name:   fmt.Sprintf("%s %s %s", statusIcon, res.Classification.String(), timeStr),
				Value:  fmt.Sprintf("ID: `%s` %s\nOwner: <@%s>", res.ID[:8], durationStr, res.Owner),
				Inline: true,
			})
		}
	}

	return dg.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embed},
		},
	})
}

// Helper functions

func (h *ReservationSlashCommandHandler) getUserInfo(ctx context.Context, i *discordgo.InteractionCreate) (userID, guildID string, err error) {
	// Get Discord user ID
	discordUserID := i.Member.User.ID
	guildID = i.GuildID

	// Convert Discord ID to Nakama user ID
	userID, err = GetUserIDByDiscordIDNK(ctx, h.nk, discordUserID)
	if err != nil {
		return "", "", fmt.Errorf("failed to get user ID: %w", err)
	}

	return userID, guildID, nil
}

func (h *ReservationSlashCommandHandler) respondError(dg *discordgo.Session, i *discordgo.InteractionCreate, message string) error {
	embed := &discordgo.MessageEmbed{
		Title:       "❌ Error",
		Description: message,
		Color:       0xff0000,
		Timestamp:   time.Now().Format(time.RFC3339),
	}

	return dg.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embed},
			Flags:  discordgo.MessageFlagsEphemeral,
		},
	})
}

func (h *ReservationSlashCommandHandler) buildReservationEmbed(reservation *MatchReservation) *discordgo.MessageEmbed {
	statusIcon := h.getStateIcon(reservation.State)

	embed := &discordgo.MessageEmbed{
		Title:     fmt.Sprintf("%s Reservation %s", statusIcon, reservation.ID[:8]),
		Color:     h.getStateColor(reservation.State),
		Timestamp: reservation.UpdatedAt.Format(time.RFC3339),
	}

	// Basic info
	embed.Fields = append(embed.Fields,
		&discordgo.MessageEmbedField{Name: "Start Time", Value: fmt.Sprintf("<t:%d:F>", reservation.StartTime.Unix()), Inline: true},
		&discordgo.MessageEmbedField{Name: "Duration", Value: fmt.Sprintf("%d minutes", int(reservation.Duration.Minutes())), Inline: true},
		&discordgo.MessageEmbedField{Name: "Classification", Value: reservation.Classification.String(), Inline: true},
		&discordgo.MessageEmbedField{Name: "Owner", Value: fmt.Sprintf("<@%s>", reservation.Owner), Inline: true},
		&discordgo.MessageEmbedField{Name: "State", Value: string(reservation.State), Inline: true},
	)

	// Add match ID if activated
	if reservation.MatchID != "" {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name: "Match ID", Value: reservation.MatchID, Inline: true,
		})
	}

	// Add state history if available
	if len(reservation.StateHistory) > 0 {
		recent := reservation.StateHistory[len(reservation.StateHistory)-1]
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:  "Last Update",
			Value: fmt.Sprintf("%s → %s\n*%s*", recent.FromState, recent.ToState, recent.Reason),
		})
	}

	return embed
}

func (h *ReservationSlashCommandHandler) getStateIcon(state ReservationState) string {
	switch state {
	case ReservationStateReserved:
		return "⏰"
	case ReservationStateActivated:
		return "🟢"
	case ReservationStateIdle:
		return "🟡"
	case ReservationStateEnded:
		return "✅"
	case ReservationStatePreempted:
		return "🔴"
	case ReservationStateExpired:
		return "⚫"
	default:
		return "❓"
	}
}

func (h *ReservationSlashCommandHandler) getStateColor(state ReservationState) int {
	switch state {
	case ReservationStateReserved:
		return 0x0066cc // Blue
	case ReservationStateActivated:
		return 0x00ff00 // Green
	case ReservationStateIdle:
		return 0xffff00 // Yellow
	case ReservationStateEnded:
		return 0x808080 // Gray
	case ReservationStatePreempted:
		return 0xff0000 // Red
	case ReservationStateExpired:
		return 0x404040 // Dark gray
	default:
		return 0x808080 // Gray
	}
}

func (h *ReservationSlashCommandHandler) handleReservationConflicts(dg *discordgo.Session, i *discordgo.InteractionCreate, conflicts []*ReservationConflict) error {
	embed := &discordgo.MessageEmbed{
		Title:       "⚠️ Reservation Conflicts",
		Description: "The requested time conflicts with existing reservations:",
		Color:       0xff6600,
		Timestamp:   time.Now().Format(time.RFC3339),
	}

	for idx, conflict := range conflicts {
		if idx >= 5 { // Limit to first 5 conflicts
			embed.Footer = &discordgo.MessageEmbedFooter{
				Text: fmt.Sprintf("... and %d more conflicts", len(conflicts)-5),
			}
			break
		}

		existing := conflict.ExistingReservation
		conflictInfo := fmt.Sprintf("**Type:** %s\n**Time:** <t:%d:t> - <t:%d:t>\n**Owner:** <@%s>",
			conflict.ConflictType,
			existing.StartTime.Unix(),
			existing.EndTime.Unix(),
			existing.Owner)

		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:  fmt.Sprintf("Conflict %d: %s (%s)", idx+1, existing.ID[:8], existing.Classification.String()),
			Value: conflictInfo,
		})
	}

	embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
		Name:  "💡 Tip",
		Value: "Use `force: true` to override conflicts or choose a different time slot.",
	})

	return dg.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embed},
			Flags:  discordgo.MessageFlagsEphemeral,
		},
	})
}

// Stub implementations for remaining handlers
func (h *ReservationSlashCommandHandler) handleRemoveReservation(ctx context.Context, dg *discordgo.Session, i *discordgo.InteractionCreate, userID, guildID string, options []*discordgo.ApplicationCommandInteractionDataOption) error {
	// TODO: Implement reservation removal
	return h.respondError(dg, i, "Remove reservation not yet implemented")
}

func (h *ReservationSlashCommandHandler) handleReservationStatus(ctx context.Context, dg *discordgo.Session, i *discordgo.InteractionCreate, userID, guildID string, options []*discordgo.ApplicationCommandInteractionDataOption) error {
	// TODO: Implement reservation status
	return h.respondError(dg, i, "Reservation status not yet implemented")
}

func (h *ReservationSlashCommandHandler) handleDashboard(ctx context.Context, dg *discordgo.Session, i *discordgo.InteractionCreate, userID, guildID string, options []*discordgo.ApplicationCommandInteractionDataOption) error {
	// TODO: Implement dashboard
	return h.respondError(dg, i, "Dashboard not yet implemented")
}

type VacateCommandHandler struct {
	nk     runtime.NakamaModule
	logger runtime.Logger
}

func NewVacateCommandHandler(nk runtime.NakamaModule, logger runtime.Logger) *VacateCommandHandler {
	return &VacateCommandHandler{
		nk:     nk,
		logger: logger,
	}
}

func (h *VacateCommandHandler) HandleVacateCommand(ctx context.Context, dg *discordgo.Session, i *discordgo.InteractionCreate, userID string) error {
	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		return errors.New("match-id required")
	}

	var matchIDStr string
	var override bool

	for _, opt := range options {
		switch opt.Name {
		case "match-id":
			matchIDStr = opt.StringValue()
		case "override":
			override = opt.BoolValue()
		}
	}

	matchIDStr = strings.TrimSpace(matchIDStr)
	if matchIDStr == "" {
		return errors.New("no match ID provided")
	}

	matchID := MatchIDFromStringOrNil(matchIDStr)
	if matchID.IsNil() {
		return fmt.Errorf("invalid match ID: %s", matchIDStr)
	}

	label, err := MatchLabelByID(ctx, h.nk, matchID)
	if err != nil {
		return fmt.Errorf("failed to get match label: %w", err)
	}

	callerUUID, err := uuid.FromString(userID)
	if err != nil {
		return fmt.Errorf("invalid user ID: %w", err)
	}

	if label.Owner != callerUUID {
		return errors.New("only the reservation owner can vacate this server")
	}

	graceSeconds := 60
	if override {
		graceSeconds = 20
	}

	signal := SignalShutdownPayload{
		GraceSeconds:         graceSeconds,
		DisconnectGameServer: false,
		DisconnectUsers:      false,
	}

	data := NewSignalEnvelope(userID, SignalShutdown, signal).String()

	if _, err := h.nk.MatchSignal(ctx, matchID.String(), data); err != nil {
		return fmt.Errorf("failed to signal match: %w", err)
	}

	return nil
}

// BuildReservationActivationDM builds a Discord DM message for reservation activation with spark link
func BuildReservationActivationDM(matchID string) string {
	matchIDUpper := strings.ToUpper(matchID)
	sparkLink := fmt.Sprintf("https://echo.taxi/spark://c/%s", matchIDUpper)
	message := fmt.Sprintf("Your reservation is now active!\n\n[Join Match](%s)", sparkLink)
	return message
}

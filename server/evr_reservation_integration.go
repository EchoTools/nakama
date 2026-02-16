package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

// ReservationIntegration handles all reservation-related functionality
type ReservationIntegration struct {
	nk                 runtime.NakamaModule
	logger             runtime.Logger
	reservationMgr     *ReservationManager
	preemptionMgr      *MatchPreemptionManager
	dashboardMgr       *ReservationDashboardManager
	commandHandler     *ReservationSlashCommandHandler
	utilizationMonitor *ServerUtilizationMonitor
}

// NewReservationIntegration creates a new reservation integration
func NewReservationIntegration(nk runtime.NakamaModule, logger runtime.Logger) *ReservationIntegration {
	reservationMgr := NewReservationManager(nk, logger)
	preemptionMgr := NewMatchPreemptionManager(nk, logger, reservationMgr)
	dashboardMgr := NewReservationDashboardManager(nk, logger, reservationMgr)
	commandHandler := NewReservationSlashCommandHandler(nk, logger, reservationMgr, preemptionMgr)
	utilizationMonitor := NewServerUtilizationMonitor(nk, logger)

	return &ReservationIntegration{
		nk:                 nk,
		logger:             logger,
		reservationMgr:     reservationMgr,
		preemptionMgr:      preemptionMgr,
		dashboardMgr:       dashboardMgr,
		commandHandler:     commandHandler,
		utilizationMonitor: utilizationMonitor,
	}
}

// RegisterDiscordCommands registers the reservation slash commands
func (ri *ReservationIntegration) RegisterDiscordCommands(dg *discordgo.Session) error {
	command := GetReserveCommandDefinition()

	_, err := dg.ApplicationCommandCreate(dg.State.User.ID, "", command)
	if err != nil {
		return fmt.Errorf("failed to register /reserve command: %w", err)
	}

	ri.logger.Info("Registered /reserve slash command")
	return nil
}

// HandleSlashCommand handles incoming slash command interactions
func (ri *ReservationIntegration) HandleSlashCommand(ctx context.Context, dg *discordgo.Session, i *discordgo.InteractionCreate) error {
	if i.ApplicationCommandData().Name == "reserve" {
		return ri.commandHandler.HandleReserveCommand(ctx, dg, i)
	}
	return nil
}

// StartMonitoring starts background monitoring tasks
func (ri *ReservationIntegration) StartMonitoring(ctx context.Context, dg *discordgo.Session) {
	// Start server utilization monitoring
	go ri.utilizationMonitor.StartMonitoring(ctx, dg)

	// Start reservation cleanup task
	go ri.startReservationCleanup(ctx)

	ri.logger.Info("Started reservation system monitoring")
}

// startReservationCleanup periodically cleans up expired reservations
func (ri *ReservationIntegration) startReservationCleanup(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(UtilizationCheckIntervalMinutes) * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ri.cleanupExpiredReservations(ctx)
		}
	}
}

// cleanupExpiredReservations removes expired reservations and updates states
func (ri *ReservationIntegration) cleanupExpiredReservations(ctx context.Context) {
	// This would need to be implemented to query all reservations and clean up expired ones
	// For now, we'll just log that the cleanup is running
	ri.logger.Debug("Running reservation cleanup task")
}

// ServerUtilizationMonitor monitors server usage and sends notifications
type ServerUtilizationMonitor struct {
	nk           runtime.NakamaModule
	logger       runtime.Logger
	alertTracker map[string]time.Time // Track when alerts were last sent for matches
}

// NewServerUtilizationMonitor creates a new utilization monitor
func NewServerUtilizationMonitor(nk runtime.NakamaModule, logger runtime.Logger) *ServerUtilizationMonitor {
	return &ServerUtilizationMonitor{
		nk:           nk,
		logger:       logger,
		alertTracker: make(map[string]time.Time),
	}
}

// StartMonitoring starts the utilization monitoring loop
func (sum *ServerUtilizationMonitor) StartMonitoring(ctx context.Context, dg *discordgo.Session) {
	ticker := time.NewTicker(time.Duration(UtilizationCheckIntervalMinutes) * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sum.checkUtilization(ctx, dg)
		}
	}
}

// checkUtilization checks all matches for low utilization and sends notifications
func (sum *ServerUtilizationMonitor) checkUtilization(ctx context.Context, dg *discordgo.Session) {
	matches, err := sum.nk.MatchList(ctx, 1000, true, "", nil, nil, "*")
	if err != nil {
		sum.logger.Error("Failed to list matches for utilization monitoring: %v", err)
		return
	}

	now := time.Now()

	for _, match := range matches {
		var label MatchLabel
		if err := json.Unmarshal([]byte(match.Label.Value), &label); err != nil {
			continue
		}

		// Skip if not enough time has passed since match start
		if label.StartTime.IsZero() || now.Sub(label.StartTime) < time.Duration(LowPlayerDurationMinutes)*time.Minute {
			continue
		}

		// Check if player count is below threshold
		if label.PlayerCount >= LowPlayerCountThreshold {
			// Remove from alert tracker if player count is now adequate
			delete(sum.alertTracker, match.MatchId)
			continue
		}

		// Check if we've already sent an alert recently
		lastAlert, exists := sum.alertTracker[match.MatchId]
		if exists && now.Sub(lastAlert) < time.Duration(UtilizationCheckIntervalMinutes)*time.Minute {
			continue
		}

		// Send low utilization notification
		if err := sum.sendLowUtilizationAlert(ctx, dg, &label, match.MatchId); err != nil {
			sum.logger.Error("Failed to send low utilization alert for match %s: %v", match.MatchId, err)
		} else {
			sum.alertTracker[match.MatchId] = now
		}
	}
}

// sendLowUtilizationAlert sends a notification about low server utilization
func (sum *ServerUtilizationMonitor) sendLowUtilizationAlert(ctx context.Context, dg *discordgo.Session, label *MatchLabel, matchID string) error {
	if label.GroupID == nil {
		return nil // Skip matches without guild associations
	}

	// Get guild ID from group ID
	guildID, err := GetGuildIDByGroupIDNK(ctx, sum.nk, label.GroupID.String())
	if err != nil {
		return fmt.Errorf("failed to get guild ID: %w", err)
	}

	// Get guild audit channel
	auditChannelID, err := GetGuildAuditChannelID(ctx, sum.nk, guildID)
	if err != nil || auditChannelID == "" {
		return nil // No audit channel configured
	}

	duration := time.Since(label.StartTime)

	embed := &discordgo.MessageEmbed{
		Title:       "⚠️ Low Server Utilization Alert",
		Description: fmt.Sprintf("Match `%s` has low player activity", matchID[:8]),
		Color:       0xff6600,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Player Count", Value: fmt.Sprintf("%d (threshold: %d)", label.PlayerCount, LowPlayerCountThreshold), Inline: true},
			{Name: "Duration", Value: fmt.Sprintf("%.0f minutes", duration.Minutes()), Inline: true},
			{Name: "Classification", Value: label.Classification.String(), Inline: true},
		},
		Timestamp: time.Now().Format(time.RFC3339),
	}

	if label.Owner != uuid.Nil {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name: "Owner", Value: fmt.Sprintf("<@%s>", label.Owner.String()), Inline: true,
		})
	}

	embed.Footer = &discordgo.MessageEmbedFooter{
		Text: "Notifications repeat every 5 minutes until activity increases",
	}

	_, err = dg.ChannelMessageSendEmbed(auditChannelID, embed)
	return err
}

// Helper functions that would need to be implemented based on existing guild/group system

// GetGuildIDByGroupIDNK converts a group ID to guild ID
func GetGuildIDByGroupIDNK(ctx context.Context, nk runtime.NakamaModule, groupID string) (string, error) {
	// This would need to be implemented based on the existing guild group system
	// For now, return a placeholder
	return "placeholder_guild_id", nil
}

// GetGuildAuditChannelID gets the audit channel ID for a guild
func GetGuildAuditChannelID(ctx context.Context, nk runtime.NakamaModule, guildID string) (string, error) {
	// This would need to be implemented based on the existing guild configuration system
	// For now, return a placeholder
	return "placeholder_audit_channel_id", nil
}

// GetGroupIDByGuildIDNK converts a guild ID to group ID
func GetGroupIDByGuildIDNK(ctx context.Context, nk runtime.NakamaModule, guildID string) (string, error) {
	// This would need to be implemented based on the existing guild group system
	// For now, return a placeholder
	return "placeholder_group_id", nil
}

// GetUserIDByDiscordIDNK converts a Discord user ID to Nakama user ID
func GetUserIDByDiscordIDNK(ctx context.Context, nk runtime.NakamaModule, discordID string) (string, error) {
	// This would need to be implemented based on the existing user linking system
	// For now, return a placeholder
	return fmt.Sprintf("user_%s", discordID), nil
}

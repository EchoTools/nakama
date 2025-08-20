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

// ReservationDashboardManager handles the dashboard system for server/reservation utilization
type ReservationDashboardManager struct {
	nk             runtime.NakamaModule
	logger         runtime.Logger
	reservationMgr *ReservationManager
}

// NewReservationDashboardManager creates a new dashboard manager
func NewReservationDashboardManager(nk runtime.NakamaModule, logger runtime.Logger, reservationMgr *ReservationManager) *ReservationDashboardManager {
	return &ReservationDashboardManager{
		nk:             nk,
		logger:         logger,
		reservationMgr: reservationMgr,
	}
}

// DashboardData represents the current server and reservation status
type DashboardData struct {
	GuildID                string                  `json:"guild_id"`
	LastUpdated           time.Time               `json:"last_updated"`
	ServerCapacity        ServerCapacityInfo      `json:"server_capacity"`
	ReservationUtilization ReservationUtilization `json:"reservation_utilization"`
	ActiveMatches         []ActiveMatchInfo       `json:"active_matches"`
	UpcomingReservations  []UpcomingReservation   `json:"upcoming_reservations"`
	LowUtilizationAlerts  []UtilizationAlert      `json:"low_utilization_alerts"`
}

// ServerCapacityInfo represents current server capacity
type ServerCapacityInfo struct {
	TotalServers     int `json:"total_servers"`
	ActiveServers    int `json:"active_servers"`
	AvailableServers int `json:"available_servers"`
	PlayerCount      int `json:"player_count"`
	MaxPlayers       int `json:"max_players"`
}

// ReservationUtilization represents reservation usage statistics
type ReservationUtilization struct {
	Next24Hours        int     `json:"next_24_hours"`
	CurrentlyActive    int     `json:"currently_active"`
	UtilizationPercent float64 `json:"utilization_percent"`
}

// ActiveMatchInfo represents currently running matches
type ActiveMatchInfo struct {
	MatchID        string                `json:"match_id"`
	Classification SessionClassification `json:"classification"`
	PlayerCount    int                   `json:"player_count"`
	Duration       time.Duration         `json:"duration"`
	Owner          string                `json:"owner"`
	LowActivity    bool                  `json:"low_activity"`
}

// UpcomingReservation represents future reservations
type UpcomingReservation struct {
	ID             string                `json:"id"`
	StartTime      time.Time             `json:"start_time"`
	Duration       time.Duration         `json:"duration"`
	Classification SessionClassification `json:"classification"`
	Owner          string                `json:"owner"`
}

// UtilizationAlert represents low utilization warnings
type UtilizationAlert struct {
	MatchID      string        `json:"match_id"`
	PlayerCount  int           `json:"player_count"`
	Duration     time.Duration `json:"duration"`
	LastNotified time.Time     `json:"last_notified"`
}

// GenerateDashboard creates or updates the dashboard for a guild
func (dm *ReservationDashboardManager) GenerateDashboard(ctx context.Context, dg *discordgo.Session, guildID string) (*discordgo.MessageEmbed, error) {
	data, err := dm.collectDashboardData(ctx, guildID)
	if err != nil {
		return nil, fmt.Errorf("failed to collect dashboard data: %w", err)
	}

	embed := dm.buildDashboardEmbed(data)
	return embed, nil
}

// collectDashboardData gathers all the information needed for the dashboard
func (dm *ReservationDashboardManager) collectDashboardData(ctx context.Context, guildID string) (*DashboardData, error) {
	// Get guild group ID
	groupID, err := GetGroupIDByGuildID(ctx, dm.nk, guildID)
	if err != nil {
		return nil, fmt.Errorf("failed to get guild group: %w", err)
	}

	data := &DashboardData{
		GuildID:     guildID,
		LastUpdated: time.Now(),
	}

	// Collect server capacity information
	if err := dm.collectServerCapacity(ctx, data); err != nil {
		dm.logger.Warn("Failed to collect server capacity: %v", err)
	}

	// Collect reservation utilization
	if err := dm.collectReservationUtilization(ctx, uuid.FromStringOrNil(groupID), data); err != nil {
		dm.logger.Warn("Failed to collect reservation utilization: %v", err)
	}

	// Collect active matches
	if err := dm.collectActiveMatches(ctx, uuid.FromStringOrNil(groupID), data); err != nil {
		dm.logger.Warn("Failed to collect active matches: %v", err)
	}

	// Collect upcoming reservations
	if err := dm.collectUpcomingReservations(ctx, uuid.FromStringOrNil(groupID), data); err != nil {
		dm.logger.Warn("Failed to collect upcoming reservations: %v", err)
	}

	return data, nil
}

// collectServerCapacity gathers server capacity information
func (dm *ReservationDashboardManager) collectServerCapacity(ctx context.Context, data *DashboardData) error {
	// Get all active matches to calculate server usage
	matches, err := dm.nk.MatchList(ctx, 1000, true, "", nil, nil, "*")
	if err != nil {
		return err
	}

	capacity := ServerCapacityInfo{
		TotalServers:  100, // This would be dynamically determined in a real system
		ActiveServers: len(matches),
	}

	for _, match := range matches {
		var label MatchLabel
		if err := json.Unmarshal([]byte(match.Label.Value), &label); err != nil {
			continue
		}

		capacity.PlayerCount += label.PlayerCount
		capacity.MaxPlayers += label.MaxSize
	}

	capacity.AvailableServers = capacity.TotalServers - capacity.ActiveServers
	data.ServerCapacity = capacity

	return nil
}

// collectReservationUtilization gathers reservation usage statistics
func (dm *ReservationDashboardManager) collectReservationUtilization(ctx context.Context, groupID uuid.UUID, data *DashboardData) error {
	now := time.Now()
	
	// Get reservations for the next 24 hours
	reservations, err := dm.reservationMgr.ListReservations(ctx, groupID, now, now.Add(24*time.Hour))
	if err != nil {
		return err
	}

	utilization := ReservationUtilization{}
	
	for _, res := range reservations {
		utilization.Next24Hours++
		
		if res.IsActive() {
			utilization.CurrentlyActive++
		}
	}

	// Calculate utilization percentage (simplified)
	if data.ServerCapacity.TotalServers > 0 {
		utilization.UtilizationPercent = float64(utilization.CurrentlyActive) / float64(data.ServerCapacity.TotalServers) * 100
	}

	data.ReservationUtilization = utilization
	return nil
}

// collectActiveMatches gathers information about currently running matches
func (dm *ReservationDashboardManager) collectActiveMatches(ctx context.Context, groupID uuid.UUID, data *DashboardData) error {
	matches, err := dm.nk.MatchList(ctx, 100, true, "", nil, nil, "*")
	if err != nil {
		return err
	}

	for _, match := range matches {
		var label MatchLabel
		if err := json.Unmarshal([]byte(match.Label.Value), &label); err != nil {
			continue
		}

		// Only include matches from this guild
		if label.GroupID == nil || *label.GroupID != groupID {
			continue
		}

		duration := time.Since(label.StartTime)
		lowActivity := label.PlayerCount < LowPlayerCountThreshold && 
			duration > time.Duration(LowPlayerDurationMinutes)*time.Minute

		activeMatch := ActiveMatchInfo{
			MatchID:        match.MatchId,
			Classification: label.Classification,
			PlayerCount:    label.PlayerCount,
			Duration:       duration,
			Owner:          label.Owner,
			LowActivity:    lowActivity,
		}

		data.ActiveMatches = append(data.ActiveMatches, activeMatch)

		// Track low utilization alerts
		if lowActivity {
			alert := UtilizationAlert{
				MatchID:     match.MatchId,
				PlayerCount: label.PlayerCount,
				Duration:    duration,
			}
			data.LowUtilizationAlerts = append(data.LowUtilizationAlerts, alert)
		}
	}

	return nil
}

// collectUpcomingReservations gathers upcoming reservation information
func (dm *ReservationDashboardManager) collectUpcomingReservations(ctx context.Context, groupID uuid.UUID, data *DashboardData) error {
	now := time.Now()
	reservations, err := dm.reservationMgr.ListReservations(ctx, groupID, now, now.Add(24*time.Hour))
	if err != nil {
		return err
	}

	for _, res := range reservations {
		if res.State == ReservationStateReserved && res.StartTime.After(now) {
			upcoming := UpcomingReservation{
				ID:             res.ID,
				StartTime:      res.StartTime,
				Duration:       res.Duration,
				Classification: res.Classification,
				Owner:          res.Owner,
			}
			data.UpcomingReservations = append(data.UpcomingReservations, upcoming)
		}
	}

	return nil
}

// buildDashboardEmbed creates the dashboard embed message
func (dm *ReservationDashboardManager) buildDashboardEmbed(data *DashboardData) *discordgo.MessageEmbed {
	embed := &discordgo.MessageEmbed{
		Title:       "📊 Server & Reservation Dashboard",
		Description: fmt.Sprintf("Last updated: <t:%d:R>", data.LastUpdated.Unix()),
		Color:       0x0066cc,
		Timestamp:   data.LastUpdated.Format(time.RFC3339),
	}

	// Server Capacity Section
	capacityValue := fmt.Sprintf(
		"**Servers:** %d/%d active (%d available)\n**Players:** %d/%d (%d%% capacity)",
		data.ServerCapacity.ActiveServers,
		data.ServerCapacity.TotalServers,
		data.ServerCapacity.AvailableServers,
		data.ServerCapacity.PlayerCount,
		data.ServerCapacity.MaxPlayers,
		getPercentage(data.ServerCapacity.PlayerCount, data.ServerCapacity.MaxPlayers),
	)

	embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
		Name:   "🖥️ Server Capacity",
		Value:  capacityValue,
		Inline: true,
	})

	// Reservation Utilization Section
	reservationValue := fmt.Sprintf(
		"**Next 24h:** %d reservations\n**Currently Active:** %d\n**Utilization:** %.1f%%",
		data.ReservationUtilization.Next24Hours,
		data.ReservationUtilization.CurrentlyActive,
		data.ReservationUtilization.UtilizationPercent,
	)

	embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
		Name:   "📅 Reservation Status",
		Value:  reservationValue,
		Inline: true,
	})

	// Active Matches Section
	if len(data.ActiveMatches) > 0 {
		activeValue := ""
		for i, match := range data.ActiveMatches {
			if i >= 5 { // Limit to 5 matches
				activeValue += fmt.Sprintf("... and %d more", len(data.ActiveMatches)-5)
				break
			}

			statusIcon := "🟢"
			if match.LowActivity {
				statusIcon = "🟡"
			}

			activeValue += fmt.Sprintf("%s `%s` (%s) - %d players\n",
				statusIcon,
				match.MatchID[:8],
				match.Classification.String(),
				match.PlayerCount,
			)
		}

		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:  "🎮 Active Matches",
			Value: activeValue,
		})
	}

	// Low Utilization Alerts
	if len(data.LowUtilizationAlerts) > 0 {
		alertValue := ""
		for i, alert := range data.LowUtilizationAlerts {
			if i >= 3 { // Limit to 3 alerts
				alertValue += fmt.Sprintf("... and %d more", len(data.LowUtilizationAlerts)-3)
				break
			}

			alertValue += fmt.Sprintf("⚠️ `%s` - %d players for %dm\n",
				alert.MatchID[:8],
				alert.PlayerCount,
				int(alert.Duration.Minutes()),
			)
		}

		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:  "⚠️ Low Activity Alerts",
			Value: alertValue,
		})
	}

	// Upcoming Reservations Section
	if len(data.UpcomingReservations) > 0 {
		upcomingValue := ""
		for i, res := range data.UpcomingReservations {
			if i >= 5 { // Limit to 5 reservations
				upcomingValue += fmt.Sprintf("... and %d more", len(data.UpcomingReservations)-5)
				break
			}

			upcomingValue += fmt.Sprintf("📅 <t:%d:t> - %s (%dm)\n",
				res.StartTime.Unix(),
				res.Classification.String(),
				int(res.Duration.Minutes()),
			)
		}

		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:  "⏰ Upcoming Reservations",
			Value: upcomingValue,
		})
	}

	// Add footer with refresh info
	embed.Footer = &discordgo.MessageEmbedFooter{
		Text: "Use /reserve dashboard to refresh • Updates every 5 minutes",
	}

	return embed
}

// Helper function to calculate percentage
func getPercentage(current, max int) int {
	if max == 0 {
		return 0
	}
	return int(float64(current) / float64(max) * 100)
}

// GetReserveCommandDefinition returns the Discord slash command definition for /reserve
func GetReserveCommandDefinition() *discordgo.ApplicationCommand {
	return &discordgo.ApplicationCommand{
		Name:        "reserve",
		Description: "Manage match reservations",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "add",
				Description: "Create a new match reservation",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionString,
						Name:        "start_time",
						Description: "Start time (YYYY-MM-DDTHH:MM format)",
						Required:    true,
					},
					{
						Type:        discordgo.ApplicationCommandOptionInteger,
						Name:        "duration",
						Description: "Duration in minutes (34-130)",
						Required:    true,
						MinValue:    float64Ptr(MinReservationMinutes),
						MaxValue:    float64(MaxReservationMinutes),
					},
					{
						Type:        discordgo.ApplicationCommandOptionString,
						Name:        "classification",
						Description: "Match classification/priority",
						Required:    true,
						Choices: []*discordgo.ApplicationCommandOptionChoice{
							{Name: "League (Highest Priority)", Value: "league"},
							{Name: "Scrimmage", Value: "scrimmage"},
							{Name: "Mixed", Value: "mixed"},
							{Name: "Pickup", Value: "pickup"},
							{Name: "None (Lowest Priority)", Value: "none"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionUser,
						Name:        "owner",
						Description: "Match owner (defaults to you)",
						Required:    false,
					},
					{
						Type:        discordgo.ApplicationCommandOptionBoolean,
						Name:        "force",
						Description: "Force creation even if conflicts exist",
						Required:    false,
					},
				},
			},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "check",
				Description: "Check the status of a reservation",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionString,
						Name:        "id",
						Description: "Reservation ID",
						Required:    true,
					},
				},
			},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "list",
				Description: "List upcoming reservations",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionInteger,
						Name:        "hours",
						Description: "Hours to look ahead (default: 24)",
						Required:    false,
						MinValue:    float64Ptr(1),
						MaxValue:    168, // 1 week
					},
				},
			},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "remove",
				Description: "Remove a reservation",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionString,
						Name:        "id",
						Description: "Reservation ID",
						Required:    true,
					},
				},
			},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "status",
				Description: "Show guild reservation status",
			},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "dashboard",
				Description: "Show server and reservation dashboard",
			},
		},
	}
}

// Helper function for float64 pointer
func float64Ptr(v float64) *float64 {
	return &v
}
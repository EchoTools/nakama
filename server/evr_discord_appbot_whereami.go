package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
)

// WhereAmIData holds the information about a player's current match
type WhereAmIData struct {
	// ServerHostIP is the external IP address of the game server
	ServerHostIP string
	// RegionCode is the auto-generated region code for the server location
	RegionCode string
	// GuildName is the name of the Discord guild/server hosting the match
	GuildName string
	// EchoTaxiLink is the link to join the match via echo.taxi (spark link)
	EchoTaxiLink string
	// MatchMode is the game mode (Arena, Combat, Social, etc.)
	MatchMode string
	// MatchID is the unique identifier for the match
	MatchID string
	// CreatorUserID is the Nakama user ID of the player who created/spawned the match
	CreatorUserID string
	// CreatorDiscord is the Discord ID of the match creator
	CreatorDiscord string
	// OperatorUserID is the Nakama user ID of the game server operator
	OperatorUserID string
	// OperatorDiscord is the Discord ID of the game server operator
	OperatorDiscord string
	// Players is the list of players currently in the match
	Players []PlayerInfo
}

// getWhereAmIData retrieves the current match information for a user
func (d *DiscordAppBot) getWhereAmIData(ctx context.Context, _ runtime.Logger, userID, _ string) (*WhereAmIData, error) {
	// Get the user's current match presence
	presences, err := d.nk.StreamUserList(StreamModeService, userID, "", StreamLabelMatchService, false, true)
	if err != nil {
		return nil, fmt.Errorf("failed to get user match presence: %w", err)
	}

	if len(presences) == 0 {
		return nil, nil // User is not in a match
	}

	// Get the match ID from the presence status
	matchIDStr := presences[0].GetStatus()
	if matchIDStr == "" {
		return nil, nil
	}

	matchID := MatchIDFromStringOrNil(matchIDStr)
	if matchID.IsNil() {
		return nil, nil
	}

	// Get the match label
	label, err := MatchLabelByID(ctx, d.nk, matchID)
	if err != nil {
		return nil, fmt.Errorf("failed to get match label: %w", err)
	}

	if label == nil {
		return nil, nil
	}

	data := &WhereAmIData{
		MatchID:   strings.ToUpper(label.ID.UUID.String()),
		MatchMode: label.Mode.String(),
		Players:   label.Players,
	}

	// Get server information
	if label.GameServer != nil {
		data.ServerHostIP = label.GameServer.Endpoint.ExternalIP.String()
		data.RegionCode = label.GameServer.LocationRegionCode(true, true)

		// Get operator information
		if !label.GameServer.OperatorID.IsNil() {
			data.OperatorUserID = label.GameServer.OperatorID.String()
			data.OperatorDiscord = d.cache.UserIDToDiscordID(data.OperatorUserID)
		}
	}

	// Get guild name
	if gg := d.guildGroupRegistry.Get(label.GetGroupID().String()); gg != nil {
		data.GuildName = gg.Name()
	}

	// Generate echo.taxi link
	data.EchoTaxiLink = fmt.Sprintf("https://echo.taxi/spark://c/%s", data.MatchID)

	// Get creator/owner information
	if label.SpawnedBy != "" {
		data.CreatorUserID = label.SpawnedBy
		data.CreatorDiscord = d.cache.UserIDToDiscordID(label.SpawnedBy)
	}

	return data, nil
}

// formatPlayerList formats a list of players for Discord display
func formatPlayerList(players []PlayerInfo) []string {
	playerList := make([]string, 0, len(players))
	for _, p := range players {
		if p.DiscordID != "" {
			playerList = append(playerList, fmt.Sprintf("<@%s>", p.DiscordID))
		} else {
			playerList = append(playerList, fmt.Sprintf("`%s`", EscapeDiscordMarkdown(p.DisplayName)))
		}
	}
	return playerList
}

// createWhereAmIEmbed creates a Discord embed with the whereami information
func (d *DiscordAppBot) createWhereAmIEmbed(data *WhereAmIData) *discordgo.MessageEmbed {
	embed := &discordgo.MessageEmbed{
		Title:  "Where Am I?",
		Color:  EmbedColorBlue,
		Fields: []*discordgo.MessageEmbedField{},
	}

	if data.ServerHostIP != "" {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Server Host",
			Value:  data.ServerHostIP,
			Inline: true,
		})
	}

	if data.RegionCode != "" {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Region",
			Value:  data.RegionCode,
			Inline: true,
		})
	}

	if data.GuildName != "" {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Guild",
			Value:  EscapeDiscordMarkdown(data.GuildName),
			Inline: true,
		})
	}

	if data.MatchMode != "" {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Match Mode",
			Value:  data.MatchMode,
			Inline: true,
		})
	}

	if data.EchoTaxiLink != "" {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Spark Link",
			Value:  fmt.Sprintf("[Join Match](%s)", data.EchoTaxiLink),
			Inline: true,
		})
	}

	// Creator/Owner
	if data.CreatorDiscord != "" {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Match Creator",
			Value:  fmt.Sprintf("<@%s>", data.CreatorDiscord),
			Inline: true,
		})
	}

	// Server Operator
	if data.OperatorDiscord != "" {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Server Operator",
			Value:  fmt.Sprintf("<@%s>", data.OperatorDiscord),
			Inline: true,
		})
	}

	// Players in match
	if len(data.Players) > 0 {
		playerList := formatPlayerList(data.Players)
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   fmt.Sprintf("Players (%d)", len(data.Players)),
			Value:  strings.Join(playerList, ", "),
			Inline: false,
		})
	}

	return embed
}

// handleWhereAmI handles the /whereami slash command
func (d *DiscordAppBot) handleWhereAmI(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
	if user == nil {
		return nil
	}

	data, err := d.getWhereAmIData(ctx, logger, userID, groupID)
	if err != nil {
		return fmt.Errorf("failed to get match information: %w", err)
	}

	if data == nil {
		return simpleInteractionResponse(s, i, "You are not currently in a match.")
	}

	embed := d.createWhereAmIEmbed(data)

	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags:  discordgo.MessageFlagsEphemeral,
			Embeds: []*discordgo.MessageEmbed{embed},
		},
	})
}

// Server Issue Report Types
const (
	// ServerIssueTypeLag represents lag or stuttering issues with the game server
	ServerIssueTypeLag = "server_lag"
	// ServerIssueTypeOther represents any other server-related issue
	ServerIssueTypeOther = "other"
)

// Server issue report input constraints
const (
	IssueDetailsMinLength = 0
	IssueDetailsMaxLength = 1000
)

// Server statistics constants
const (
	// MinActiveMatchSize is the minimum number of players (including the game server)
	// for a match to be considered "active" vs "idle"
	MinActiveMatchSize = 2
	// MaxMatchListSize is the maximum number of matches to retrieve for server statistics
	MaxMatchListSize = 100
)

// handleReportServerIssue handles the /report-server-issue slash command
// It displays server information and provides buttons to report lag or other issues
func (d *DiscordAppBot) handleReportServerIssue(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
	if user == nil {
		return nil
	}

	// Check if user is in a match
	data, err := d.getWhereAmIData(ctx, logger, userID, groupID)
	if err != nil {
		return fmt.Errorf("failed to get match information: %w", err)
	}

	if data == nil {
		return simpleInteractionResponse(s, i, "You are not currently in a match. You can only report server issues while in a match.")
	}

	// Create the server info embed
	embed := d.createServerInfoEmbed(data)

	// Create buttons with match context encoded in CustomID
	// Format: report_server_issue:<issue_type>:<matchID>:<serverIP>:<regionCode>
	serverContext := fmt.Sprintf("%s:%s:%s", data.MatchID, data.ServerHostIP, data.RegionCode)

	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				&discordgo.Button{
					Label:    "Report Server Lag",
					Style:    discordgo.DangerButton,
					CustomID: fmt.Sprintf("report_server_issue:lag:%s", serverContext),
					Emoji:    &discordgo.ComponentEmoji{Name: "⚡"},
				},
				&discordgo.Button{
					Label:    "Report Other Issue...",
					Style:    discordgo.SecondaryButton,
					CustomID: fmt.Sprintf("report_server_issue:other:%s", serverContext),
					Emoji:    &discordgo.ComponentEmoji{Name: "📝"},
				},
			},
		},
	}

	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags:      discordgo.MessageFlagsEphemeral,
			Embeds:     []*discordgo.MessageEmbed{embed},
			Components: components,
		},
	})
}

// createServerInfoEmbed creates an embed showing current server information
func (d *DiscordAppBot) createServerInfoEmbed(data *WhereAmIData) *discordgo.MessageEmbed {
	embed := &discordgo.MessageEmbed{
		Title:       "Current Server Information",
		Description: "Select an option below to report an issue with this server.",
		Color:       EmbedColorBlue,
		Fields:      []*discordgo.MessageEmbedField{},
	}

	if data.ServerHostIP != "" {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Server Host",
			Value:  data.ServerHostIP,
			Inline: true,
		})
	}

	if data.RegionCode != "" {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Region",
			Value:  data.RegionCode,
			Inline: true,
		})
	}

	if data.GuildName != "" {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Guild",
			Value:  EscapeDiscordMarkdown(data.GuildName),
			Inline: true,
		})
	}

	if data.MatchMode != "" {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Match Mode",
			Value:  data.MatchMode,
			Inline: true,
		})
	}

	if data.OperatorDiscord != "" {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Server Operator",
			Value:  fmt.Sprintf("<@%s>", data.OperatorDiscord),
			Inline: true,
		})
	}

	// Players in match
	if len(data.Players) > 0 {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Players",
			Value:  fmt.Sprintf("%d in match", len(data.Players)),
			Inline: true,
		})
	}

	return embed
}

// handleReportServerIssueLag handles the "Report Server Lag" button click
func (d *DiscordAppBot) handleReportServerIssueLag(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, serverContext string) error {
	user := getScopedUser(i)
	if user == nil {
		return fmt.Errorf("user is nil")
	}

	userID := d.cache.DiscordIDToUserID(user.ID)
	groupID := d.cache.GuildIDToGroupID(i.GuildID)

	// Parse server context: matchID:serverIP:regionCode
	parts := strings.SplitN(serverContext, ":", 3)
	if len(parts) < 3 {
		return fmt.Errorf("invalid server context")
	}
	matchID := parts[0]
	serverIP := parts[1]
	regionCode := parts[2]

	// Get current match data for additional context
	data, _ := d.getWhereAmIData(ctx, logger, userID, groupID)

	// Create the report embed
	embed := d.createServerIssueReportEmbed(user, ServerIssueTypeLag, "", data)

	// Add reported server info if we don't have current data
	if data == nil {
		embed.Fields = append(embed.Fields,
			&discordgo.MessageEmbedField{
				Name:   "Reported Match ID",
				Value:  matchID,
				Inline: true,
			},
			&discordgo.MessageEmbedField{
				Name:   "Reported Server",
				Value:  serverIP,
				Inline: true,
			},
			&discordgo.MessageEmbedField{
				Name:   "Reported Region",
				Value:  regionCode,
				Inline: true,
			},
		)
	}

	// Get server statistics
	serverStats := d.getServerStatsByHost(ctx, logger)
	if serverStats != "" {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Server Statistics",
			Value:  serverStats,
			Inline: false,
		})
	}

	// Post to audit/reports channels
	d.postServerIssueReport(ctx, logger, s, groupID, data, embed)

	// Update the original message to disable buttons and show confirmation
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{
				{
					Title:       "✅ Thank You for Reporting!",
					Description: "Your lag report has been submitted. This helps us identify and address server issues.",
					Color:       EmbedColorGreen,
					Fields: []*discordgo.MessageEmbedField{
						{
							Name:   "Server Reported",
							Value:  fmt.Sprintf("%s (%s)", serverIP, regionCode),
							Inline: false,
						},
					},
				},
			},
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						&discordgo.Button{
							Label:    "Lag Report Submitted",
							Style:    discordgo.SuccessButton,
							CustomID: "nil",
							Disabled: true,
							Emoji:    &discordgo.ComponentEmoji{Name: "✅"},
						},
					},
				},
			},
		},
	}); err != nil {
		return fmt.Errorf("failed to respond to interaction: %w", err)
	}

	return nil
}

// handleReportServerIssueOther handles the "Report Other Issue" button click by showing a modal
func (d *DiscordAppBot) handleReportServerIssueOther(_ context.Context, _ runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, serverContext string) error {
	// Show a modal for the user to describe the issue
	modal := &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID: fmt.Sprintf("server_issue_modal:other:%s", serverContext),
			Title:    "Report Server Issue",
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.TextInput{
							CustomID:    "issue_details",
							Label:       "Describe the issue",
							Style:       discordgo.TextInputParagraph,
							Placeholder: "Please describe the issue you're experiencing...",
							Required:    true,
							MinLength:   10,
							MaxLength:   IssueDetailsMaxLength,
						},
					},
				},
			},
		},
	}

	return s.InteractionRespond(i.Interaction, modal)
}

// postServerIssueReport posts the issue report to the appropriate channels
func (d *DiscordAppBot) postServerIssueReport(_ context.Context, logger runtime.Logger, s *discordgo.Session, groupID string, data *WhereAmIData, embed *discordgo.MessageEmbed) {
	gg := d.guildGroupRegistry.Get(groupID)
	if gg == nil {
		return
	}

	// Post to audit channel
	if gg.AuditChannelID != "" {
		if _, err := s.ChannelMessageSendEmbed(gg.AuditChannelID, embed); err != nil {
			logger.WithField("error", err).Warn("Failed to send server issue report to audit channel")
		}
	}

	// Post to server reports channel
	if gg.ServerReportsChannelID != "" {
		// Mention operator if available
		content := ""
		if data != nil && data.OperatorDiscord != "" {
			content = fmt.Sprintf("<@%s>", data.OperatorDiscord)
		}

		if _, err := s.ChannelMessageSendComplex(gg.ServerReportsChannelID, &discordgo.MessageSend{
			Content: content,
			Embed:   embed,
		}); err != nil {
			logger.WithField("error", err).Warn("Failed to send server issue report to server reports channel")
		}
	}
}

// handleServerIssueTypeSelection handles the select menu for issue type selection
func (d *DiscordAppBot) handleServerIssueTypeSelection(_ context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, matchID string) error {
	// Get the selected issue type
	data := i.MessageComponentData()
	if len(data.Values) == 0 {
		logger.Warn("Discord select menu submitted with no values. This should not happen. InteractionID: %s, UserID: %s", i.Interaction.ID, i.Interaction.Member.User.ID)
		return simpleInteractionResponse(s, i, "No issue type selected.")
	}
	issueType := data.Values[0]

	// Show the modal for issue details
	modal := &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID: fmt.Sprintf("server_issue_modal:%s:%s", issueType, matchID),
			Title:    "Report Server Issue",
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.TextInput{
							CustomID:    "issue_details",
							Label:       "Issue Details (optional)",
							Style:       discordgo.TextInputParagraph,
							Placeholder: "Please describe the issue in detail...",
							Required:    false,
							MinLength:   IssueDetailsMinLength,
							MaxLength:   IssueDetailsMaxLength,
						},
					},
				},
			},
		},
	}

	return s.InteractionRespond(i.Interaction, modal)
}

// handleServerIssueModalSubmit handles the modal submission for server issue reports
func (d *DiscordAppBot) handleServerIssueModalSubmit(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, issueType, serverContext string) error {
	user := getScopedUser(i)
	if user == nil {
		return fmt.Errorf("user is nil")
	}

	userID := d.cache.DiscordIDToUserID(user.ID)
	groupID := d.cache.GuildIDToGroupID(i.GuildID)

	// Parse server context: matchID:serverIP:regionCode
	var reportedMatchID, reportedServerIP, reportedRegionCode string
	contextParts := strings.SplitN(serverContext, ":", 3)
	if len(contextParts) >= 1 {
		reportedMatchID = contextParts[0]
	}
	if len(contextParts) >= 2 {
		reportedServerIP = contextParts[1]
	}
	if len(contextParts) >= 3 {
		reportedRegionCode = contextParts[2]
	}

	// Get modal data
	modalData := i.ModalSubmitData()

	var issueDetails string
	var issueTypeInput string

	for _, row := range modalData.Components {
		if actionRow, ok := row.(*discordgo.ActionsRow); ok {
			for _, comp := range actionRow.Components {
				switch c := comp.(type) {
				case *discordgo.TextInput:
					switch c.CustomID {
					case "issue_details":
						issueDetails = c.Value
					case "issue_type":
						issueTypeInput = c.Value
					}
				}
			}
		}
	}

	// Determine the issue type from input
	// If the issueType parameter is empty (new modal format), use the input from modal
	// If the issueType parameter is provided (legacy format), use it
	if issueType == "" && issueTypeInput != "" {
		// Parse the issue type from user input
		lowerInput := strings.ToLower(strings.TrimSpace(issueTypeInput))
		// Check for exact matches first (case-insensitive)
		switch lowerInput {
		case "lag", "stutter", "stuttering", "server_lag", "latency":
			issueType = ServerIssueTypeLag
		case "other":
			issueType = ServerIssueTypeOther
		default:
			// Fallback to contains-based matching for more flexible input
			if strings.Contains(lowerInput, "lag") || strings.Contains(lowerInput, "stutter") {
				issueType = ServerIssueTypeLag
			} else {
				issueType = ServerIssueTypeOther
			}
		}
	}

	// Get current match data
	data, err := d.getWhereAmIData(ctx, logger, userID, groupID)
	if err != nil {
		logger.WithField("error", err).Warn("Failed to get match data for server issue report")
	}

	// Create the report embed
	embed := d.createServerIssueReportEmbed(user, issueType, issueDetails, data)

	// If we don't have current match data but have server context from the button, add it
	if data == nil && (reportedServerIP != "" || reportedRegionCode != "") {
		if reportedMatchID != "" {
			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
				Name:   "Reported Match ID",
				Value:  reportedMatchID,
				Inline: true,
			})
		}
		if reportedServerIP != "" {
			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
				Name:   "Reported Server",
				Value:  reportedServerIP,
				Inline: true,
			})
		}
		if reportedRegionCode != "" {
			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
				Name:   "Reported Region",
				Value:  reportedRegionCode,
				Inline: true,
			})
		}
	}

	/*
		// Get server statistics (active/idle servers by host)
		serverStats := d.getServerStatsByHost(ctx, logger)
		if serverStats != "" {
			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
				Name:   "Server Statistics",
				Value:  serverStats,
				Inline: false,
			})
		}
	*/

	// Post to audit/reports channels
	d.postServerIssueReport(ctx, logger, s, groupID, data, embed)

	// Respond to the user
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags:   discordgo.MessageFlagsEphemeral,
			Content: "Your server issue report has been submitted. Thank you for helping improve the game experience!",
		},
	})
}

// createServerIssueReportEmbed creates an embed for a server issue report
func (d *DiscordAppBot) createServerIssueReportEmbed(user *discordgo.User, issueType, issueDetails string, data *WhereAmIData) *discordgo.MessageEmbed {
	issueTypeLabel := "Other"
	if issueType == ServerIssueTypeLag {
		issueTypeLabel = "Server Lag/Stuttering"
	}

	embed := &discordgo.MessageEmbed{
		Title:       "Server Issue Report",
		Description: fmt.Sprintf("Reported by <@%s>", user.ID),
		Color:       EmbedColorOrange,
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:   "Issue Type",
				Value:  issueTypeLabel,
				Inline: true,
			},
		},
	}

	// Add details field only if provided
	if issueDetails != "" {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Details",
			Value:  issueDetails,
			Inline: false,
		})
	}

	// Add match information if available
	if data != nil {
		if data.ServerHostIP != "" {
			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
				Name:   "Server Host",
				Value:  data.ServerHostIP,
				Inline: true,
			})
		}

		if data.RegionCode != "" {
			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
				Name:   "Region",
				Value:  data.RegionCode,
				Inline: true,
			})
		}

		if data.GuildName != "" {
			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
				Name:   "Guild",
				Value:  EscapeDiscordMarkdown(data.GuildName),
				Inline: true,
			})
		}

		if data.MatchMode != "" {
			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
				Name:   "Match Mode",
				Value:  data.MatchMode,
				Inline: true,
			})
		}

		if data.EchoTaxiLink != "" {
			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
				Name:   "Spark Link",
				Value:  fmt.Sprintf("[Match Link](%s)", data.EchoTaxiLink),
				Inline: true,
			})
		}

		if data.OperatorDiscord != "" {
			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
				Name:   "Server Operator",
				Value:  fmt.Sprintf("<@%s>", data.OperatorDiscord),
				Inline: true,
			})
		}

		// Players in match
		if len(data.Players) > 0 {
			playerList := formatPlayerList(data.Players)
			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
				Name:   fmt.Sprintf("Players (%d)", len(data.Players)),
				Value:  strings.Join(playerList, ", "),
				Inline: false,
			})
		}
	}

	return embed
}

// getServerStatsByHost returns a summary of active and idle servers by host
func (d *DiscordAppBot) getServerStatsByHost(ctx context.Context, logger runtime.Logger) string {
	// Get all matches
	matches, err := d.nk.MatchList(ctx, MaxMatchListSize, true, "", nil, nil, "")
	if err != nil {
		logger.WithField("error", err).Warn("Failed to get match list for server stats")
		return ""
	}

	// Count servers by host IP
	// Active: matches with players (Size >= MinActiveMatchSize)
	// Idle: matches with only the game server connected
	type hostStats struct {
		Active int
		Idle   int
	}
	statsByHost := make(map[string]*hostStats)

	for _, match := range matches {
		label := &MatchLabel{}
		if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), label); err != nil {
			continue
		}

		if label.GameServer == nil {
			continue
		}

		hostIP := label.GameServer.Endpoint.ExternalIP.String()
		if _, ok := statsByHost[hostIP]; !ok {
			statsByHost[hostIP] = &hostStats{}
		}

		if label.Size >= MinActiveMatchSize {
			statsByHost[hostIP].Active++
		} else {
			statsByHost[hostIP].Idle++
		}
	}

	if len(statsByHost) == 0 {
		return ""
	}

	// Build summary string
	var sb strings.Builder
	for host, stats := range statsByHost {
		sb.WriteString(fmt.Sprintf("`%s`: %d active, %d idle\n", host, stats.Active, stats.Idle))
	}

	return strings.TrimRight(sb.String(), "\n")
}

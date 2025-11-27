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
func (d *DiscordAppBot) getWhereAmIData(ctx context.Context, logger runtime.Logger, userID, groupID string) (*WhereAmIData, error) {
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
		playerList := make([]string, 0, len(data.Players))
		for _, p := range data.Players {
			if p.DiscordID != "" {
				playerList = append(playerList, fmt.Sprintf("<@%s>", p.DiscordID))
			} else {
				playerList = append(playerList, fmt.Sprintf("`%s`", EscapeDiscordMarkdown(p.DisplayName)))
			}
		}
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
	IssueDetailsMinLength = 10
	IssueDetailsMaxLength = 1000
)

// Server statistics constants
const (
	// MinActiveMatchSize is the minimum number of players (including the game server)
	// for a match to be considered "active" vs "idle"
	MinActiveMatchSize = 2
	// MaxMatchListSize is the maximum number of matches to retrieve for server statistics
	MaxMatchListSize = 1000
)

// handleReportServerIssue handles the "Report Server Issue" context menu command
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

	// Show a message with select menu options first
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags:   discordgo.MessageFlagsEphemeral,
			Content: "Select the type of server issue you're experiencing:",
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.SelectMenu{
							CustomID:    fmt.Sprintf("server_issue_type:%s", data.MatchID),
							Placeholder: "Select issue type",
							Options: []discordgo.SelectMenuOption{
								{
									Label:       "Server Lag/Stuttering",
									Value:       ServerIssueTypeLag,
									Description: "The server is experiencing lag or stuttering",
								},
								{
									Label:       "Other",
									Value:       ServerIssueTypeOther,
									Description: "Other server-related issue",
								},
							},
						},
					},
				},
			},
		},
	})
}

// handleServerIssueTypeSelection handles the select menu for issue type selection
func (d *DiscordAppBot) handleServerIssueTypeSelection(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, matchID string) error {
	// Get the selected issue type
	data := i.MessageComponentData()
	if len(data.Values) == 0 {
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
							Label:       "Issue Details",
							Style:       discordgo.TextInputParagraph,
							Placeholder: "Please describe the issue in detail...",
							Required:    true,
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
func (d *DiscordAppBot) handleServerIssueModalSubmit(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, issueType, matchID string) error {
	user := getScopedUser(i)
	if user == nil {
		return fmt.Errorf("user is nil")
	}

	userID := d.cache.DiscordIDToUserID(user.ID)
	groupID := d.cache.GuildIDToGroupID(i.GuildID)

	// Get modal data
	modalData := i.ModalSubmitData()

	var issueDetails string

	for _, row := range modalData.Components {
		if actionRow, ok := row.(*discordgo.ActionsRow); ok {
			for _, comp := range actionRow.Components {
				switch c := comp.(type) {
				case *discordgo.TextInput:
					if c.CustomID == "issue_details" {
						issueDetails = c.Value
					}
				}
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

	// Get server statistics (active/idle servers by host)
	serverStats := d.getServerStatsByHost(ctx, logger)
	if serverStats != "" {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Server Statistics",
			Value:  serverStats,
			Inline: false,
		})
	}

	// Post to audit log
	gg := d.guildGroupRegistry.Get(groupID)
	if gg != nil {
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
			{
				Name:   "Details",
				Value:  issueDetails,
				Inline: false,
			},
		},
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
			playerList := make([]string, 0, len(data.Players))
			for _, p := range data.Players {
				if p.DiscordID != "" {
					playerList = append(playerList, fmt.Sprintf("<@%s>", p.DiscordID))
				} else {
					playerList = append(playerList, fmt.Sprintf("`%s`", EscapeDiscordMarkdown(p.DisplayName)))
				}
			}
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

	return sb.String()
}

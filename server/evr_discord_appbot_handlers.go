package server

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (d *DiscordAppBot) handleInteractionApplicationCommand(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, commandName string, commandFn DiscordCommandHandlerFn) error {
	ctx := d.ctx

	user, member := getScopedUserMember(i)

	if user == nil {
		return fmt.Errorf("user is nil")
	}

	userID := d.cache.DiscordIDToUserID(user.ID)
	groupID := d.cache.GuildIDToGroupID(i.GuildID)

	// Log the interaction
	if cID := ServiceSettings().CommandLogChannelID; cID != "" {
		data := i.ApplicationCommandData()
		if guild, err := d.dg.State.Guild(i.GuildID); err != nil {
			logger.WithField("error", err).Warn("Failed to get guild")
		} else if member, err := d.dg.GuildMember(i.GuildID, user.ID); err != nil {
			logger.WithField("error", err).Warn("Failed to get guild member")
		} else {
			signature := d.interactionToSignature(data.Name, data.Options)
			displayName := member.DisplayName()
			if displayName == "" {
				displayName = member.User.Username
			}

			content := fmt.Sprintf("<@%s> (%s) used %s in `%s`", member.User.ID, displayName, signature, guild.Name)

			if _, err := d.dg.ChannelMessageSendComplex(cID, &discordgo.MessageSend{
				Content:         content,
				AllowedMentions: &discordgo.MessageAllowedMentions{},
			}); err != nil {
				logger.WithField("error", err).Warn("Failed to log interaction to channel")
			}
		}
	}

	switch commandName {
	case "link", "link-headset":

	case "unlink", "unlink-headset":

		account, err := d.nk.AccountGetId(ctx, userID)
		if err != nil {
			return fmt.Errorf("failed to get account: %w", err)
		}

		if account.GetDisableTime() != nil {
			return simpleInteractionResponse(s, i, "This account has been disabled.")
		}

	default:
		if userID == "" {
			return simpleInteractionResponse(s, i, "a headset must be linked to this Discord account to use slash commands")
		}
	}

	if groupID == "" {
		return simpleInteractionResponse(s, i, "This command can only be used in a guild.")
	}

	// Global security check
	switch commandName {

	case "create":

		gg := d.guildGroupRegistry.Get(groupID)
		if gg == nil {
			return simpleInteractionResponse(s, i, "This guild is not registered.")
		}

		if err := d.LogInteractionToChannel(i, gg.AuditChannelID); err != nil {
			logger.Warn("Failed to log interaction to channel")
		}

		if gg.DisableCreateCommand {
			return simpleInteractionResponse(s, i, "This guild does not allow public allocation.")
		}

	case "allocate":
		gg := d.guildGroupRegistry.Get(groupID)
		if gg == nil {
			return simpleInteractionResponse(s, i, "This guild is not registered.")
		}

		if err := d.LogInteractionToChannel(i, gg.AuditChannelID); err != nil {
			logger.Warn("Failed to log interaction to channel")
		}

		if !gg.IsAllocator(userID) {
			return simpleInteractionResponse(s, i, "You must be a guild allocator to use this command.")
		}

	case "join-player", "igp", "ign", "shutdown-match":

		gg := d.guildGroupRegistry.Get(groupID)
		if gg == nil {
			return simpleInteractionResponse(s, i, "This guild is not registered.")
		}

		if err := d.LogInteractionToChannel(i, gg.AuditChannelID); err != nil {
			logger.Warn("Failed to log interaction to channel")
		}

		if !gg.IsEnforcer(userID) {
			return simpleInteractionResponse(s, i, "You must be a guild enforcer to use this command.")
		}

	case "set-command-channel", "generate-button":

		gg := d.guildGroupRegistry.Get(groupID)
		if gg == nil {
			return simpleInteractionResponse(s, i, "This guild is not registered.")
		}

		if err := d.LogInteractionToChannel(i, gg.AuditChannelID); err != nil {
			logger.Warn("Failed to log interaction to channel")
		}

		if !gg.IsAuditor(userID) {
			return simpleInteractionResponse(s, i, "You must be a guild auditor to use this command.")
		}
	}
	return commandFn(logger, s, i, user, member, userID, groupID)
}

func (d *DiscordAppBot) handleInteractionMessageComponent(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, commandName, value string) error {
	ctx := d.ctx
	nk := d.nk
	user, member := getScopedUserMember(i)

	if user == nil {
		return fmt.Errorf("user is nil")
	}
	_, _ = user, member

	userID := d.cache.DiscordIDToUserID(user.ID)
	groupID := d.cache.GuildIDToGroupID(i.GuildID)

	_ = groupID

	switch commandName {
	case "approve_ip":

		history := &LoginHistory{}
		if err := StorageRead(ctx, nk, userID, history, true); err != nil {
			return fmt.Errorf("failed to load login history: %w", err)
		}

		if value != "" {
			ip := net.ParseIP(value)
			if ip == nil {
				return simpleInteractionResponse(s, i, "Invalid IP address.")
			}
		} else {
			// it's an option
			data := i.Interaction.MessageComponentData()
			if len(data.Values) == 0 {
				return simpleInteractionResponse(s, i, "Invalid device ID.")
			}

			if len(data.Values) != 1 {
				return simpleInteractionResponse(s, i, "Invalid code.")
			}

			strs := strings.SplitN(data.Values[0], ":", 2)
			if len(strs) != 2 {
				return simpleInteractionResponse(s, i, "Invalid code.")
			}

			if err := history.AuthorizeIPWithCode(strs[0], strs[1]); err != nil {

				// Store the history
				if _, err := StorageWrite(ctx, nk, userID, history); err != nil {
					return fmt.Errorf("failed to save login history: %w", err)
				}

				if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseDeferredMessageUpdate,
				}); err != nil {
					return fmt.Errorf("failed to respond to interaction: %w", err)
				}

				if _, err := s.ChannelMessageEditComplex(&discordgo.MessageEdit{
					Channel: i.Message.ChannelID,
					ID:      i.Message.ID,
					Components: &[]discordgo.MessageComponent{
						discordgo.ActionsRow{
							Components: []discordgo.MessageComponent{
								discordgo.Button{
									Label:    "Incorrect Code",
									Style:    discordgo.DangerButton,
									CustomID: "nil",
									Disabled: true,
								},
							},
						},
					},
				}); err != nil {
					return fmt.Errorf("failed to edit message: %w", err)
				}

			}
		}

		if _, err := StorageWrite(ctx, nk, userID, history); err != nil {
			return fmt.Errorf("failed to save login history: %w", err)
		}

		if i.Message == nil {
			return fmt.Errorf("message is nil")
		}

		if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Flags:   discordgo.MessageFlagsEphemeral,
				Content: ":white_check_mark: IP address approved.\n\n## **Restart your game.**",
			},
		}); err != nil {
			return fmt.Errorf("failed to respond to interaction: %w", err)
		}

		if _, err := s.ChannelMessageEditComplex(&discordgo.MessageEdit{
			Channel: i.Message.ChannelID,
			ID:      i.Message.ID,
			Components: &[]discordgo.MessageComponent{
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.Button{
							Label:    "Approved",
							Style:    discordgo.SuccessButton,
							CustomID: "nil",
							Disabled: true,
						},
					},
				},
			},
		}); err != nil {
			return fmt.Errorf("failed to edit message: %w", err)
		}

		return nil
	case "link-headset-modal":

		modal := &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseModal,
			Data: &discordgo.InteractionResponseData{
				CustomID: "linkcode_modal",
				Title:    "Link EchoVRCE",
				Content:  "Enter your 4-letter link code.",
				Components: []discordgo.MessageComponent{
					discordgo.ActionsRow{
						Components: []discordgo.MessageComponent{
							discordgo.TextInput{
								CustomID:    "linkcode_input",
								Label:       "Link Code",
								Placeholder: "Enter your 4-letter link code",
								Style:       discordgo.TextInputShort,
								MinLength:   4,
								MaxLength:   4,
								Required:    true,
							},
						},
					},
				},
			},
		}
		s.InteractionRespond(i.Interaction, modal)
		return nil

	case "unlink", "unlink-headset":
		data := i.Interaction.MessageComponentData()
		if len(data.Values) == 0 {
			return simpleInteractionResponse(s, i, "Invalid device ID.")
		}
		value = data.Values[0]
		if value == "" {
			return simpleInteractionResponse(s, i, "Invalid device ID.")
		}

		if err := nk.UnlinkDevice(ctx, userID, value); err != nil {
			return fmt.Errorf("failed to unlink device ID: %w", err)
		}

		if err := d.cache.updateLinkStatus(ctx, i.Member.User.ID); err != nil {
			return fmt.Errorf("failed to update link status: %w", err)
		}

		// Modify the interaction response
		if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Flags:   discordgo.MessageFlagsEphemeral,
				Content: fmt.Sprintf("Unlinked device ID `%s`.", value),
			},
		}); err != nil {
			return fmt.Errorf("failed to respond to interaction: %w", err)
		}
	}

	return nil
}

func (d *DiscordAppBot) handleAllocateMatch(ctx context.Context, logger runtime.Logger, userID, guildID string, regionCode string, mode, level evr.Symbol, startTime time.Time) (l *MatchLabel, rtt float64, err error) {

	// Find a parking match to prepare

	groupID := d.cache.GuildIDToGroupID(guildID)

	// Get a list of the groups that this user has allocate access to
	guildGroups, err := GuildUserGroupsList(ctx, d.nk, d.guildGroupRegistry, userID)
	if err != nil {
		return nil, 0, status.Errorf(codes.Internal, "failed to get guild group memberships: %v", err)
	}

	gg, ok := guildGroups[groupID]
	if !ok {
		return nil, 0, status.Error(codes.PermissionDenied, "user is not a member of the guild")
	}
	allocatorGroupIDs := make([]string, 0, len(guildGroups))
	for gid, g := range guildGroups {
		if g.IsAllocator(userID) {
			allocatorGroupIDs = append(allocatorGroupIDs, gid)
		}
	}

	if !gg.IsAllocator(userID) {
		return nil, 0, status.Error(codes.PermissionDenied, "user does not have the allocator role in this guild.")
	}

	// Load the latency history for this user
	latencyHistory := NewLatencyHistory()
	if err := StorageRead(ctx, d.nk, userID, latencyHistory, false); err != nil && status.Code(err) != codes.NotFound {
		return nil, 0, status.Errorf(codes.Internal, "failed to read latency history: %v", err)
	}

	latestRTTs := latencyHistory.LatestRTTs()

	// Prepare the session for the match.
	settings := &MatchSettings{
		Mode:      mode,
		Level:     level,
		GroupID:   uuid.FromStringOrNil(groupID),
		StartTime: startTime.UTC().Add(10 * time.Minute),
		SpawnedBy: userID,
	}
	queryAddon := ServiceSettings().Matchmaking.QueryAddons.Create
	label, err := LobbyGameServerAllocate(ctx, logger, d.nk, allocatorGroupIDs, latestRTTs, settings, []string{regionCode}, false, true, queryAddon)
	if err != nil {
		if strings.Contains("bad request:", err.Error()) {
			err = NewLobbyErrorf(BadRequest, "required features not supported")
		}
		logger.Warn("Failed to allocate game server", zap.Error(err), zap.Any("settings", settings))
		return nil, 0, fmt.Errorf("failed to allocate game server: %w", err)
	}

	return label, rtt, nil
}

func (d *DiscordAppBot) handleCreateMatch(ctx context.Context, logger runtime.Logger, userID, guildID, region string, mode, level evr.Symbol, startTime time.Time) (l *MatchLabel, latencyMillis int, err error) {

	// Find a parking match to prepare
	groupID := d.cache.GuildIDToGroupID(guildID)

	guildGroups, err := GuildUserGroupsList(ctx, d.nk, d.guildGroupRegistry, userID)
	if err != nil {
		return nil, 0, status.Errorf(codes.Internal, "failed to get guild groups: %v", err)
	}

	group, ok := guildGroups[groupID]
	if !ok {
		return nil, 0, status.Error(codes.PermissionDenied, "user is not a member of the guild")
	}

	if group.IsSuspended(userID, nil) {
		return nil, 0, status.Error(codes.PermissionDenied, "user is suspended from the guild")
	}

	if group.DisableCreateCommand && !group.IsAllocator(userID) {
		return nil, 0, status.Error(codes.PermissionDenied, "guild does not allow public match creation")
	}

	limiter := d.loadPrepareMatchRateLimiter(userID, groupID)
	if !limiter.Allow() {
		return nil, 0, status.Error(codes.ResourceExhausted, fmt.Sprintf("rate limit exceeded (%0.0f requests per minute)", limiter.Limit()*60))
	}

	latencyHistory := NewLatencyHistory()
	if err := StorageRead(ctx, d.nk, userID, latencyHistory, false); err != nil && status.Code(err) != codes.NotFound {
		return nil, 0, status.Errorf(codes.Internal, "failed to read latency history: %v", err)
	}
	extIPs := latencyHistory.AverageRTTs(true)

	settings := &MatchSettings{
		Mode:      mode,
		Level:     level,
		GroupID:   uuid.FromStringOrNil(groupID),
		StartTime: startTime.UTC().Add(1 * time.Minute),
		SpawnedBy: userID,
	}

	queryAddon := ServiceSettings().Matchmaking.QueryAddons.Create
	label, err := LobbyGameServerAllocate(ctx, logger, d.nk, []string{groupID}, extIPs, settings, []string{region}, true, false, queryAddon)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to allocate game server: %w", err)
	}

	if label == nil {
		return nil, 0, fmt.Errorf("failed to allocate game server: label is nil")
	}

	latencyMillis = latencyHistory.AverageRTT(label.GameServer.Endpoint.ExternalIP.String(), true)

	return label, latencyMillis, nil
}

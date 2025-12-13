package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
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

func (d *DiscordAppBot) handleInteractionApplicationCommand(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, commandName string, commandFn DiscordCommandHandlerFn) error {
	user, member := getScopedUserMember(i)

	if user == nil {
		return fmt.Errorf("user is nil")
	}

	userID := d.cache.DiscordIDToUserID(user.ID)
	groupID := d.cache.GuildIDToGroupID(i.GuildID)

	isGlobalOperator := false
	var err error
	if userID != "" {
		isGlobalOperator, err = CheckSystemGroupMembership(ctx, d.db, userID, GroupGlobalOperators)
		if err != nil {
			return fmt.Errorf("error checking global operator status: %w", err)
		}
	}
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

			go func() {
				if _, err := d.dg.ChannelMessageSendComplex(cID, &discordgo.MessageSend{
					Content:         content,
					AllowedMentions: &discordgo.MessageAllowedMentions{},
				}); err != nil {
					logger.WithField("error", err).Warn("Failed to log interaction to channel")
				}
			}()
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

		if !isGlobalOperator && !gg.IsAllocator(userID) {
			return simpleInteractionResponse(s, i, "You must be a guild allocator to use this command.")
		}
	case "kick-player":
		gg := d.guildGroupRegistry.Get(groupID)
		if gg == nil {
			return simpleInteractionResponse(s, i, "This guild is not registered.")
		}

		if err := d.LogInteractionToChannel(i, gg.AuditChannelID); err != nil {
			logger.Warn("Failed to log interaction to channel")
		}

	case "join-player", "igp", "ign", "shutdown-match":

		gg := d.guildGroupRegistry.Get(groupID)
		if gg == nil {
			return simpleInteractionResponse(s, i, "This guild is not registered.")
		}

		if err := d.LogInteractionToChannel(i, gg.AuditChannelID); err != nil {
			logger.Warn("Failed to log interaction to channel")
		}

		if !isGlobalOperator && !gg.IsEnforcer(userID) {
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

		if !isGlobalOperator && !gg.IsAuditor(userID) {
			return simpleInteractionResponse(s, i, "You must be a guild auditor to use this command.")
		}
	}
	return commandFn(ctx, logger, s, i, user, member, userID, groupID)
}

func (d *DiscordAppBot) handleInteractionMessageComponent(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, commandName, value string) error {
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
		if err := StorableRead(ctx, nk, userID, history, true); err != nil {
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
				if err := StorableWrite(ctx, nk, userID, history); err != nil {
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
				return nil
			}
		}

		if err := StorableWrite(ctx, nk, userID, history); err != nil {
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
	case "igp":

		return d.handleInGamePanelInteraction(i, value)

	case "lookup":
		// Handle lookup button interactions (e.g., "set_ign_override")
		action, params, _ := strings.Cut(value, ":")
		switch action {
		case "set_ign_override":
			// Parse targetUserID:groupID
			parts := strings.SplitN(params, ":", 2)
			if len(parts) != 2 {
				return fmt.Errorf("invalid parameters for set_ign_override")
			}
			targetUserID := parts[0]
			targetGroupID := parts[1]

			// Verify caller has permissions
			callerGuildGroups, err := GuildUserGroupsList(ctx, d.nk, d.guildGroupRegistry, userID)
			if err != nil {
				return fmt.Errorf("failed to get guild groups: %w", err)
			}

			isAuditorOrEnforcer := false
			if gg, ok := callerGuildGroups[targetGroupID]; ok && (gg.IsAuditor(userID) || gg.IsEnforcer(userID)) {
				isAuditorOrEnforcer = true
			}
			isGlobalOperator, _ := CheckSystemGroupMembership(ctx, d.db, userID, GroupGlobalOperators)
			isAuditorOrEnforcer = isAuditorOrEnforcer || isGlobalOperator

			if !isAuditorOrEnforcer {
				return simpleInteractionResponse(s, i, "You do not have permission to set IGN overrides.")
			}

			// Load target profile
			targetProfile, err := EVRProfileLoad(ctx, nk, targetUserID)
			if err != nil {
				return fmt.Errorf("failed to load target profile: %w", err)
			}

			// Get current IGN data
			groupIGN := targetProfile.GetGroupIGNData(targetGroupID)
			currentDisplayName := groupIGN.DisplayName
			if currentDisplayName == "" {
				// Fallback to username
				if a, err := nk.AccountGetId(ctx, targetUserID); err == nil {
					currentDisplayName = a.User.Username
				}
			}

			// Show modal with prepopulated data
			modal := d.createLookupSetIGNModal(currentDisplayName, groupIGN.IsLocked)
			// Store context in customID for modal submission
			modal.Data.CustomID = fmt.Sprintf("lookup:set_ign_modal:%s:%s", targetUserID, targetGroupID)
			return s.InteractionRespond(i.Interaction, modal)

		default:
			return fmt.Errorf("unknown lookup action: %s", action)
		}
	case "server_issue_type":
		// Handle server issue type selection
		return d.handleServerIssueTypeSelection(ctx, logger, s, i, value)

	case "report_server_issue":
		// Handle report server issue button clicks
		// value format: <issue_type>:<matchID>:<serverIP>:<regionCode>
		parts := strings.SplitN(value, ":", 2)
		if len(parts) < 2 {
			return fmt.Errorf("invalid report_server_issue format")
		}
		issueType := parts[0]
		serverContext := parts[1]

		switch issueType {
		case "lag":
			return d.handleReportServerIssueLag(ctx, logger, s, i, serverContext)
		case "other":
			return d.handleReportServerIssueOther(ctx, logger, s, i, serverContext)
		default:
			return fmt.Errorf("unknown issue type: %s", issueType)
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

	isGlobalOperator := false
	isGlobalOperator, err = CheckSystemGroupMembership(ctx, d.db, userID, GroupGlobalOperators)
	if err != nil {
		return nil, 0, status.Errorf(codes.Internal, "error checking global operator status: %v", err)
	}

	if !gg.IsAllocator(userID) && !isGlobalOperator {
		return nil, 0, status.Error(codes.PermissionDenied, "user does not have the allocator role in this guild.")
	}

	// Load the latency history for this user
	latencyHistory := NewLatencyHistory()
	if err := StorableRead(ctx, d.nk, userID, latencyHistory, false); err != nil && status.Code(err) != codes.NotFound {
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
	queryAddon := ServiceSettings().Matchmaking.QueryAddons.Allocate
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
	if err := StorableRead(ctx, d.nk, userID, latencyHistory, false); err != nil && status.Code(err) != codes.NotFound {
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

func (d *DiscordAppBot) kickPlayer(logger runtime.Logger, i *discordgo.InteractionCreate, caller *discordgo.Member, target *discordgo.User, duration, userNotice, notes string, requireCommunityValues bool, allowPrivateLobbies bool) error {
	var (
		ctx                = d.ctx
		nk                 = d.nk
		db                 = d.db
		suspensionExpiry   time.Time
		suspensionDuration time.Duration
	)

	callerUserID := d.cache.DiscordIDToUserID(caller.User.ID)
	if callerUserID == "" {
		return errors.New("failed to get target user ID")
	}
	targetUserID := d.cache.DiscordIDToUserID(target.ID)
	if targetUserID == "" {
		return errors.New("failed to get target user ID")
	}
	groupID := d.cache.GuildIDToGroupID(i.GuildID)
	if groupID == "" {
		return errors.New("failed to get group ID")
	}

	// Parse minutes, hours, days, and weeks (m, h, d, w)
	if duration != "" {
		var unit time.Duration
		if duration == "0" {
			suspensionExpiry = time.Now()
		} else {
			switch duration[len(duration)-1] {
			case 'm':
				unit = time.Minute
			case 'h':
				unit = time.Hour
			case 'd':
				unit = 24 * time.Hour
			case 'w':
				unit = 7 * 24 * time.Hour
			default:
				duration += "m"
				unit = time.Minute
			}
			if duration, err := strconv.Atoi(duration[:len(duration)-1]); err == nil {
				suspensionDuration = time.Duration(duration) * unit
				suspensionExpiry = time.Now().Add(time.Duration(duration) * unit)
			} else {
				helpMessage := fmt.Sprintf("Duration parse error (`%s`).\n\n**Use a number followed by m, h, d, or w (e.g., 30m, 1h, 2d, 1w)**", err.Error())
				if i != nil {
					return simpleInteractionResponse(d.dg, i, helpMessage)
				}
				return errors.New(helpMessage) // Return an error if the duration is invalid
			}
		}
	}
	presences, err := d.nk.StreamUserList(StreamModeService, targetUserID, "", StreamLabelMatchService, false, true)
	if err != nil {
		return err
	}

	var (
		cnt                   = 0
		timeoutMessage        string
		actions               = make([]string, 0, len(presences))
		doDisconnect          = false
		isEnforcer            = false
		kickPlayer            = false
		voidActiveSuspensions = !suspensionExpiry.IsZero() && time.Now().After(suspensionExpiry)
		addSuspension         = !suspensionExpiry.IsZero() && time.Now().Before(suspensionExpiry)
		recordsByGroupID      = make(map[string][]GuildEnforcementRecord, 1)
		voids                 = make(map[string]GuildEnforcementRecordVoid, 0)
	)

	gg, err := GuildGroupLoad(ctx, nk, groupID)
	if err != nil {
		return errors.New("failed to load guild group")
	} else if gg.IsEnforcer(callerUserID) {
		isEnforcer = true
	}

	isGlobalOperator, err := CheckSystemGroupMembership(ctx, db, callerUserID, GroupGlobalOperators)
	if err != nil {
		return fmt.Errorf("error checking global operator status: %w", err)
	}

	if isEnforcer || isGlobalOperator {
		// Kick the player if this is not a voiding action
		if !voidActiveSuspensions {
			kickPlayer = true
		}

		journal := NewGuildEnforcementJournal(targetUserID)
		if err := StorableRead(ctx, nk, targetUserID, journal, false); err != nil && status.Code(err) != codes.NotFound {
			return fmt.Errorf("failed to read storage: %w", err)
		}

		if addSuspension {
			// Add a new record
			actions = append(actions, fmt.Sprintf("suspension expires <t:%d:R>", suspensionExpiry.UTC().Unix()))
			record := journal.AddRecord(groupID, callerUserID, caller.User.ID, userNotice, notes, requireCommunityValues, allowPrivateLobbies, suspensionDuration)
			recordsByGroupID[groupID] = append(recordsByGroupID[groupID], record)

			// Send DM notification to the user
			guildName := ""
			if gg != nil {
				guildName = gg.Name()
			}
			sent, err := SendEnforcementNotification(ctx, d.dg, record, target.ID, guildName)
			if err != nil {
				// Note: DM failures are logged but don't block enforcement.
				// Common failures: user has DMs disabled, blocked bot, or left shared servers.
				// The enforcement action still applies, and users can check /whoami for details.
				// Failed attempts are tracked in metrics and notification status for review.
				logger.Warn("Failed to send enforcement notification DM", zap.Error(err), zap.String("user_id", target.ID))
			}
			// Update the record with notification status (tracks both success and failure)
			if updateErr := journal.UpdateRecordNotificationStatus(groupID, record.ID, sent); updateErr != nil {
				logger.Warn("Failed to update notification status", zap.Error(updateErr))
			}
			// Save the updated journal with notification status
			if err := StorableWrite(ctx, nk, targetUserID, journal); err != nil {
				logger.Warn("Failed to save journal after notification update", zap.Error(err))
			}

			// Record to public enforcement log if enabled
			if err := RecordPublicEnforcement(ctx, nk, record, false, time.Time{}); err != nil {
				logger.Warn("Failed to record public enforcement", zap.Error(err))
			}

			// Record metrics for this enforcement action
			if err := RecordEnforcementMetrics(ctx, nk, record, sent); err != nil {
				logger.Warn("Failed to record enforcement metrics", zap.Error(err))
			}

		} else if voidActiveSuspensions {

			currentGroupID := groupID

			groupIDs := []string{currentGroupID}
			// Add inherited groups
			if gg.SuspensionInheritanceGroupIDs != nil {
				groupIDs = append(groupIDs, gg.SuspensionInheritanceGroupIDs...)
			}

			// Void any active suspensions for this group and any inherited groups
			for _, gID := range groupIDs {
				if recordsByGroupID[gID] == nil {
					recordsByGroupID[gID] = make([]GuildEnforcementRecord, 0)
				}

				for _, record := range journal.GroupRecords(gID) {
					// Ignore expired or already-voided records
					if record.IsExpired() || journal.IsVoid(gID, record.ID) || journal.IsVoid(currentGroupID, record.ID) {
						continue
					}
					actions = append(actions, fmt.Sprintf("suspension voided:\n  <t:%d:R> by <@%s> (expires <t:%d:R>): %s", record.CreatedAt.Unix(), record.EnforcerDiscordID, record.Expiry.Unix(), record.UserNoticeText))

					recordsByGroupID[currentGroupID] = append(recordsByGroupID[currentGroupID], record)

					details := userNotice
					if notes != "" {
						details += "\n" + notes
					}
					void := journal.VoidRecord(currentGroupID, record.ID, callerUserID, caller.User.ID, details)
					voids[void.RecordID] = void

					// Record voiding to public enforcement log if enabled
					if err := RecordPublicEnforcement(ctx, nk, record, true, void.VoidedAt); err != nil {
						logger.Warn("Failed to record voided enforcement to public log", zap.Error(err))
					}

					// Record voiding metrics
					if err := RecordVoidingMetrics(ctx, nk, currentGroupID, targetUserID); err != nil {
						logger.Warn("Failed to record voiding metrics", zap.Error(err))
					}
				}
			}
		}

		if err := StorableWrite(ctx, nk, targetUserID, journal); err != nil {
			return fmt.Errorf("failed to write storage: %w", err)
		}

		if gg.EnforcementNoticeChannelID != "" {
			profile, err := EVRProfileLoad(ctx, nk, targetUserID)
			if err != nil {
				return fmt.Errorf("failed to load account metadata: %w", err)
			}

			title := "Kicked Player"
			if len(voids) > 0 {
				title = "Voided Suspension(s)"
			} else if len(recordsByGroupID) > 0 {
				title = fmt.Sprintf("Suspension: *%s*", Query.QuoteStringValue(profile.GetGroupIGN(groupID)))
			}
			targetDN := profile.GetGroupIGN(groupID)
			targetDN = EscapeDiscordMarkdown(targetDN)
			callerDN := caller.DisplayName()
			if callerDN == "" {
				callerDN = caller.User.Username
			}
			embed := &discordgo.MessageEmbed{
				Author: &discordgo.MessageEmbedAuthor{
					Name:    fmt.Sprintf("%s (%s)", callerDN, caller.User.Username),
					IconURL: caller.AvatarURL(""),
				},
				Title: title,
				Color: 0x9656ce,
				Fields: []*discordgo.MessageEmbedField{
					{
						Name:   "Target User",
						Value:  fmt.Sprintf("%s (<@!%s>)", targetDN, target.ID),
						Inline: false,
					},
				},
				Footer: &discordgo.MessageEmbedFooter{
					Text: "Confidential. Do not share.",
				},
			}
			if len(recordsByGroupID) == 0 {
				// this is just a kick.
				parts := []string{
					fmt.Sprintf("<t:%d:R> by <@!%s>:", time.Now().UTC().Unix(), caller.User.ID),
					fmt.Sprintf("- `%s`", userNotice),
				}
				if notes != "" {
					parts = append(parts,
						fmt.Sprintf("- *%s*", notes),
					)
				}
				embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
					Name:   "Kick Details",
					Value:  strings.Join(parts, "\n"),
					Inline: true,
				})
			}

			for gID, records := range recordsByGroupID {
				if len(records) == 0 {
					continue
				}
				// Get the group name
				gn := gID
				if gg := d.guildGroupRegistry.Get(gID); gg != nil {
					gn = gg.Name()
				}

				// Create a field for each group
				// Always show enforcer ID in enforcement notice channel (it's for moderators only)
				field := createSuspensionDetailsEmbedField(gn, records, voids, true, true, true, gID)
				embed.Fields = append(embed.Fields, field)
			}
			_, err = d.dg.ChannelMessageSendComplex(gg.EnforcementNoticeChannelID, &discordgo.MessageSend{
				Embed:           embed,
				AllowedMentions: &discordgo.MessageAllowedMentions{},
			})
			if err != nil {
				logger.WithFields(map[string]interface{}{
					"error": err,
				}).Error("Failed to send enforcement notice")
			}

		}
	}

	if kickPlayer {
		for _, p := range presences {

			// Match only the target user
			if p.GetUserId() != targetUserID {
				continue
			}

			label, _ := MatchLabelByID(ctx, d.nk, MatchIDFromStringOrNil(p.GetStatus()))
			if label == nil {
				continue
			}

			// Don't kick game servers
			if label.GameServer.SessionID.String() == p.GetSessionId() {
				continue
			}

			permissions := make([]string, 0)

			// Check if the user is the match owner of a private match
			if label.SpawnedBy == callerUserID && label.IsPrivate() {
				permissions = append(permissions, "match owner")
			}

			// Check if the user is the game server operator
			if label.GameServer.OperatorID.String() == callerUserID {
				permissions = append(permissions, "game server operator")
			}

			if isGlobalOperator {
				doDisconnect = true
				permissions = append(permissions, "global operator")
			}

			if isEnforcer && label.GetGroupID().String() == groupID {
				doDisconnect = true
				permissions = append(permissions, "enforcer")
			}

			if len(permissions) == 0 {
				actions = append(actions, "user's match is not from this guild")
				continue
			}

			// Kick the player from the match
			if err := KickPlayerFromMatch(ctx, d.nk, label.ID, targetUserID); err != nil {
				actions = append(actions, fmt.Sprintf("failed to kick player from [%s](https://echo.taxi/spark://c/%s) (error: %s)", label.Mode.String(), strings.ToUpper(label.ID.UUID.String()), err.Error()))
				continue
			}

			actions = append(actions, fmt.Sprintf("kicked from [%s](https://echo.taxi/spark://c/%s) session. (%s) [%s]", label.Mode.String(), strings.ToUpper(label.ID.UUID.String()), userNotice, strings.Join(permissions, ", ")))

			cnt++

		}

		if doDisconnect {
			go func() {
				<-time.After(time.Second * 5)
				// Just disconnect the user, wholesale
				if count, err := DisconnectUserID(ctx, d.nk, targetUserID, true, true, false); err != nil {
					logger.Warn("Failed to disconnect user", zap.Error(err))
				} else if count > 0 {
					_, _ = d.LogAuditMessage(ctx, groupID, fmt.Sprintf("%s disconnected player %s (%s) from login/match service (%d sessions).", caller.Mention(), target.Mention(), target.Username, count), false)
				}
			}()
		}

		if cnt == 0 {
			actions = append(actions, "no active sessions found")
		}
	}

	_, _ = d.LogAuditMessage(ctx, groupID, fmt.Sprintf("%s's `kick-player` actions summary for %s (%s):\n %s", caller.Mention(), target.Mention(), target.Username, strings.Join(actions, ";\n ")), false)

	if i != nil {
		return simpleInteractionResponse(d.dg, i, fmt.Sprintf("[%d sessions found]%s\n%s", cnt, timeoutMessage, strings.Join(actions, "\n")))
	}
	return nil
}

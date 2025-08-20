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
	"go.uber.org/thriftrw/ptr"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const errCompoundDurationWithDW = "compound durations with 'd' (days) or 'w' (weeks) are not supported; use simple format like '2d' or convert to hours (e.g., '48h' instead of '2d')"

// parseSuspensionDuration parses a duration string for suspension/ban durations.
// Supports formats like: "15m", "2h", "7d", "1w", "2h30m", "1h30m45s"
// If no unit is specified (e.g., "15"), defaults to minutes.
// Returns the parsed duration or an error if the format is invalid.
func parseSuspensionDuration(inputDuration string) (time.Duration, error) {
	duration := strings.TrimSpace(inputDuration)

	if duration == "" {
		return 0, nil
	}

	if duration == "0" {
		// Zero duration means void existing suspension
		return 0, nil
	}

	// Check for compound durations with unsupported units (d or w)
	// Count occurrences of unit letters and check if d/w appears with other units
	hasD := strings.Contains(duration, "d")
	hasW := strings.Contains(duration, "w")
	hasOtherUnits := strings.ContainsAny(duration, "mhs")

	// Check if both d and w are present (e.g., "1w2d")
	if hasD && hasW {
		return 0, fmt.Errorf(errCompoundDurationWithDW)
	}

	// Check if d or w appears with other standard units
	if (hasD || hasW) && hasOtherUnits {
		return 0, fmt.Errorf(errCompoundDurationWithDW)
	}

	// Try parsing with Go's time.ParseDuration first for compound durations (e.g., "2h25m")
	// Note: time.ParseDuration supports ns, us, ms, s, m, h but not d (days) or w (weeks)
	if parsedDuration, err := time.ParseDuration(duration); err == nil {
		// Successfully parsed compound duration like "2h25m" or "1h30m45s"
		// Reject negative durations
		if parsedDuration < 0 {
			return 0, fmt.Errorf("duration must be positive, got: %v", parsedDuration)
		}
		return parsedDuration, nil
	}

	// Fallback to custom parsing for simple durations with d/w units
	// Additional safety check to prevent panic
	if len(duration) == 0 {
		return 0, fmt.Errorf("invalid duration format: empty string")
	}

	var unit time.Duration
	var numStr string
	lastChar := duration[len(duration)-1]

	switch lastChar {
	case 'm':
		unit = time.Minute
		numStr = duration[:len(duration)-1]
	case 'h':
		unit = time.Hour
		numStr = duration[:len(duration)-1]
	case 'd':
		unit = 24 * time.Hour
		numStr = duration[:len(duration)-1]
	case 'w':
		unit = 7 * 24 * time.Hour
		numStr = duration[:len(duration)-1]
	default:
		// No unit specified, default to minutes
		unit = time.Minute
		numStr = duration // Use the entire string as the numeric part
	}

	// Parse the numeric part
	durationVal, err := strconv.Atoi(numStr)
	if err != nil {
		return 0, fmt.Errorf("invalid duration format: %w", err)
	}

	// Reject negative durations
	if durationVal < 0 {
		return 0, fmt.Errorf("duration must be positive, got: %d", durationVal)
	}

	return time.Duration(durationVal) * unit, nil
}

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
		// Try cached permissions first, fallback to DB query
		perms := PermissionsFromContext(ctx)
		if perms != nil {
			isGlobalOperator = perms.IsGlobalOperator
		} else {
			isGlobalOperator, err = CheckSystemGroupMembership(ctx, d.db, userID, GroupGlobalOperators)
			if err != nil {
				return fmt.Errorf("error checking global operator status: %w", err)
			}
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
	case "show":
		gg := d.guildGroupRegistry.Get(groupID)
		if gg == nil {
			return simpleInteractionResponse(s, i, "This guild is not registered.")
		}

		if err := d.LogInteractionToChannel(i, gg.AuditChannelID); err != nil {
			logger.Warn("Failed to log interaction to channel")
		}

		if !gg.EnableServerEmbedsCommand {
			return simpleInteractionResponse(s, i, "The /show command is not enabled for this guild.")
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

	case "join-player", "igp", "ign":

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

	case "shutdown-match":
		// Permission check is deferred to the handler because it depends on the match's guild,
		// not the calling guild. The handler checks: global operator, server owner, or guild
		// enforcer of the session running on the server.
		gg := d.guildGroupRegistry.Get(groupID)
		if gg == nil {
			return simpleInteractionResponse(s, i, "This guild is not registered.")
		}

		if err := d.LogInteractionToChannel(i, gg.AuditChannelID); err != nil {
			logger.Warn("Failed to log interaction to channel")
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

		if err := d.cache.updateLinkStatus(ctx, user.ID); err != nil {
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
	case "configure_roles":
		return d.handleConfigureRoles(ctx, logger, s, i, userID, groupID)
	case "role_select":
		return d.handleRoleSelect(ctx, logger, s, i, userID, groupID, value)
	case "igp":

		return d.handleInGamePanelInteraction(i, value)
	case "taxi":
		// Handle taxi button - create a spark link for the session
		sessionUUID := strings.ToLower(value)
		sparkURL := fmt.Sprintf("https://echo.taxi/spark://j/%s", sessionUUID)
		
		return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Flags:   discordgo.MessageFlagsEphemeral,
				Content: fmt.Sprintf("🚕 **Taxi Link**\n%s", sparkURL),
			},
		})
	case "join":
		// Handle join button - create a direct spark link
		sessionUUID := strings.ToLower(value)
		sparkLink := fmt.Sprintf("spark://j/%s", sessionUUID)
		
		return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Flags:   discordgo.MessageFlagsEphemeral,
				Content: fmt.Sprintf("🔗 **Direct Join Link**\n`%s`\n\n*Copy this link and paste it in your browser or EchoVR to join directly*", sparkLink),
			},
		})
	}

	return nil
}

func (d *DiscordAppBot) handleAllocateMatch(ctx context.Context, logger runtime.Logger, userID, guildID string, regionCode string, mode, level evr.Symbol, startTime time.Time, description string) (l *MatchLabel, rtt float64, err error) {

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

	// Try cached permissions first, fallback to DB query
	perms := PermissionsFromContext(ctx)
	var isGlobalOperator bool
	if perms != nil {
		isGlobalOperator = perms.IsGlobalOperator
	} else {
		isGlobalOperator, err = CheckSystemGroupMembership(ctx, d.db, userID, GroupGlobalOperators)
		if err != nil {
			return nil, 0, status.Errorf(codes.Internal, "error checking global operator status: %v", err)
		}
	}

	if !gg.IsAllocator(userID) && !isGlobalOperator {
		return nil, 0, status.Error(codes.PermissionDenied, "user does not have the allocator role in this guild.")
	}

	// If user is a global operator, ensure the current guild group is included (avoid duplicates)
	if isGlobalOperator {
		found := false
		for _, gid := range allocatorGroupIDs {
			if gid == groupID {
				found = true
				break
			}
		}
		if !found {
			allocatorGroupIDs = append(allocatorGroupIDs, groupID)
		}
	}

	// Load the latency history for this user
	latencyHistory := NewLatencyHistory()
	if err := StorableRead(ctx, d.nk, userID, latencyHistory, false); err != nil && status.Code(err) != codes.NotFound {
		return nil, 0, status.Errorf(codes.Internal, "failed to read latency history: %v", err)
	}

	latestRTTs := latencyHistory.LatestRTTs()

	// Prepare the session for the match.
	settings := &MatchSettings{
		Mode:        mode,
		Level:       level,
		GroupID:     uuid.FromStringOrNil(groupID),
		StartTime:   startTime.UTC().Add(10 * time.Minute),
		SpawnedBy:   userID,
		Description: description,
	}
	queryAddon := ServiceSettings().Matchmaking.QueryAddons.Allocate
	label, err := LobbyGameServerAllocate(ctx, logger, d.nk, allocatorGroupIDs, latestRTTs, settings, []string{regionCode}, false, true, queryAddon)
	if err != nil {
		// Check if this is a region fallback error
		var regionErr ErrMatchmakingNoServersInRegion
		if errors.As(err, &regionErr) && regionErr.FallbackInfo != nil {
			// Return the fallback info so the caller can handle the user prompt
			return nil, 0, regionErr
		}

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

	// Check if user is suspended from the guild
	if group.IsSuspended(userID, nil) {
		return nil, 0, status.Error(codes.PermissionDenied, "user is suspended from the guild")
	}

	if group.DisableCreateCommand && !group.IsAllocator(userID) {
		return nil, 0, status.Error(codes.PermissionDenied, "guild does not allow public match creation")
	}

	isGuildModerator := group.IsEnforcer(userID) || group.IsAuditor(userID)
	isGlobalOperator, _ := CheckSystemGroupMembership(ctx, d.db, userID, GroupGlobalOperators)
	isPrivileged := isGuildModerator || isGlobalOperator

	if !isPrivileged {
		// Check if guild has custom rate limit configured
		var rateLimitSeconds int
		var rateLimitMessage string

		if group.CreateCommandRateLimitPerMinute > 0 {
			// Use guild-configured rate limit (converts from per-minute to seconds)
			rateLimitSeconds = int(60.0 / group.CreateCommandRateLimitPerMinute)
			rateLimitMessage = fmt.Sprintf("rate limit exceeded for match creation (%.1f per minute)", group.CreateCommandRateLimitPerMinute)
		} else {
			// Use default rate limits per mode
			rateLimitConfig := map[evr.Symbol]struct {
				seconds int
				message string
			}{
				evr.ModeCombatPublic:  {300, "rate limit exceeded for public combat matches (1 per 5 minutes)"},
				evr.ModeArenaPublic:   {900, "rate limit exceeded for public arena matches (1 per 15 minutes)"},
				evr.ModeCombatPrivate: {60, "rate limit exceeded for private combat matches (1 per minute)"},
				evr.ModeArenaPrivate:  {60, "rate limit exceeded for private arena matches (1 per minute)"},
				evr.ModeSocialPublic:  {60, "rate limit exceeded for public social lobbies (1 per minute)"},
				evr.ModeSocialPrivate: {60, "rate limit exceeded for private social lobbies (1 per minute)"},
			}

			config, exists := rateLimitConfig[mode]
			if !exists {
				config = struct {
					seconds int
					message string
				}{60, "rate limit exceeded for match creation (1 per minute)"}
			}
			rateLimitSeconds = config.seconds
			rateLimitMessage = config.message
		}

		key := fmt.Sprintf("%s:%s:%s", userID, groupID, mode.String())
		limiter, _ := d.matchCreateRateLimiters.LoadOrStore(key, rate.NewLimiter(rate.Limit(1.0/float64(rateLimitSeconds)), 1))

		if !limiter.Allow() {
			return nil, 0, status.Error(codes.ResourceExhausted, rateLimitMessage)
		}
	}

	latencyHistory := NewLatencyHistory()
	if err := StorableRead(ctx, d.nk, userID, latencyHistory, false); err != nil && status.Code(err) != codes.NotFound {
		return nil, 0, status.Errorf(codes.Internal, "failed to read latency history: %v", err)
	}
	extIPs := latencyHistory.AverageRTTs(true)

	// Filter servers to only those with RTT <= 90ms
	// Note: 90ms is stricter than the 100ms HighLatencyThresholdMs used in matchmaking
	// because /create is a manual server selection that should prioritize low latency
	const maxLatencyMs = 90
	filteredIPs := make(map[string]int)
	minLatencyMs := 0
	hasLatencyData := false

	for ip, latency := range extIPs {
		if !hasLatencyData || latency < minLatencyMs {
			minLatencyMs = latency
			hasLatencyData = true
		}
		if latency <= maxLatencyMs {
			filteredIPs[ip] = latency
		}
	}

	// If no servers within 90ms, return appropriate error
	if len(filteredIPs) == 0 {
		if !hasLatencyData {
			return nil, 0, fmt.Errorf("no latency history exists for your account; play some matches first to establish server latency data")
		}
		return nil, 0, fmt.Errorf("no servers within 90ms of your location. Your best server has %dms latency", minLatencyMs)
	}

	settings := &MatchSettings{
		Mode:      mode,
		Level:     level,
		GroupID:   uuid.FromStringOrNil(groupID),
		StartTime: startTime.UTC().Add(1 * time.Minute),
		SpawnedBy: userID,
	}

	queryAddon := ServiceSettings().Matchmaking.QueryAddons.Create

	// Determine requested regions and whether to require them.
	// Treat "no region selected" (empty or RegionDefault) as unrestricted so allocation can pick the best server.
	var regions []string
	requireRegion := false
	if region != "" && region != RegionDefault {
		// User explicitly selected a region: enforce it and use fallback UI if no servers are available there.
		regions = []string{region}
		requireRegion = true
	}
	// Otherwise: no explicit region selected (empty or RegionDefault), pass empty regions slice to allow allocator to choose best region without triggering fallback UI.

	label, err := LobbyGameServerAllocate(ctx, logger, d.nk, []string{groupID}, filteredIPs, settings, regions, true, requireRegion, queryAddon)
	if err != nil {
		// Check if this is a region fallback error
		var regionErr ErrMatchmakingNoServersInRegion
		if errors.As(err, &regionErr) && regionErr.FallbackInfo != nil {
			// Return the fallback info so the caller can handle the user prompt
			return nil, 0, regionErr
		}
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

	// Do not allow bots to be suspended
	if target.Bot {
		return simpleInteractionResponse(d.dg, i, "Bots cannot be suspended.")
	}

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

	// Parse duration using shared helper function
	if duration != "" {
		var err error
		suspensionDuration, err = parseSuspensionDuration(duration)
		if err != nil {
			helpMessage := fmt.Sprintf("Duration parse error: %s\n\n**Use a number followed by m, h, d, or w (e.g., 30m, 1h, 2d, 1w) or compound durations (e.g., 2h30m)**", err.Error())
			if i != nil {
				return simpleInteractionResponse(d.dg, i, helpMessage)
			}
			return errors.New(helpMessage)
		}

		if suspensionDuration == 0 {
			// Zero duration means void existing suspension
			suspensionExpiry = time.Now()
		} else {
			suspensionExpiry = time.Now().Add(suspensionDuration)
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
		sentEmbedResponse     = false
	)

	// Check permissions (global operator or guild enforcer)
	isGlobalOperator, isEnforcer, gg, err := RequireEnforcerOrOperator(ctx, db, nk, callerUserID, groupID)
	if err != nil {
		return errors.New("You must be a guild enforcer or global operator to use this command")
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
			// Use AddRecordWithOptions to support new fields (RuleViolated, IsPubliclyVisible)
			record := journal.AddRecordWithOptions(groupID, callerUserID, caller.User.ID, userNotice, notes, "", requireCommunityValues, allowPrivateLobbies, false, suspensionDuration)
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
					// Record voiding metrics
					if err := RecordVoidingMetrics(ctx, nk, currentGroupID, targetUserID); err != nil {
						logger.Warn("Failed to record voiding metrics", zap.Error(err))
					}
				}
			}
		}

		if err := SyncJournalAndProfile(ctx, nk, targetUserID, journal); err != nil {
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
					Text: fmt.Sprintf("Confidential. Do not share. | Edit on portal: check suspension details"),
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

			// Add enforcement buttons for suspension records
			var components []discordgo.MessageComponent
			if len(recordsByGroupID) > 0 && len(voids) == 0 {
				// Only add buttons if there are active suspensions (not voids)
				for _, records := range recordsByGroupID {
					for _, record := range records {
						// Create buttons for each record
						buttons := []discordgo.MessageComponent{
							&discordgo.Button{
								Label:    "Edit",
								Style:    discordgo.PrimaryButton,
								CustomID: fmt.Sprintf("enf:e:%s:%s:%s", record.ID, i.GuildID, target.ID),
							},
							&discordgo.Button{
								Label:    "Void",
								Style:    discordgo.DangerButton,
								CustomID: fmt.Sprintf("enf:v:%s:%s:%s", record.ID, i.GuildID, target.ID),
							},
						}
						components = append(components, &discordgo.ActionsRow{
							Components: buttons,
						})
						break // Only add buttons for the first record to avoid clutter
					}
					break // Only process first group
				}
			}

			_, err = d.dg.ChannelMessageSendComplex(gg.EnforcementNoticeChannelID, &discordgo.MessageSend{
				Embed:           embed,
				Components:      components,
				AllowedMentions: &discordgo.MessageAllowedMentions{},
			})
			if err != nil {
				logger.WithFields(map[string]interface{}{
					"error": err,
				}).Error("Failed to send enforcement notice")
			}

			// Send the same embed to the moderator as an ephemeral response
			if i != nil {
				err = d.dg.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:      discordgo.MessageFlagsEphemeral,
						Embeds:     []*discordgo.MessageEmbed{embed},
						Components: components,
					},
				})
				if err != nil {
					logger.WithFields(map[string]interface{}{
						"error": err,
					}).Error("Failed to send ephemeral response to moderator")
				} else {
					sentEmbedResponse = true
				}
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

	if i != nil && !sentEmbedResponse {
		return simpleInteractionResponse(d.dg, i, fmt.Sprintf("[%d sessions found]%s\n%s", cnt, timeoutMessage, strings.Join(actions, "\n")))
	}
	return nil
}

func (d *DiscordAppBot) handleConfigureRoles(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, userID string, groupID string) error {
	// Get the current guild roles
	metadata, err := GroupMetadataLoad(ctx, d.db, groupID)
	if err != nil {
		return fmt.Errorf("failed to get guild group metadata: %w", err)
	}

	// Get all roles in the guild
	guild, err := s.Guild(i.GuildID)
	if err != nil {
		return fmt.Errorf("failed to get guild: %w", err)
	}

	// Create select menu options for roles
	roleOptions := []discordgo.SelectMenuOption{
		{
			Label:       "None",
			Value:       "none",
			Description: "No role assigned",
		},
	}

	// Add all guild roles as options
	for _, role := range guild.Roles {
		// Skip @everyone role
		if role.ID == guild.ID {
			continue
		}
		roleOptions = append(roleOptions, discordgo.SelectMenuOption{
			Label:       role.Name,
			Value:       role.ID,
			Description: fmt.Sprintf("Role: %s", role.Name),
		})
	}

	// Pre-select current roles
	roles := metadata.RoleMap
	d.preselectRoleInOptions(roleOptions, roles.Member)
	d.preselectRoleInOptions(roleOptions, roles.Enforcer)
	d.preselectRoleInOptions(roleOptions, roles.ServerHost)
	d.preselectRoleInOptions(roleOptions, roles.Suspended)
	d.preselectRoleInOptions(roleOptions, roles.Allocator)

	// Helper to clone options and set default
	cloneAndPreselect := func(options []discordgo.SelectMenuOption, roleID string) []discordgo.SelectMenuOption {
		cloned := make([]discordgo.SelectMenuOption, len(options))
		for i, opt := range options {
			cloned[i] = opt
			cloned[i].Default = (opt.Value == roleID)
		}
		return cloned
	}

	memberOptions := cloneAndPreselect(roleOptions, roles.Member)
	enforcerOptions := cloneAndPreselect(roleOptions, roles.Enforcer)
	serverHostOptions := cloneAndPreselect(roleOptions, roles.ServerHost)
	suspendedOptions := cloneAndPreselect(roleOptions, roles.Suspended)
	allocatorOptions := cloneAndPreselect(roleOptions, roles.Allocator)
	// Build the configuration interface with select menus for each role type
	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.SelectMenu{
					CustomID:    "role_select:member",
					Placeholder: "Select Member Role",
					Options:     memberOptions,
					MaxValues:   1,
				},
			},
		},
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.SelectMenu{
					CustomID:    "role_select:moderator",
					Placeholder: "Select Moderator Role",
					Options:     enforcerOptions,
					MaxValues:   1,
				},
			},
		},
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.SelectMenu{
					CustomID:    "role_select:serverhost",
					Placeholder: "Select Server Host Role",
					Options:     serverHostOptions,
					MaxValues:   1,
				},
			},
		},
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.SelectMenu{
					CustomID:    "role_select:suspension",
					Placeholder: "Select Suspension Role",
					Options:     suspendedOptions,
					MaxValues:   1,
				},
			},
		},
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.SelectMenu{
					CustomID:    "role_select:allocator",
					Placeholder: "Select Allocator Role",
					Options:     allocatorOptions,
					MaxValues:   1,
				},
			},
		},
	}

	// Create the response
	content := "**Configure Guild Roles for EchoVRCE**\n\n" +
		"Select a role for each category below, or choose 'None' to remove the role assignment.\n\n" +
		"**Role Types:**\n" +
		"• **Member**: Allows joining social lobbies, matchmaking, or creating private matches\n" +
		"• **Moderator**: Access to detailed `/lookup` information and moderation tools\n" +
		"• **Server Host**: Allowed to host a game server for the guild\n" +
		"• **Suspension**: Disallowed from joining any guild matches\n" +
		"• **Allocator**: Allowed to allocate matches using the `/allocate` command"

	response := &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Content:    content,
			Components: components,
			Flags:      discordgo.MessageFlagsEphemeral,
		},
	}

	return s.InteractionRespond(i.Interaction, response)
}

func (d *DiscordAppBot) handleRoleSelect(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, userID string, groupID string, roleType string) error {
	nk := d.nk

	data := i.Interaction.MessageComponentData()
	if len(data.Values) == 0 {
		return simpleInteractionResponse(s, i, "No role selected.")
	}

	selectedValue := data.Values[0]
	var selectedRoleID string
	if selectedValue != "none" {
		selectedRoleID = selectedValue
	}

	// Get the current metadata
	metadata, err := GroupMetadataLoad(ctx, d.db, groupID)
	if err != nil {
		return fmt.Errorf("failed to get guild group metadata: %w", err)
	}

	// Update the specific role
	roles := metadata.RoleMap
	switch roleType {
	case "member":
		roles.Member = selectedRoleID
	case "moderator":
		roles.Enforcer = selectedRoleID
	case "serverhost":
		roles.ServerHost = selectedRoleID
	case "suspension":
		roles.Suspended = selectedRoleID
	case "allocator":
		roles.Allocator = selectedRoleID
	default:
		return simpleInteractionResponse(s, i, "Invalid role type.")
	}

	// Save the updated metadata
	groupData, err := metadata.MarshalToMap()
	if err != nil {
		return fmt.Errorf("error marshalling group data: %w", err)
	}

	if err := nk.GroupUpdate(ctx, groupID, SystemUserID, "", "", "", "", "", false, groupData, 1000000); err != nil {
		return fmt.Errorf("error updating group: %w", err)
	}

	// Update the registry
	gg, err := GuildGroupLoad(ctx, nk, groupID)
	if err != nil {
		return fmt.Errorf("failed to load guild group: %w", err)
	}
	d.guildGroupRegistry.Add(gg)

	// Update the interface to show the change
	return d.handleConfigureRoles(ctx, logger, s, i, userID, groupID)
}

func (d *DiscordAppBot) preselectRoleInOptions(options []discordgo.SelectMenuOption, roleID string) {
	// Reset all options to not be default
	for i := range options {
		options[i].Default = false
	}
	// Set the matching option as default
	for i := range options {
		if options[i].Value == roleID || (roleID == "" && options[i].Value == "none") {
			options[i].Default = true
			break
		}
	}
}

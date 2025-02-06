package server

import (
	"context"
	"encoding/json"
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

	var err error
	userID := d.cache.DiscordIDToUserID(user.ID)
	groupID := d.cache.GuildIDToGroupID(i.GuildID)

	switch commandName {
	case "link-headset":

	case "unlink-headset":

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

	groups, err := d.nk.GroupsGetId(ctx, []string{groupID})
	if err != nil {
		return fmt.Errorf("failed to get group: %w", err)
	}
	if len(groups) == 0 {
		logger.Warn("Guild not registered", zap.String("group_id", groupID), zap.String("guild_id", i.GuildID))
		return simpleInteractionResponse(s, i, "This guild is not registered.")
	}

	group, err := NewGuildGroup(groups[0])
	if err != nil {
		return fmt.Errorf("failed to create guild group: %w", err)
	}

	// Global security check
	switch commandName {

	case "create":

		if group.AuditChannelID != "" {
			if err := d.LogInteractionToChannel(i, group.AuditChannelID); err != nil {
				logger.Warn("Failed to log interaction to channel")
			}
		}

		if group.DisableCreateCommand {
			return simpleInteractionResponse(s, i, "This guild does not allow public allocation.")
		}

	case "allocate":

		if !group.IsAllocator(userID) {
			return simpleInteractionResponse(s, i, "You must be a guild allocator to use this command.")
		}

	case "trigger-cv", "kick-player", "join-player":

		if group.AuditChannelID != "" {
			if err := d.LogInteractionToChannel(i, group.AuditChannelID); err != nil {
				logger.Warn("Failed to log interaction to channel")
			}
		}

		if !group.IsModerator(userID) {
			return simpleInteractionResponse(s, i, "You must be a guild moderator to use this command.")
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

		history, err := LoginHistoryLoad(ctx, nk, userID)
		if err != nil {
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
				if err := LoginHistoryStore(ctx, nk, userID, history); err != nil {
					return fmt.Errorf("failed to save login history: %w", err)
				}

				return simpleInteractionResponse(s, i, fmt.Sprintf("Invalid: %v", err))
			}
		}

		if err := LoginHistoryStore(ctx, nk, userID, history); err != nil {
			return fmt.Errorf("failed to save login history: %w", err)
		}

		// Modify the interaction

		if i.Message == nil {
			return fmt.Errorf("message is nil")
		}

		i.Message.Components = []discordgo.MessageComponent{
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
		}

		if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("Approved IP address.\n### Please restart the game to play."),
			},
		}); err != nil {
			return fmt.Errorf("failed to respond to interaction: %w", err)
		}
		return err
	case "unlink-headset":
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
		// Modify the interaction response

		if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("Unlinked device ID `%s`.", value),
			},
		}); err != nil {
			return fmt.Errorf("failed to respond to interaction: %w", err)
		}
	}

	return nil
}

func (d *DiscordAppBot) handleAllocateMatch(ctx context.Context, logger runtime.Logger, userID, guildID string, region, mode, level evr.Symbol, startTime time.Time) (l *MatchLabel, rtt float64, err error) {

	// Find a parking match to prepare

	groupID := d.cache.GuildIDToGroupID(guildID)

	// Get a list of the groups that this user has allocate access to
	guildGroups, err := GuildUserGroupsList(ctx, d.nk, userID)
	if err != nil {
		return nil, 0, status.Errorf(codes.Internal, "failed to get guild group memberships: %v", err)
	}
	membership, ok := guildGroups[groupID]
	if !ok {
		return nil, 0, status.Error(codes.PermissionDenied, "user is not a member of the guild")
	}
	allocatorGroupIDs := make([]string, 0, len(guildGroups))
	for gid, g := range guildGroups {
		if g.IsAllocator(userID) {
			allocatorGroupIDs = append(allocatorGroupIDs, gid)
		}
	}

	if !membership.IsAllocator(userID) {
		return nil, 0, status.Error(codes.PermissionDenied, "user does not have the allocator role in this guild.")
	}

	limiter := d.loadPrepareMatchRateLimiter(userID, groupID)
	if !limiter.Allow() {
		return nil, 0, status.Error(codes.ResourceExhausted, fmt.Sprintf("rate limit exceeded (%0.0f requests per minute)", limiter.Limit()*60))
	}

	query := fmt.Sprintf("+label.lobby_type:unassigned +label.broadcaster.group_ids:/(%s)/ +label.broadcaster.regions:/(%s)/", Query.Join(allocatorGroupIDs, "|"), region.String())

	minSize := 1
	maxSize := 1
	matches, err := d.nk.MatchList(ctx, 100, true, "", &minSize, &maxSize, query)
	if err != nil {
		return nil, 0, status.Errorf(codes.Internal, "failed to list matches `%s`: %v", query, err)
	}

	if len(matches) == 0 {
		return nil, 0, status.Error(codes.NotFound, fmt.Sprintf("no game servers are available in region `%s`", region.String()))
	}

	labels := make([]*MatchLabel, 0, len(matches))
	for _, match := range matches {
		label := MatchLabel{}
		if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), &label); err != nil {
			return nil, 0, status.Errorf(codes.Internal, "failed to unmarshal match label: %v", err)
		}
		labels = append(labels, &label)
	}

	if region == evr.DefaultRegion {
		// Find the closest to the player.
		zapLogger := logger.(*RuntimeGoLogger).logger
		latencyHistory, err := LoadLatencyHistory(ctx, zapLogger, d.db, uuid.FromStringOrNil(userID))
		if err != nil {
			return nil, 0, fmt.Errorf("failed to load latency history: %w", err)
		}

		labelLatencies := make([]int, len(labels))
		for _, label := range labels {
			if history, ok := latencyHistory[label.Broadcaster.Endpoint.GetExternalIP()]; ok && len(history) != 0 {

				average := 0
				for _, l := range history {
					average += l
				}
				average /= len(history)
				labelLatencies = append(labelLatencies, average)

			} else {

				labelLatencies = append(labelLatencies, 0)
			}
		}

		params := LobbySessionParameters{
			Region: region,
			Mode:   mode,
		}

		lobbyCreateSortOptions(labels, labelLatencies, &params)
	}

	match := matches[0]
	matchID := MatchIDFromStringOrNil(match.GetMatchId())
	gid := uuid.FromStringOrNil(groupID)
	// Prepare the session for the match.

	if membership.IsAllocator(userID) {
		startTime = startTime.UTC().Add(10 * time.Minute)
	} else {
		startTime.UTC().Add(1 * time.Minute)
	}

	settings := MatchSettings{
		Mode:      mode,
		Level:     level,
		GroupID:   gid,
		StartTime: startTime.UTC().Add(1 * time.Minute),
		SpawnedBy: userID,
	}

	label, err := LobbyPrepareSession(ctx, d.nk, matchID, &settings)
	if err != nil {
		logger.Warn("Failed to prepare session", zap.Error(err), zap.String("mid", matchID.UUID.String()))
		return nil, -1, fmt.Errorf("failed to prepare session: %w", err)
	}

	return label, rtt, nil
}

func (d *DiscordAppBot) handleCreateMatch(ctx context.Context, logger runtime.Logger, userID, guildID string, region, mode, level evr.Symbol, startTime time.Time) (l *MatchLabel, latencyMillis int, err error) {

	// Find a parking match to prepare

	groupID := d.cache.GuildIDToGroupID(guildID)

	guildGroups, err := GuildUserGroupsList(ctx, d.nk, userID)
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

	if group.DisableCreateCommand {
		return nil, 0, status.Error(codes.PermissionDenied, "guild does not allow public match creation")
	}

	limiter := d.loadPrepareMatchRateLimiter(userID, groupID)
	if !limiter.Allow() {
		return nil, 0, status.Error(codes.ResourceExhausted, fmt.Sprintf("rate limit exceeded (%0.0f requests per minute)", limiter.Limit()*60))
	}

	zapLogger := logger.(*RuntimeGoLogger).logger
	latencyHistory, err := LoadLatencyHistory(ctx, zapLogger, d.db, uuid.FromStringOrNil(userID))
	if err != nil {
		return nil, 0, fmt.Errorf("failed to load latency history: %w", err)
	}

	extIPs := latencyHistory.AverageRTTs(true, true)

	settings := &MatchSettings{
		Mode:      mode,
		Level:     level,
		GroupID:   uuid.FromStringOrNil(groupID),
		StartTime: startTime.UTC().Add(1 * time.Minute),
		SpawnedBy: userID,
	}

	label, err := AllocateGameServer(ctx, logger, d.nk, groupID, extIPs, settings, []string{region.String()}, true, false)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to allocate game server: %w", err)
	}

	if label == nil {
		return nil, 0, fmt.Errorf("failed to allocate game server: label is nil")
	}

	latencyMillis = latencyHistory.AverageRTT(label.Broadcaster.Endpoint.ExternalIP.String(), true)

	return label, latencyMillis, nil
}

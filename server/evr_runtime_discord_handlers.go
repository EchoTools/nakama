package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (d *DiscordAppBot) syncLinkedRoles(ctx context.Context, userID string) error {

	memberships, err := GetGuildGroupMemberships(ctx, d.nk, userID)
	if err != nil {
		return fmt.Errorf("failed to get guild group memberships: %w", err)
	}
	guildGroups := d.cache.guildGroupCache.GuildGroups()
	if guildGroups == nil {
		return fmt.Errorf("guild groups cache is nil")
	}

	for groupID, _ := range memberships {
		guildGroup, ok := guildGroups[groupID]
		if !ok {
			continue
		}

		if guildGroup.Metadata.Roles.AccountLinked != "" {
			if guildGroup.Metadata.IsAccountLinked(userID) {
				if err := d.dg.GuildMemberRoleAdd(groupID, userID, guildGroup.Metadata.Roles.AccountLinked); err != nil {
					return fmt.Errorf("error adding role to member: %w", err)
				}
			} else {
				if err := d.dg.GuildMemberRoleRemove(groupID, userID, guildGroup.Metadata.Roles.AccountLinked); err != nil {
					return fmt.Errorf("error removing role from member: %w", err)
				}
			}
		}
	}
	return nil
}

func (d *DiscordAppBot) handleInteractionCreate(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, commandName string, commandFn DiscordCommandHandlerFn) error {
	ctx := d.ctx

	user, member := getScopedUserMember(i)

	if user == nil {
		return fmt.Errorf("user is nil")
	}
	// Check if the interaction is a command
	if i.Type != discordgo.InteractionApplicationCommand {
		// Handle the command
		logger.WithField("type", i.Type).Warn("Unhandled interaction type")
		return nil
	}

	var err error
	userID := d.cache.DiscordIDToUserID(user.ID)
	groupID := d.cache.GuildIDToGroupID(i.GuildID)

	switch commandName {
	case "whoami", "link-headset":

		// Authenticate/create an account.
		userID = d.cache.DiscordIDToUserID(user.ID)
		if userID == "" {
			userID, _, _, err = d.nk.AuthenticateCustom(ctx, user.ID, user.Username, true)
			if err != nil {
				return fmt.Errorf("failed to authenticate (or create) user %s: %w", user.ID, err)
			}
		}

		// Do some profile checks and cleanups
		// The user must be in a guild for the empty groupID to be valid
		d.cache.Queue(userID, groupID)
		d.cache.Queue(userID, "")
	default:
		if userID == "" {
			return simpleInteractionResponse(s, i, "a headset must be linked to this Discord account to use slash commands")
		}
	}

	if groupID == "" {
		return simpleInteractionResponse(s, i, "This command can only be used in a guild.")
	}

	// Require guild moderator
	memberships, err := GetGuildGroupMemberships(ctx, d.nk, userID)
	if err != nil {
		return fmt.Errorf("failed to get guild group membership: %w", err)
	}

	membership := memberships[groupID]

	gg, ok := d.cache.guildGroupCache.GuildGroup(groupID)
	if !ok {
		return simpleInteractionResponse(s, i, "This guild does not exist.")
	}

	md := gg.Metadata
	// Global security check
	switch commandName {

	case "create":

		if md.AuditChannelID != "" {
			if err := d.LogInteractionToChannel(i, md.AuditChannelID); err != nil {
				logger.Warn("Failed to log interaction to channel")
			}
		}

		if md.DisableCreateCommand {
			return simpleInteractionResponse(s, i, "This guild does not allow public allocation.")
		}

	case "allocate":

		if !membership.IsAllocator {
			return simpleInteractionResponse(s, i, "You must be a guild allocator to use this command.")
		}

	case "trigger-cv", "kick-player":

		if md.AuditChannelID != "" {
			if err := d.LogInteractionToChannel(i, md.AuditChannelID); err != nil {
				logger.Warn("Failed to log interaction to channel")
			}
		}

		if !membership.IsModerator {
			return simpleInteractionResponse(s, i, "You must be a guild moderator to use this command.")
		}

	}

	return commandFn(logger, s, i, user, member, userID, groupID)
}

func (d *DiscordAppBot) handleAllocateMatch(ctx context.Context, logger runtime.Logger, userID, guildID string, region, mode, level evr.Symbol, startTime time.Time) (l *MatchLabel, rtt float64, err error) {

	// Find a parking match to prepare

	groupID := d.cache.GuildIDToGroupID(guildID)

	// Get a list of the groups that this user has allocate access to
	memberships, err := GetGuildGroupMemberships(ctx, d.nk, userID)
	if err != nil {
		return nil, 0, status.Errorf(codes.Internal, "failed to get guild group memberships: %v", err)
	}
	membership, ok := memberships[groupID]
	if !ok {
		return nil, 0, status.Error(codes.PermissionDenied, "user is not a member of the guild")
	}
	allocatorGroupIDs := make([]string, 0, len(memberships))
	for gid, _ := range memberships {
		if membership.IsAllocator {
			allocatorGroupIDs = append(allocatorGroupIDs, gid)
		}
	}

	if !membership.IsAllocator {
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
		return nil, 0, status.Errorf(codes.Internal, "failed to list matches: %v", err)
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

	// Pick a random result
	match := matches[0]
	matchID := MatchIDFromStringOrNil(match.GetMatchId())
	gid := uuid.FromStringOrNil(groupID)
	// Prepare the session for the match.

	if membership.IsAllocator {
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
		logger.Warn("Failed to prepare session", zap.Error(err), zap.String("mid", label.ID.UUID.String()))
		return nil, -1, fmt.Errorf("failed to prepare session: %w", err)
	}

	return label, rtt, nil
}

func (d *DiscordAppBot) handleCreateMatch(ctx context.Context, logger runtime.Logger, userID, guildID string, region, mode, level evr.Symbol, startTime time.Time) (l *MatchLabel, rtt float64, err error) {

	// Find a parking match to prepare

	groupID := d.cache.GuildIDToGroupID(guildID)

	// Get a list of the groups that this user has allocate access to
	memberships, err := GetGuildGroupMemberships(ctx, d.nk, userID)
	if err != nil {
		return nil, 0, status.Errorf(codes.Internal, "failed to get guild group memberships: %v", err)
	}

	_, ok := memberships[groupID]
	if !ok {
		return nil, 0, status.Error(codes.PermissionDenied, "user is not a member of the guild")
	}

	guildGroup, found := d.cache.guildGroupCache.GuildGroup(groupID)
	if !found {
		return nil, 0, status.Error(codes.Internal, "guild groups cache is nil")
	}

	if guildGroup.Metadata.DisableCreateCommand {
		return nil, 0, status.Error(codes.PermissionDenied, "guild does not allow public allocation")
	}

	limiter := d.loadPrepareMatchRateLimiter(userID, groupID)
	if !limiter.Allow() {
		return nil, 0, status.Error(codes.ResourceExhausted, fmt.Sprintf("rate limit exceeded (%0.0f requests per minute)", limiter.Limit()*60))
	}

	query := fmt.Sprintf("+label.lobby_type:unassigned +label.broadcaster.group_ids:/(%s)/ +label.broadcaster.regions:/(default)/ +label.broadcaster.regions:/(%s)/", groupID, region.String())

	minSize := 1
	maxSize := 1
	matches, err := d.nk.MatchList(ctx, 100, true, "", &minSize, &maxSize, query)
	if err != nil {
		return nil, 0, status.Errorf(codes.Internal, "failed to list matches: %v", err)
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

	match := matches[0]
	matchID := MatchIDFromStringOrNil(match.GetMatchId())

	// Prepare the session for the match.

	settings := MatchSettings{
		Mode:      mode,
		Level:     level,
		GroupID:   uuid.FromStringOrNil(groupID),
		StartTime: startTime.UTC().Add(1 * time.Minute),
		SpawnedBy: userID,
	}

	label, err := LobbyPrepareSession(ctx, d.nk, matchID, &settings)
	if err != nil {
		logger.Warn("Failed to prepare session", zap.Error(err), zap.String("mid", label.ID.UUID.String()))
		return nil, -1, fmt.Errorf("failed to prepare session: %w", err)
	}

	return label, rtt, nil
}

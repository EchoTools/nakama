package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (d *DiscordAppBot) syncLinkedRoles(ctx context.Context, userID string) error {

	memberships, err := GetGuildGroupMemberships(ctx, d.nk, userID, nil)
	if err != nil {
		return fmt.Errorf("failed to get guild group memberships: %w", err)
	}
	for _, membership := range memberships {
		if membership.GuildGroup.Metadata.Roles.AccountLinked != "" {
			if membership.GuildGroup.Metadata.IsAccountLinked(userID) {
				if err := d.dg.GuildMemberRoleAdd(membership.GuildGroup.ID().String(), userID, membership.GuildGroup.Metadata.Roles.AccountLinked); err != nil {
					return fmt.Errorf("error adding role to member: %w", err)
				}
			} else {
				if err := d.dg.GuildMemberRoleRemove(membership.GuildGroup.ID().String(), userID, membership.GuildGroup.Metadata.Roles.AccountLinked); err != nil {
					return fmt.Errorf("error removing role from member: %w", err)
				}
			}
		}
	}
	return nil
}

func (d *DiscordAppBot) handleInteractionCreate(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, commandName string, commandFn DiscordCommandHandlerFn) error {
	ctx := d.ctx
	db := d.db

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

		groupID := d.cache.GuildIDToGroupID(i.GuildID)
		// Do some profile checks and cleanups
		// The user must be in a guild for the empty groupID to be valid
		d.cache.Queue(userID, groupID)
		d.cache.Queue(userID, "")
	default:
		if userID == "" {
			return simpleInteractionResponse(s, i, "a headsets must be linked to this Discord account to use slash commands")
		}
	}

	// Global security check
	switch commandName {
	case "allocate", "create":

		groupID := d.cache.GuildIDToGroupID(i.GuildID)
		if groupID == "" {
			return simpleInteractionResponse(s, i, "This command can only be used in a guild.")
		}

	case "trigger-cv", "kick-player":

		if isGlobalModerator, err := CheckSystemGroupMembership(ctx, db, userID, GroupGlobalModerators); err != nil {
			return errors.New("failed to check global moderator status")
		} else if !isGlobalModerator {
			return simpleInteractionResponse(s, i, "You must be a global moderator to use this command.")
		}
	}

	groupID := d.cache.GuildIDToGroupID(i.GuildID)
	if groupID == "" {
		return simpleInteractionResponse(s, i, "This command can only be used in a guild.")
	}

	return commandFn(logger, s, i, user, member, userID, groupID)
}

func (d *DiscordAppBot) handleAllocateMatch(ctx context.Context, logger runtime.Logger, userID, guildID string, region, mode, level evr.Symbol, startTime time.Time) (l *MatchLabel, rtt float64, err error) {

	// Find a parking match to prepare

	groupID := d.cache.GuildIDToGroupID(guildID)

	// Get a list of the groups that this user has allocate access to
	memberships, err := GetGuildGroupMemberships(ctx, d.nk, userID, nil)
	if err != nil {
		return nil, 0, status.Errorf(codes.Internal, "failed to get guild group memberships: %v", err)
	}
	var membership *GuildGroupMembership

	allocatorGroupIDs := make([]string, 0, len(memberships))
	for _, m := range memberships {
		if m.GuildGroup.Metadata.IsAllocator(userID) {
			allocatorGroupIDs = append(allocatorGroupIDs, m.GuildGroup.ID().String())
		}
		if m.GuildGroup.ID() == uuid.FromStringOrNil(groupID) {
			membership = &m
		}
	}

	if membership == nil {
		return nil, 0, status.Error(codes.PermissionDenied, "user is not a member of the guild")
	}

	if !membership.GuildGroup.Metadata.IsAllocator(userID) {
		return nil, 0, status.Error(codes.PermissionDenied, "user does not have the allocator role in this guild.")
	}

	limiter := d.loadPrepareMatchRateLimiter(userID, groupID)
	if !limiter.Allow() {
		return nil, 0, status.Error(codes.ResourceExhausted, fmt.Sprintf("rate limit exceeded (%0.0f requests per minute)", limiter.Limit()*60))
	}

	query := fmt.Sprintf("+label.lobby_type:unassigned +label.broadcaster.group_ids:/(%s)/ +label.broadcaster.regions:/(%s)/", strings.Join(allocatorGroupIDs, "|"), region.String())

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
			return nil, 0, fmt.Errorf("failed to load latency history: %v", err)
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

		params := SessionParameters{
			Region: region,
			Mode:   mode,
		}
		lobbyCreateSortOptions(labels, labelLatencies, params)
	}

	// Pick a random result
	match := matches[0]
	matchID := MatchIDFromStringOrNil(match.GetMatchId())
	gid := uuid.FromStringOrNil(groupID)
	// Prepare the session for the match.
	label := MatchLabel{}
	label.SpawnedBy = userID

	label.StartTime = startTime.UTC().Add(1 * time.Minute)
	if membership.GuildGroup.Metadata.IsAllocator(userID) {
		label.StartTime = startTime.UTC().Add(10 * time.Minute)
	}

	label.GroupID = &gid
	label.Mode = mode
	if !level.IsNil() {
		label.Level = level
	}
	nk_ := d.nk.(*RuntimeGoNakamaModule)
	response, err := SignalMatch(ctx, nk_.matchRegistry, matchID, SignalPrepareSession, label)
	if err != nil {
		return nil, 0, status.Errorf(codes.Internal, "failed to signal match: %v", err)
	}

	label = MatchLabel{}
	if err := json.Unmarshal([]byte(response), &label); err != nil {
		return nil, 0, status.Errorf(codes.Internal, "failed to unmarshal match label: %v", err)
	}

	return &label, rtt, nil
}

func (d *DiscordAppBot) handleCreateMatch(ctx context.Context, logger runtime.Logger, userID, guildID string, region, mode, level evr.Symbol, startTime time.Time) (l *MatchLabel, rtt float64, err error) {

	// Find a parking match to prepare

	groupID := d.cache.GuildIDToGroupID(guildID)

	// Get a list of the groups that this user has allocate access to
	memberships, err := GetGuildGroupMemberships(ctx, d.nk, userID, nil)
	if err != nil {
		return nil, 0, status.Errorf(codes.Internal, "failed to get guild group memberships: %v", err)
	}

	var membership *GuildGroupMembership
	for _, m := range memberships {
		if m.GuildGroup.ID() == uuid.FromStringOrNil(groupID) {
			membership = &m
		}
	}

	if membership == nil {
		return nil, 0, status.Error(codes.PermissionDenied, "user is not a member of the guild")
	}

	if membership.GuildGroup.Metadata.DisablePublicAllocateCommand {
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
		return nil, 0, fmt.Errorf("failed to load latency history: %v", err)
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

	params := SessionParameters{
		Region: region,
		Mode:   mode,
	}
	lobbyCreateSortOptions(labels, labelLatencies, params)

	match := matches[0]
	matchID := MatchIDFromStringOrNil(match.GetMatchId())
	gid := uuid.FromStringOrNil(groupID)
	// Prepare the session for the match.
	label := MatchLabel{}
	label.SpawnedBy = userID

	label.StartTime = startTime.UTC().Add(1 * time.Minute)

	label.GroupID = &gid
	label.Mode = mode
	if !level.IsNil() {
		label.Level = level
	}
	nk_ := d.nk.(*RuntimeGoNakamaModule)
	response, err := SignalMatch(ctx, nk_.matchRegistry, matchID, SignalPrepareSession, label)
	if err != nil {
		return nil, 0, status.Errorf(codes.Internal, "failed to signal match: %v", err)
	}

	label = MatchLabel{}
	if err := json.Unmarshal([]byte(response), &label); err != nil {
		return nil, 0, status.Errorf(codes.Internal, "failed to unmarshal match label: %v", err)
	}

	return &label, rtt, nil
}

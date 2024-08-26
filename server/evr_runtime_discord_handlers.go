package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (d *DiscordAppBot) checkDisplayName(ctx context.Context, nk runtime.NakamaModule, userID string, displayName string) (string, error) {

	// Filter usernames of other players
	users, err := nk.UsersGetUsername(ctx, []string{displayName})
	if err != nil {
		return "", fmt.Errorf("error getting users by username: %w", err)
	}
	for _, u := range users {
		if u.Id == userID {
			continue
		}
		return "", status.Errorf(codes.AlreadyExists, "username `%s` is already taken", displayName)
	}

	result, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: DisplayNameCollection,
			Key:        strings.ToLower(displayName),
		},
	})
	if err != nil {
		return "", fmt.Errorf("error reading displayNames: %w", err)
	}

	for _, o := range result {
		if o.UserId == userID {
			continue
		}
		return "", status.Errorf(codes.AlreadyExists, "display name `%s` is already taken", displayName)
	}

	return displayName, nil
}

func (d *DiscordAppBot) recordDisplayName(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, groupID string, userID string, displayName string) error {

	// Purge old display names
	records, err := GetDisplayNameRecords(ctx, logger, nk, userID)
	if err != nil {
		return fmt.Errorf("error getting display names: %w", err)
	}
	storageDeletes := []*runtime.StorageDelete{}
	if len(records) > 2 {
		// Sort the records by create time
		sort.SliceStable(records, func(i, j int) bool {
			return records[i].CreateTime.Seconds > records[j].CreateTime.Seconds
		})
		// Delete all but the first two
		for i := 2; i < len(records); i++ {
			storageDeletes = append(storageDeletes, &runtime.StorageDelete{
				Collection: DisplayNameCollection,
				Key:        records[i].Key,
				UserID:     userID,
			})
		}
	}

	content := map[string]string{
		"displayName": displayName,
	}
	data, _ := json.Marshal(content)

	// Update the account
	accountUpdates := []*runtime.AccountUpdate{}

	storageWrites := []*runtime.StorageWrite{
		{
			Collection: DisplayNameCollection,
			Key:        strings.ToLower(displayName),
			UserID:     userID,
			Value:      string(data),
			Version:    "",
		},
	}

	walletUpdates := []*runtime.WalletUpdate{}
	updateLedger := true
	if _, _, err = nk.MultiUpdate(ctx, accountUpdates, storageWrites, storageDeletes, walletUpdates, updateLedger); err != nil {
		return fmt.Errorf("error updating account: %w", err)
	}
	return nil
}

func (d *DiscordAppBot) updateAccount(ctx context.Context, guildID string, member *discordgo.Member) error {
	groupID := d.GuildIDToGroupID(guildID)
	userID := d.DiscordIDToUserID(member.User.ID)
	_ = d.nk.GroupUsersAdd(ctx, SystemUserID, groupID, []string{userID})
	account, membership, err := d.GuildUser(ctx, member.GuildID, member.User.ID)
	if err != nil {
		return fmt.Errorf("failed to get guild user: %w", err)
	}
	update := false

	displayName := sanitizeDisplayName(member.DisplayName())
	if account.GetDisplayName(membership.GuildGroup.ID().String()) != displayName {

		if displayName, err := d.checkDisplayName(ctx, d.nk, account.ID(), displayName); err == nil {
			if err := d.recordDisplayName(ctx, d.logger, d.nk, membership.GuildGroup.ID().String(), account.ID(), displayName); err != nil {
				return fmt.Errorf("error setting display name: %w", err)
			}
			groupID := d.GuildIDToGroupID(member.GuildID)
			account.SetGroupDisplayName(groupID, displayName)
		}
	}

	if account.Username() != member.User.Username {
		update = true
	}

	langTag := strings.SplitN(member.User.Locale, "-", 2)[0]
	if account.LangTag() != langTag {
		update = true
	}

	if account.AvatarURL() != member.User.AvatarURL("512") {
		update = true
	}

	if update {
		if err := d.nk.AccountUpdateId(ctx, account.ID(), member.User.Username, account.MarshalMap(), displayName, "", "", langTag, member.User.AvatarURL("512")); err != nil {
			return fmt.Errorf("failed to update account: %w", err)
		}
	}

	if membership.GuildGroup.UpdateRoleCache(account.ID(), member.Roles) {
		g := membership.GuildGroup.Group
		mdMap := membership.GuildGroup.Metadata.MarshalMap()
		if err := d.nk.GroupUpdate(ctx, g.Id, SystemUserID, g.Name, g.CreatorId, g.LangTag, g.Description, g.AvatarUrl, g.Open.Value, mdMap, int(g.MaxCount)); err != nil {
			return fmt.Errorf("error updating group: %w", err)
		}
	}

	accountLinkedRole := membership.GuildGroup.Metadata.Roles.AccountLinked
	if accountLinkedRole != "" {
		if len(account.account.Devices) == 0 {
			if slices.Contains(member.Roles, accountLinkedRole) {
				// Remove the role
				if err := d.dg.GuildMemberRoleRemove(member.GuildID, member.User.ID, accountLinkedRole); err != nil {
					return fmt.Errorf("error adding role to member: %w", err)
				}
			}
		} else {
			if !slices.Contains(member.Roles, accountLinkedRole) {
				// Assign the role
				if err := d.dg.GuildMemberRoleAdd(member.GuildID, member.User.ID, accountLinkedRole); err != nil {
					return fmt.Errorf("error adding role to member: %w", err)
				}
			}
		}
	}

	return nil
}

func (d *DiscordAppBot) syncLinkedRoles(ctx context.Context, userID string) error {

	memberships, err := GetGuildGroupMemberships(ctx, d.nk, userID, nil)
	if err != nil {
		return fmt.Errorf("failed to get guild group memberships: %w", err)
	}
	for _, membership := range memberships {
		if membership.GuildGroup.Metadata.Roles.AccountLinked != "" {
			if membership.GuildGroup.IsAccountLinked(userID) {
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

func (d *DiscordAppBot) updateGuild(ctx context.Context, logger *zap.Logger, guild *discordgo.Guild) error {

	var err error
	botUserID := d.DiscordIDToUserID(d.dg.State.User.ID)
	if botUserID == "" {
		botUserID, _, _, err = d.nk.AuthenticateCustom(ctx, d.dg.State.User.ID, d.dg.State.User.Username, true)
		if err != nil {
			return fmt.Errorf("failed to authenticate (or create) bot user %s: %w", d.dg.State.User.ID, err)
		}
	}

	ownerUserID := d.DiscordIDToUserID(guild.OwnerID)
	if ownerUserID == "" {
		ownerMember, err := d.dg.GuildMember(guild.ID, guild.OwnerID)
		if err != nil {
			return fmt.Errorf("failed to get guild owner: %w", err)
		}
		ownerUserID, _, _, err = d.nk.AuthenticateCustom(ctx, guild.OwnerID, ownerMember.User.Username, true)
		if err != nil {
			return fmt.Errorf("failed to authenticate (or create) guild owner %s: %w", guild.OwnerID, err)
		}
	}

	groupID := d.GuildIDToGroupID(guild.ID)
	if groupID == "" {

		gm, err := NewGuildGroupMetadata(guild.ID).MarshalToMap()
		if err != nil {
			return fmt.Errorf("error marshalling group metadata: %w", err)
		}

		_, err = d.nk.GroupCreate(ctx, ownerUserID, guild.Name, botUserID, GuildGroupLangTag, guild.Description, guild.IconURL("512"), false, gm, 100000)
		if err != nil {
			return fmt.Errorf("error creating group: %w", err)
		}
	} else {

		md, err := GetGuildGroupMetadata(ctx, d.db, groupID)
		if err != nil {
			return fmt.Errorf("error getting guild group metadata: %w", err)
		}

		md.RoleCache = make(map[string][]string, len(guild.Roles))

		for _, role := range md.Roles.Slice() {
			md.RoleCache[role] = make([]string, 0)
		}

		// Update the group
		md.RulesText = "No #rules channel found. Please create the channel and set the topic to the rules."

		for _, channel := range guild.Channels {
			if channel.Type == discordgo.ChannelTypeGuildText && channel.Name == "rules" {
				md.RulesText = channel.Topic
				break
			}
		}

		if err := d.nk.GroupUpdate(ctx, groupID, SystemUserID, guild.Name, botUserID, GuildGroupLangTag, guild.Description, guild.IconURL("512"), true, md.MarshalMap(), 100000); err != nil {
			return fmt.Errorf("error updating group: %w", err)
		}
	}

	return nil
}

func (d *DiscordAppBot) handleGuildCreate(logger *zap.Logger, s *discordgo.Session, e *discordgo.GuildCreate) error {
	ctx, cancel := context.WithTimeout(d.ctx, time.Second*5)
	defer cancel()
	logger.Info("Guild Create", zap.Any("guild", e.Guild))
	if err := d.updateGuild(ctx, logger, e.Guild); err != nil {
		return fmt.Errorf("failed to update guild: %w", err)
	}
	return nil
}

func (d *DiscordAppBot) handleGuildUpdate(logger *zap.Logger, s *discordgo.Session, e *discordgo.GuildUpdate) error {
	ctx, cancel := context.WithTimeout(d.ctx, time.Second*5)
	defer cancel()
	logger.Info("Guild Update", zap.Any("guild", e.Guild))
	if err := d.updateGuild(ctx, logger, e.Guild); err != nil {
		return fmt.Errorf("failed to update guild: %w", err)
	}
	return nil
}

func (d *DiscordAppBot) handleGuildDelete(logger *zap.Logger, s *discordgo.Session, e *discordgo.GuildDelete) error {
	ctx, cancel := context.WithTimeout(d.ctx, time.Second*5)
	defer cancel()
	logger.Info("Guild Delete", zap.Any("guild", e.Guild))
	groupID := d.GuildIDToGroupID(e.Guild.ID)
	if groupID == "" {
		return nil
	}

	if err := d.nk.GroupDelete(ctx, groupID); err != nil {
		return fmt.Errorf("error deleting group: %w", err)
	}
	return nil
}

func (d *DiscordAppBot) handleMemberAdd(logger *zap.Logger, s *discordgo.Session, e *discordgo.GuildMemberAdd) error {
	ctx, cancel := context.WithTimeout(d.ctx, time.Second*5)
	defer cancel()
	logger.Info("Member Add", zap.Any("member", e))

	if err := d.updateAccount(ctx, e.GuildID, e.Member); err != nil {
		return fmt.Errorf("failed to update account: %w", err)
	}

	return nil
}

func (d *DiscordAppBot) handleMemberUpdate(logger *zap.Logger, s *discordgo.Session, e *discordgo.GuildMemberUpdate) error {
	if e.Member == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(d.ctx, time.Second*5)
	defer cancel()
	logger.Info("Member Update", zap.Any("member", e))
	if err := d.updateAccount(ctx, e.GuildID, e.Member); err != nil {
		return fmt.Errorf("failed to update account: %w", err)
	}
	return nil
}

func (d *DiscordAppBot) handleMemberRemove(logger *zap.Logger, s *discordgo.Session, e *discordgo.GuildMemberRemove) error {

	ctx, cancel := context.WithTimeout(d.ctx, time.Second*5)
	defer cancel()
	logger.Info("Member Remove", zap.Any("member", e))
	userID := d.DiscordIDToUserID(e.Member.User.ID)
	if userID == "" {
		return nil
	}

	// Remove the user from the guild group
	groupID := d.GuildIDToGroupID(e.GuildID)
	if groupID == "" {
		return nil
	}

	account, err := GetEVRAccountID(ctx, d.nk, userID)
	if err != nil {
		return fmt.Errorf("failed to get account ID: %w", err)
	}

	if err := d.nk.GroupUserLeave(ctx, groupID, userID, account.Username()); err != nil {
		return fmt.Errorf("error removing user from group: %w", err)
	}

	delete(account.GroupDisplayNames, groupID)
	if account.GetActiveGroupID().String() == groupID {
		account.SetActiveGroupID(uuid.Nil)
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

	var userID string
	var err error

	switch commandName {
	case "whoami", "link-headset":

		// Authenticate/create an account.
		userID, err = GetUserIDByDiscordID(ctx, d.db, user.ID)
		if err != nil {
			userID, _, _, err = d.nk.AuthenticateCustom(ctx, user.ID, user.Username, true)
			if err != nil {
				return fmt.Errorf("failed to authenticate (or create) user %s: %w", user.ID, err)
			}
		}

	case "trigger-cv", "kick-player":

		userID, err = GetUserIDByDiscordID(ctx, d.db, user.ID)
		if err != nil {
			return fmt.Errorf("a headsets must be linked to this Discord account to use slash commands")
		}
		if isGlobalModerator, err := CheckSystemGroupMembership(ctx, db, userID, GroupGlobalModerators); err != nil {
			return errors.New("failed to check global moderator status")
		} else if !isGlobalModerator {
			return simpleInteractionResponse(s, i, "You must be a global moderator to use this command.")

		}

	default:
		userID, err = GetUserIDByDiscordID(ctx, d.db, user.ID)
		if err != nil {
			return fmt.Errorf("a headsets must be linked to this Discord account to use slash commands")
		}

	}

	groupID, err := GetGroupIDByGuildID(ctx, d.db, i.GuildID)
	if err != nil {
		logger.Error("Failed to get guild ID", zap.Error(err))
	}
	return commandFn(logger, s, i, user, member, userID, groupID)
}

func (d *DiscordAppBot) handleProfileRequest(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, s *discordgo.Session, discordRegistry DiscordRegistry, i *discordgo.InteractionCreate, discordID string, username string, guildID string, includePrivate bool) error {
	whoami := &WhoAmI{
		DiscordID:             discordID,
		EVRIDLogins:           make(map[string]time.Time),
		DisplayNames:          make([]string, 0),
		ClientAddresses:       make([]string, 0),
		DeviceLinks:           make([]string, 0),
		GuildGroupMemberships: make([]GuildGroupMembership, 0),
		MatchIDs:              make([]string, 0),
	}
	// Get the user's ID

	userIDStr, err := GetUserIDByDiscordID(ctx, d.db, discordID)
	if err != nil {
		userIDStr, _, _, err = d.nk.AuthenticateCustom(ctx, discordID, i.Member.User.Username, true)
		if err != nil {
			return fmt.Errorf("failed to authenticate (or create) user %s: %w", discordID, err)
		}
	}
	userID := uuid.FromStringOrNil(userIDStr)

	i.Member.GuildID = i.GuildID

	if includePrivate {
		// Do some profile checks and cleanups
		if err := d.updateAccount(ctx, i.GuildID, i.Member); err != nil {
			return fmt.Errorf("failed to update account: %w", err)
		}
	}

	// Basic account details
	whoami.NakamaID = userID

	account, err := nk.AccountGetId(ctx, whoami.NakamaID.String())
	if err != nil {
		return err
	}

	whoami.Username = account.GetUser().GetUsername()

	if account.GetUser().GetCreateTime() != nil {
		whoami.CreateTime = account.GetUser().GetCreateTime().AsTime().UTC()
	}

	// Get the device links from the account
	whoami.DeviceLinks = make([]string, 0, len(account.GetDevices()))
	for _, device := range account.GetDevices() {
		whoami.DeviceLinks = append(whoami.DeviceLinks, fmt.Sprintf("`%s`", device.GetId()))
	}

	whoami.HasPassword = account.GetEmail() != ""

	var groupIDs []uuid.UUID
	if guildID != "" {
		groupID, err := GetGroupIDByGuildID(ctx, d.db, guildID)
		if err != nil {
			return fmt.Errorf("guild group not found")
		}

		groupIDs = []uuid.UUID{uuid.FromStringOrNil(groupID)}
	}

	whoami.GuildGroupMemberships, err = d.discordRegistry.GetGuildGroupMemberships(ctx, userID, groupIDs)
	if err != nil {
		return err
	}

	evrIDRecords, err := GetEVRRecords(ctx, logger, nk, userID.String())
	if err != nil {
		return err
	}

	whoami.EVRIDLogins = make(map[string]time.Time, len(evrIDRecords))

	for evrID, record := range evrIDRecords {
		whoami.EVRIDLogins[evrID.String()] = record.UpdateTime.UTC()
	}

	// Get the past displayNames
	displayNameObjs, err := GetDisplayNameRecords(ctx, logger, nk, userID.String())
	if err != nil {
		return err
	}
	// Sort displayNames by age
	sort.SliceStable(displayNameObjs, func(i, j int) bool {
		return displayNameObjs[i].GetUpdateTime().AsTime().After(displayNameObjs[j].GetUpdateTime().AsTime())
	})

	whoami.DisplayNames = make([]string, 0, len(displayNameObjs))
	for _, dn := range displayNameObjs {
		m := map[string]string{}
		if err := json.Unmarshal([]byte(dn.GetValue()), &m); err != nil {
			return err
		}
		whoami.DisplayNames = append(whoami.DisplayNames, m["displayName"])
	}

	// Get the MatchIDs for the user from it's presence
	presences, err := d.nk.StreamUserList(StreamModeService, userID.String(), "", StreamLabelMatchService, true, true)
	if err != nil {
		return err
	}

	whoami.MatchIDs = make([]string, 0, len(presences))
	for _, p := range presences {
		if p.GetStatus() != "" {
			mid := MatchIDFromStringOrNil(p.GetStatus())
			if mid.IsNil() {
				continue
			}
			whoami.MatchIDs = append(whoami.MatchIDs, mid.UUID.String())
		}
	}

	if !includePrivate {
		whoami.HasPassword = false
		whoami.ClientAddresses = nil
		whoami.DeviceLinks = nil
		if len(whoami.EVRIDLogins) > 0 {
			// Set the timestamp to zero
			for k := range whoami.EVRIDLogins {
				whoami.EVRIDLogins[k] = time.Time{}
			}
		}

		if guildID == "" {
			whoami.GuildGroupMemberships = nil
		}
		whoami.MatchIDs = nil
	}

	fields := []*discordgo.MessageEmbedField{
		{Name: "Nakama ID", Value: whoami.NakamaID.String(), Inline: false},
		{Name: "Online", Value: func() string {
			if len(presences) > 0 {
				return "Yes"
			}
			return "No"
		}(), Inline: true},
		{Name: "Create Time", Value: fmt.Sprintf("<t:%d:R>", whoami.CreateTime.Unix()), Inline: false},
		{Name: "Username", Value: whoami.Username, Inline: true},
		{Name: "Discord ID", Value: whoami.DiscordID, Inline: true},
		{Name: "Password Set", Value: func() string {
			if whoami.HasPassword {
				return "Yes"
			}
			return ""
		}(), Inline: true},
		{Name: "Linked Devices", Value: strings.Join(whoami.DeviceLinks, "\n"), Inline: false},
		{Name: "Display Names", Value: strings.Join(whoami.DisplayNames, "\n"), Inline: false},
		{Name: "Recent Logins", Value: func() string {
			lines := lo.MapToSlice(whoami.EVRIDLogins, func(k string, v time.Time) string {
				if v.IsZero() {
					// Don't use the timestamp
					return k
				} else {
					return fmt.Sprintf("<t:%d:R> - %s", v.Unix(), k)
				}
			})
			slices.Sort(lines)
			slices.Reverse(lines)
			return strings.Join(lines, "\n")
		}(), Inline: false},
		{Name: "Guild Memberships", Value: strings.Join(lo.Map(whoami.GuildGroupMemberships, func(m GuildGroupMembership, index int) string {
			s := m.GuildGroup.Name()
			roles := make([]string, 0)
			if m.isMember {
				roles = append(roles, "member")
			}
			if m.isModerator {
				roles = append(roles, "moderator")
			}
			if m.isServerHost {
				roles = append(roles, "server-host")
			}
			if m.isAllocator {
				roles = append(roles, "allocator")
			}
			if len(roles) > 0 {
				s += fmt.Sprintf(" (%s)", strings.Join(roles, ", "))
			}
			return s
		}), "\n"), Inline: false},
		{Name: "Current Match(es)", Value: strings.Join(lo.Map(whoami.MatchIDs, func(m string, index int) string {
			return fmt.Sprintf("https://echo.taxi/spark://c/%s", strings.ToUpper(m))
		}), "\n"), Inline: false},
	}

	// Remove any blank fields
	fields = lo.Filter(fields, func(f *discordgo.MessageEmbedField, _ int) bool {
		return f.Value != ""
	})

	// Send the response
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsEphemeral,
			Embeds: []*discordgo.MessageEmbed{
				{
					Title:  "EchoVRCE Account",
					Color:  0xCCCCCC,
					Fields: fields,
				},
			},
		},
	})
	return nil
}

func (d *DiscordAppBot) handlePrepareMatch(ctx context.Context, logger runtime.Logger, userID, discordID, guildID string, region, mode, level evr.Symbol, startTime time.Time) (l *MatchLabel, rtt float64, err error) {

	// Find a parking match to prepare

	groupID, err := GetGroupIDByGuildID(ctx, d.db, guildID)
	if err != nil {
		return nil, 0, status.Errorf(codes.NotFound, "guild not found: %s", guildID)
	}

	// Get a list of the groups that this user has allocate access to
	memberships, err := d.discordRegistry.GetGuildGroupMemberships(ctx, uuid.FromStringOrNil(userID), nil)
	if err != nil {
		return nil, 0, status.Errorf(codes.Internal, "failed to get guild group memberships: %v", err)
	}
	var membership *GuildGroupMembership
	allocatorGroupIDs := make([]string, 0, len(memberships))
	for _, m := range memberships {
		if m.GuildGroup.IsAllocator(userID) {
			allocatorGroupIDs = append(allocatorGroupIDs, m.GuildGroup.ID().String())
		}
		if m.GuildGroup.ID() == uuid.FromStringOrNil(groupID) {
			membership = &m
		}
	}

	if membership == nil {
		return nil, 0, status.Error(codes.PermissionDenied, "user is not a member of the guild")
	}

	regions := []evr.Symbol{region}

	if !membership.GuildGroup.IsAllocator(userID) {
		if membership.GuildGroup.Metadata.DisablePublicAllocateCommand {
			return nil, 0, status.Error(codes.PermissionDenied, "guild does not allow public allocation")
		} else {
			regions = append(regions, evr.DefaultRegion)
		}

		limiter := d.loadPrepareMatchRateLimiter(userID, groupID)
		if !limiter.Allow() {
			return nil, 0, status.Error(codes.ResourceExhausted, fmt.Sprintf("rate limit exceeded (%0.0f requests per minute)", limiter.Limit()*60))
		}
	}

	regionstrs := make([]string, 0, len(regions))
	for _, r := range regions {
		regionstrs = append(regionstrs, r.String())
	}

	query := fmt.Sprintf("+label.lobby_type:unassigned +label.broadcaster.group_ids:/(%s)/ +label.broadcaster.regions:/(%s)/", strings.Join(allocatorGroupIDs, "|"), strings.Join(regionstrs, "|"))

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
			if history, ok := latencyHistory[label.Broadcaster.Endpoint.GetExternalIP()]; ok {
				if len(history) == 0 {
					labelLatencies = append(labelLatencies, 0)
					continue
				}

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
	if membership.GuildGroup.IsAllocator(userID) {
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
func SnowflakeToTime(snowflakeID string) time.Time {
	// Discord snowflake epoch timestamp
	const discordEpoch = 1420070400000

	// Convert the ID to an integer
	id, _ := strconv.ParseInt(snowflakeID, 10, 64)

	// Extract the timestamp from the snowflake
	timestamp := (id >> 22) + discordEpoch

	// Return the time.Time object
	return time.Unix(timestamp/1000, (timestamp%1000)*1000000).UTC()
}

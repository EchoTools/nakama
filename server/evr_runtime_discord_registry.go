package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/go-playground/validator/v10"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	DiscordRegistryLookupCollection = "DiscordRegistry"
	DiscordRegistryLookupKey        = "LookupTables"
)

var (
	validate = validator.New(validator.WithRequiredStructEnabled())

	ErrGroupIsNotaGuild = fmt.Errorf("group is not a guild")
)

type LookupTable struct {
	sync.RWMutex
	Store map[string]string `json:"store"`
}

type DiscordRegistry interface {
	Get(discordId string) (nakamaId string, ok bool)
	GetBot() *discordgo.Session
	Logger() runtime.Logger
	RuntimeModule() runtime.NakamaModule
	Store(discordId string, nakamaId string)
	ClearCache(discordId string)
	GetDiscordIdByUserId(ctx context.Context, userId uuid.UUID) (discordId string, err error)
	GetUserIdByUsername(ctx context.Context, username string, create bool) (userId uuid.UUID, err error)
	UpdateAccount(ctx context.Context, userId uuid.UUID) error
	GetUserIdByDiscordId(ctx context.Context, discordId string, create bool) (userId uuid.UUID, err error)
	GetGuildByGroupId(ctx context.Context, groupId string) (*discordgo.Guild, error)
	ReplaceMentions(guildID, s string) string
	PopulateCache() (cnt int, err error)
	GetGuildGroupMetadata(ctx context.Context, groupId string) (metadata *GroupMetadata, err error)
	SetGuildGroupMetadata(ctx context.Context, groupId string, metadata *GroupMetadata) error
	// GetGuildMember looks up the Discord member by the guild ID and member ID. Potentially using the state cache.
	GetGuildMember(ctx context.Context, guildId, memberId string) (*discordgo.Member, error)
	SynchronizeGroup(ctx context.Context, guild *discordgo.Guild) error
	GetGuild(ctx context.Context, guildId string) (*discordgo.Guild, error)
	// GetGuildGroupMemberships looks up the guild groups by the user ID (and optionally by group IDs)
	GetGuildGroupMemberships(ctx context.Context, userId uuid.UUID, groupIDs []uuid.UUID) ([]GuildGroupMembership, error)
	// GetUser looks up the Discord user by the user ID. Potentially using the state cache.
	GetUser(ctx context.Context, discordId string) (*discordgo.User, error)
	UpdateGuildGroup(ctx context.Context, logger runtime.Logger, userID uuid.UUID, guildID string) error
	UpdateAllGuildGroupsForUser(ctx context.Context, logger runtime.Logger, userID uuid.UUID) error
	isModerator(ctx context.Context, guildID, discordID string) (isModerator bool, isGlobal bool, err error)
	IsGlobalModerator(ctx context.Context, userID uuid.UUID) (ok bool, err error)
	ProcessRequest(ctx context.Context, session *sessionWS, in evr.Message) error
	CheckUser2FA(ctx context.Context, userID uuid.UUID) (bool, error)
}

type LocalDiscordRegistry struct {
	sync.RWMutex
	ctx      context.Context
	nk       runtime.NakamaModule
	logger   runtime.Logger
	metrics  Metrics
	pipeline *Pipeline

	bot       *discordgo.Session // The bot
	botUserID uuid.UUID

	cache         sync.Map // Generic cache for map[discordId]nakamaId lookup
	expiringCache *Cache
}

func NewLocalDiscordRegistry(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, metrics Metrics, config Config, pipeline *Pipeline, dg *discordgo.Session) (r *LocalDiscordRegistry) {

	dg.StateEnabled = true

	discordRegistry := &LocalDiscordRegistry{
		ctx:           ctx,
		nk:            nk,
		logger:        logger,
		metrics:       metrics,
		pipeline:      pipeline,
		bot:           dg,
		cache:         sync.Map{},
		expiringCache: NewCache(),
	}

	dg.AddHandler(func(s *discordgo.Session, m *discordgo.Ready) {
		discordRegistry.PopulateCache() // Populate the cache with all the guilds and their roles
	})

	return discordRegistry
}

func (r *LocalDiscordRegistry) GetBot() *discordgo.Session {
	return r.bot
}

func (r *LocalDiscordRegistry) Logger() runtime.Logger {
	return r.logger
}

func (r *LocalDiscordRegistry) RuntimeModule() runtime.NakamaModule {
	return r.nk
}

// PopulateCache populates the lookup cache with all the guilds and their roles
func (r *LocalDiscordRegistry) PopulateCache() (cnt int, err error) {
	ctx, cancel := context.WithTimeout(r.ctx, 10*time.Second)
	defer cancel()

	userID, err := r.GetUserIdByDiscordId(ctx, r.bot.State.User.ID, true)
	if err == nil {
		r.botUserID = userID
	}
	// Populate the cache with all the guild groups
	cnt = 0
	var groups []*api.Group
	cursor := ""
	for {
		groups, cursor, err = r.nk.GroupsList(ctx, "", "guild", nil, nil, 100, cursor)
		if err != nil {
			return
		}
		for _, group := range groups {
			// Check if the cache already has this group -> discordId entry
			if d, ok := r.Get(group.Id); ok {
				// Check that the reverse is true
				if g, ok := r.Get(d); ok {
					if g == group.Id {
						continue
					}
				}
			}

			metadata := &GroupMetadata{}
			if err := json.Unmarshal([]byte(group.Metadata), metadata); err != nil {
				r.logger.Warn(fmt.Sprintf("Error unmarshalling group metadata for group %s:  %s", group.Id, err))
			}

			if metadata.GuildID != "" {
				guild, err := r.GetGuild(r.ctx, metadata.GuildID)
				if err != nil {
					if code := discordError(err); code == discordgo.ErrCodeUnknownGuild {
						r.logger.Warn(fmt.Sprintf("Bot is no longer member of guild `%s`. removing.", group.Name))
						err := r.nk.GroupDelete(ctx, group.Id)
						if err != nil {
							r.logger.Error(fmt.Sprintf("Error deleting group %s: %s", group.Id, err))
						}
						r.ClearCache(metadata.GuildID)
						r.ClearCache(group.Id)
					} else {
						r.logger.Error(fmt.Sprintf("Error getting guild `%s`: %s", metadata.GuildID, err))
					}
					continue
				}
				r.Store(metadata.GuildID, group.Id)
				r.Store(group.Id, metadata.GuildID)
				r.bot.State.GuildAdd(guild)
				cnt++
			}

		}
		if cursor == "" {
			break
		}
	}

	r.logger.Info("Populated registry lookup cache with %d guilds/roles/users", cnt)
	return
}

// Get looks up the Nakama group ID by the Discord guild or role ID from cache.
func (r *LocalDiscordRegistry) Get(discordId string) (nakamaId string, ok bool) {
	if v, ok := r.cache.Load(discordId); ok {
		return v.(string), ok
	}
	return "", false
}

// Store adds or updates the Nakama group ID by the Discord guild or role ID
func (r *LocalDiscordRegistry) Store(discordId string, nakamaId string) {
	if discordId == "" || nakamaId == "" {
		r.logger.Error("discordId and nakamaId cannot be nil")
	}
	if discordId == "00000000-0000-0000-0000-000000000000" || nakamaId == "00000000-0000-0000-0000-000000000000" {
		r.logger.Error("discordId and nakamaId cannot be nil")
	}
	r.cache.Store(discordId, nakamaId)
}

// Delete removes the Nakama group ID by the Discord guild or role ID
func (r *LocalDiscordRegistry) ClearCache(discordId string) {
	r.cache.Delete(discordId)
}

// GetUser looks up the Discord user by the user ID. Potentially using the state cache.
func (r *LocalDiscordRegistry) GetUser(ctx context.Context, discordId string) (*discordgo.User, error) {
	if discordId == "" {
		return nil, fmt.Errorf("discordId is required")
	}

	// Try to find the user in a guild state first.
	for _, guild := range r.bot.State.Guilds {
		if member, err := r.bot.State.Member(guild.ID, discordId); err == nil {
			if member.User == nil || member.User.Username == "" || member.User.GlobalName == "" {
				continue
			}
			return member.User, nil
		}
	}

	// Make requests for the known guilds of the user
	guildMap, err := GetGuildGroupIDsByUser(ctx, r.pipeline.db, discordId)
	if err != nil {
		return nil, fmt.Errorf("error getting guilds by user: %w", err)
	}

	for _, guildID := range guildMap {
		if member, err := r.bot.GuildMember(guildID, discordId); err == nil {
			if member.User == nil || member.User.Username == "" || member.User.GlobalName == "" {
				continue
			}
			return member.User, nil
		}
	}
	// Make a request for the user from the API
	return r.bot.User(discordId)
}

// GetGuild looks up the Discord guild by the guild ID. Potentially using the state cache.
func (r *LocalDiscordRegistry) GetGuild(ctx context.Context, guildId string) (*discordgo.Guild, error) {

	if guildId == "" {
		return nil, fmt.Errorf("guildId is required")
	}
	// Check the cache
	if guild, err := r.bot.State.Guild(guildId); err == nil {
		return guild, nil
	}
	return r.bot.Guild(guildId)
}

// GetGuildByGroupId looks up the Discord guild by the group ID. Potentially using the state cache.
func (r *LocalDiscordRegistry) GetGuildByGroupId(ctx context.Context, groupId string) (*discordgo.Guild, error) {
	if groupId == "" {
		return nil, fmt.Errorf("guildId is required")
	}
	// Get the guild group metadata
	md, err := r.GetGuildGroupMetadata(ctx, groupId)
	if err != nil {
		return nil, fmt.Errorf("error getting guild group metadata: %w", err)
	}
	return r.GetGuild(ctx, md.GuildID)

}

// GetUserIdByMemberId looks up the Nakama user ID by the Discord user ID
func (r *LocalDiscordRegistry) GetUserIdByUsername(ctx context.Context, username string, create bool) (userId uuid.UUID, err error) {
	if username == "" {
		return userId, fmt.Errorf("username is required")
	}

	// Lookup the user by the username
	users, err := r.nk.UsersGetUsername(ctx, []string{username})
	if err != nil {
		return userId, err
	}
	if len(users) == 0 {
		return userId, status.Error(codes.NotFound, "User not found")
	}
	userId = uuid.FromStringOrNil(users[0].Id)
	return userId, nil
}

// GetGuildMember looks up the Discord member by the guild ID and member ID. Potentially using the state cache.
func (r *LocalDiscordRegistry) GetGuildMember(ctx context.Context, guildId, memberId string) (*discordgo.Member, error) {
	// Check if guildId and memberId are provided
	if guildId == "" {
		return nil, fmt.Errorf("guildId is required")
	}
	if memberId == "" {
		return nil, fmt.Errorf("memberId is required")
	}

	// Try to find the member in the guild state (cache) first
	if member, err := r.bot.State.Member(guildId, memberId); err == nil {
		return member, nil
	}

	// If member is not found in the cache, get it from the API
	member, err := r.bot.GuildMember(guildId, memberId)
	if err != nil {
		if restError, _ := err.(*discordgo.RESTError); errors.As(err, &restError) && restError.Message != nil && restError.Message.Code == discordgo.ErrCodeUnknownMember {
			return nil, nil
		}
		return nil, fmt.Errorf("error getting guild member: %w", err)
	}

	r.bot.State.MemberAdd(member)
	return member, nil
}

func (r *LocalDiscordRegistry) GetGuildGroupMetadata(ctx context.Context, groupId string) (*GroupMetadata, error) {
	// Check if groupId is provided
	if groupId == "" {
		return nil, fmt.Errorf("groupId is required")
	}

	// Fetch the group using the provided groupID
	groups, err := r.nk.GroupsGetId(ctx, []string{groupId})
	if err != nil {
		return nil, fmt.Errorf("error getting group (%s): %w", groupId, err)
	}

	// Check if the group exists
	if len(groups) == 0 {
		return nil, fmt.Errorf("group not found: %s", groupId)
	}

	if groups[0].LangTag != "guild" {
		return nil, ErrGroupIsNotaGuild
	}
	// Extract the metadata from the group
	data := groups[0].GetMetadata()

	// Unmarshal the metadata into a GroupMetadata struct
	guildGroup := &GroupMetadata{}
	if err := json.Unmarshal([]byte(data), guildGroup); err != nil {
		return nil, fmt.Errorf("error unmarshalling group metadata: %w", err)
	}

	// Update the cache
	r.Store(groupId, guildGroup.GuildID)
	r.Store(guildGroup.GuildID, groupId)

	key := groupId + ":debug-channel"
	r.expiringCache.Set(key, guildGroup.DebugChannel, 10*time.Minute)

	// Return the unmarshalled GroupMetadata
	return guildGroup, nil
}

func (r *LocalDiscordRegistry) SetGuildGroupMetadata(ctx context.Context, groupId string, metadata *GroupMetadata) error {
	// Check if groupId is provided
	if groupId == "" {
		return fmt.Errorf("groupId is required")
	}

	// Marshal the metadata
	mdMap, err := metadata.MarshalToMap()
	if err != nil {
		return fmt.Errorf("error marshalling group metadata: %w", err)
	}

	// Get the group
	groups, err := r.nk.GroupsGetId(ctx, []string{groupId})
	if err != nil {
		return fmt.Errorf("error getting group: %w", err)
	}
	g := groups[0]

	// Update the group
	if err := r.nk.GroupUpdate(ctx, g.Id, SystemUserID, g.Name, g.CreatorId, g.LangTag, g.Description, g.AvatarUrl, g.Open.Value, mdMap, int(g.MaxCount)); err != nil {
		return fmt.Errorf("error updating group: %w", err)
	}

	return nil
}

type GuildGroup struct {
	Metadata GroupMetadata
	Group    *api.Group
}

func (g *GuildGroup) GuildID() string {
	return g.Metadata.GuildID
}

func (g *GuildGroup) Name() string {
	return g.Group.Name
}

func (g *GuildGroup) Description() string {
	return g.Group.Description
}

func (g *GuildGroup) ID() uuid.UUID {
	return uuid.FromStringOrNil(g.Group.Id)
}

func (g *GuildGroup) Size() int {
	return int(g.Group.EdgeCount)
}

func (g *GuildGroup) ServerHostUserIDs() []string {
	return g.Metadata.ServerHostUserIDs
}

func (g *GuildGroup) AllocatorUserIDs() []string {
	return g.Metadata.AllocatorUserIDs
}
func NewGuildGroup(group *api.Group) *GuildGroup {

	md := &GroupMetadata{}
	if err := json.Unmarshal([]byte(group.Metadata), md); err != nil {
		return nil
	}

	return &GuildGroup{
		Metadata: *md,
		Group:    group,
	}
}

type GuildGroupMembership struct {
	GuildGroup   GuildGroup
	isMember     bool
	isModerator  bool // Admin
	isServerHost bool // Broadcaster Host
	canAllocate  bool // Can allocate servers with slash command
}

func NewGuildGroupMembership(group *api.Group, userID uuid.UUID, state api.UserGroupList_UserGroup_State) GuildGroupMembership {
	gg := NewGuildGroup(group)

	return GuildGroupMembership{
		GuildGroup:   *gg,
		isMember:     state <= api.UserGroupList_UserGroup_MEMBER,
		isModerator:  state <= api.UserGroupList_UserGroup_ADMIN,
		isServerHost: slices.Contains(gg.ServerHostUserIDs(), userID.String()),
		canAllocate:  slices.Contains(gg.AllocatorUserIDs(), userID.String()),
	}
}

// GetGuildGroupMemberships looks up the guild groups by the user ID
func (r *LocalDiscordRegistry) GetGuildGroupMemberships(ctx context.Context, userID uuid.UUID, groupIDs []uuid.UUID) ([]GuildGroupMembership, error) {
	// Check if userId is provided
	if userID == uuid.Nil {
		return nil, fmt.Errorf("userId is required")
	}
	userIDStr := userID.String()
	memberships := make([]GuildGroupMembership, 0)
	cursor := ""
	for {
		// Fetch the groups using the provided userId
		userGroups, _, err := r.nk.UserGroupsList(ctx, userIDStr, 100, nil, cursor)
		if err != nil {
			return nil, fmt.Errorf("error getting user groups: %w", err)
		}
		for _, ug := range userGroups {
			g := ug.GetGroup()
			if g.GetLangTag() != "guild" {
				continue
			}
			if len(groupIDs) > 0 && !slices.Contains(groupIDs, uuid.FromStringOrNil(g.GetId())) {
				continue
			}

			membership := NewGuildGroupMembership(g, userID, api.UserGroupList_UserGroup_State(ug.GetState().GetValue()))

			memberships = append(memberships, membership)
		}
		if cursor == "" {
			break
		}
	}
	return memberships, nil
}

// UpdateAccount updates the Nakama account with the Discord user data
func (r *LocalDiscordRegistry) UpdateAccount(ctx context.Context, userID uuid.UUID) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	logger := r.logger.WithField("function", "UpdateAccount")
	discordId, err := r.GetDiscordIdByUserId(ctx, userID)
	if err != nil {
		return fmt.Errorf("error getting discord id: %v", err)
	}

	timer := time.Now()
	if r.metrics != nil {

		defer func() { logger.Debug("UpdateAccount took %dms", time.Since(timer)/time.Millisecond) }()
		defer func() {
			r.metrics.CustomTimer("UpdateAccountFn_duration", nil, time.Since(timer))
		}()
	}

	// Get the discord User
	u, err := r.GetUser(ctx, discordId)
	if err != nil {
		return fmt.Errorf("error getting discord user: %v", err)
	}

	// Get the nakama account for this discord user
	userId, err := r.GetUserIdByDiscordId(ctx, discordId, true)
	if err != nil {
		return fmt.Errorf("error getting nakama user: %v", err)
	}

	// Map Discord user data onto Nakama account data
	username := u.Username
	s := strings.SplitN(u.Locale, "-", 2)
	langTag := s[0]
	avatar := u.AvatarURL("512")

	// Update the basic account details

	if err := r.nk.AccountUpdateId(ctx, userId.String(), username, nil, "", "", "", langTag, avatar); err != nil {
		r.logger.Error("Error updating account %s: %v", username, err)
	}

	r.Store(discordId, userId.String())
	r.Store(userId.String(), discordId)

	return nil
}

func (r *LocalDiscordRegistry) UpdateGuildGroup(ctx context.Context, logger runtime.Logger, userID uuid.UUID, guildID string) error {
	// If discord bot is not responding, return
	if r.bot == nil {
		return fmt.Errorf("discord bot is not responding")
	}
	userIDStrs := []string{userID.String()}
	// Get teh user's discordID
	discordID, err := r.GetDiscordIdByUserId(ctx, userID)
	if err != nil {
		return fmt.Errorf("error getting discord id: %v", err)
	}

	groupIDStr, found := r.Get(guildID)
	if !found || groupIDStr == "" {
		return fmt.Errorf("group not found: `%s`", guildID)
	}
	groupID := uuid.FromStringOrNil(groupIDStr)

	md, err := r.GetGuildGroupMetadata(ctx, groupIDStr)
	if err != nil {
		return fmt.Errorf("error getting guild group metadata: %v", err)
	}

	// Get the member
	member, err := r.GetGuildMember(ctx, guildID, discordID)
	// return if the context is cancelled
	if err != nil {
		return fmt.Errorf("error getting guild member: %v", err)
	}

	// Get all of the user's memberships
	memberships, err := r.GetGuildGroupMemberships(ctx, userID, []uuid.UUID{groupID})
	if err != nil {
		return fmt.Errorf("error getting guild group memberships: %v", err)
	}
	var membership *GuildGroupMembership
	for _, m := range memberships {
		if m.GuildGroup.GuildID() == guildID {
			membership = &m
			break
		}
	}
	// If the member is not found, the user is not in the guild
	if member == nil {
		if membership != nil {
			if err := r.nk.GroupUsersKick(ctx, SystemUserID, groupIDStr, userIDStrs); err != nil {
				return fmt.Errorf("error kicking user from group: %w", err)
			}
		}
		return nil
	}

	currentRoles := member.Roles

	if membership == nil {
		if md.MemberRole == "" || slices.Contains(currentRoles, md.MemberRole) {
			if err := r.nk.GroupUsersAdd(ctx, SystemUserID, groupIDStr, userIDStrs); err != nil {
				return fmt.Errorf("error adding user to group: %w", err)
			}
		}
	}

	// Get the current membership state of the user
	memberships, err = r.GetGuildGroupMemberships(ctx, userID, []uuid.UUID{groupID})
	if err != nil {
		return fmt.Errorf("error getting guild group memberships: %v", err)
	}

	if len(memberships) == 0 {
		return fmt.Errorf("unexpected: no memberships found for user %s", userID)
	}
	membership = &memberships[0]

	isSuspended := slices.Contains(currentRoles, md.SuspensionRole)

	if md.ModeratorRole != "" {
		// Make sure the user is an "admin" in the group
		if slices.Contains(currentRoles, md.ModeratorRole) {
			if !membership.isModerator {
				if err := r.nk.GroupUsersPromote(ctx, SystemUserID, groupIDStr, userIDStrs); err != nil {
					return fmt.Errorf("error promoting user to moderator: %w", err)
				}
			}
		} else if membership.isModerator {
			if err := r.nk.GroupUsersDemote(ctx, SystemUserID, groupIDStr, userIDStrs); err != nil {
				return fmt.Errorf("error demoting user from moderator: %w", err)
			}
		}
	}
	userIDStr := userID.String()

	needsUpdate := false
	if md.ServerHostRole != "" && slices.Contains(currentRoles, md.ServerHostRole) != slices.Contains(md.ServerHostUserIDs, userIDStr) {
		// Needs updating. If the userID is in, remove it. If it's missing, add it.

		if slices.Contains(md.ServerHostUserIDs, userIDStr) {
			for i, v := range md.ServerHostUserIDs {
				if v == userIDStr {
					md.ServerHostUserIDs = append(md.ServerHostUserIDs[:i], md.ServerHostUserIDs[i+1:]...)
				}
			}
		} else {
			md.ServerHostUserIDs = append(md.ServerHostUserIDs, userID.String())
		}

		needsUpdate = true
	}

	if md.AllocatorRole != "" && slices.Contains(currentRoles, md.AllocatorRole) != slices.Contains(md.AllocatorUserIDs, userIDStr) {
		// Needs updating. If the userID is in, remove it. If it's missing, add it.

		if slices.Contains(md.AllocatorUserIDs, userIDStr) {
			for i, v := range md.AllocatorUserIDs {
				if v == userIDStr {
					md.AllocatorUserIDs = append(md.AllocatorUserIDs[:i], md.AllocatorUserIDs[i+1:]...)
				}
			}
		} else {
			md.AllocatorUserIDs = append(md.AllocatorUserIDs, userID.String())
		}
		needsUpdate = true
	}

	if needsUpdate {
		mdMap, err := md.MarshalToMap()
		if err != nil {
			return fmt.Errorf("error marshalling group metadata: %w", err)
		}
		// Get the group
		groups, err := r.nk.GroupsGetId(ctx, []string{groupIDStr})
		if err != nil {
			return fmt.Errorf("error getting group: %w", err)
		}
		g := groups[0]

		if err := r.nk.GroupUpdate(ctx, g.Id, SystemUserID, g.Name, g.CreatorId, g.LangTag, g.Description, g.AvatarUrl, g.Open.Value, mdMap, int(g.MaxCount)); err != nil {
			return fmt.Errorf("error updating group: %w", err)
		}
	}
	if isSuspended {

		// If the player has a match connection, disconnect it.
		subject := userID.String()
		users, err := r.nk.StreamUserList(StreamModeService, subject, "", StreamLabelMatchService, true, true)
		if err != nil {
			r.logger.Error("Error getting stream users: %w", err)
		}

		// Disconnect any matchmaking sessions (this will put them back to the login screen)
		for _, user := range users {
			// Disconnect the user
			if user.GetUserId() == userID.String() {

				go func(userID, sessionID string) {
					r.logger.Debug("Disconnecting suspended user %s match session: %s", userID, sessionID)
					// Add a wait time, otherwise the user will not see the suspension message
					<-time.After(15 * time.Second)
					if err := r.nk.SessionDisconnect(ctx, sessionID, runtime.PresenceReasonDisconnect); err != nil {
						r.logger.Error("Error disconnecting suspended user: %w", err)
					}
				}(user.GetUserId(), user.GetSessionId())
			}
		}
	}

	return nil
}

func (r *LocalDiscordRegistry) UpdateAllGuildGroupsForUser(ctx context.Context, logger runtime.Logger, userID uuid.UUID) error {
	// Check every guild the bot is in for this user.
	for _, guild := range r.bot.State.Guilds {
		if err := r.UpdateGuildGroup(ctx, logger, userID, guild.ID); err != nil {
			continue
		}
	}
	return nil
}

// GetUserIdByDiscordId looks up, or creates, the Nakama user ID by the Discord user ID; potentially using the cache.
func (r *LocalDiscordRegistry) GetUserIdByDiscordId(ctx context.Context, discordID string, create bool) (userID uuid.UUID, err error) {

	if discordID == "" {
		return userID, fmt.Errorf("discordId is required")
	}

	// Check the cache
	if s, found := r.Get(discordID); found {
		userID, err = uuid.FromString(s)
		if err != nil {
			return userID, fmt.Errorf("error parsing user id: %w", err)
		}
		return userID, nil
	}

	userIDstr, err := GetUserIDByDiscordID(ctx, r.pipeline.db, discordID)
	if err != nil {
		// Get the user from discord
		discordUser, err := r.GetUser(ctx, discordID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("error getting discord user %s: %w", discordID, err)
		}
		if discordUser == nil {
			return uuid.Nil, fmt.Errorf("discord user not found: %s", discordID)
		}
		username := discordUser.Username
		if create {
			userIDstr, _, _, err = r.nk.AuthenticateCustom(ctx, discordID, username, create)
			if err != nil {
				return uuid.Nil, fmt.Errorf("error authenticating user %s: %w", discordID, err)
			}
		} else {
			return uuid.Nil, fmt.Errorf("user not found: %s", discordID)
		}
	}

	userID = uuid.FromStringOrNil(userIDstr)

	if userID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("uuid is nil")
	}

	defer r.Store(discordID, userID.String())
	defer r.Store(userID.String(), discordID)

	return userID, err

}

func (r *LocalDiscordRegistry) GetGroupIDbyGuildID(ctx context.Context, guildID string) (groupID uuid.UUID, err error) {
	if guildID == "" {
		return uuid.Nil, fmt.Errorf("guildID is required")
	}

	if v, ok := r.cache.Load(guildID); ok {
		return uuid.FromStringOrNil(v.(string)), nil
	}

	return groupID, nil
}

// GetDiscordIdByUserId looks up the Discord user ID by the Nakama user ID; potentially using the cache.
func (r *LocalDiscordRegistry) GetDiscordIdByUserId(ctx context.Context, userId uuid.UUID) (discordId string, err error) {
	if userId == uuid.Nil {
		return "", fmt.Errorf("userId is required")
	}

	// Check the cache
	if v, ok := r.cache.Load(userId.String()); ok {
		return v.(string), nil
	}

	// Lookup the discord user by the nakama user id
	account, err := r.nk.AccountGetId(ctx, userId.String())
	if err != nil {
		return "", err
	}

	discordId = account.GetCustomId()

	// Store the discordId and userId in the cache when the function returns
	defer r.Store(discordId, userId.String())

	return discordId, nil
}

// ReplaceMentions replaces the discord user mentions with the user's display name
func (r *LocalDiscordRegistry) ReplaceMentions(guildID, s string) string {
	s = strings.Replace(s, "\\u003c", " <", -1)
	s = strings.Replace(s, "\\u003e", "> ", -1)
	f := strings.Fields(s)
	for i, v := range f {
		if strings.HasPrefix(v, "<@") && strings.HasSuffix(v, ">") {
			f[i] = strings.Trim(v, "<@>")
			u, err := r.bot.GuildMember(guildID, f[i])
			if err != nil {
				continue
			}
			f[i] = "@" + u.DisplayName()
		}
	}
	return strings.Join(f, " ")
}

func parseDuration(s string) (time.Duration, error) {

	f := strings.Fields(s)
	if len(f) != 2 {
		return 0, fmt.Errorf("invalid duration: invalid number of fields: %s", s)
	}
	d, err := strconv.Atoi(f[0])
	if err != nil {
		return 0, fmt.Errorf("invalid duration: unable to parse: %s", s)
	}

	switch f[1][:1] {
	case "s":
		return time.Duration(d) * time.Second, nil
	case "m":
		return time.Duration(d) * time.Minute, nil
	case "h":
		return time.Duration(d) * time.Hour, nil
	case "d":
		return time.Duration(d) * 24 * time.Hour, nil
	case "w":
		return time.Duration(d) * 7 * 24 * time.Hour, nil
	}
	return 0, fmt.Errorf("invalid duration: invalid unit: %s", s)
}

// Helper function to get or create a group
func (r *LocalDiscordRegistry) findOrCreateGroup(ctx context.Context, groupId, name, description, ownerId, langtype string, guild *discordgo.Guild) (*api.Group, error) {
	nk := r.nk
	var group *api.Group

	// Try to retrieve the group by ID
	if groupId != "" {
		groups, err := nk.GroupsGetId(ctx, []string{groupId})
		if err != nil {
			return nil, fmt.Errorf("error getting group by id: %w", err)
		}
		if len(groups) != 0 {
			group = groups[0]
		}
	}

	// Next attempt to find the group by name.
	if group == nil {
		if groups, _, err := nk.GroupsList(ctx, name, "", nil, nil, 1, ""); err == nil && len(groups) == 1 {
			group = groups[0]
		}
	}
	// If the group was found, update the lookup table

	// If the group wasn't found, create it
	if group == nil {
		md := GroupMetadata{
			GuildID: guild.ID,
		}
		gm, err := md.MarshalToMap()
		if err != nil {
			return nil, fmt.Errorf("error marshalling group metadata: %w", err)
		}
		// Create the group
		group, err = nk.GroupCreate(ctx, r.botUserID.String(), name, ownerId, langtype, description, guild.IconURL("512"), false, gm, 100000)
		if err != nil {
			return nil, fmt.Errorf("error creating group: %w", err)
		}
	}

	if langtype == "guild" {
		// Set the group in the registry
		r.Store(guild.ID, group.GetId())
	}

	return group, nil
}

func (r *LocalDiscordRegistry) SynchronizeGroup(ctx context.Context, guild *discordgo.Guild) error {
	var err error

	// Get the owner's nakama user
	uid, err := r.GetUserIdByDiscordId(ctx, guild.OwnerID, true)
	if err != nil {
		return fmt.Errorf("error getting guild owner id: %w", err)
	}
	ownerId := uid.String()
	// Check the lookup table for the guild group
	groupId, found := r.Get(guild.ID)
	if !found {
		groupId = ""
	}

	// Find or create the guild group
	guildGroup, err := r.findOrCreateGroup(ctx, groupId, guild.Name, guild.Description, ownerId, "guild", guild)
	if err != nil {
		return fmt.Errorf("error finding/creating guild group: %w", err)
	}

	// Unmarshal the guild group's metadata for updating.
	guildMetadata := &GroupMetadata{}
	if err := json.Unmarshal([]byte(guildGroup.GetMetadata()), guildMetadata); err != nil {
		return fmt.Errorf("error unmarshalling group metadata: %w", err)
	}

	// Set the group Id in the metadata so it can be found during an error.
	guildMetadata.GuildID = guild.ID

	// Set a default rules, or get the rules from the channel topic
	guildMetadata.RulesText = "No #rules channel found. Please create the channel and set the topic to the rules."
	channels, err := r.bot.GuildChannels(guild.ID)
	if err != nil {
		return fmt.Errorf("error getting guild channels: %w", err)
	}
	for _, channel := range channels {
		if channel.Type == discordgo.ChannelTypeGuildText && channel.Name == "rules" {
			guildMetadata.RulesText = channel.Topic
			break
		}
	}

	// Rewrite the guild groups metadata
	md, err := guildMetadata.MarshalToMap()
	if err != nil {
		return fmt.Errorf("error marshalling group metadata: %w", err)
	}

	// Update the guild group
	if err := r.nk.GroupUpdate(ctx, guildGroup.GetId(), r.botUserID.String(), guild.Name, ownerId, "guild", guild.Description, guild.IconURL("512"), false, md, 100000); err != nil {
		return fmt.Errorf("error updating guild group: %w", err)
	}

	return nil
}

func (r *LocalDiscordRegistry) OnGuildMembersChunk(ctx context.Context, b *discordgo.Session, e *discordgo.GuildMembersChunk, logger runtime.Logger, nk runtime.NakamaModule, initializer runtime.Initializer) error {
	// Get the nakama group for the guild

	// Add all the members of the guild to the group, in chunks
	chunkSize := 10
	logger.Debug("Received guild member chunk %d of %d", chunkSize, len(e.Members))

	for i := 0; i < len(e.Members); i += chunkSize {
		members := e.Members[i:min(i+chunkSize, len(e.Members))]
		usernames := make([]string, len(members))
		for i, member := range members {
			usernames[i] = member.User.Username
		}

		users, err := nk.UsersGetUsername(ctx, usernames)
		if err != nil {
			return fmt.Errorf("error getting account Ids for guild members: %w", err)
		}

		accountIds := make([]string, len(users))
		for _, user := range users {
			accountIds[i] = user.Id
		}
		// Add the member to the group
		if err := nk.GroupUsersAdd(ctx, SystemUserID, members[0].GuildID, accountIds); err != nil {
			return fmt.Errorf("group add users error: %w", err)
		}
	}
	return nil
}

func (r *LocalDiscordRegistry) GetAllSuspensions(ctx context.Context, userId uuid.UUID) ([]*SuspensionStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Get the discordId for the userId
	discordId, err := r.GetDiscordIdByUserId(ctx, userId)
	if err != nil {
		return nil, err
	}

	// Get a list of the bot's guilds
	groups, err := r.GetGuildGroupMemberships(ctx, userId, nil)
	if err != nil {
		return nil, err
	}

	// Get the metadata for each guild and it's suspension roles
	suspensions := make([]*SuspensionStatus, 0)
	for _, gm := range groups {
		group := gm.GuildGroup
		md := group.Metadata
		// Get the guild member's roles
		member, err := r.GetGuildMember(ctx, md.GuildID, discordId)
		if err != nil {
			return nil, fmt.Errorf("error getting guild member: %w", err)
		}
		if member == nil {
			continue
		}
		// Look for an intersection between suspension roles and the member's roles
		if slices.Contains(member.Roles, md.SuspensionRole) {
			// Get the role's name
			role, err := r.bot.State.Role(md.GuildID, md.SuspensionRole)
			if err != nil {
				return nil, fmt.Errorf("error getting guild role: %w", err)
			}
			status := &SuspensionStatus{
				GuildId:       group.GuildID(),
				GuildName:     group.Name(),
				UserDiscordId: discordId,
				UserId:        userId.String(),
				RoleId:        md.SuspensionRole,
				RoleName:      role.Name,
			}
			// Apppend the suspension status to the list
			suspensions = append(suspensions, status)
		}
	}
	return suspensions, nil
}

func (r *LocalDiscordRegistry) isModerator(ctx context.Context, guildID, discordID string) (isModerator bool, isGlobal bool, err error) {

	userID, err := r.GetUserIdByDiscordId(ctx, discordID, false)
	if userID == uuid.Nil {
		return false, false, fmt.Errorf("error getting user id: %w", err)
	}

	groupID, found := r.Get(guildID)
	if !found {
		return false, false, fmt.Errorf("group not found for guild %s", guildID)
	}

	md, err := r.GetGuildGroupMetadata(ctx, groupID)
	if err != nil {
		return false, false, fmt.Errorf("error getting guild group metadata: %w", err)
	}

	// Get the member
	member, err := r.GetGuildMember(ctx, guildID, discordID)
	if err != nil {
		return false, false, fmt.Errorf("error getting guild member: %w", err)
	}
	if member == nil {
		return false, false, fmt.Errorf("member not found: %s", discordID)
	}
	// Check if the member has the moderator role
	return slices.Contains(member.Roles, md.ModeratorRole), false, nil
}

func (r *LocalDiscordRegistry) IsGlobalModerator(ctx context.Context, userID uuid.UUID) (bool, error) {
	return r.isSystemGroupMember(ctx, userID, "Global Moderators")
}

func (r *LocalDiscordRegistry) isSystemGroupMember(ctx context.Context, userID uuid.UUID, groupName string) (bool, error) {
	// Check if they are a member of the Global Moderators group
	groups, _, err := r.nk.UserGroupsList(ctx, userID.String(), 100, nil, "")
	if err != nil {
		return false, fmt.Errorf("error getting user groups: %w", err)
	}

	for _, g := range groups {
		if g.Group.LangTag == SystemGroupLangTag && g.Group.Name == groupName {
			return true, nil
		}
	}
	return false, nil
}

func (d *LocalDiscordRegistry) ProcessRequest(ctx context.Context, session *sessionWS, in evr.Message) error {

	groupID, ok := ctx.Value(ctxGroupIDKey{}).(uuid.UUID)
	if !ok {
		return nil
	}

	// Check the cache for the "<groupID>:#echovrce-debug" value that will provide the channelID
	channelID, found := d.Get(groupID.String() + ":#echovrce-debug")
	if !found {
		return nil
	}

	switch in := in.(type) {
	case *evr.LobbyCreateSessionRequest, *evr.LobbyFindSessionRequest, *evr.LobbyJoinSessionRequest:
		// Message the channel with the request info
		request := fmt.Sprintf("%T: %v", in, in)
		if _, err := d.bot.ChannelMessageSend(channelID, request); err != nil {
			return fmt.Errorf("error sending message: %w", err)
		}
	}
	return nil
}

func discordError(err error) int {
	if restError, _ := err.(*discordgo.RESTError); errors.As(err, &restError) && restError.Message != nil {
		return restError.Message.Code
	}
	return -1
}
func (d *LocalDiscordRegistry) CheckUser2FA(ctx context.Context, userID uuid.UUID) (bool, error) {
	discordID, err := GetDiscordIDByUserID(ctx, d.pipeline.db, userID.String())
	if err != nil {
		return false, fmt.Errorf("error getting discord id: %w", err)
	}
	user, err := d.bot.User(discordID)
	if err != nil {
		return false, fmt.Errorf("error getting discord user: %w", err)
	}
	return user.MFAEnabled, nil
}

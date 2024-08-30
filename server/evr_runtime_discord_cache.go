package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	ErrMemberNotFound = errors.New("member not found")
)

type QueueEntry struct {
	UserID  string
	GroupID string
}

// Responsible for caching and synchronizing data with Discord.
type DiscordCache struct {
	ctx      context.Context
	cancelFn context.CancelFunc
	logger   *zap.Logger

	config          Config
	metrics         Metrics
	pipeline        *Pipeline
	profileRegistry *ProfileRegistry
	statusRegistry  StatusRegistry
	nk              runtime.NakamaModule
	db              *sql.DB
	dg              *discordgo.Session

	queueCh       chan QueueEntry
	queueLimiters MapOf[QueueEntry, *rate.Limiter]

	idcache *MapOf[string, string]
}

func NewDiscordCache(ctx context.Context, logger *zap.Logger, config Config, metrics Metrics, pipeline *Pipeline, profileRegistry *ProfileRegistry, statusRegistry StatusRegistry, nk runtime.NakamaModule, db *sql.DB, dg *discordgo.Session) *DiscordCache {
	ctx, cancelFn := context.WithCancel(ctx)
	return &DiscordCache{
		ctx:      ctx,
		cancelFn: cancelFn,
		logger:   logger,

		config:          config,
		metrics:         metrics,
		pipeline:        pipeline,
		profileRegistry: profileRegistry,
		statusRegistry:  statusRegistry,
		nk:              nk,
		db:              db,
		dg:              dg,

		idcache:       &MapOf[string, string]{},
		queueCh:       make(chan QueueEntry, 150),
		queueLimiters: MapOf[QueueEntry, *rate.Limiter]{},
	}
}

func (c *DiscordCache) Stop() {
	c.cancelFn()
}

func (c *DiscordCache) Start() {
	dg := c.dg
	logger := c.logger.With(zap.String("module", "discord_cache"))

	cleanupTicker := time.NewTicker(time.Minute * 1)
	// Start the cache worker.
	go func() {
		for {
			select {
			case <-c.ctx.Done():
				return
			case entry := <-c.queueCh:
				// If the entry has a group ID, sync the user to the guild group.
				if entry.GroupID == "" {
					logger.Debug("Syncing user", zap.String("user_id", entry.UserID))
					if err := c.SyncUser(c.ctx, entry.UserID); err != nil {
						logger.Warn("Error syncing user", zap.String("user_id", entry.UserID), zap.Error(err))
					}
				} else {
					logger.Debug("Syncing guild group member", zap.String("user_id", entry.UserID), zap.String("group_id", entry.GroupID))
					if err := c.SyncGuildGroupMember(c.ctx, entry.UserID, entry.GroupID); err != nil {
						logger.Warn("Error syncing guild group member", zap.String("user_id", entry.UserID), zap.String("group_id", entry.GroupID), zap.Error(err))
					}
				}

			case <-cleanupTicker.C:
				// Cleanup the queue limiters.
				c.queueLimiters.Range(func(k QueueEntry, v *rate.Limiter) bool {
					if v.Tokens() >= float64(v.Burst()) {
						logger.Debug("Removing queue limiter", zap.Any("entry", k))

						c.queueLimiters.Delete(k)
					}
					return true
				})
			}
		}
	}()

	dg.AddHandler(func(s *discordgo.Session, m *discordgo.GuildCreate) {
		if err := c.handleGuildCreate(logger, s, m); err != nil {
			logger.Error("Error handling guild create", zap.Error(err))
		}
	})

	dg.AddHandler(func(s *discordgo.Session, m *discordgo.GuildUpdate) {
		if err := c.handleGuildUpdate(logger, s, m); err != nil {
			logger.Error("Error handling guild update", zap.Error(err))
		}
	})

	dg.AddHandler(func(s *discordgo.Session, m *discordgo.GuildDelete) {
		if err := c.handleGuildDelete(logger, s, m); err != nil {
			logger.Error("Error handling guild delete", zap.Error(err))
		}
	})

	dg.AddHandler(func(s *discordgo.Session, m *discordgo.GuildMemberAdd) {
		if err := c.handleMemberAdd(logger, s, m); err != nil {
			logger.Error("Error handling member add", zap.Error(err))
		}
	})

	dg.AddHandler(func(s *discordgo.Session, m *discordgo.GuildMemberUpdate) {
		if err := c.handleMemberUpdate(logger, s, m); err != nil {
			logger.Error("Error handling member update", zap.Error(err))
		}
	})

	dg.AddHandler(func(s *discordgo.Session, m *discordgo.GuildMemberRemove) {
		ctx, cancel := context.WithTimeout(c.ctx, time.Second*5)
		defer cancel()
		logger.Info("Member Remove", zap.Any("member", m.Member.User.ID))
		if err := c.GuildGroupMemberRemove(ctx, m.GuildID, m.Member.User.ID); err != nil {
			logger.Warn("Error removing guild group member", zap.Error(err))
		}
	})
	c.logger.Info("Starting Discord cache")
}

// Queue a user for caching/updating.
func (c *DiscordCache) Queue(userID string, groupID string) {
	queueEntry := QueueEntry{userID, groupID}
	limiter, _ := c.queueLimiters.LoadOrStore(queueEntry, rate.NewLimiter(rate.Every(time.Second*10), 1))
	if !limiter.Allow() {
		c.logger.Debug("Rate limited queue entry", zap.String("user_id", userID), zap.String("group_id", groupID))
		return
	}
	c.queueCh <- queueEntry
}

// Discord ID to Nakama UserID, with a lookup cache
func (d *DiscordCache) DiscordIDToUserID(discordID string) string {
	userID, ok := d.idcache.Load(discordID)
	if !ok {
		var err error
		userID, err = GetUserIDByDiscordID(context.Background(), d.db, discordID)
		if err != nil {
			return ""
		}
		d.idcache.Store(discordID, userID)
		d.idcache.Store(userID, discordID)
	}
	return userID
}

func (d *DiscordCache) UserIDToDiscordID(userID string) string {
	discordID, ok := d.idcache.Load(userID)
	if !ok {
		var err error
		discordID, err = GetDiscordIDByUserID(context.Background(), d.db, userID)
		if err != nil {
			return ""
		}
		d.idcache.Store(userID, discordID)
		d.idcache.Store(discordID, userID)
	}
	return discordID
}

// Guild ID to Nakama Group ID, with a lookup cache
func (d *DiscordCache) GuildIDToGroupID(guildID string) string {
	groupID, ok := d.idcache.Load(guildID)
	if !ok {
		var err error
		groupID, err = GetGroupIDByGuildID(context.Background(), d.db, guildID)
		if err != nil {
			return ""
		}
		d.idcache.Store(guildID, groupID)
		d.idcache.Store(groupID, guildID)
	}
	return groupID
}

func (c *DiscordCache) GroupIDToGuildID(groupID string) string {
	guildID, ok := c.idcache.Load(groupID)
	if !ok {
		var err error
		guildID, err = GetGuildIDByGroupID(context.Background(), c.db, groupID)
		if err != nil {
			return ""
		}
		c.idcache.Store(groupID, guildID)
		c.idcache.Store(guildID, groupID)
	}
	return guildID
}

// Sync's a user to all of their guilds.
func (c *DiscordCache) SyncUser(ctx context.Context, userID string) error {
	logger := c.logger

	md, err := GetAccountMetadata(ctx, c.nk, userID)
	if err != nil {
		return nil
	}
	discordID := md.account.GetCustomId()
	logger = logger.With(zap.String("uid", userID), zap.String("discord_id", discordID))

	memberships, err := GetGuildGroupMemberships(ctx, c.nk, userID, nil)
	if err != nil {
		return fmt.Errorf("error getting guild group memberships: %w", err)
	} else if len(memberships) == 0 {
		return fmt.Errorf("user not in guild group")
	}

	// Check if the user is missing nay groups for any guilds.
	currentGuildIDs := make(map[string]struct{}, len(memberships))
	for _, membership := range memberships {
		currentGuildIDs[membership.GuildGroup.GuildID()] = struct{}{}
	}

	// Update the user's existing membership to the groups.

	for _, membership := range memberships {
		logger := logger.With(zap.String("group_id", membership.GuildGroup.ID().String()))

		err := c.SyncGuildGroupMember(ctx, userID, membership.GuildGroup.ID().String())
		if err != nil {
			if errors.Is(err, ErrMemberNotFound) {
				logger.Warn("Member not found in guild group")
			}
			logger.Warn("Error syncing guild group member", zap.Error(err))
			continue
		}
	}

	// Use the first memberships to update the user data
	if len(memberships) == 0 {
		return nil
	}

	update := false
	guildID := memberships[0].GuildGroup.GuildID()

	member, _, err := c.GuildMember(guildID, discordID)
	if err != nil {
		return fmt.Errorf("error getting guild member: %w", err)
	}

	account, err := c.nk.AccountGetId(ctx, userID)
	if err != nil {
		return fmt.Errorf("error getting account: %w", err)
	}

	if account.User.Username != member.User.Username {
		update = true
	}

	langTag := strings.SplitN(member.User.Locale, "-", 2)[0]
	if account.User.LangTag != langTag {
		update = true
	}

	if account.User.AvatarUrl != member.User.AvatarURL("512") {
		update = true
	}

	if update {
		displayName := md.GetActiveGroupDisplayName()
		if err := c.nk.AccountUpdateId(ctx, account.User.Id, member.User.Username, md.MarshalMap(), displayName, "", "", langTag, member.User.AvatarURL("512")); err != nil {
			return fmt.Errorf("failed to update account: %w", err)
		}
	}

	return nil
}

func (c *DiscordCache) SyncGuildGroupMember(ctx context.Context, userID, groupID string) error {
	if userID == "" || groupID == "" {
		return nil
	}
	discordID := c.UserIDToDiscordID(userID)
	guildID := c.GroupIDToGuildID(groupID)
	member, _, err := c.GuildMember(guildID, discordID)
	if err != nil {
		// Remove the user from the guild group.

		if err := c.GuildGroupMemberRemove(ctx, guildID, userID); err != nil {
			return fmt.Errorf("failed to remove guild group member: %w", err)
		}
		return ErrMemberNotFound
	}

	// If they are not a member add them.
	memberships, err := GetGuildGroupMemberships(ctx, c.nk, userID, []string{groupID})
	if err != nil {
		return fmt.Errorf("error getting guild group memberships: %w", err)
	}
	if len(memberships) == 0 {
		// Add the user to the guild group.
		if err := c.nk.GroupUserJoin(ctx, groupID, userID, member.User.Username); err != nil {
			return fmt.Errorf("error adding user to group: %w", err)
		}
		memberships, err = GetGuildGroupMemberships(ctx, c.nk, userID, []string{groupID})
		if err != nil {
			return fmt.Errorf("error getting guild group memberships: %w", err)
		}
	}
	if len(memberships) == 0 {
		return fmt.Errorf("user not in guild group after update")
	}
	membership := memberships[0]

	accountMetadata, err := GetAccountMetadata(ctx, c.nk, userID)
	if err != nil {
		return fmt.Errorf("error getting account metadata: %w", err)
	}

	prevDisplayName := accountMetadata.GetDisplayName(membership.GuildGroup.ID().String())
	curDisplayName := sanitizeDisplayName(member.DisplayName())
	if prevDisplayName != "" && curDisplayName != "" && prevDisplayName != curDisplayName {

		if displayName, err := c.checkDisplayName(ctx, c.nk, accountMetadata.ID(), curDisplayName); err == nil {
			if err := c.recordDisplayName(ctx, c.nk, membership.GuildGroup.ID().String(), accountMetadata.ID(), displayName); err != nil {
				return fmt.Errorf("error setting display name: %w", err)
			}
			groupID := c.GuildIDToGroupID(member.GuildID)
			accountMetadata.SetGroupDisplayName(groupID, displayName)
		}
	}

	// Update the Guild Group's role cache if necessary.
	if membership.GuildGroup.UpdateRoleCache(userID, member.Roles) {
		g := membership.GuildGroup.Group
		mdMap := membership.GuildGroup.Metadata.MarshalMap()
		if err := c.nk.GroupUpdate(ctx, g.Id, SystemUserID, g.Name, g.CreatorId, g.LangTag, g.Description, g.AvatarUrl, g.Open.Value, mdMap, int(g.MaxCount)); err != nil {
			return fmt.Errorf("error updating group: %w", err)
		}
	}

	account, err := c.nk.AccountGetId(ctx, userID)
	if err != nil {
		return fmt.Errorf("error getting account: %w", err)
	}
	// Add the linked role if necessary.
	accountLinkedRole := membership.GuildGroup.Metadata.Roles.AccountLinked
	if accountLinkedRole != "" {
		if len(account.Devices) == 0 {
			if slices.Contains(member.Roles, accountLinkedRole) {
				// Remove the role
				if err := c.dg.GuildMemberRoleRemove(member.GuildID, member.User.ID, accountLinkedRole); err != nil {
					return fmt.Errorf("error adding role to member: %w", err)
				}
			}
		} else {
			if !slices.Contains(member.Roles, accountLinkedRole) {
				// Assign the role
				if err := c.dg.GuildMemberRoleAdd(member.GuildID, member.User.ID, accountLinkedRole); err != nil {
					return fmt.Errorf("error adding role to member: %w", err)
				}
			}
		}
	}

	return nil
}

// Loads/Adds a user to the cache.
func (c *DiscordCache) GuildMember(guildID, discordID string) (*discordgo.Member, bool, error) {
	var member *discordgo.Member
	var err error
	if member, err = c.dg.State.Member(guildID, discordID); err == nil {
		return member, true, nil
	} else if member, err = c.dg.GuildMember(guildID, discordID); err != nil {
		if restError, _ := err.(*discordgo.RESTError); errors.As(err, &restError) && restError.Message != nil && restError.Message.Code == discordgo.ErrCodeUnknownMember {
			return nil, false, ErrMemberNotFound
		}
		return nil, false, fmt.Errorf("error getting guild member: %w", err)
	}
	c.dg.State.MemberAdd(member)
	return member, false, nil
}

func (d *DiscordCache) updateGuild(ctx context.Context, logger *zap.Logger, guild *discordgo.Guild) error {

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

func (d *DiscordCache) handleGuildCreate(logger *zap.Logger, s *discordgo.Session, e *discordgo.GuildCreate) error {
	ctx, cancel := context.WithTimeout(d.ctx, time.Second*5)
	defer cancel()
	logger.Info("Guild Create", zap.Any("guild", e.Guild.ID))
	if err := d.updateGuild(ctx, logger, e.Guild); err != nil {
		return fmt.Errorf("failed to update guild: %w", err)
	}
	return nil
}

func (d *DiscordCache) handleGuildUpdate(logger *zap.Logger, s *discordgo.Session, e *discordgo.GuildUpdate) error {
	ctx, cancel := context.WithTimeout(d.ctx, time.Second*5)
	defer cancel()
	logger.Info("Guild Update", zap.Any("guild", e.Guild.ID))
	if err := d.updateGuild(ctx, logger, e.Guild); err != nil {
		return fmt.Errorf("failed to update guild: %w", err)
	}
	return nil
}

func (d *DiscordCache) handleGuildDelete(logger *zap.Logger, s *discordgo.Session, e *discordgo.GuildDelete) error {
	ctx, cancel := context.WithTimeout(d.ctx, time.Second*5)
	defer cancel()
	logger.Info("Guild Delete", zap.Any("guild", e.Guild.ID))
	groupID := d.GuildIDToGroupID(e.Guild.ID)
	if groupID == "" {
		return nil
	}

	if err := d.nk.GroupDelete(ctx, groupID); err != nil {
		return fmt.Errorf("error deleting group: %w", err)
	}
	return nil
}

func (d *DiscordCache) handleMemberAdd(logger *zap.Logger, s *discordgo.Session, e *discordgo.GuildMemberAdd) error {
	ctx, cancel := context.WithTimeout(d.ctx, time.Second*5)
	defer cancel()

	if d.DiscordIDToUserID(e.Member.User.ID) == "" {
		return nil
	}

	logger.Info("Member Add", zap.Any("member", e))

	if err := d.SyncGuildGroupMember(ctx, d.DiscordIDToUserID(e.Member.User.ID), d.GuildIDToGroupID(e.GuildID)); err != nil {
		return fmt.Errorf("failed to sync guild group member: %w", err)
	}

	return nil
}

func (d *DiscordCache) handleMemberUpdate(logger *zap.Logger, s *discordgo.Session, e *discordgo.GuildMemberUpdate) error {
	if e.Member == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(d.ctx, time.Second*5)
	defer cancel()

	if d.DiscordIDToUserID(e.Member.User.ID) == "" {
		return nil
	}
	logger.Info("Member Update", zap.Any("member", e))

	if err := d.SyncGuildGroupMember(ctx, d.DiscordIDToUserID(e.Member.User.ID), d.GuildIDToGroupID(e.GuildID)); err != nil {
		return fmt.Errorf("failed to sync guild group member: %w", err)
	}
	return nil
}

func (d *DiscordCache) GuildGroupMemberRemove(ctx context.Context, guildID, discordID string) error {

	userID := d.DiscordIDToUserID(discordID)
	if userID == "" {
		return nil
	}

	// Remove the user from the guild group
	groupID := d.GuildIDToGroupID(guildID)
	if groupID == "" {
		return nil
	}

	d.logger.Info("Member Remove", zap.Any("member", discordID))

	md, err := GetAccountMetadata(ctx, d.nk, userID)
	if err != nil {
		return fmt.Errorf("error getting account metadata: %w", err)
	}
	if err := d.nk.GroupUserLeave(ctx, groupID, userID, md.Username()); err != nil {
		return fmt.Errorf("error removing user from group: %w", err)
	}

	delete(md.GroupDisplayNames, groupID)
	if md.GetActiveGroupID().String() == groupID {
		md.SetActiveGroupID(uuid.Nil)
	}
	return nil
}

func (d *DiscordCache) checkDisplayName(ctx context.Context, nk runtime.NakamaModule, userID string, displayName string) (string, error) {

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

func (d *DiscordCache) recordDisplayName(ctx context.Context, nk runtime.NakamaModule, groupID string, userID string, displayName string) error {

	// Purge old display names
	records, err := GetDisplayNameRecords(ctx, nk, userID)
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

func (d *DiscordCache) CheckUser2FA(ctx context.Context, userID uuid.UUID) (bool, error) {
	discordID, err := GetDiscordIDByUserID(ctx, d.pipeline.db, userID.String())
	if err != nil {
		return false, fmt.Errorf("error getting discord id: %w", err)
	}
	user, err := d.dg.User(discordID)
	if err != nil {
		return false, fmt.Errorf("error getting discord user: %w", err)
	}
	return user.MFAEnabled, nil
}

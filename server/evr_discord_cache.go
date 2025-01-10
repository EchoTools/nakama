package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	ErrMemberNotFound = errors.New("member not found")
)

var mentionRegex = regexp.MustCompile(`<@([-0-9A-Fa-f]+?)>`)

type QueueEntry struct {
	DiscordID string
	GuildID   string
}

// Responsible for caching and synchronizing data with Discord.
type DiscordCache struct {
	ctx      context.Context
	cancelFn context.CancelFunc
	logger   *zap.Logger

	nk runtime.NakamaModule
	db *sql.DB
	dg *discordgo.Session

	queueCh chan QueueEntry

	idcache *MapOf[string, string]
}

func NewDiscordCache(ctx context.Context, logger *zap.Logger, config Config, metrics Metrics, nk runtime.NakamaModule, db *sql.DB, dg *discordgo.Session) *DiscordCache {
	ctx, cancelFn := context.WithCancel(ctx)

	guildGroups := &atomic.Value{}
	guildGroups.Store(make(map[string]*GuildGroup))

	return &DiscordCache{
		ctx:      ctx,
		cancelFn: cancelFn,
		logger:   logger,

		nk: nk,
		db: db,
		dg: dg,

		idcache: &MapOf[string, string]{},

		queueCh: make(chan QueueEntry, 250),
	}
}

func (c *DiscordCache) Stop() {
	c.cancelFn()
}

func (c *DiscordCache) Start() {
	dg := c.dg
	logger := c.logger.With(zap.String("module", "discord_cache"))

	queueCooldowns := make(map[QueueEntry]time.Time)
	// Start the cache worker.
	go func() {
		cooldownTicker := time.NewTicker(time.Second * 3)
		defer cooldownTicker.Stop()
		for {
			select {
			case <-c.ctx.Done():
				return
			case entry := <-c.queueCh:
				logger := logger.With(
					zap.String("discord_id", entry.DiscordID),
					zap.String("guild_id", entry.GuildID),
				)
				if _, ok := queueCooldowns[entry]; ok {
					continue
				}

				queueCooldowns[entry] = time.Now().Add(time.Second * 30)

				if err := c.syncMember(c.ctx, logger, entry.DiscordID, entry.GuildID); err != nil {
					logger.Warn("Error syncing guild group member", zap.Error(err))
				}
				logger.Debug("Synced guild group member")

			case <-cooldownTicker.C:

				for entry, t := range queueCooldowns {
					if time.Now().After(t) {
						delete(queueCooldowns, entry)
						if err := c.syncMember(c.ctx, logger, entry.DiscordID, entry.GuildID); err != nil {
							logger.Warn("Error syncing guild group member", zap.Error(err))
							continue
						}
						logger.Debug("Synced guild group member")
					}
				}
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
			logger.Error("Error handling guild delete", zap.Any("guildDelete", m), zap.Error(err))
		}
	})

	dg.AddHandler(func(s *discordgo.Session, m *discordgo.GuildMemberAdd) {
		if err := c.handleMemberAdd(logger, s, m); err != nil {
			logger.Error("Error handling member add", zap.Any("guildMemberAdd", m), zap.Error(err))
		}
	})

	dg.AddHandler(func(s *discordgo.Session, m *discordgo.GuildMemberUpdate) {
		if err := c.handleMemberUpdate(logger, s, m); err != nil {
			logger.Error("Error handling member update", zap.Any("guildMemberUpdate", m), zap.Error(err))
		}
	})

	dg.AddHandler(func(s *discordgo.Session, m *discordgo.GuildMemberRemove) {
		ctx, cancel := context.WithTimeout(c.ctx, time.Second*5)
		defer cancel()
		logger.Info("Member Remove", zap.Any("member", m.Member.User.ID))
		if err := c.GuildGroupMemberRemove(ctx, m.GuildID, m.Member.User.ID, ""); err != nil {
			logger.Warn("Error removing guild group member", zap.Any("guildMemberRemove", m), zap.Error(err))
		}
	})

	dg.AddHandler(func(s *discordgo.Session, m *discordgo.GuildBanAdd) {
		if err := c.handleGuildBanAdd(c.ctx, logger, s, m); err != nil {
			logger.Error("Error handling guild ban add", zap.Any("guildBanAdd", m), zap.Error(err))
		}
	})
	c.logger.Info("Starting Discord cache")
}

// Queue a user for caching/updating.
func (c *DiscordCache) QueueSyncMember(guildID, discordID string) {
	select {
	case c.queueCh <- QueueEntry{GuildID: guildID, DiscordID: discordID}:
		// Success
	default:
		// Queue is full
		c.logger.Warn("Queue is full; dropping entry", zap.String("discord_id", discordID), zap.String("guild_id", guildID))
	}
}

// Purge removes both the reference and the reverse from the cache.
func (d *DiscordCache) Purge(id string) bool {
	value, loaded := d.idcache.LoadAndDelete(id)
	if !loaded {
		return false
	}
	d.idcache.Delete(value)
	return true
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
		if err != nil || groupID == "" || groupID == uuid.Nil.String() {
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
func (c *DiscordCache) syncMember(ctx context.Context, logger *zap.Logger, discordID, guildID string) error {
	if guildID == "" {
		return fmt.Errorf("guild not specified")
	}
	groupID := c.GuildIDToGroupID(guildID)
	if groupID == "" {
		return fmt.Errorf("guild group not found")
	}

	member, err := c.GuildMember(guildID, discordID)
	if err == ErrMemberNotFound {
		// Remove the user from the guild group.
		if err := c.GuildGroupMemberRemove(ctx, guildID, discordID, ""); err != nil {
			return fmt.Errorf("failed to remove guild group member: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("error getting guild member: %w", err)
	}

	account, err := c.nk.AccountGetId(ctx, c.DiscordIDToUserID(discordID))
	if err != nil {
		return fmt.Errorf("error getting account: %w", err)
	}

	evrAccount, err := NewEVRAccount(account)
	if err != nil {
		return fmt.Errorf("error building evr account: %w", err)
	}

	groups, err := GuildUserGroupsList(ctx, c.nk, c.DiscordIDToUserID(discordID))
	if err != nil {
		return fmt.Errorf("error getting user guild groups: %w", err)
	}

	group, ok := groups[groupID]
	if !ok {
		// Add the player to the group
		if err := c.nk.GroupUsersAdd(ctx, SystemUserID, groupID, []string{evrAccount.ID()}); err != nil {
			return fmt.Errorf("error joining group: %w", err)
		}

		// Get the group data again
		groups, err = GuildUserGroupsList(ctx, c.nk, c.DiscordIDToUserID(discordID))
		if err != nil {
			return fmt.Errorf("error getting user guild groups: %w", err)
		}

		group, ok = groups[groupID]
		if !ok {
			return fmt.Errorf("guild group not found")
		}
	}

	if group == nil {
		return fmt.Errorf("guild group not found")
	}

	if member == nil {
		// Clear the role cache for the user
		group.RolesCacheUpdate(evrAccount.ID(), nil)
		return nil
	}

	if updated := group.RolesCacheUpdate(evrAccount.ID(), member.Roles); updated {
		// save the group data
		data, err := group.MarshalToMap()
		if err != nil {
			return fmt.Errorf("error marshalling group data: %w", err)
		}

		if err := c.nk.GroupUpdate(ctx, group.ID().String(), SystemUserID, "", "", "", "", "", false, data, 1000000); err != nil {
			return fmt.Errorf("error updating group: %w", err)
		}

	}

	// Update headset linked role
	if r := group.Roles.AccountLinked; r != "" {
		if len(evrAccount.Devices) == 0 {
			if slices.Contains(member.Roles, r) {
				// Remove the role
				if err := c.dg.GuildMemberRoleRemove(guildID, discordID, r); err != nil {
					logger.Warn("Error removing headset-linked role from member", zap.String("role", r), zap.Error(err))
				}
			}
		} else {
			if !slices.Contains(member.Roles, r) {
				// Assign the role
				if err := c.dg.GuildMemberRoleAdd(guildID, discordID, r); err != nil {
					logger.Warn("Error adding headset-linked role to member", zap.String("role", r), zap.Error(err))
				}
			}
		}
	}

	displayName, err := c.checkDisplayName(ctx, c.nk, evrAccount.ID(), member.DisplayName())
	if err == nil {
		// Ensure the display name history exists.
		isActive := evrAccount.IsLinked() && !evrAccount.IsDisabled()
		if err := DisplayNameHistoryUpdate(ctx, c.nk, account.User.Id, groupID, displayName, account.User.Username, isActive); err != nil {
			logger.Warn("Failed to set display name history", zap.Error(err))
		}
	}

	return nil
}

func (c DiscordCache) memberUpdateDisplayName(ctx context.Context, member *discordgo.Member, evrAccount *EVRAccount) error {

	displayName := sanitizeDisplayName(member.DisplayName())
	if displayName == "" {
		displayName = sanitizeDisplayName(member.User.GlobalName)
	}
	if displayName == "" {
		displayName = sanitizeDisplayName(member.User.Username)
	}

	isActive := evrAccount.IsLinked() && !evrAccount.IsDisabled()

	groupID := c.GuildIDToGroupID(member.GuildID)
	if groupID == "" {
		return nil
	}

	prevDisplayName := evrAccount.GetDisplayName(groupID)

	if prevDisplayName != displayName {
		logger := c.logger.With(zap.String("display_name", displayName), zap.String("prev_display_name", prevDisplayName))
		displayName, err := c.checkDisplayName(ctx, c.nk, evrAccount.ID(), displayName)
		if isActive && err != nil {
			switch e := err.(type) {
			case DisplayNameInUseError:
				logger.Warn("Display name in use", zap.Error(e))

				evrAccount.SetGroupDisplayName(groupID, member.User.Username)

				// send user a message telling them their display name is in use.
				channel, err := c.dg.UserChannelCreate(member.User.ID)
				if err != nil {
					return fmt.Errorf("error creating user channel: %w", err)
				}

				otherDiscordID := c.UserIDToDiscordID(e.UserIDs[0])
				message := fmt.Sprintf("The display name `%s` is already in use/reserved by <@%s>. Your in-game name will be your username: `%s`", EscapeDiscordMarkdown(e.DisplayName), otherDiscordID, EscapeDiscordMarkdown(member.User.Username))
				if _, err := c.dg.ChannelMessageSend(channel.ID, message); err != nil {
					return fmt.Errorf("error sending message: %w", err)
				}

			default:
				logger.Warn("Error checking display name", zap.Error(err))
			}
		} else {

			if err := DisplayNameHistoryUpdate(ctx, c.nk, evrAccount.ID(), groupID, displayName, evrAccount.User.Username, isActive); err != nil {
				return fmt.Errorf("error adding display name history entry: %w", err)
			}

			evrAccount.SetGroupDisplayName(groupID, displayName)
		}

		if err := c.nk.AccountUpdateId(ctx, evrAccount.ID(), member.User.Username, evrAccount.MarshalMap(), evrAccount.GetActiveGroupDisplayName(), "", "", "", member.User.Avatar); err != nil {
			return fmt.Errorf("failed to update account: %w", err)
		}
	}

	return nil
}

// Loads/Adds a user to the cache.
func (c *DiscordCache) GuildMember(guildID, discordID string) (member *discordgo.Member, err error) {
	// Check the cache first.
	if member, err = c.dg.State.Member(guildID, discordID); err == nil && member != nil {
		return member, nil
	} else if member, err = c.dg.GuildMember(guildID, discordID); err != nil {
		if restError, _ := err.(*discordgo.RESTError); errors.As(err, &restError) && restError.Message != nil && restError.Message.Code == discordgo.ErrCodeUnknownMember {
			return nil, ErrMemberNotFound
		}
		return nil, fmt.Errorf("error getting guild member: %w", err)
	}

	c.dg.State.MemberAdd(member)
	return member, nil
}

func (d *DiscordCache) updateGuild(ctx context.Context, logger *zap.Logger, guild *discordgo.Guild) error {

	var err error
	botUserID := d.DiscordIDToUserID(d.dg.State.User.ID)
	if botUserID == "" {
		var created bool

		botUserID, _, created, err = d.nk.AuthenticateCustom(ctx, d.dg.State.User.ID, d.dg.State.User.Username, true)
		if err != nil {
			return fmt.Errorf("failed to authenticate (or create) bot user %s: %w", d.dg.State.User.ID, err)
		}
		if created {
			// Add to the global bots group
			if err := d.nk.GroupUsersAdd(ctx, SystemUserID, GroupGlobalBots, []string{botUserID}); err != nil {
				return fmt.Errorf("error adding bot to global bots group: %w", err)
			}
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
			// Leave guilds where the owner is globally banned.
			if status.Code(err) == codes.PermissionDenied {
				logger.Warn("Guild owner is globally banned. Leaving guild.", zap.String("guild_id", guild.ID), zap.String("owner_id", guild.OwnerID))
				if err := d.dg.GuildLeave(guild.ID); err != nil {
					return fmt.Errorf("error leaving guild: %w", err)
				}
			}
			return fmt.Errorf("failed to authenticate (or create) guild owner %s: %w", guild.OwnerID, err)
		}
	}

	ownerAccount, err := d.nk.AccountGetId(ctx, ownerUserID)
	if err != nil {
		return fmt.Errorf("error getting owner account: %w", err)
	}

	if ownerAccount.GetDisableTime() != nil {
		logger.Warn("Guild owner is globally banned. Leaving guild.", zap.String("guild_id", guild.ID), zap.String("owner_id", guild.OwnerID))
		if err := d.dg.GuildLeave(guild.ID); err != nil {
			return fmt.Errorf("error leaving guild: %w", err)
		}

		return nil
	}

	groupID := d.GuildIDToGroupID(guild.ID)
	if groupID == "" {
		// This is a new guild.

		gm, err := NewGuildGroupMetadata(guild.ID).MarshalToMap()
		if err != nil {
			return fmt.Errorf("error marshalling group metadata: %w", err)
		}

		_, err = d.nk.GroupCreate(ctx, ownerUserID, guild.Name, botUserID, GuildGroupLangTag, guild.Description, guild.IconURL("512"), false, gm, 100000)
		if err != nil {
			return fmt.Errorf("error creating group: %w", err)
		}

		// Invite the owner to the game service guild.
	} else {

		md, err := GetGuildGroupMetadata(ctx, d.db, groupID)
		if err != nil {
			return fmt.Errorf("error getting guild group metadata: %w", err)
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
	/*
		logger.Info("Member Add", zap.Any("member", e))


			if err := d.SyncGuildGroupMember(ctx, d.DiscordIDToUserID(e.Member.User.ID), d.GuildIDToGroupID(e.GuildID)); err != nil {
				return fmt.Errorf("failed to sync guild group member: %w", err)
			}
	*/

	return nil
}

func (d *DiscordCache) handleMemberUpdate(logger *zap.Logger, s *discordgo.Session, e *discordgo.GuildMemberUpdate) error {
	if e.Member == nil || e.Member.User == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(d.ctx, time.Second*5)
	defer cancel()

	userID := d.DiscordIDToUserID(e.Member.User.ID)

	if userID == "" {
		return nil
	}

	// Ignore members who haven't logged into echo.
	if ok, _ := HasLoggedIntoEcho(ctx, d.nk, userID); !ok {
		return nil
	}

	// Ignore unknown guilds
	groupID := d.GuildIDToGroupID(e.GuildID)
	if groupID == "" {
		return nil
	}

	d.QueueSyncMember(e.GuildID, e.Member.User.ID)
	logger.Info("Member Updated", zap.Any("member", e))

	return nil
}

func (d *DiscordCache) GuildGroupMemberRemove(ctx context.Context, guildID, discordID string, callerDiscordID string) error {
	groupID := d.GuildIDToGroupID(guildID)
	if groupID == "" {
		return nil
	}

	userID := d.DiscordIDToUserID(discordID)
	if userID == "" {
		return nil
	}

	callerID := ""
	if callerDiscordID != "" {
		callerID = d.DiscordIDToUserID(callerDiscordID)
	}

	md, err := AccountMetadataLoad(ctx, d.nk, userID)
	if err != nil {
		return fmt.Errorf("error getting account metadata: %w", err)
	}

	if callerID != "" {
		if err := d.nk.GroupUsersKick(ctx, callerID, groupID, []string{userID}); err != nil {
			return fmt.Errorf("error kicking user from group: %w", err)
		}
	} else {
		if err := d.nk.GroupUserLeave(ctx, groupID, userID, md.Username()); err != nil {
			return fmt.Errorf("error removing user from group: %w", err)
		}
	}

	delete(md.GroupDisplayNames, groupID)
	if md.GetActiveGroupID().String() == groupID {
		md.SetActiveGroupID(uuid.Nil)
	}

	// Store the account metadata
	if err := d.nk.AccountUpdateId(ctx, userID, md.Username(), md.MarshalMap(), md.GetActiveGroupDisplayName(), "", "", "", ""); err != nil {
		return fmt.Errorf("failed to update account: %w", err)
	}
	return nil
}

func (d *DiscordCache) handleGuildBanAdd(ctx context.Context, logger *zap.Logger, s *discordgo.Session, e *discordgo.GuildBanAdd) error {

	groupID := d.GuildIDToGroupID(e.GuildID)
	if groupID == "" {
		return fmt.Errorf("guild not found")
	}

	userID := d.DiscordIDToUserID(e.User.ID)
	if userID == "" {
		return fmt.Errorf("user not found")
	}

	if ok, _ := HasLoggedIntoEcho(ctx, d.nk, userID); !ok {
		return nil
	}

	logger = logger.With(zap.String("event", "GuildBanAdd"), zap.String("guild_id", e.GuildID), zap.String("discord_id", e.User.ID), zap.String("gid", groupID), zap.String("uid", userID), zap.String("username", e.User.Username))

	ctx, cancel := context.WithTimeout(ctx, time.Second*5)
	defer cancel()

	// Fetch the audit log for recent bans
	auditLogs, err := s.GuildAuditLog(e.GuildID, "", "", int(discordgo.AuditLogActionMemberBanAdd), 1)
	if err != nil {
		return fmt.Errorf("error fetching audit log: %w", err)
	}
	var issuerDiscordID string
	if len(auditLogs.AuditLogEntries) == 0 {
		logger.Warn("No relevant audit log entries found.")
	} else if latestBan := auditLogs.AuditLogEntries[0]; latestBan.TargetID != e.User.ID {
		logger.Warn("Latest ban action does not match the user ID", zap.String("target_id", latestBan.TargetID))
	} else if issuer, err := s.User(latestBan.UserID); err != nil {
		logger.Warn("Failed to fetch issuing user", zap.Error(err))
	} else {
		issuerDiscordID = issuer.ID
		issuerUserID := d.DiscordIDToUserID(issuer.ID)
		logger = logger.With(
			zap.String("issuer_username", issuer.Username),
			zap.String("issuer_user_id", issuerUserID),
			zap.String("issuer_discord_id", issuer.ID),
			zap.String("reason", latestBan.Reason),
			zap.Any("audit_log", latestBan),
		)
	}

	if err := d.GuildGroupMemberRemove(ctx, e.GuildID, e.User.ID, issuerDiscordID); err != nil {
		logger.Warn("Error removing guild group member", zap.Any("guildMemberRemove", e), zap.Error(err))
	}

	logger.Info("User was banned from guild", zap.String("event", "GuildBanAdd"))
	return nil
}

type DisplayNameInUseError struct {
	DisplayName string
	UserIDs     []string
}

func (e DisplayNameInUseError) Error() string {
	return fmt.Sprintf("display name `%s` is already in use by %s", e.DisplayName, e.UserIDs)
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
		return "", DisplayNameInUseError{DisplayName: displayName, UserIDs: []string{u.Id}}
	}
	userIDs, err := DisplayNameHistoryActiveList(ctx, nk, displayName)
	if err != nil {
		return "", fmt.Errorf("error getting display name history: %w", err)
	}

	switch len(userIDs) {
	case 0:
		return displayName, nil
	case 1:
		if userIDs[0] == userID {
			return displayName, nil
		}
		return "", DisplayNameInUseError{DisplayName: displayName, UserIDs: userIDs}
	default:
		d.logger.Warn("Multiple users found with the same display name", zap.String("display_name", displayName), zap.Strings("user_ids", userIDs))
		return "", DisplayNameInUseError{DisplayName: displayName, UserIDs: userIDs}
	}
}

func (d *DiscordCache) CheckUser2FA(ctx context.Context, userID uuid.UUID) (bool, error) {
	discordID, err := GetDiscordIDByUserID(ctx, d.db, userID.String())
	if err != nil {
		return false, fmt.Errorf("error getting discord id: %w", err)
	}
	user, err := d.dg.User(discordID)
	if err != nil {
		return false, fmt.Errorf("error getting discord user: %w", err)
	}
	return user.MFAEnabled, nil
}

func (d *DiscordCache) ReplaceMentions(message string) string {

	replacedMessage := mentionRegex.ReplaceAllStringFunc(message, func(mention string) string {
		matches := mentionRegex.FindStringSubmatch(mention)
		if len(matches) > 1 {
			userID := matches[1]
			if uuid.FromStringOrNil(userID).IsNil() {
				return mention
			}
			discordID := d.UserIDToDiscordID(userID)
			return "<@" + discordID + ">"
		}
		return mention
	})
	return replacedMessage
}

func HasLoggedIntoEcho(ctx context.Context, nk runtime.NakamaModule, userID string) (bool, error) {
	// If the member hasn't ever logged into echo, then don't sync them.
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: LoginStorageCollection,
			Key:        LoginHistoryStorageKey,
			UserID:     userID,
		},
	})
	if err != nil {
		return false, fmt.Errorf("error reading game profile: %w", err)
	}

	return len(objs) > 0, nil
}

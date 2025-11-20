package server

import (
	"context"
	"database/sql"
	"encoding/json"
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
	DiscordID    string
	GuildID      string
	DoFullUpdate bool
}

// Responsible for caching and synchronizing data with Discord.
type DiscordIntegrator struct {
	ctx      context.Context
	cancelFn context.CancelFunc
	logger   *zap.Logger

	nk runtime.NakamaModule
	db *sql.DB
	dg *discordgo.Session

	guildGroupRegistry *GuildGroupRegistry
	queueCh            chan QueueEntry
	queueCooldowns     *MapOf[QueueEntry, time.Time]
	idcache            *MapOf[string, string]
}

func NewDiscordIntegrator(ctx context.Context, logger *zap.Logger, config Config, metrics Metrics, nk runtime.NakamaModule, db *sql.DB, dg *discordgo.Session, guildGroupRegistry *GuildGroupRegistry) *DiscordIntegrator {
	ctx, cancelFn := context.WithCancel(ctx)

	guildGroups := &atomic.Value{}
	guildGroups.Store(make(map[string]*GuildGroup))

	return &DiscordIntegrator{
		ctx:      ctx,
		cancelFn: cancelFn,
		logger:   logger,

		nk: nk,
		db: db,
		dg: dg,

		guildGroupRegistry: guildGroupRegistry,
		queueCooldowns:     &MapOf[QueueEntry, time.Time]{},
		idcache:            &MapOf[string, string]{},

		queueCh: make(chan QueueEntry, 50),
	}
}

func (c *DiscordIntegrator) Stop() {
	c.cancelFn()
}

func (c *DiscordIntegrator) Start() {
	dg := c.dg
	logger := c.logger.With(zap.String("module", "discord_integration"))

	// Start the cache worker.
	go func() {

		var (
			queueEmpty     bool
			started        time.Time
			maxQueueLength int
			processed      int
			cooldownTicker = time.NewTicker(time.Second * 3)
			pruneTicker    = time.NewTicker(time.Second * 30) // Sets itself to 15 after the first run.
			runtimeLogger  = NewRuntimeGoLogger(c.logger)
		)

		for {

			// Log the queue length every time it empties
			switch len(c.queueCh) {
			case 0:
				if !queueEmpty {
					if maxQueueLength > 0 {
						logger.Debug("Sync queue is empty", zap.Duration("uptime", time.Since(started)), zap.Int("max_queue_len", maxQueueLength), zap.Int("processed", processed))
					}
					queueEmpty = true
				}
			case 1:
				if queueEmpty {
					queueEmpty = false
					started = time.Now()
					maxQueueLength = 1
				}

			default:
				if len(c.queueCh) > maxQueueLength {
					maxQueueLength = len(c.queueCh)
				}
			}

			select {
			case <-c.ctx.Done():
				logger.Warn("Stopping Discord integrator syncing")
				return
			case entry := <-c.queueCh:
				if entry.GuildID == "" || entry.DiscordID == "" {
					logger.Warn("Invalid queue entry", zap.String("discord_id", entry.DiscordID), zap.String("guild_id", entry.GuildID))
					continue
				}
				started = time.Now()
				processed++
				logger := logger.With(
					zap.String("discord_id", entry.DiscordID),
					zap.String("guild_id", entry.GuildID),
					zap.String("gid", c.GuildIDToGroupID(entry.GuildID)),
					zap.String("uid", c.DiscordIDToUserID(entry.DiscordID)),
					zap.Bool("full_update", entry.DoFullUpdate),
				)

				if err := c.syncMember(c.ctx, logger, entry.DiscordID, entry.GuildID, entry.DoFullUpdate); err != nil {
					logger.Warn("Error syncing guild group member", zap.Error(err))
				}
				logger.Debug("Synced guild group member", zap.Duration("duration", time.Since(started)))

			case <-cooldownTicker.C:

				c.queueCooldowns.Range(func(entry QueueEntry, t time.Time) bool {
					if time.Now().After(t) {
						logger := logger.With(
							zap.String("discord_id", entry.DiscordID),
							zap.String("guild_id", entry.GuildID),
							zap.String("gid", c.GuildIDToGroupID(entry.GuildID)),
							zap.String("uid", c.DiscordIDToUserID(entry.DiscordID)),
						)
						c.queueCooldowns.Delete(entry)
						processed++
						if entry.GuildID == "" || entry.DiscordID == "" {
							logger.Warn("Invalid queue entry", zap.String("discord_id", entry.DiscordID), zap.String("guild_id", entry.GuildID))
							return true
						} else if err := c.syncMember(c.ctx, logger, entry.DiscordID, entry.GuildID, entry.DoFullUpdate); err != nil {
							logger.Warn("Error syncing guild group member", zap.Error(err))
						} else {
							logger.Debug("Synced guild group member")
						}
					}
					return true
				})

			case <-pruneTicker.C:
				// Adjust the prune ticker to run every 15 minutes after the first run.

				pruneTicker.Reset(time.Minute * 15)

				// Prune the guild groups
				doLeaves := ServiceSettings().PruneSettings.LeaveOrphanedGuilds
				doDeletes := ServiceSettings().PruneSettings.DeleteOrphanedGroups
				pruneSafetyThreshold := ServiceSettings().PruneSettings.SafetyLimit
				if err := c.pruneGuildGroups(c.ctx, runtimeLogger, doLeaves, doDeletes, pruneSafetyThreshold); err != nil {
					logger.Error("Error pruning guild groups", zap.Error(err), zap.Bool("do_leaves", doLeaves), zap.Bool("do_deletes", doDeletes), zap.Int("prune_safety_threshold", pruneSafetyThreshold))
				}
			}
		}
	}()

	dg.AddHandler(func(s *discordgo.Session, m *discordgo.GuildCreate) {
		if err := c.handleGuildCreate(logger, s, m); err != nil {
			logger.Error("Error handling guild create", zap.Any("guildCreate", m), zap.Error(err))
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
		logger.Info("Member Remove", zap.Any("member", m.Member.User.ID))
		if err := c.GuildGroupMemberRemove(c.ctx, m.GuildID, m.Member.User.ID, ""); err != nil {
			logger.Warn("Error removing guild group member", zap.Any("guildMemberRemove", m), zap.Error(err))
		}
	})

	dg.AddHandler(func(s *discordgo.Session, m *discordgo.GuildBanAdd) {
		if err := c.handleGuildBanAdd(c.ctx, logger, s, m); err != nil {
			logger.Error("Error handling guild ban add", zap.Any("guildBanAdd", m), zap.Error(err))
		}
	})

	dg.AddHandler(func(s *discordgo.Session, m *discordgo.GuildRoleUpdate) {
		if err := c.handleGuildRoleUpdate(c.ctx, logger, s, m); err != nil {
			logger.Error("Error handling guild role update", zap.Any("guildRoleUpdate", m), zap.Error(err))
		}
	})
	c.logger.Info("Starting Discord cache")
}

// Queue a user for caching/updating.
func (c *DiscordIntegrator) QueueSyncMember(guildID, discordID string, full bool) {
	metricsTags := map[string]string{
		"guild_id": guildID,
		"group_id": c.GuildIDToGroupID(guildID),
	}
	defer func() { c.nk.MetricsCounterAdd("discord_integrator_queue_sync_member", metricsTags, 1) }()

	entry := QueueEntry{GuildID: guildID, DiscordID: discordID}
	_, exists := c.queueCooldowns.LoadOrStore(entry, time.Now().Add(time.Second*30))
	if exists {
		// Already in the queue, no need to add it again.
		metricsTags["result"] = "on_cooldown"
		return
	}

	select {
	case c.queueCh <- QueueEntry{GuildID: guildID, DiscordID: discordID, DoFullUpdate: full}:
		// Success
		metricsTags["result"] = "queued"
	default:
		// Queue is full
		metricsTags["result"] = "queue_full"
		c.logger.Warn("Queue is full; dropping entry", zap.String("discord_id", discordID), zap.String("guild_id", guildID))
	}
}

// Purge removes both the reference and the reverse from the cache.
func (d *DiscordIntegrator) Purge(id string) bool {
	value, loaded := d.idcache.LoadAndDelete(id)
	if !loaded {
		return false
	}
	d.idcache.Delete(value)
	return true
}

// Discord ID to Nakama UserID, with a lookup cache
func (d *DiscordIntegrator) DiscordIDToUserID(discordID string) string {
	userID, ok := d.idcache.Load(discordID)
	if !ok {
		var err error
		userID, err = GetUserIDByDiscordID(d.ctx, d.db, discordID)
		if err != nil {
			return ""
		}
		d.idcache.Store(discordID, userID)
		d.idcache.Store(userID, discordID)
	}
	return userID
}

func (d *DiscordIntegrator) UserIDToDiscordID(userID string) string {
	discordID, ok := d.idcache.Load(userID)
	if !ok {
		var err error
		discordID, err = GetDiscordIDByUserID(d.ctx, d.db, userID)
		if err != nil {
			return ""
		}
		d.idcache.Store(userID, discordID)
		d.idcache.Store(discordID, userID)
	}
	return discordID
}

// Guild ID to Nakama Group ID, with a lookup cache
func (d *DiscordIntegrator) GuildIDToGroupID(guildID string) string {
	groupID, ok := d.idcache.Load(guildID)
	if !ok {
		var err error
		groupID, err = GetGroupIDByGuildID(d.ctx, d.db, guildID)
		if err != nil || groupID == "" || groupID == uuid.Nil.String() {
			return ""
		}

		d.idcache.Store(guildID, groupID)
		d.idcache.Store(groupID, guildID)
	}
	return groupID
}

func (c *DiscordIntegrator) GroupIDToGuildID(groupID string) string {
	guildID, ok := c.idcache.Load(groupID)
	if !ok {
		var err error
		guildID, err = GetGuildIDByGroupID(c.ctx, c.db, groupID)
		if err != nil {
			return ""
		}
		c.idcache.Store(groupID, guildID)
		c.idcache.Store(guildID, groupID)
	}
	return guildID
}

// Syncs a user to all of their guilds.
func (c *DiscordIntegrator) syncMember(ctx context.Context, logger *zap.Logger, discordID, guildID string, full bool) error {
	if guildID == "" {
		return errors.New("guild not specified")
	}
	groupID := c.GuildIDToGroupID(guildID)
	if groupID == "" {
		return errors.New("guild group not found")
	}

	member, err := c.GuildMember(guildID, discordID)
	if errors.Is(err, ErrMemberNotFound) || member == nil {
		if removeErr := c.GuildGroupMemberRemove(ctx, guildID, discordID, ""); removeErr != nil {
			return fmt.Errorf("failed to remove guild group member: %w", removeErr)
		}
		logger.Info("Member not found, removed from guild group", zap.String("discord_id", discordID), zap.String("guild_id", guildID))
		return nil
	}
	if err != nil {
		return fmt.Errorf("error getting guild member: %w", err)
	}

	userID := c.DiscordIDToUserID(discordID)
	account, err := c.nk.AccountGetId(ctx, userID)
	if err != nil {
		return fmt.Errorf("error getting account: %w", err)
	}

	profile, err := BuildEVRProfileFromAccount(account)
	if err != nil {
		return fmt.Errorf("error building evr account: %w", err)
	}

	groups, err := GuildUserGroupsList(ctx, c.nk, c.guildGroupRegistry, userID)
	if err != nil {
		return fmt.Errorf("error getting user guild groups: %w", err)
	}

	group, ok := groups[groupID]
	if !ok || group == nil {
		if err := c.nk.GroupUsersAdd(ctx, SystemUserID, groupID, []string{profile.ID()}); err != nil {
			return fmt.Errorf("error joining group: %w", err)
		}
		// Refresh group data
		groups, err = GuildUserGroupsList(ctx, c.nk, c.guildGroupRegistry, userID)
		if err != nil {
			return fmt.Errorf("error getting user guild groups: %w", err)
		}
		group, ok = groups[groupID]
		if !ok || group == nil {
			return errors.New("guild group not found")
		}
	}

	// Update the group state with the member's roles.
	if group.RoleCacheUpdate(profile, member.Roles) {
		if err := GuildGroupStore(ctx, c.nk, c.guildGroupRegistry, group); err != nil {
			return fmt.Errorf("error storing guild group: %w", err)
		}
	}

	// Update the display name if needed
	if currentDisplayName, _ := profile.GetGroupDisplayName(groupID); full || currentDisplayName != InGameName(member) {
		if err := c.syncMembersIGN(ctx, logger, profile, member, group); err != nil {
			return fmt.Errorf("error syncing display name: %w", err)
		}
	}

	// Update headset-linked role
	roleID := group.RoleMap.AccountLinked
	hasRole := profile.IsLinked() && !profile.IsDisabled()
	if err := c.updateMemberRole(member, roleID, hasRole); err != nil {
		logger.Warn("Error updating headset-linked role", zap.String("role", roleID), zap.Error(err))
	}

	return nil
}

func (d *DiscordIntegrator) updateLinkStatus(ctx context.Context, discordID string) error {
	userID := d.DiscordIDToUserID(discordID)
	if userID == "" {
		return fmt.Errorf("failed to get user ID by discord ID")
	}
	account, err := d.nk.AccountGetId(ctx, userID)
	if err != nil {
		return fmt.Errorf("error getting account: %w", err)
	}

	evrAccount, err := BuildEVRProfileFromAccount(account)
	if err != nil {
		return fmt.Errorf("error building evr account: %w", err)
	}
	// Get the GroupID from the user's metadata
	guildGroups, err := GuildUserGroupsList(ctx, d.nk, d.guildGroupRegistry, userID)
	if err != nil {
		return fmt.Errorf("failed to get guild group memberships: %w", err)
	}

	isLinked := evrAccount.IsLinked()

	for gid, g := range guildGroups {
		member, err := d.GuildMember(g.GuildID, discordID)
		if err != nil {
			d.logger.Warn("Error getting guild member", zap.String("guild_id", gid), zap.String("discord_id", discordID), zap.Error(err))
			continue
		}
		// If the guild has a linked role, update it.
		if err := d.updateMemberRole(member, g.RoleMap.AccountLinked, isLinked); err != nil {
			d.logger.Warn("Error updating headset-linked role", zap.String("discord_id", discordID), zap.String("guild_id", gid), zap.String("role", g.RoleMap.AccountLinked), zap.Error(err))
			continue
		}
	}

	return nil
}

func InGameName(m *discordgo.Member) string {
	if m == nil || m.User == nil {
		return ""
	}
	for _, name := range [...]string{m.Nick, m.User.GlobalName, m.User.Username} {
		if n := sanitizeDisplayName(name); n != "" {
			return n
		}
	}
	return ""
}

// Loads/Adds a user to the cache.
func (c *DiscordIntegrator) GuildMember(guildID, discordID string) (member *discordgo.Member, err error) {
	// Check the cache first.
	if member, err = c.dg.State.Member(guildID, discordID); err == nil && member != nil {
		return member, nil
	} else if member, err = c.dg.GuildMember(guildID, discordID); err != nil {
		if IsDiscordErrorCode(err, discordgo.ErrCodeUnknownMember) {
			return nil, ErrMemberNotFound
		}
		return nil, fmt.Errorf("error getting guild member: %w", err)
	}

	c.dg.State.MemberAdd(member)
	return member, nil
}

func (d *DiscordIntegrator) guildSync(ctx context.Context, logger *zap.Logger, guild *discordgo.Guild) error {
	logger = logger.With(zap.String("guild_id", guild.ID), zap.String("guild_name", guild.Name))

	var err error
	botUserID := d.DiscordIDToUserID(d.dg.State.User.ID)
	if botUserID == "" {
		return fmt.Errorf("failed to get bot user ID from state")
	}

	var groupID string
	// Ensure the guild owner is in the system.
	ownerUserID := d.DiscordIDToUserID(guild.OwnerID)
	if ownerUserID == "" {
		ownerMember, err := d.dg.GuildMember(guild.ID, guild.OwnerID)
		if err != nil {
			return fmt.Errorf("failed to get guild owner: %w", err)
		}

		ownerUserID, _, _, err = AuthenticateCustom(ctx, logger, d.db, ownerMember.User.ID, ownerMember.User.Username, true)
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
		defer func() { d.QueueSyncMember(groupID, ownerMember.User.ID, false) }()
	}

	ownerAccount, err := d.nk.AccountGetId(ctx, ownerUserID)
	if err != nil {
		return fmt.Errorf("error getting owner account: %w", err)
	}

	if ownerAccount.GetDisableTime() != nil {
		message := fmt.Sprintf("The owner of the guild `%s` (ID: %s) owned by <@%s> is globally banned. The guild will be removed.", guild.Name, guild.ID, guild.OwnerID)
		d.LogServiceAuditMessage(ctx, message, false)

		logger.Warn("Guild owner is globally banned. Leaving guild.", zap.String("guild_id", guild.ID), zap.String("owner_id", guild.OwnerID))
		if err := d.dg.GuildLeave(guild.ID); err != nil {
			return fmt.Errorf("error leaving guild: %w", err)
		}

		return nil
	}

	groupID = d.GuildIDToGroupID(guild.ID)
	if groupID == "" {
		// This is a new guild.
		gm := NewGuildGroupMetadata(guild.ID)
		if serviceGuildID := ServiceSettings().ServiceGuildID; serviceGuildID != "" && serviceGuildID != guild.ID {
			if serviceGroupID := d.GuildIDToGroupID(serviceGuildID); serviceGroupID != "" {
				// add the service guild ID to the list of inherited groups (global suspensions)
				gm.SuspensionInheritanceGroupIDs = []string{serviceGroupID}
			}
		}

		metadataMap, err := gm.MarshalToMap()
		if err != nil {
			return fmt.Errorf("error marshalling guild group metadata: %w", err)
		}

		group, err := d.nk.GroupCreate(ctx, ownerUserID, guild.Name, botUserID, GuildGroupLangTag, guild.Description, guild.IconURL("512"), false, metadataMap, 100000)
		if err != nil {
			return fmt.Errorf("error creating group: %w", err)
		}

		groupID = group.Id
		d.LogServiceAuditMessage(ctx, fmt.Sprintf("Created guild `%s` (ID: %s) owned by <@%s>", guild.Name, guild.ID, guild.OwnerID), false)
		// Invite the owner to the game service guild.
	} else {
		// Update the group data
		if err := d.nk.GroupUpdate(ctx, groupID, SystemUserID, guild.Name, botUserID, GuildGroupLangTag, guild.Description, guild.IconURL("512"), false, nil, 100000); err != nil {
			return fmt.Errorf("error updating group: %w", err)
		}
	}

	// Load the guild group
	gg, err := GuildGroupLoad(ctx, d.nk, groupID)
	if err != nil {
		return fmt.Errorf("error loading guild group: %w", err)
	}

	gg.OwnerID = ownerUserID
	gg.State.RulesText = "No #rules channel found. Please create the channel and set the topic to the rules."

	for _, channel := range guild.Channels {
		if channel.Type == discordgo.ChannelTypeGuildText && channel.Name == "rules" {
			gg.State.RulesText = channel.Topic
			break
		}
	}

	if err := GuildGroupStore(ctx, d.nk, d.guildGroupRegistry, gg); err != nil {
		logger.Error("Error storing guild group", zap.Error(err))
		return fmt.Errorf("error storing guild group: %w", err)
	}

	d.guildGroupRegistry.Add(gg)

	return nil
}

func (d *DiscordIntegrator) handleGuildCreate(logger *zap.Logger, s *discordgo.Session, e *discordgo.GuildCreate) error {
	logger.Info("Guild Create", zap.Any("guild", e.Guild.ID))
	if err := d.guildSync(d.ctx, logger, e.Guild); err != nil {
		return fmt.Errorf("error during guild sync: %w", err)
	}
	return nil
}

func (d *DiscordIntegrator) handleGuildUpdate(logger *zap.Logger, s *discordgo.Session, e *discordgo.GuildUpdate) error {
	logger.Info("Guild Update", zap.Any("guild", e.Guild.ID))
	if err := d.guildSync(d.ctx, logger, e.Guild); err != nil {
		return fmt.Errorf("error during guild sync: %w", err)
	}
	return nil
}

func (d *DiscordIntegrator) handleGuildDelete(logger *zap.Logger, s *discordgo.Session, e *discordgo.GuildDelete) error {

	d.LogServiceAuditMessage(d.ctx, fmt.Sprintf("Deleted guild `%s` (ID: %s), owned by <@%s>", e.Guild.ID, e.Guild.ID, e.Guild.OwnerID), false)
	logger.Info("Guild Delete", zap.Any("guild", e.Guild.ID))
	groupID := d.GuildIDToGroupID(e.Guild.ID)
	if groupID == "" {
		return nil
	}

	// Log the metadata of the group before deleting it.
	gg := d.guildGroupRegistry.Get(groupID)
	logger.Info("Deleting guild group", zap.String("group_id", groupID), zap.Any("metadata", gg.GroupMetadata))

	if err := d.nk.GroupDelete(d.ctx, groupID); err != nil {
		return fmt.Errorf("error deleting group: %w", err)
	}
	d.Purge(e.Guild.ID)
	return nil
}

func (d *DiscordIntegrator) handleMemberAdd(logger *zap.Logger, s *discordgo.Session, e *discordgo.GuildMemberAdd) error {
	/*
		logger.Info("Member Add", zap.Any("member", e))
		if err := d.SyncGuildGroupMember(ctx, d.DiscordIDToUserID(e.Member.User.ID), d.GuildIDToGroupID(e.GuildID)); err != nil {
			return fmt.Errorf("failed to sync guild group member: %w", err)
		}
	*/

	return nil
}

func (d *DiscordIntegrator) handleMemberUpdate(logger *zap.Logger, s *discordgo.Session, e *discordgo.GuildMemberUpdate) error {
	if e.Member == nil || e.Member.User == nil {
		return nil
	}

	// Ignore unknown guilds
	groupID := d.GuildIDToGroupID(e.GuildID)
	if groupID == "" {
		return nil
	}

	ctx := d.ctx
	userID := d.DiscordIDToUserID(e.Member.User.ID)

	if userID == "" {
		return nil
	}

	// Ignore members who haven't logged into echo
	if ok, _ := HasLoggedIntoEcho(ctx, d.nk, userID); !ok {
		return nil
	}

	// Retrieve the EVR account for the user
	evrAccount, err := EVRProfileLoad(ctx, d.nk, userID)
	if err != nil {
		return fmt.Errorf("error loading evr profile: %w", err)
	}

	// Get the guild group
	group := d.guildGroupRegistry.Get(groupID)
	if group == nil {
		return nil // No group found, nothing to do.
	}

	accountUpdate := false

	// If the guild has a linked role, update it.
	isActive := evrAccount.IsLinked() && !evrAccount.IsDisabled()
	if err := d.updateMemberRole(e.Member, group.RoleMap.AccountLinked, isActive); err != nil {
		logger.Warn("Error updating headset-linked role", zap.String("role", group.RoleMap.AccountLinked), zap.Error(err))
	} else {
		accountUpdate = true
	}

	// Ensure the user is in the group
	if groups, err := GuildUserGroupsList(ctx, d.nk, d.guildGroupRegistry, userID); err != nil {
		return fmt.Errorf("error getting user guild groups: %w", err)
		// Check if the user is already in the group
	} else if _, ok := groups[groupID]; !ok {
		// Add the player to the group
		if err := d.nk.GroupUsersAdd(ctx, SystemUserID, groupID, []string{evrAccount.ID()}); err != nil {
			return fmt.Errorf("error joining group: %w", err)
		}
		accountUpdate = true
	}

	// Update the role cache
	if updated := group.RoleCacheUpdate(evrAccount, e.Member.Roles); updated {
		if err := GuildGroupStore(ctx, d.nk, d.guildGroupRegistry, group); err != nil {
			return fmt.Errorf("error storing guild group: %w", err)
		}
	}

	locale := e.User.Locale
	avatarURL := ""
	// If this is there active group, update the account with this guild
	if groupID == evrAccount.GetActiveGroupID().String() {
		avatarURL = e.Member.AvatarURL("512")
		accountUpdate = true
	}

	if e.BeforeUpdate != nil && e.BeforeUpdate.User != nil {
		// Update the username if it has changed.
		if evrAccount.Username() != e.User.Username {
			accountUpdate = true
		}

		if InGameName(e.Member) != InGameName(e.BeforeUpdate) {
			if err := d.syncMembersIGN(ctx, logger, evrAccount, e.Member, group); err != nil {
				return fmt.Errorf("error syncing display name: %w", err)
			}
			accountUpdate = true
		}
	}

	if accountUpdate {
		if err := d.nk.AccountUpdateId(ctx, evrAccount.ID(), e.Member.User.Username, evrAccount.MarshalMap(), evrAccount.GetActiveGroupDisplayName(), "", "", locale, avatarURL); err != nil {
			return fmt.Errorf("failed to update account: %w", err)
		}
	}

	// If the guild forces IGNs to match discord while online
	if group.DisplayNameForceNickToIGN {
		// And the player is online
		// Search for them in a match from this guild
		query := fmt.Sprintf("+group_id:%s +players.user_id:%s", groupID, evrAccount.ID())
		matches, err := d.nk.MatchList(ctx, 100, true, "", nil, nil, query)
		if err != nil {
			logger.Warn("Failed to list matches for guild group member", zap.Error(err), zap.String("query", query))
			return fmt.Errorf("failed to list matches for guild group member: %w", err)
		}
		// Check that the player is not just a spectator
		if len(matches) > 0 {
			for _, match := range matches {
				label := MatchLabel{}
				if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), &label); err != nil {
					logger.Warn("Failed to unmarshal match label", zap.Error(err), zap.String("string", match.GetLabel().GetValue()))
					continue
				}
				if player := label.GetPlayerByUserID(evrAccount.ID()); player != nil {
					if player.DisplayName != InGameName(e.Member) {
						AuditLogSendGuild(s, group, fmt.Sprintf("Forcing guild nick to match in-game name for `%s` (`%s` -> `%s`)", e.Member.User.Username, InGameName(e.Member), player.DisplayName))
						// Force the display name to match the in-game name
						if err := s.GuildMemberNickname(group.GuildID, e.Member.User.ID, player.DisplayName); err != nil {
							logger.Warn("Failed to set display name", zap.Error(err))
						}
					}
					break
				}
			}
		}
	}

	logger.Info("Member Updated", zap.Any("before_update", e.BeforeUpdate), zap.Any("member", e.Member))

	return nil
}

func (d *DiscordIntegrator) syncMembersIGN(ctx context.Context, logger *zap.Logger, profile *EVRProfile, member *discordgo.Member, guildGroup *GuildGroup) error {
	displayName := InGameName(member)
	ownerMap, err := DisplayNameOwnerSearch(ctx, d.nk, []string{displayName})
	if err != nil {
		// If it errors, set the display name to their username
		logger.Error("Error checking owner of display name.", zap.String("display_name", displayName), zap.Error(err))
		return err
	}
	if len(ownerMap) > 0 && !slices.Contains(ownerMap[displayName], profile.ID()) {
		// The display name is owned by some one else.
		if guildGroup.DisplayNameInUseNotifications {
			// Notify the user that the display name they have chosen is in use.
			ownerID := ownerMap[displayName][0]
			logger.Warn("Display name in use", zap.String("owner_id", ownerID), zap.String("display_name", displayName), zap.String("caller_user_id", profile.ID()))
			if err := d.SendDisplayNameInUseNotification(ctx, member.User.ID, d.UserIDToDiscordID(ownerID), displayName, member.User.Username); err != nil {
				logger.Debug("Error sending display name in use notification", zap.String("owner_id", ownerID), zap.String("display_name", displayName), zap.Error(err))
			}
		}
		return nil
	}

	// This user may use this display name.
	history, err := DisplayNameHistoryLoad(ctx, d.nk, profile.ID())
	if err != nil {
		return fmt.Errorf("error loading display name history: %w", err)
	}
	// Update and store the display name history.
	history.Update(guildGroup.IDStr(), displayName, member.User.Username, false)
	err = DisplayNameHistoryStore(ctx, d.nk, profile.ID(), history)
	if err != nil {
		return fmt.Errorf("error storing display name history: %w", err)
	}

	return nil
}

func (d *DiscordIntegrator) SendDisplayNameInUseNotification(ctx context.Context, discordID, ownerDiscordID, displayName string, fallbackDisplayName string) error {
	// Only notify the user if the force display name to match IGN is set.
	message := fmt.Sprintf("The display name `%s` is already in use/reserved by <@%s>. Your in-game name will be your username: `%s`", displayName, ownerDiscordID, fallbackDisplayName)
	if _, err := SendUserMessage(ctx, d.dg, discordID, message); err != nil {
		return fmt.Errorf("error sending message: %w", err)
	}
	return nil
}
func (d *DiscordIntegrator) updateMemberRole(member *discordgo.Member, roleID string, hasRole bool) error {
	if roleID == "" || member == nil {
		return nil
	}
	if hasRole && !slices.Contains(member.Roles, roleID) {
		if err := d.dg.GuildMemberRoleAdd(member.GuildID, member.User.ID, roleID); err != nil {
			return fmt.Errorf("error adding role to member: %w", err)
		}
	} else if !hasRole && slices.Contains(member.Roles, roleID) {
		if err := d.dg.GuildMemberRoleRemove(member.GuildID, member.User.ID, roleID); err != nil {
			return fmt.Errorf("error removing role from member: %w", err)
		}
	}
	return nil
}

func (d *DiscordIntegrator) GuildGroupMemberRemove(ctx context.Context, guildID, discordID string, callerDiscordID string) error {
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

	md, err := EVRProfileLoad(ctx, d.nk, userID)
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

	md.DeleteGroupDisplayName(groupID)
	if md.GetActiveGroupID().String() == groupID {
		md.SetActiveGroupID(uuid.Nil)
	}

	// Store the account metadata
	if err := d.nk.AccountUpdateId(ctx, userID, "", md.MarshalMap(), md.GetActiveGroupDisplayName(), "", "", "", ""); err != nil {
		return fmt.Errorf("failed to update account: %w", err)
	}
	return nil
}

func (d *DiscordIntegrator) handleGuildBanAdd(ctx context.Context, logger *zap.Logger, s *discordgo.Session, e *discordgo.GuildBanAdd) error {

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

func (d *DiscordIntegrator) handleGuildRoleUpdate(ctx context.Context, logger *zap.Logger, s *discordgo.Session, e *discordgo.GuildRoleUpdate) error {
	// Get the guild group for this guild
	groupID := d.GuildIDToGroupID(e.GuildID)
	if groupID == "" {
		// Not a tracked guild, ignore
		return nil
	}

	guildGroup := d.guildGroupRegistry.Get(groupID)
	if guildGroup == nil {
		return nil
	}

	// Check if the updated role is a managed role
	managedRoleSet := guildGroup.RoleMap.AsSet()
	if _, isManagedRole := managedRoleSet[e.Role.ID]; !isManagedRole {
		// Not a managed role we care about, ignore
		return nil
	}

	// Determine which managed role this is for better logging
	roleType := "unknown"
	switch e.Role.ID {
	case guildGroup.RoleMap.AccountLinked:
		roleType = "linked"
	case guildGroup.RoleMap.Member:
		roleType = "member"
	case guildGroup.RoleMap.Enforcer:
		roleType = "enforcer"
	case guildGroup.RoleMap.Auditor:
		roleType = "auditor"
	case guildGroup.RoleMap.ServerHost:
		roleType = "server_host"
	case guildGroup.RoleMap.Allocator:
		roleType = "allocator"
	case guildGroup.RoleMap.Suspended:
		roleType = "suspended"
	case guildGroup.RoleMap.APIAccess:
		roleType = "api_access"
	case guildGroup.RoleMap.AccountAgeBypass:
		roleType = "account_age_bypass"
	case guildGroup.RoleMap.VPNBypass:
		roleType = "vpn_bypass"
	case guildGroup.RoleMap.UsernameOnly:
		roleType = "username_only"
	}

	logger = logger.With(
		zap.String("event", "GuildRoleUpdate"),
		zap.String("guild_id", e.GuildID),
		zap.String("role_id", e.Role.ID),
		zap.String("role_name", e.Role.Name),
		zap.String("role_type", roleType),
		zap.String("group_id", groupID),
	)

	// Fetch the audit log to determine who made the change
	// Note: We fetch only the most recent entry (limit=1) for the role update action.
	// In the case of rapid concurrent updates to different roles, the entry is verified
	// against the target role ID to ensure we're processing the correct event.
	auditLogs, err := s.GuildAuditLog(e.GuildID, "", "", int(discordgo.AuditLogActionRoleUpdate), 1)
	if err != nil {
		return fmt.Errorf("error fetching audit log: %w", err)
	}

	// Check if we have audit log entries
	if len(auditLogs.AuditLogEntries) == 0 {
		logger.Warn("No audit log entries found for role update")
		return nil
	}

	latestUpdate := auditLogs.AuditLogEntries[0]

	// Verify this audit log entry is for the role we're tracking
	if latestUpdate.TargetID != e.Role.ID {
		logger.Info("Latest audit log entry does not match the role ID - possible race condition with multiple role updates", 
			zap.String("target_id", latestUpdate.TargetID),
			zap.String("expected_role_id", e.Role.ID))
		return nil
	}

	// Check if the change was made by the bot itself
	if latestUpdate.UserID == s.State.User.ID {
		logger.Debug("Role update was made by the bot itself, skipping audit message")
		return nil
	}

	// Build the audit message
	// Note: We use the UserID from the audit log directly to avoid an extra API call
	// The audit log already contains the necessary user identification
	issuerMention := fmt.Sprintf("<@%s>", latestUpdate.UserID)

	// Extract changes from audit log if available
	var changeDetails string
	if len(latestUpdate.Changes) > 0 {
		changeDetails = " Changes:"
		for _, change := range latestUpdate.Changes {
			changeDetails += fmt.Sprintf("\n  • %s: `%v` → `%v`", change.Key, change.OldValue, change.NewValue)
		}
	}

	auditMessage := fmt.Sprintf(
		"⚠️ Managed role `%s` (%s) was modified by %s%s",
		e.Role.Name,
		roleType,
		issuerMention,
		changeDetails,
	)

	if latestUpdate.Reason != "" {
		auditMessage += fmt.Sprintf("\n**Reason:** %s", latestUpdate.Reason)
	}

	// Send the audit message to the guild's audit channel
	if _, err := AuditLogSendGuild(s, guildGroup, auditMessage); err != nil {
		logger.Error("Failed to send audit message for role update", zap.Error(err))
		return fmt.Errorf("failed to send audit message: %w", err)
	}

	logger.Info("Sent audit message for managed role modification", zap.String("issuer_id", latestUpdate.UserID))
	return nil
}

type DisplayNameInUseError struct {
	DisplayName string
	UserIDs     []string
}

func (e DisplayNameInUseError) Error() string {
	return fmt.Sprintf("display name `%s` is already in use by %s", e.DisplayName, e.UserIDs)
}

func (d *DiscordIntegrator) CheckUser2FA(ctx context.Context, userID uuid.UUID) (bool, error) {
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

func (d *DiscordIntegrator) ReplaceMentions(message string) string {

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

func (d *DiscordIntegrator) LogServiceAuditMessage(ctx context.Context, message string, replaceMentions bool) (*discordgo.Message, error) {
	// replace all <@uuid> mentions with <@discordID>

	if settings := ServiceSettings(); settings.ServiceGuildID != "" {
		if replaceMentions {
			message = d.ReplaceMentions(message)
		}
		return d.dg.ChannelMessageSend(settings.ServiceGuildID, message)
	}
	return nil, nil
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

func SendUserMessage(ctx context.Context, dg *discordgo.Session, userID, message string) (*discordgo.Message, error) {
	channel, err := dg.UserChannelCreate(userID)
	if err != nil {
		return nil, fmt.Errorf("error creating user channel: %w", err)
	}

	return dg.ChannelMessageSend(channel.ID, message)
}

func (d *DiscordIntegrator) GuildGroupName(groupID string) string {
	guildID := d.GroupIDToGuildID(groupID)
	if guildID == "" {
		return ""
	}
	guild, err := d.dg.Guild(guildID)
	if err != nil {
		return ""
	}
	return guild.Name
}

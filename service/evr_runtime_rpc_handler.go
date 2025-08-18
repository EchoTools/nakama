package service

import (
	"context"
	"database/sql"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server"
)

type RPCHandler struct {
	ctx context.Context
	db  *sql.DB
	dg  *discordgo.Session

	sessionRegistry server.SessionRegistry
	matchRegistry   server.MatchRegistry
	tracker         server.Tracker
	metrics         server.Metrics
	config          server.Config
	sessionCache    server.SessionCache

	responseCache *Cache
	idcache       *server.MapOf[string, string]
}

func NewRPCHandler(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer, sessionRegistry server.SessionRegistry, matchRegistry server.MatchRegistry, tracker server.Tracker, metrics server.Metrics, sessionCache server.SessionCache, config server.Config, dg *discordgo.Session) *RPCHandler {
	return &RPCHandler{
		ctx: ctx,
		db:  db,

		sessionRegistry: sessionRegistry,
		matchRegistry:   matchRegistry,
		tracker:         tracker,
		metrics:         metrics,

		dg:            dg,
		responseCache: NewCache(),
		idcache:       &server.MapOf[string, string]{},
	}
}

// Purge removes both the reference and the reverse from the cache.
func (h *RPCHandler) cacheDelete(id string) bool {
	value, loaded := h.idcache.LoadAndDelete(id)
	if !loaded {
		return false
	}
	h.idcache.Delete(value)
	return true
}

// Discord ID to Nakama UserID, with a lookup cache
func (d *RPCHandler) DiscordIDToUserID(discordID string) string {
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

func (d *RPCHandler) UserIDToDiscordID(userID string) string {
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
func (d *RPCHandler) GuildIDToGroupID(guildID string) string {
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

func (c *RPCHandler) GroupIDToGuildID(groupID string) string {
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

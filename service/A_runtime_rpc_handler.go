package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server"
	"go.uber.org/zap"
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

func NewRPCHandler(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, sessionRegistry server.SessionRegistry, matchRegistry server.MatchRegistry, tracker server.Tracker, metrics server.Metrics, sessionCache server.SessionCache, config server.Config, dg *discordgo.Session) *RPCHandler {
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

type AuthenticatePasswordRequest struct {
	UserID       string `json:"user_id"`
	DiscordID    string `json:"discord_id"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	RefreshToken string `json:"refresh_token"`
	IntentStr    string `json:"intents"`
}

var ErrAuthenticateFailed = runtime.NewError("authentication failed", StatusUnauthenticated)
var RPCErrInvalidRequest = runtime.NewError("invalid request", StatusInvalidArgument)

func (h *RPCHandler) AuthenticatePasswordRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, _nk runtime.NakamaModule, payload string) (string, error) {
	nk := _nk.(*server.RuntimeGoNakamaModule)

	if payload == "" {
		return "", RPCErrInvalidRequest
	}

	request := AuthenticatePasswordRequest{}
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", runtime.NewError(fmt.Sprintf("Failed to unmarshal request: %v", err), StatusInvalidArgument)
	}

	var err error
	var userID, username, tokenID string
	var tokenIssuedAt int64 = time.Now().UTC().Unix()
	var vars map[string]string
	if request.RefreshToken != "" {
		var userUUID uuid.UUID

		zapLogger, err := zap.NewProduction()

		zapLogger.Info("AuthenticatePasswordRPC with refresh token")
		userUUID, username, vars, tokenID, tokenIssuedAt, err = server.SessionRefresh(ctx, zapLogger, db, h.config, h.sessionCache, request.RefreshToken)
		if err != nil {
			return "", err
		}
		userID = userUUID.String()
	} else {

		switch {

		case request.UserID != "":
			userID = request.UserID

		case request.DiscordID != "":
			userID, err = GetUserIDByDiscordID(ctx, db, request.DiscordID)
			if err != nil {
				return "", err
			}
		case request.Username != "":
			users, err := nk.UsersGetUsername(ctx, []string{request.Username})
			if err != nil {
				return "", err
			}
			if len(users) != 1 {
				return "", ErrAuthenticateFailed
			}
			userID = users[0].Id

		default:
			return "", RPCErrInvalidRequest
		}

		sessionVars := SessionVars{}

		// If the user requests intents, they must be a global developer
		if request.IntentStr != "" {
			// Check if they have the required intents
			if isMember, err := CheckGroupMembershipByName(ctx, db, userID, GroupGlobalDevelopers, "system"); err != nil {
				return "", err
			} else if isMember {
				sessionVars.Intents.UnmarshalText([]byte(request.IntentStr))
			}
		}
		vars = sessionVars.MarshalVars()

		var account *api.Account
		if account, err = nk.AccountGetId(ctx, userID); err != nil || account == nil {
			return "", server.ErrAccountNotFound
		}
		if userID, username, _, err = nk.AuthenticateEmail(ctx, account.Email, request.Password, "", false); err != nil {
			return "", err
		}
		tokenID = uuid.Must(uuid.NewV4()).String()

		guildGroups, err := GuildUserGroupsList(ctx, nk, nil, userID)
		if err != nil {
			return "", err
		}

		varMemberships := make(map[string]uint64)

		for id, gg := range guildGroups {
			varMemberships[id] = gg.MembershipBitSet(userID)
		}

		data, _ := json.Marshal(varMemberships)
		vars["memberships"] = string(data)
	}

	tokenExpirySecs := int64(h.config.GetSession().GetRefreshTokenExpirySec())
	if tokenExpirySecs <= 0 {
		tokenExpirySecs = 3600
	}
	exp := tokenIssuedAt + tokenExpirySecs
	encKey := h.config.GetSession().GetEncryptionKey()
	// Generate a new token and refresh token.
	token, exp := generateToken(tokenExpirySecs, encKey, tokenID, tokenIssuedAt, userID, username, vars)
	refreshToken, refreshExp := generateRefreshToken(tokenExpirySecs, encKey, tokenID, tokenIssuedAt, userID, username, vars)
	h.sessionCache.Add(uuid.FromStringOrNil(userID), exp, tokenID, refreshExp, tokenID)
	session := &api.Session{Token: token, RefreshToken: refreshToken}

	response, err := json.Marshal(session)
	if err != nil {
		return "", runtime.NewError(fmt.Errorf("error marshalling response: %w", err).Error(), StatusInvalidArgument)
	}
	return string(response), nil
}

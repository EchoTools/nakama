package service

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"github.com/go-redis/redis"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.uber.org/atomic"

	_ "google.golang.org/protobuf/proto"
)

var (
	appAcceptorPtr     = atomic.NewPointer[func(http.ResponseWriter, *http.Request)](nil)
	AppAcceptorProxyFn = func(w http.ResponseWriter, r *http.Request) {
		if fn := appAcceptorPtr.Load(); fn != nil {
			(*fn)(w, r)
		}
	}

	nakamaStartTime = time.Now().UTC()
)

const (
	GroupGlobalDevelopers        = "Global Developers"
	GroupGlobalOperators         = "Global Operators"
	GroupGlobalTesters           = "Global Testers"
	GroupGlobalBots              = "Global Bots"
	GroupGlobalBadgeAdmins       = "Global Badge Admins"
	GroupGlobalPrivateDataAccess = "Global Private Data Access"
	GroupGlobalRequire2FA        = "Global Require 2FA"
	SystemGroupLangTag           = "system"
	GuildGroupLangTag            = "guild"
)

func ServiceSettingsLoop(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule) {
	if _, err := ServiceSettingsLoad(ctx, nk); err != nil {
		logger.WithField("err", err).Error("Failed to load global settings")
		panic(err)
	}
	// Load the service settings every 30 seconds
	interval := 30 * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			ticker.Stop()
			return
		case <-ticker.C:
			if _, err := ServiceSettingsLoad(ctx, nk); err != nil {
				logger.WithField("error", err).Warn("Failed to load global settings")
			}
		}
	}
}

type MatchLabelMeta struct {
	TickRate  int
	Presences []*rtapi.UserPresence
	State     *MatchLabel
}

func CreateCoreGroups(ctx context.Context, db *sql.DB, nk runtime.NakamaModule) error {
	// Create user for use by the discord bot (and core group ownership)
	userId, _, _, err := nk.AuthenticateDevice(ctx, SystemUserID, "discordbot", true)
	if err != nil {
		return fmt.Errorf("Error creating discordbot user: %v", err)
	}

	coreGroups := []string{
		GroupGlobalDevelopers,
		GroupGlobalOperators,
		GroupGlobalTesters,
		GroupGlobalBadgeAdmins,
		GroupGlobalBots,
		GroupGlobalPrivateDataAccess,
		GroupGlobalRequire2FA,
	}

	for _, name := range coreGroups {
		// Search for group first
		groups, _, err := nk.GroupsList(ctx, name, "", nil, nil, 1, "")
		if err != nil {
			return fmt.Errorf("group list error: %v", err)
		}
		// remove groups that are not lang tag of 'system'
		for i, group := range groups {
			if group.LangTag != SystemGroupLangTag {
				groups = append(groups[:i], groups[i+1:]...)
			}
		}

		if len(groups) == 0 {
			// Create a nakama core group
			_, err = nk.GroupCreate(ctx, userId, name, userId, SystemGroupLangTag, name, "", false, map[string]interface{}{}, 1000)
			if err != nil {
				return fmt.Errorf("group `%s` create error: %v", name, err)
			}
		}
	}

	return nil
}

// Register Indexes for any Storables
func RegisterIndexes(ctx context.Context, storageIndex server.StorageIndex) error {

	// Register storage indexes for any Storables
	storables := []StorableIndexer{
		&DisplayNameHistory{},
		&DeveloperApplications{},
		&MatchmakingSettings{},
		&VRMLPlayerSummary{},
		&LoginHistory{},
		&LinkTicketStore{},
		&ConfigDocumentData{},
		&ChannelInfoData{},
		&GuildGroupState{},
	}
	for _, s := range storables {
		for _, idx := range s.StorageIndexes() {
			if err := storageIndex.CreateIndex(ctx, idx.Name, idx.Collection, idx.Key, idx.Fields, idx.SortableFields, idx.MaxEntries, idx.IndexOnly); err != nil {
				return err
			}
		}
	}
	return nil
}

func ConnectMongoDB(ctx context.Context, mongoURI string) (*mongo.Client, error) {
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(mongoURI))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to MongoDB: %v", err)
	}

	// Ping the database to verify connection
	if err := client.Ping(ctx, nil); err != nil {
		client.Disconnect(ctx)
		return nil, fmt.Errorf("failed to ping MongoDB: %v", err)
	}

	return client, nil
}

func ConnectRedis(ctx context.Context, redisURL string) (*redis.Client, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Redis URL: %v", err)
	}
	client := redis.NewClient(opt)
	if err := client.Ping().Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %v", err)
	}
	return client, nil
}

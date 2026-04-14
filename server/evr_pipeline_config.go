package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

type cachedConfigEntry struct {
	json   string
	expiry time.Time
}

var configCache sync.Map // map[string]*cachedConfigEntry
const configCacheTTL = 5 * time.Minute

func (p *EvrPipeline) configRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	message := in.(*evr.ConfigRequest)

	// Check in-memory cache first — config is global and rarely changes,
	// so we don't need a DB hit on every unauthenticated ConfigRequest.
	cacheKey := message.Type + ":" + message.ID
	if v, ok := configCache.Load(cacheKey); ok {
		entry := v.(*cachedConfigEntry)
		if time.Now().Before(entry.expiry) {
			resource := make(map[string]any)
			if err := json.Unmarshal([]byte(entry.json), &resource); err == nil {
				return session.SendEvrUnrequire(evr.NewConfigSuccess(message.Type, message.ID, resource))
			}
		}
	}

	// Retrieve the requested object.
	objs, err := StorageReadObjects(ctx, logger, session.pipeline.db, uuid.Nil, []*api.ReadStorageObjectId{
		{
			Collection: "Config:" + message.Type,
			Key:        message.Type,
			UserId:     uuid.Nil.String(),
		},
	})

	var jsonResource string
	if err != nil {
		// DB error: log and fall back to the default below.
		logger.Warn("failed to read config objects, falling back to default", zap.Error(err))
	} else if len(objs.Objects) != 0 {
		jsonCandidate := objs.Objects[0].Value
		// Validate the stored JSON before using it.
		var probe map[string]any
		if jsonErr := json.Unmarshal([]byte(jsonCandidate), &probe); jsonErr != nil {
			logger.Warn("stored config resource is invalid JSON, falling back to default",
				zap.String("type", message.Type), zap.Error(jsonErr))
		} else {
			jsonResource = jsonCandidate
		}
	}

	// Fall back to the built-in default if no valid DB entry was found.
	if jsonResource == "" {
		jsonResource = evr.GetDefaultConfigResource(message.Type, message.ID)
	}

	if jsonResource == "" {
		logger.Warn("config resource not found", zap.String("type", message.Type), zap.String("id", message.ID))
		session.SendEvrUnrequire(evr.NewConfigFailure(message.Type, message.ID))
		return fmt.Errorf("config resource not found: type=%s id=%s", message.Type, message.ID)
	}

	// Parse the JSON resource into a generic map for re-serialization.
	resource := make(map[string]any)
	if err := json.Unmarshal([]byte(jsonResource), &resource); err != nil {
		// This should not happen for defaults; log and report.
		logger.Error("failed to parse config resource JSON",
			zap.String("type", message.Type), zap.Error(err))
		session.SendEvrUnrequire(evr.NewConfigFailure(message.Type, message.ID))
		return fmt.Errorf("failed to parse config JSON: type=%s: %w", message.Type, err)
	}

	// Cache the result.
	configCache.Store(cacheKey, &cachedConfigEntry{json: jsonResource, expiry: time.Now().Add(configCacheTTL)})

	// Send the resource to the client.
	if err := session.SendEvrUnrequire(evr.NewConfigSuccess(message.Type, message.ID, resource)); err != nil {
		return fmt.Errorf("failed to send SNSConfigSuccess: %w", err)
	}
	return nil
}

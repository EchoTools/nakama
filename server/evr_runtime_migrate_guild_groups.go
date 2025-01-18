package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

var _ = SystemMigrator(&MigrationGuildGroups{})

type MigrationGuildGroups struct{}

func (m *MigrationGuildGroups) MigrateSystem(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) error {

	startTime := time.Now()

	cursor := ""
	for {
		groups, cursor, err := nk.GroupsList(ctx, "", "guild", nil, nil, 100, cursor)
		if err != nil {
			return err
		}

		for _, group := range groups {
			logger = logger.WithField("group_id", group.Id)
			// Migrate the group's metadata.
			if err := m.migrateGroupMetadata(ctx, logger, db, nk, group); err != nil {
				logger.Error("Failed to migrate group metadata", zap.Error(err))
			}
		}

		if cursor == "" {
			break
		}
	}

	logger.WithField("duration_ms", time.Since(startTime).Milliseconds()).Info("Migrated guild group metadata")
	return nil
}

func (*MigrationGuildGroups) migrateGroupMetadata(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, group *api.Group) error {
	// Get the group metadata.
	md := map[string]interface{}{}

	if err := json.Unmarshal([]byte(group.Metadata), &md); err != nil {
		return err
	}

	// Convert the RoleCache from a slice to a map.
	migrated := false
	if oldCache, ok := md["role_cache"].(map[string]interface{}); ok {
		newCache := make(map[string]map[string]struct{})

		for role, users := range oldCache {
			if userIDs, ok := users.([]interface{}); ok {
				newCache[role] = make(map[string]struct{})
				for _, userID := range userIDs {
					if userID, ok := userID.(string); ok {
						migrated = true
						newCache[role][userID] = struct{}{}
					}
				}
			}
		}
		if migrated {
			md["role_cache"] = newCache
		}
	}
	// Convert the CommunityValues from a slice to a map.
	communityValues := make(map[string]time.Time)
	if userIDs, ok := md["community_values_user_ids"].([]interface{}); ok {
		for _, userID := range userIDs {
			if userID, ok := userID.(string); ok {
				migrated = true
				communityValues[userID] = time.Now()
			}
		}
	}

	md["community_values_user_ids"] = communityValues

	if err := nk.GroupUpdate(ctx, group.Id, "", "", "", "", "", "", false, md, 500000); err != nil {
		return err
	}

	return nil
}

package server

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"go.uber.org/atomic"
	"go.uber.org/zap"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

type GuildGroupRegistry struct {
	ctx    context.Context
	logger runtime.Logger
	nk     runtime.NakamaModule
	db     *sql.DB

	writeMu        sync.Mutex // Mutex to protect writes to the guildGroups map
	guildGroups    *atomic.Pointer[map[uuid.UUID]*GuildGroup]
	inheritanceMap *atomic.Pointer[map[uuid.UUID]map[uuid.UUID]struct{}] // Maps downstream group IDs to upstream group IDs
}

func NewGuildGroupRegistry(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, db *sql.DB) *GuildGroupRegistry {

	registry := &GuildGroupRegistry{
		ctx:    ctx,
		logger: logger,
		nk:     nk,
		db:     db,

		guildGroups:    atomic.NewPointer(&map[uuid.UUID]*GuildGroup{}),
		inheritanceMap: atomic.NewPointer(&map[uuid.UUID]map[uuid.UUID]struct{}{}),
	}
	// Regularly update the guild groups from the database.
	go func() {
		ctx := ctx
		ticker := time.NewTicker(time.Second * 1)
		firstRun := true
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Initial delay before starting the ticker
				ticker.Reset(time.Minute * 1)
			}
			registry.writeMu.Lock()
			registry.rebuildGuildGroups()
			registry.writeMu.Unlock()

			if firstRun {
				firstRun = false
				for _, gg := range registry.GuildGroups() {
					// Store the guild groups back to ensure all the fields are set.
					if err := nk.GroupUpdate(ctx, gg.IDStr(), "", "", "", "", "", "", gg.Group.Open.Value, gg.MarshalMap(), int(gg.Group.MaxCount)); err != nil {
						logger.Warn("Error updating guild group", zap.Error(err))
						continue
					}
				}
			}
			logger.Debug("Guild group registry updated")
		}
	}()

	return registry
}

func (r *GuildGroupRegistry) rebuildGuildGroups() {
	var (
		ctx    = r.ctx
		logger = r.logger
		nk     = r.nk
	)
	// Load all guild groups from the database.
	guildGroups := make(map[uuid.UUID]*GuildGroup)
	inheritanceMap := make(map[uuid.UUID]map[uuid.UUID]struct{})

	cursor := ""
	for {
		// List all guild groups.
		groups, cursor, err := nk.GroupsList(ctx, "", "guild", nil, nil, 100, cursor)
		if err != nil {
			logger.Warn("Error listing guild groups", zap.Error(err))
			break
		}

		for _, group := range groups {
			// Load the state
			state, err := GuildGroupStateLoad(ctx, nk, ServiceSettings().DiscordBotUserID, group.Id)
			if err != nil {
				logger.Warn("Error loading guild group state", zap.Error(err))
				continue
			}
			gUUID := uuid.FromStringOrNil(group.Id)
			gg, err := NewGuildGroup(group, state)
			if err != nil {
				logger.Warn("Error creating guild group", zap.Error(err))
				continue
			}
			// Add the group to the registry.
			guildGroups[gUUID] = gg

			// Build the inheretence map.
			inheritanceMap[gUUID] = make(map[uuid.UUID]struct{}, len(gg.SuspensionInheritanceGroupIDs))
			for _, parentID := range gg.SuspensionInheritanceGroupIDs {
				// Ensure the parent ID is valid.
				pUUID, err := uuid.FromString(parentID)
				if err != nil {
					logger.Warn("Invalid parent group ID in metadata", zap.String("parentID", parentID), zap.Error(err))
					continue
				}
				// Add the parent ID to the inheritance map.
				inheritanceMap[gUUID][pUUID] = struct{}{}
			}
		}
		if cursor == "" {
			break
		}
	}
	// Update the registry with the new guild groups and inheritance map.
	r.guildGroups.Store(&guildGroups)
	r.inheritanceMap.Store(&inheritanceMap)
}

func (r *GuildGroupRegistry) InheretanceMap() map[uuid.UUID]map[uuid.UUID]struct{} {
	return *r.inheritanceMap.Load()
}

func (r *GuildGroupRegistry) GuildGroups() map[uuid.UUID]*GuildGroup {
	return *r.guildGroups.Load()
}

func (r *GuildGroupRegistry) Stop() {}

func (r *GuildGroupRegistry) Get(groupID string) *GuildGroup {
	gid := uuid.FromStringOrNil(groupID)
	if gid == uuid.Nil {
		// Return nil
		return nil
	}

	return r.GuildGroups()[gid]
}

func (r *GuildGroupRegistry) Add(gg *GuildGroup) {
	if gg.ID().IsNil() {
		return
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	// Rebuild the guild groups map
	newGuildGroups := make(map[uuid.UUID]*GuildGroup, len(*r.guildGroups.Load())+1)
	for id, group := range *r.guildGroups.Load() {
		newGuildGroups[id] = group
	}
	newGuildGroups[gg.ID()] = gg
	r.guildGroups.Store(&newGuildGroups)
}

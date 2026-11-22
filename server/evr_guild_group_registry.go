package server

import (
	"context"
	"database/sql"
	"maps"
	"sync"
	"time"

	"go.uber.org/atomic"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

type GuildGroupRegistry struct {
	ctx    context.Context
	logger runtime.Logger
	nk     runtime.NakamaModule
	db     *sql.DB

	writeMu        sync.Mutex                              // Mutex to protect writes to the guildGroups map
	guildGroups    *atomic.Pointer[map[string]*GuildGroup] // map[groupID]GuildGroup
	inheritanceMap *atomic.Pointer[map[string][]string]    // map[srcGroupID][]dstGroupID

	// inheritanceFilters holds the non-default filter of each inheritance link.
	// It is nil on a registry built without NewGuildGroupRegistry (test doubles),
	// which InheritanceFilters reads as "every link inherits everything".
	inheritanceFilters *atomic.Pointer[InheritanceFilters]
}

func NewGuildGroupRegistry(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, db *sql.DB) *GuildGroupRegistry {

	registry := &GuildGroupRegistry{
		ctx:    ctx,
		logger: logger,
		nk:     nk,
		db:     db,

		guildGroups:    atomic.NewPointer(&map[string]*GuildGroup{}),
		inheritanceMap: atomic.NewPointer(&map[string][]string{}),

		inheritanceFilters: atomic.NewPointer(&InheritanceFilters{}),
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
						logger.WithField("error", err).Warn("Error updating guild group")
						continue
					}
				}
			}
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

	var (
		guildGroups    = make(map[string]*GuildGroup, len(*r.guildGroups.Load()))
		inheritanceMap = make(map[string][]string, len(guildGroups))
		filters        = InheritanceFilters{}
		cursor         string
	)
	for {
		var groups []*api.Group
		var err error
		// List all guild groups.
		groups, cursor, err = nk.GroupsList(ctx, "", GuildGroupLangTag, nil, nil, 100, cursor)
		if err != nil {
			logger.WithField("error", err).Warn("Error listing guild groups")
			break
		}

		for _, group := range groups {
			// Load the state
			state, err := GuildGroupStateLoad(ctx, nk, ServiceSettings().DiscordBotUserID, group.Id)
			if err != nil {
				logger.WithField("error", err).Warn("Error loading guild group state")
				continue
			}
			gg, err := NewGuildGroup(group, state)
			if err != nil {
				logger.WithField("error", err).Warn("Error creating guild group")
				continue
			}
			// Add the group to the registry.
			guildGroups[group.Id] = gg

			// Build the inheritance map and the per-link filters.
			addInheritanceLinks(group.Id, &gg.GroupMetadata, inheritanceMap, filters)
		}
		if cursor == "" {
			break
		}
	}
	// Update the registry with the new guild groups and inheritance map.
	r.guildGroups.Store(&guildGroups)
	r.inheritanceMap.Store(&inheritanceMap)
	r.inheritanceFilters.Store(&filters)
}

// addInheritanceLinks records every link the guild inheritingGroupID has to a
// source guild, and the guild's filter on it when that filter is not the default.
func addInheritanceLinks(inheritingGroupID string, metadata *GroupMetadata, inheritanceMap map[string][]string, filters InheritanceFilters) {
	for _, parentID := range metadata.SuspensionInheritanceGroupIDs {
		inheritanceMap[parentID] = append(inheritanceMap[parentID], inheritingGroupID)
		if f := metadata.InheritanceFilterFor(parentID); !f.IsDefault() {
			if filters[parentID] == nil {
				filters[parentID] = make(map[string]SuspensionInheritanceFilter)
			}
			filters[parentID][inheritingGroupID] = f
		}
	}
}

// map[parentGroupID]map[childGroupID]bool
func (r *GuildGroupRegistry) InheritanceByParentGroupID() map[string][]string {
	return *r.inheritanceMap.Load()
}

// InheritanceFilters returns the filter on each inheritance link that has one.
// Pass it, with InheritanceByParentGroupID, to CheckEnforcementSuspensions.
func (r *GuildGroupRegistry) InheritanceFilters() InheritanceFilters {
	if r.inheritanceFilters == nil {
		return nil
	}
	return *r.inheritanceFilters.Load()
}

func (r *GuildGroupRegistry) GuildGroups() map[string]*GuildGroup {
	return *r.guildGroups.Load()
}

// Length returns the number of guild groups in the registry.
func (r *GuildGroupRegistry) Length() int {
	return len(*r.guildGroups.Load())
}

// Get a guild group by its ID.
func (r *GuildGroupRegistry) Get(groupID string) *GuildGroup {
	return r.GuildGroups()[groupID]
}

// Add a guild group to the registry.
func (r *GuildGroupRegistry) Add(gg *GuildGroup) {
	if gg == nil || gg.ID().IsNil() {
		return
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	// Rebuild the guild groups map
	newMap := maps.Clone(*r.guildGroups.Load())
	newMap[gg.IDStr()] = gg
	r.guildGroups.Store(&newMap)
}

func (r *GuildGroupRegistry) Stop() {}

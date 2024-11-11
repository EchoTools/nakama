package server

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

type GuildGroupCache struct {
	sync.Mutex // For writers only
	ctx        context.Context
	cancelFn   context.CancelFunc

	logger runtime.Logger
	nk     runtime.NakamaModule

	cache *atomic.Value // map[string]*GuildGroup
}

func NewGuildGroupCache(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule) *GuildGroupCache {
	ctx, cancelFn := context.WithCancel(ctx)
	return &GuildGroupCache{
		ctx:      ctx,
		cancelFn: cancelFn,
		nk:       nk,
		cache:    &atomic.Value{},
	}
}

func (g *GuildGroupCache) Start() {
	g.cache.Store(make(map[string]*GuildGroup))

	// Reload the guild groups
	if err := g.loadGuildGroups(); err != nil {
		g.logger.Error("Failed to load guild groups", zap.Error(err))
	}

	go func() {
		guildGroupTicker := time.NewTicker(3 * time.Minute)
		defer guildGroupTicker.Stop()

		for {
			select {
			case <-g.ctx.Done():
				g.Stop()
				return
			case <-guildGroupTicker.C:
			}

			// Reload the guild groups
			if err := g.loadGuildGroups(); err != nil {
				g.logger.Error("Failed to load guild groups", zap.Error(err))
			}
		}
	}()
}

func (g *GuildGroupCache) Stop() {
	g.cancelFn()
}

func (g *GuildGroupCache) loadGuildGroups() error {
	// Retrieve the guild groups
	g.Lock()
	defer g.Unlock()
	guildGroups := make(map[string]*GuildGroup, 100)

	cursor := ""
	var groups []*api.Group
	var err error

	for {
		groups, cursor, err = g.nk.GroupsList(g.ctx, "", "guild", nil, nil, 100, "")
		if err != nil {
			return err
		}

		for _, group := range groups {
			gg, err := NewGuildGroup(group)
			if err != nil {
				return err
			}
			guildGroups[gg.Group.Id] = gg
		}

		if cursor == "" {
			break
		}
	}

	g.cache.Store(guildGroups)
	return nil
}

func (d *GuildGroupCache) GuildGroups() map[string]*GuildGroup {
	return d.cache.Load().(map[string]*GuildGroup)
}

func (d *GuildGroupCache) GuildGroup(groupID string) (*GuildGroup, bool) {
	guildGroups := d.GuildGroups()
	g, ok := guildGroups[groupID]
	return g, ok
}

func (d *GuildGroupCache) UpdateMetadata(ctx context.Context, groupID string, metadata *GroupMetadata) error {
	d.Lock()
	defer d.Unlock()

	return d.update(ctx, groupID, metadata)
}

func (d *GuildGroupCache) update(ctx context.Context, groupID string, metadata *GroupMetadata) error {

	// Marshal the metadata
	mdMap, err := metadata.MarshalToMap()
	if err != nil {
		return fmt.Errorf("error marshalling group metadata: %w", err)
	}

	// Get the group
	groups, err := d.nk.GroupsGetId(ctx, []string{groupID})
	if err != nil {
		return fmt.Errorf("error getting group: %w", err)
	}
	if len(groups) == 0 {
		return fmt.Errorf("group not found")
	}
	g := groups[0]

	// Update the group
	if err := d.nk.GroupUpdate(ctx, g.Id, SystemUserID, g.Name, g.CreatorId, g.LangTag, g.Description, g.AvatarUrl, g.Open.Value, mdMap, int(g.MaxCount)); err != nil {
		return fmt.Errorf("error updating group: %w", err)
	}

	guildGroups := d.GuildGroups()
	newMap := make(map[string]*GuildGroup, len(guildGroups))
	for k, v := range guildGroups {
		newMap[k] = v
	}

	newMap[groupID] = &GuildGroup{
		Metadata: metadata,
		Group:    g,
	}

	d.cache.Store(newMap)

	return nil
}

func (g *GuildGroupCache) UpdateMemberRoles(ctx context.Context, groupID, userID string, assignedRoles []string) error {
	g.Lock()
	defer g.Unlock()

	guildGroup, ok := g.GuildGroup(groupID)
	if !ok {
		return fmt.Errorf("error getting guild group")
	}

	// Update the Guild Group's role cache if necessary.

	updated := false

	roleCache := guildGroup.Metadata.RoleCache

	if roleCache == nil {
		updated = true
		roleCache = make(map[string][]string)
	}

	// Ensure the guildRoles exist in the cache
	newCache := make(map[string][]string)

	for _, role := range guildGroup.Metadata.Roles.Slice() {

		isAssigned := slices.Contains(assignedRoles, role)

		if _, ok := roleCache[role]; !ok {
			updated = true
			roleCache[role] = make([]string, 0)
		}
		newCache[role] = make([]string, 0)

		for _, id := range roleCache[role] {

			if !isAssigned && id == userID {
				updated = true
				continue // Skip the user
			}
			newCache[role] = append(newCache[role], id)

		}

		if isAssigned && !slices.Contains(newCache[role], userID) {
			newCache[role] = append(newCache[role], userID)
			updated = true
		}
	}

	md := *guildGroup.Metadata
	md.RoleCache = newCache

	if updated {
		return g.update(ctx, guildGroup.ID().String(), &md)
	}
	return nil
}

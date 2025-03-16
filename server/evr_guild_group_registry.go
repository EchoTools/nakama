// Copyright 2018 The Nakama Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

type GuildGroupRegistry struct {
	sync.RWMutex
	ctx    context.Context
	logger runtime.Logger
	nk     runtime.NakamaModule
	db     *sql.DB

	guildGroups map[uuid.UUID]*GuildGroup
}

func NewGuildGroupRegistry(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, db *sql.DB) *GuildGroupRegistry {

	registry := &GuildGroupRegistry{
		ctx:    ctx,
		logger: logger,
		nk:     nk,
		db:     db,

		guildGroups: make(map[uuid.UUID]*GuildGroup),
	}

	// Regularly update the guild groups from the database.
	go func() {
		ctx := ctx
		ticker := time.NewTicker(time.Minute * 1)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				registry.Lock()
				// Load all guild groups from the database.
				registry.guildGroups = make(map[uuid.UUID]*GuildGroup)

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
						if state, err := GuildGroupStateLoad(ctx, nk, ServiceSettings().DiscordBotUserID, group.Id); err != nil {
							logger.Warn("Error loading guild group state", zap.Error(err))
							continue
						} else if registry.guildGroups[uuid.FromStringOrNil(group.Id)], err = NewGuildGroup(group, state); err != nil {
							logger.Warn("Error creating guild group", zap.Error(err))
							continue
						}
					}
					if cursor == "" {
						break
					}
				}
				registry.Unlock()
			}
		}
	}()

	return registry
}

func (r *GuildGroupRegistry) Stop() {}

func (r *GuildGroupRegistry) Get(groupID string) *GuildGroup {
	gid := uuid.FromStringOrNil(groupID)
	if gid == uuid.Nil {
		// Return nil
		return nil
	}

	r.RLock()
	defer r.RUnlock()
	gg, ok := r.guildGroups[gid]
	if !ok {
		return nil
	}
	return gg
}

func (r *GuildGroupRegistry) Add(gg *GuildGroup) {
	if gg.ID().IsNil() {
		return
	}
	r.Lock()
	defer r.Unlock()
	r.guildGroups[gg.ID()] = gg
}

func (r *GuildGroupRegistry) Remove(groupID uuid.UUID) {
	r.Lock()
	defer r.Unlock()
	delete(r.guildGroups, groupID)
}

// MetadataLoad retrieves the metadata for a guild group from the database.
func (r *GuildGroupRegistry) MetadataLoad(ctx context.Context, groupID string) (*GroupMetadata, error) {
	r.Lock()
	defer r.Unlock()
	metadata, err := GroupMetadataLoad(ctx, r.db, groupID)
	if err != nil {
		return nil, fmt.Errorf("error loading group metadata: %w", err)
	}
	return metadata, nil
}

// MetadataStore writes the metadata for a guild group to the database.
func (r *GuildGroupRegistry) MetadataStore(ctx context.Context, groupID uuid.UUID, metadata *GroupMetadata) error {
	r.Lock()
	defer r.Unlock()
	data, err := metadata.MarshalToMap()
	if err != nil {
		return fmt.Errorf("error marshalling group data: %w", err)
	}
	if err := r.nk.GroupUpdate(ctx, groupID.String(), SystemUserID, "", "", "", "", "", false, data, 1000000); err != nil {
		return fmt.Errorf("error updating group: %w", err)
	}
	return nil
}

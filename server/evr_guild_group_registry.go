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
	"github.com/gofrs/uuid/v5"
)

type GuildGroupRegistry struct {
	guildGroups *MapOf[uuid.UUID, *GuildGroup]
}

func NewGuildGroupRegistry() *GuildGroupRegistry {
	return &GuildGroupRegistry{
		guildGroups: &MapOf[uuid.UUID, *GuildGroup]{},
	}
}

func (r *GuildGroupRegistry) Stop() {}

func (r *GuildGroupRegistry) Get(groupID uuid.UUID) *GuildGroup {
	if groupID.IsNil() {
		return nil
	}

	gg, ok := r.guildGroups.Load(groupID)
	if !ok {
		return nil
	}
	return gg
}

func (r *GuildGroupRegistry) Add(gg *GuildGroup) {
	if gg.ID().IsNil() {
		return
	}
	r.guildGroups.Store(gg.ID(), gg)
}

func (r *GuildGroupRegistry) Remove(groupID uuid.UUID) {
	r.guildGroups.Delete(groupID)
}

func (r *GuildGroupRegistry) Range(fn func(*GuildGroup) bool) {
	r.guildGroups.Range(func(id uuid.UUID, gg *GuildGroup) bool {
		return fn(gg)
	})
}

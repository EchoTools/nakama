package server

import (
	"encoding/json"
	"slices"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
)

type GuildGroup struct {
	Metadata GroupMetadata
	Group    *api.Group
}

func (g *GuildGroup) GuildID() string {
	return g.Metadata.GuildID
}

func (g *GuildGroup) Name() string {
	return g.Group.Name
}

func (g *GuildGroup) Description() string {
	return g.Group.Description
}

func (g *GuildGroup) ID() uuid.UUID {
	return uuid.FromStringOrNil(g.Group.Id)
}

func (g *GuildGroup) Size() int {
	return int(g.Group.EdgeCount)
}

func (g *GuildGroup) ServerHostUserIDs() []string {
	return g.Metadata.ServerHostUserIDs
}

func (g *GuildGroup) AllocatorUserIDs() []string {
	return g.Metadata.AllocatorUserIDs
}

func (g *GuildGroup) ModeratorUserIDs() []string {
	return g.Metadata.ModeratorUserIDs
}

func (g *GuildGroup) IsServerHost(userID string) bool {
	return slices.Contains(g.ServerHostUserIDs(), userID)
}

func (g *GuildGroup) IsAllocator(userID string) bool {
	return slices.Contains(g.AllocatorUserIDs(), userID)
}

func (g *GuildGroup) IsModerator(userID string) bool {
	return slices.Contains(g.ModeratorUserIDs(), userID)
}

func NewGuildGroup(group *api.Group) *GuildGroup {

	md := &GroupMetadata{}
	if err := json.Unmarshal([]byte(group.Metadata), md); err != nil {
		return nil
	}

	return &GuildGroup{
		Metadata: *md,
		Group:    group,
	}
}

type GuildGroupMembership struct {
	GuildGroup            GuildGroup
	isMember              bool
	isModerator           bool // Admin
	isServerHost          bool // Broadcaster Host
	canAllocateNonDefault bool // Can allocate servers with slash command
}

func NewGuildGroupMembership(group *api.Group, userID uuid.UUID, state api.UserGroupList_UserGroup_State) GuildGroupMembership {
	gg := NewGuildGroup(group)

	return GuildGroupMembership{
		GuildGroup:            *gg,
		isMember:              state <= api.UserGroupList_UserGroup_MEMBER,
		isModerator:           state <= api.UserGroupList_UserGroup_ADMIN,
		isServerHost:          slices.Contains(gg.ServerHostUserIDs(), userID.String()),
		canAllocateNonDefault: slices.Contains(gg.AllocatorUserIDs(), userID.String()),
	}
}

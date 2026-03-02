package server

import (
	"context"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"go.uber.org/atomic"
)

func TestCanShutdownMatch_GlobalOperatorCanShutdownAnyMatch(t *testing.T) {
	userID := uuid.Must(uuid.NewV4()).String()
	serverOperatorID := uuid.Must(uuid.NewV4())

	ctx := WithUserPermissions(context.Background(), &UserPermissions{IsGlobalOperator: true})
	label := &MatchLabel{GameServer: &GameServerPresence{OperatorID: serverOperatorID}}

	allowed, err := (&DiscordAppBot{}).canShutdownMatch(ctx, userID, label)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatal("global operator should be allowed to shutdown any match")
	}
}

func TestCanShutdownMatch_ServerHostOperatorCanShutdown(t *testing.T) {
	serverOperatorID := uuid.Must(uuid.NewV4())
	label := &MatchLabel{GameServer: &GameServerPresence{OperatorID: serverOperatorID}}

	allowed, err := (&DiscordAppBot{}).canShutdownMatch(context.Background(), serverOperatorID.String(), label)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatal("server host operator should be allowed to shutdown this match")
	}
}

func TestCanShutdownMatch_EnforcerOfMatchGuildCanShutdown(t *testing.T) {
	userID := uuid.Must(uuid.NewV4()).String()
	groupID := uuid.Must(uuid.NewV4())

	registry := newTestGuildGroupRegistry()
	registry.Add(newTestGuildGroup(groupID, userID))

	label := &MatchLabel{
		GroupID:    &groupID,
		GameServer: &GameServerPresence{OperatorID: uuid.Must(uuid.NewV4())},
	}

	bot := &DiscordAppBot{guildGroupRegistry: registry}
	allowed, err := bot.canShutdownMatch(context.Background(), userID, label)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatal("guild enforcer should be allowed to shutdown matches in their guild")
	}
}

func TestCanShutdownMatch_EnforcerOfOtherGuildCannotShutdown(t *testing.T) {
	userID := uuid.Must(uuid.NewV4()).String()
	userGuildID := uuid.Must(uuid.NewV4())
	matchGuildID := uuid.Must(uuid.NewV4())

	registry := newTestGuildGroupRegistry()
	registry.Add(newTestGuildGroup(userGuildID, userID))

	label := &MatchLabel{
		GroupID:    &matchGuildID,
		GameServer: &GameServerPresence{OperatorID: uuid.Must(uuid.NewV4())},
	}

	bot := &DiscordAppBot{guildGroupRegistry: registry}
	allowed, err := bot.canShutdownMatch(context.Background(), userID, label)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowed {
		t.Fatal("enforcer from another guild should not be allowed to shutdown this match")
	}
}

func newTestGuildGroupRegistry() *GuildGroupRegistry {
	groups := map[string]*GuildGroup{}
	inheritance := map[string][]string{}
	return &GuildGroupRegistry{
		guildGroups:    atomic.NewPointer(&groups),
		inheritanceMap: atomic.NewPointer(&inheritance),
	}
}

func newTestGuildGroup(groupID uuid.UUID, enforcerUserID string) *GuildGroup {
	enforcerRoleID := "role-enforcer"
	return &GuildGroup{
		GroupMetadata: GroupMetadata{
			RoleMap: GuildGroupRoles{Enforcer: enforcerRoleID},
		},
		State: &GuildGroupState{
			GroupID: groupID.String(),
			RoleCache: map[string]map[string]bool{
				enforcerRoleID: {
					enforcerUserID: true,
				},
			},
		},
		Group: &api.Group{Id: groupID.String()},
	}
}

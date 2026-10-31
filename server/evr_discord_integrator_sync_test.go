package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestSyncGuildMembersForSession_RefreshesEveryGuildWhenEnabled(t *testing.T) {
	guilds := map[string]*GuildGroup{
		"group-a": {GroupMetadata: GroupMetadata{GuildID: "guild-a"}},
		"group-b": {GroupMetadata: GroupMetadata{GuildID: "guild-b"}},
		"default": {GroupMetadata: GroupMetadata{}},
	}
	var got []string
	syncMember := func(_ context.Context, _ *zap.Logger, discordID, guildID string, full bool) error {
		require.Equal(t, "discord-account", discordID)
		require.True(t, full)
		got = append(got, guildID)
		return nil
	}
	logger := zap.NewNop()

	require.NoError(t, syncGuildMembersForSession(context.Background(), logger, false, "discord-account", guilds, true, syncMember))
	require.Empty(t, got, "disabled flag must preserve today's behavior")
	require.NoError(t, syncGuildMembersForSession(context.Background(), logger, true, "discord-account", guilds, true, syncMember))
	require.ElementsMatch(t, []string{"guild-a", "guild-b"}, got)
}

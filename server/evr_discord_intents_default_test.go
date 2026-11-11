package server

import (
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/stretchr/testify/require"
)

// The zero-value service settings must not ask Discord for the privileged
// GUILD_MEMBERS intent: members sync over REST unless a host opts in.
func TestDiscordIdentifyIntents_DefaultSettingsOmitGuildMembers(t *testing.T) {
	var settings ServiceSettingsData
	require.True(t, settings.SyncGuildMembersWithoutIntent())
	intents := discordIdentifyIntents(settings.SyncGuildMembersWithoutIntent())
	require.Zero(t, intents&discordgo.IntentGuildMembers)

	settings.RequestGuildMembersIntent = true
	require.False(t, settings.SyncGuildMembersWithoutIntent())
	intents = discordIdentifyIntents(settings.SyncGuildMembersWithoutIntent())
	require.NotZero(t, intents&discordgo.IntentGuildMembers)
}

package server

import (
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/stretchr/testify/require"
)

func TestDiscordIdentifyIntents_MembersIntentFollowsSyncFlag(t *testing.T) {
	tests := []struct {
		name        string
		syncEnabled bool
		wantMembers bool
	}{
		{name: "default behavior", wantMembers: true},
		{name: "no-intent sync enabled", syncEnabled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intents := discordIdentifyIntents(tt.syncEnabled)
			require.NotZero(t, intents&discordgo.IntentsGuilds)
			require.NotZero(t, intents&discordgo.IntentsGuildBans)
			require.Equal(t, tt.wantMembers, intents&discordgo.IntentGuildMembers != 0)
		})
	}
}

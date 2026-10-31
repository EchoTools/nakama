package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type accountCosmeticUpdateRecorder struct {
	runtime.NakamaModule
	called   bool
	userID   string
	username string
	langTag  string
	avatar   string
}

// No embedder defines AccountUpdateId; this leaf method shadows the nil
// embedded interface method reached through runtime.NakamaModule.
func (r *accountCosmeticUpdateRecorder) AccountUpdateId(_ context.Context, userID, username string, _ map[string]any, _, _, _, langTag, avatarURL string) error {
	r.called = true
	r.userID = userID
	r.username = username
	r.langTag = langTag
	r.avatar = avatarURL
	return nil
}

type unknownMemberTransport struct{}

func (unknownMemberTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(`{"message":"Unknown Member","code":10007}`)),
		Request:    req,
	}, nil
}

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

func TestSyncMember_RemovesGuildMembershipWhenDiscordMemberIsAbsent(t *testing.T) {
	previous := ServiceSettings()
	updated := *previous
	updated.SyncGuildMembersWithoutIntent = true
	ServiceSettingsUpdate(&updated)
	t.Cleanup(func() { ServiceSettingsUpdate(previous) })

	dg, err := discordgo.New("Bot test")
	require.NoError(t, err)
	dg.Client.Transport = unknownMemberTransport{}
	removed := 0
	d := &DiscordIntegrator{
		dg:          dg,
		memberCache: &MapOf[string, cachedMember]{},
		idcache:     &MapOf[string, string]{},
		guildGroupMemberRemoveFn: func(_ context.Context, guildID, discordID, _ string) error {
			assert.Equal(t, "guild", guildID)
			assert.Equal(t, "account", discordID)
			removed++
			return nil
		},
	}
	d.idcache.Store("guild", "group")
	require.NoError(t, d.syncMember(context.Background(), zap.NewNop(), "account", "guild", false))
	require.Equal(t, 1, removed)
}

func TestSyncMemberProfileCosmetics_UpdatesUsernameAvatarAndLocale(t *testing.T) {
	userID := uuid.Must(uuid.NewV4()).String()
	groupID := uuid.Must(uuid.NewV4())
	profile := &EVRProfile{account: &api.Account{User: &api.User{
		Id: userID, Username: "before", LangTag: "en", AvatarUrl: "old-avatar",
	}}}
	profile.SetActiveGroupID(groupID)
	member := &discordgo.Member{
		GuildID: groupID.String(),
		Avatar:  "new-avatar",
		User:    &discordgo.User{ID: uuid.Must(uuid.NewV4()).String(), Username: "after", Locale: "fr"},
	}
	module := &accountCosmeticUpdateRecorder{}
	require.NoError(t, syncMemberProfileCosmetics(context.Background(), module, profile, member, groupID.String()))
	require.True(t, module.called)
	require.Equal(t, userID, module.userID)
	require.Equal(t, "after", module.username)
	require.Equal(t, "fr", module.langTag)
	require.Equal(t, member.AvatarURL("512"), module.avatar)
}

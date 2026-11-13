package server

import (
	"bytes"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	"context"
	"github.com/bwmarrin/discordgo"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// unknownGuildTransport answers every Discord call with 404 / code 10004
// (Unknown Guild), as the API does for a guild the bot is no longer in.
type unknownGuildTransport struct{ calls atomic.Int64 }

func (u *unknownGuildTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	u.calls.Add(1)
	return &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(`{"message":"Unknown Guild","code":10004}`)),
		Request:    req,
	}, nil
}

func newUnknownGuildIntegrator(t *testing.T) (*DiscordIntegrator, *unknownGuildTransport) {
	t.Helper()
	previous := ServiceSettings()
	updated := *previous
	updated.RequestGuildMembersIntent = false
	ServiceSettingsUpdate(&updated)
	t.Cleanup(func() { ServiceSettingsUpdate(previous) })

	dg, err := discordgo.New("Bot test")
	require.NoError(t, err)
	tr := &unknownGuildTransport{}
	dg.Client.Transport = tr
	d := &DiscordIntegrator{
		dg:          dg,
		memberCache: &MapOf[string, cachedMember]{},
		idcache:     &MapOf[string, string]{},
	}
	d.idcache.Store("guild-gone", "group")
	return d, tr
}

// A guild the bot has left answers 10004 for every member. The sync must ask
// Discord once, say so once, and not touch that guild again per member.
func TestSyncMember_UnknownGuildIsNotRetriedPerMember(t *testing.T) {
	d, tr := newUnknownGuildIntegrator(t)
	core, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)

	for _, member := range []string{"m1", "m2", "m3", "m4", "m5"} {
		require.NoError(t, d.syncMember(context.Background(), logger, member, "guild-gone", false), "member %s", member)
	}

	require.EqualValues(t, 1, tr.calls.Load(), "Discord must be asked about an unreachable guild once, not once per member")
	warns := logs.FilterLevelExact(zapcore.WarnLevel).All()
	require.Len(t, warns, 1, "exactly one Warn for the guild")
	require.Equal(t, "guild-gone", warns[0].ContextMap()["guild_id"])
}

// The unreachable mark is dropped on the integrator's next full refresh, so a
// guild the bot has rejoined is picked up again.
func TestSyncMember_UnknownGuildIsRecheckedAfterRefresh(t *testing.T) {
	d, tr := newUnknownGuildIntegrator(t)
	logger := zap.NewNop()

	require.NoError(t, d.syncMember(context.Background(), logger, "m1", "guild-gone", false))
	require.NoError(t, d.syncMember(context.Background(), logger, "m2", "guild-gone", false))
	require.EqualValues(t, 1, tr.calls.Load())

	d.resetUnreachableGuilds()

	require.NoError(t, d.syncMember(context.Background(), logger, "m3", "guild-gone", false))
	require.EqualValues(t, 2, tr.calls.Load(), "re-checked once after the refresh")
}

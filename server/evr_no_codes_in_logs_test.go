package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func newObservedLogger() (runtime.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(zapcore.DebugLevel)
	return NewRuntimeGoLogger(zap.New(core)), logs
}

// requireLoggedWithout asserts that some line was logged and that no message or
// field of any line contains secret.
func requireLoggedWithout(t *testing.T, logs *observer.ObservedLogs, secret string) {
	t.Helper()
	all := logs.All()
	require.NotEmpty(t, all, "expected the handler to keep logging")
	for _, e := range all {
		require.NotContains(t, e.Message, secret)
		for _, f := range e.Context {
			require.NotContains(t, f.String, secret)
			if f.Interface != nil {
				b, _ := json.Marshal(f.Interface)
				require.NotContains(t, string(b), secret)
			}
		}
	}
}

func TestDiscordSignInRpcDoesNotLogOAuthCode(t *testing.T) {
	const secret = "oauth-code-SECRET-123"
	logger, logs := newObservedLogger()

	// Valid JSON, no redirect URL: returns before any network call.
	p, _ := json.Marshal(map[string]string{"code": secret})
	_, err := DiscordSignInRpc(context.Background(), logger, nil, nil, string(p))
	require.Error(t, err)

	// Malformed JSON that still contains the code.
	_, err = DiscordSignInRpc(context.Background(), logger, nil, nil, `{"code":"`+secret+`",`)
	require.Error(t, err)

	requireLoggedWithout(t, logs, secret)
}

func TestLinkRpcsDoNotLogLinkCode(t *testing.T) {
	const secret = "link-code-SECRET-456"
	logger, logs := newObservedLogger()
	bad := `{"link_code":"` + secret + `",`
	_, err := LinkDeviceRpc(context.Background(), logger, nil, nil, bad)
	require.Error(t, err)
	_, err = LinkUserIdDeviceRpc(context.Background(), logger, nil, nil, bad)
	require.Error(t, err)
	requireLoggedWithout(t, logs, secret)
}

func TestDeviceAuthDoesNotLogCode(t *testing.T) {
	const secret = "QZXW"
	logger, logs := newObservedLogger()
	nk := &fakeDeviceAuthNK{}
	limiter := newDeviceAuthRateLimiter(100, time.Minute)
	ctx := sessionCtx("user-1", "alice", nil)

	_, err := deviceAuthRequest(ctx, logger, nk, func() string { return secret })
	require.NoError(t, err)
	_, err = deviceAuthVerify(ctx, logger, nk, fakeDiscordLookup, limiter, codePayload(strings.ToLower(secret)))
	require.NoError(t, err)

	requireLoggedWithout(t, logs, secret)
}

func TestLinkSlashCommandsDoNotLogLinkCode(t *testing.T) {
	const secret = "ZQXW"
	for _, name := range []string{"link", "link-headset"} {
		logger, logs := newObservedLogger()
		i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
			Type: discordgo.InteractionApplicationCommand,
			Data: discordgo.ApplicationCommandInteractionData{
				Name: name,
				Options: []*discordgo.ApplicationCommandInteractionDataOption{
					{Name: "link-code", Type: discordgo.ApplicationCommandOptionString, Value: secret},
				},
			},
		}}
		logApplicationCommand(logger, i)
		requireLoggedWithout(t, logs, secret)
	}
}

func TestLinkSlashCommandsKeepOtherOptionsInLog(t *testing.T) {
	logger, logs := newObservedLogger()
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionApplicationCommand,
		Data: discordgo.ApplicationCommandInteractionData{
			Name: "whoami",
			Options: []*discordgo.ApplicationCommandInteractionDataOption{
				{Name: "user", Type: discordgo.ApplicationCommandOptionString, Value: "someone"},
			},
		},
	}}
	logApplicationCommand(logger, i)
	b, _ := json.Marshal(logs.All()[0].ContextMap()["options"])
	require.Contains(t, string(b), "someone")
}

func TestFailedHeadsetLinkDoesNotLogLinkCode(t *testing.T) {
	const secret = "ZQXW"
	logger, logs := newObservedLogger()
	cache := &DiscordIntegrator{idcache: &MapOf[string, string]{}}
	cache.idcache.Store("guild-1", "group-1")
	cache.idcache.Store("discord-1", "user-1")
	bot := &DiscordAppBot{nk: &fakeDeviceAuthNK{}, cache: cache}
	member := &discordgo.Member{GuildID: "guild-1", User: &discordgo.User{ID: "discord-1", Username: "alice"}}

	err := bot.linkHeadset(context.Background(), logger, member, secret)
	require.Error(t, err)
	require.NotContains(t, strings.ToUpper(err.Error()), secret)
	requireLoggedWithout(t, logs, secret)
}

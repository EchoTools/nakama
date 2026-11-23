package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

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

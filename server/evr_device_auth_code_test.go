package server

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/stretchr/testify/require"
)

// fakeDeviceAuthNK is just enough of runtime.NakamaModule for the device auth
// code flow: one storage object plus token generation.
type fakeDeviceAuthNK struct {
	runtime.NakamaModule
	mu    sync.Mutex
	value string
	has   bool
}

func (f *fakeDeviceAuthNK) StorageRead(_ context.Context, _ []*runtime.StorageRead) ([]*api.StorageObject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.has {
		return nil, nil
	}
	return []*api.StorageObject{{Value: f.value}}, nil
}

func (f *fakeDeviceAuthNK) StorageWrite(_ context.Context, w []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.value, f.has = w[0].Value, true
	return nil, nil
}

func (f *fakeDeviceAuthNK) AuthenticateTokenGenerate(userID, username string, exp int64, vars map[string]string) (string, int64, error) {
	return "tok-" + userID, exp, nil
}

func (f *fakeDeviceAuthNK) pending(t *testing.T) map[string]*DeviceAuthCode {
	t.Helper()
	m := map[string]*DeviceAuthCode{}
	if f.has {
		require.NoError(t, json.Unmarshal([]byte(f.value), &m))
	}
	return m
}

func newFakeDeviceAuthLogger(t *testing.T) runtime.Logger {
	return NewRuntimeGoLogger(loggerForTest(t))
}

func codePayload(code string) string {
	b, _ := json.Marshal(map[string]string{"code": code})
	return string(b)
}

func TestDeviceAuthCodeFormat(t *testing.T) {
	require.Equal(t, 4, deviceAuthCodeLength)
	for i := 0; i < 2000; i++ {
		code := generateDeviceAuthCode()
		require.Len(t, code, deviceAuthCodeLength)
		require.NotContains(t, code, "-")
		for _, r := range code {
			require.True(t, strings.ContainsRune(deviceAuthCodeAlphabet, r), "char %q of %q not in alphabet", r, code)
		}
	}
}

func TestDeviceAuthRequestAllCollideReturnsError(t *testing.T) {
	nk := &fakeDeviceAuthNK{}
	logger := newFakeDeviceAuthLogger(t)
	ctx := context.Background()

	out, err := deviceAuthRequest(ctx, logger, nk, func() string { return "AAAA" })
	require.NoError(t, err)
	require.Contains(t, out, "AAAA")

	// Every retry now collides with the pending AAAA.
	calls := 0
	out, err = deviceAuthRequest(ctx, logger, nk, func() string { calls++; return "AAAA" })
	require.Error(t, err)
	require.Empty(t, out)
	require.Equal(t, StatusInternalError, runtimeErrCode(t, err))
	require.Equal(t, deviceAuthCodeMaxAttempts, calls)
	require.Len(t, nk.pending(t), 1, "the existing code must not be overwritten")
}

func TestDeviceAuthVerifiedCodeIsSingleUse(t *testing.T) {
	nk := &fakeDeviceAuthNK{}
	logger := newFakeDeviceAuthLogger(t)
	ctx := sessionCtx("user-1", "alice", nil)
	limiter := newDeviceAuthRateLimiter(100, time.Minute)

	_, err := deviceAuthRequest(ctx, logger, nk, func() string { return "ACDE" })
	require.NoError(t, err)

	_, err = deviceAuthVerify(ctx, logger, nk, fakeDiscordLookup, limiter, codePayload("acde"))
	require.NoError(t, err)

	// A second verify of the same code (even by another user) is refused and
	// does not replace the first user's tokens.
	other := sessionCtx("user-2", "mallory", nil)
	_, err = deviceAuthVerify(other, logger, nk, fakeDiscordLookup, limiter, codePayload("ACDE"))
	require.Error(t, err)
	require.Equal(t, "user-1", nk.pending(t)["ACDE"].UserID)

	// The poll hands the tokens out once and consumes the code.
	out, err := DeviceAuthPollRpc(ctx, logger, nil, nk, codePayload("ACDE"))
	require.NoError(t, err)
	require.Contains(t, out, `"verified"`)
	require.Contains(t, out, "tok-user-1")
	require.Empty(t, nk.pending(t))

	out, err = DeviceAuthPollRpc(ctx, logger, nil, nk, codePayload("ACDE"))
	require.NoError(t, err)
	require.Contains(t, out, `"expired"`)
	require.NotContains(t, out, "tok-user-1")

	_, err = deviceAuthVerify(ctx, logger, nk, fakeDiscordLookup, limiter, codePayload("ACDE"))
	require.Error(t, err)
}

func TestDeviceAuthVerifyRateLimit(t *testing.T) {
	nk := &fakeDeviceAuthNK{}
	logger := newFakeDeviceAuthLogger(t)
	limiter := newDeviceAuthRateLimiter(deviceAuthVerifyPerMinute, time.Minute)
	ctx := sessionCtx("user-1", "alice", nil)

	// Ten wrong-code attempts are answered normally (not found)...
	for i := 0; i < deviceAuthVerifyPerMinute; i++ {
		_, err := deviceAuthVerify(ctx, logger, nk, fakeDiscordLookup, limiter, codePayload("ZZZZ"))
		require.Error(t, err)
		require.Equal(t, StatusNotFound, runtimeErrCode(t, err), "attempt %d", i+1)
	}
	// ...and the 11th is rejected, even though the code is now valid.
	_, err := deviceAuthRequest(ctx, logger, nk, func() string { return "ZZZZ" })
	require.NoError(t, err)
	_, err = deviceAuthVerify(ctx, logger, nk, fakeDiscordLookup, limiter, codePayload("ZZZZ"))
	require.Error(t, err)
	require.Equal(t, StatusResourceExhausted, runtimeErrCode(t, err))

	// Another user is unaffected.
	_, err = deviceAuthVerify(sessionCtx("user-2", "bob", nil), logger, nk, fakeDiscordLookup, limiter, codePayload("ZZZZ"))
	require.NoError(t, err)
}

func TestDeviceAuthRateLimiterRefills(t *testing.T) {
	l := newDeviceAuthRateLimiter(10, time.Minute)
	now := time.Now()
	for i := 0; i < 10; i++ {
		require.True(t, l.allow("u", now))
	}
	require.False(t, l.allow("u", now))
	require.True(t, l.allow("u", now.Add(time.Minute)))
}

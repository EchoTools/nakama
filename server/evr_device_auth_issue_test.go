package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/stretchr/testify/require"
)

type deviceAuthIssueEnv struct {
	nk     *RuntimeGoNakamaModule
	key    []byte
	logger runtime.Logger
	cfg    Config
}

func newDeviceAuthIssueEnv(t *testing.T) *deviceAuthIssueEnv {
	t.Helper()
	zl := loggerForTest(t)
	cfg := NewConfig(zl)
	cfg.GetSession().EncryptionKey = "device-auth-issue-test-key"
	return &deviceAuthIssueEnv{
		nk:     &RuntimeGoNakamaModule{config: cfg, sessionCache: NewLocalSessionCache(3_600, 7_200)},
		key:    []byte(cfg.GetSession().EncryptionKey),
		logger: NewRuntimeGoLogger(zl),
		cfg:    cfg,
	}
}

func (e *deviceAuthIssueEnv) refresh(t *testing.T, refreshToken string) (map[string]any, error) {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"refresh_token": refreshToken})
	require.NoError(t, err)
	out, err := deviceAuthRefresh(context.Background(), e.logger, e.nk, e.key, fakeDiscordLookup, string(payload))
	if err != nil {
		return nil, err
	}
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &body))
	return body, nil
}

func (e *deviceAuthIssueEnv) issue(ctx context.Context) (map[string]any, error) {
	out, err := deviceAuthIssue(ctx, e.logger, e.nk, fakeDiscordLookup)
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		return nil, err
	}
	return body, nil
}

func fakeDiscordLookup(_ context.Context, _ string) (string, error) {
	return "123456789012345678", nil
}

// sessionCtx builds the context Nakama hands an RPC. The keys are untyped
// string constants in nakama-common, so SA1029 cannot be satisfied here.
func sessionCtx(userID, username string, vars map[string]string) context.Context {
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, userID) //nolint:staticcheck
	ctx = context.WithValue(ctx, runtime.RUNTIME_CTX_USERNAME, username)                //nolint:staticcheck
	return context.WithValue(ctx, runtime.RUNTIME_CTX_VARS, vars)                       //nolint:staticcheck
}

func runtimeErrCode(t *testing.T, err error) int {
	t.Helper()
	var re *runtime.Error
	require.True(t, errors.As(err, &re), "expected *runtime.Error, got %T: %v", err, err)
	return re.Code
}

// The bug: a Discord-login session (POST /v2/account/authenticate/custom) holds
// a built-in refresh token, which device/auth/refresh refuses. After
// device/auth/issue the same user holds a token that it accepts, and the token
// it hands back is itself refreshable.
func TestDeviceAuthIssue_DiscordLoginSessionBecomesRefreshable(t *testing.T) {
	e := newDeviceAuthIssueEnv(t)
	userID := uuid.Must(uuid.NewV4()).String()

	// What the web app holds after custom auth.
	tokenID := uuid.Must(uuid.NewV4()).String()
	builtinRefresh, _ := generateRefreshToken(e.cfg, tokenID, time.Now().Unix(), userID, "alice", nil)
	_, err := e.refresh(t, builtinRefresh)
	require.Error(t, err, "premise: a built-in refresh token is not accepted by device/auth/refresh")
	// Built-in refresh tokens are signed with session.refresh_encryption_key, not
	// session.encryption_key, so this is the "invalid or expired" 401 the web app sees.
	require.Equal(t, StatusUnauthenticated, runtimeErrCode(t, err))

	// Exchange the authenticated session for a device-auth pair.
	issued, err := e.issue(sessionCtx(userID, "alice", map[string]string{}))
	require.NoError(t, err)
	require.Equal(t, userID, issued["user_id"])
	require.Equal(t, "alice", issued["username"])
	require.NotEmpty(t, issued["access_token"])
	require.Equal(t, "Bearer", issued["token_type"])
	require.InDelta(t, deviceAuthAccessTokenTTL.Seconds(), issued["expires_in"], 5)
	require.InDelta(t, deviceAuthRefreshTokenTTL.Seconds(), issued["refresh_token_expires_in"], 5)

	// It refreshes, and what it returns refreshes again.
	first, err := e.refresh(t, issued["refresh_token"].(string))
	require.NoError(t, err)
	require.Equal(t, userID, first["user_id"])
	second, err := e.refresh(t, first["refresh_token"].(string))
	require.NoError(t, err)
	require.Equal(t, userID, second["user_id"])
}

// Negative controls: the new RPC must not loosen device/auth/refresh.
func TestDeviceAuthIssue_RefreshStillRejectsNonRefreshTokens(t *testing.T) {
	e := newDeviceAuthIssueEnv(t)
	userID := uuid.Must(uuid.NewV4()).String()

	// A built-in refresh token is still refused.
	builtinRefresh, _ := generateRefreshToken(e.cfg, uuid.Must(uuid.NewV4()).String(), time.Now().Unix(), userID, "alice", nil)
	_, err := e.refresh(t, builtinRefresh)
	require.Error(t, err)
	require.Equal(t, StatusUnauthenticated, runtimeErrCode(t, err))

	// So is the access token issue returns (right key, but no refresh var: 400).
	issued, err := e.issue(sessionCtx(userID, "alice", nil))
	require.NoError(t, err)
	_, err = e.refresh(t, issued["access_token"].(string))
	require.Error(t, err)
	require.Equal(t, StatusInvalidArgument, runtimeErrCode(t, err))

	// And garbage.
	_, err = e.refresh(t, "not-a-jwt")
	require.Error(t, err)
	require.Equal(t, StatusUnauthenticated, runtimeErrCode(t, err))
}

func TestDeviceAuthIssue_RefusesCallersThatMustNotMint(t *testing.T) {
	e := newDeviceAuthIssueEnv(t)
	userID := uuid.Must(uuid.NewV4()).String()

	cases := map[string]struct {
		ctx  context.Context
		code int
	}{
		"unauthenticated":      {context.Background(), StatusUnauthenticated},
		"refresh-token caller": {sessionCtx(userID, "alice", map[string]string{"refresh": "true"}), StatusPermissionDenied},
		"impersonated caller":  {sessionCtx(userID, "alice", map[string]string{"impersonated_by": uuid.Must(uuid.NewV4()).String()}), StatusPermissionDenied},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := e.issue(tc.ctx)
			require.Error(t, err)
			require.Equal(t, tc.code, runtimeErrCode(t, err))
		})
	}
}

// A failed Discord lookup degrades to a pair without `did`, as refresh does.
func TestDeviceAuthIssue_DiscordLookupFailureStillIssues(t *testing.T) {
	e := newDeviceAuthIssueEnv(t)
	userID := uuid.Must(uuid.NewV4()).String()
	out, err := deviceAuthIssue(sessionCtx(userID, "alice", nil), e.logger, e.nk,
		func(context.Context, string) (string, error) { return "", errors.New("db down") })
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &body))
	_, err = e.refresh(t, body["refresh_token"].(string))
	require.NoError(t, err)
}

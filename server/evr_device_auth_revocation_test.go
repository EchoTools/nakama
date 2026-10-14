package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The storage contract is pinned here as literals, not read from the production
// constants, so a rename in production fails these tests instead of following it.
const (
	testRevokedBeforeCollection = "device_auth"
	testRevokedBeforeKey        = "revoked_before"
	testSessionsCollection      = "device_auth_sessions"

	testRefreshRefusal = "invalid or expired refresh token"
)

// revocationTestNK wraps the real module (for AuthenticateTokenGenerate and the
// session cache) and replaces its storage and account reads with an in-memory
// store that has real Nakama OCC semantics (occTestNakamaModule).
//
// Defect class 1 (AGENTS.md): every method production reaches is defined HERE
// on the outermost type, and each delegates to the store explicitly; nothing
// relies on an embedded type dispatching to an override.
type revocationTestNK struct {
	*RuntimeGoNakamaModule
	store *occTestNakamaModule

	// disableTime, when non-nil, is returned as the account's disable_time.
	disableTime *time.Time
	// accountErr and readErr inject faults.
	accountErr error
	readErr    error
}

func (n *revocationTestNK) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	if n.readErr != nil {
		return nil, n.readErr
	}
	return n.store.StorageRead(ctx, reads)
}

func (n *revocationTestNK) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	return n.store.StorageWrite(ctx, writes)
}

func (n *revocationTestNK) StorageDelete(_ context.Context, deletes []*runtime.StorageDelete) error {
	n.store.mu.Lock()
	defer n.store.mu.Unlock()
	for _, d := range deletes {
		delete(n.store.objects, occStorageKey(d.UserID, d.Collection, d.Key))
	}
	return nil
}

func (n *revocationTestNK) StorageList(_ context.Context, _, userID, collection string, _ int, _ string) ([]*api.StorageObject, string, error) {
	if n.readErr != nil {
		return nil, "", n.readErr
	}
	n.store.mu.Lock()
	defer n.store.mu.Unlock()
	var out []*api.StorageObject
	for _, o := range n.store.objects {
		if o.UserId == userID && o.Collection == collection {
			out = append(out, o)
		}
	}
	return out, "", nil
}

func (n *revocationTestNK) AccountGetId(_ context.Context, userID string) (*api.Account, error) {
	if n.accountErr != nil {
		return nil, n.accountErr
	}
	acct := &api.Account{User: &api.User{Id: userID}}
	if n.disableTime != nil {
		acct.DisableTime = timestamppb.New(*n.disableTime)
	}
	return acct, nil
}

func (n *revocationTestNK) objectCount(userID, collection string) int {
	n.store.mu.Lock()
	defer n.store.mu.Unlock()
	c := 0
	for _, o := range n.store.objects {
		if o.UserId == userID && o.Collection == collection {
			c++
		}
	}
	return c
}

type revocationEnv struct {
	nk     *revocationTestNK
	key    []byte
	logger runtime.Logger
}

func newRevocationEnv(t *testing.T, zl *zap.Logger) *revocationEnv {
	t.Helper()
	if zl == nil {
		zl = loggerForTest(t)
	}
	cfg := NewConfig(zl)
	cfg.GetSession().EncryptionKey = "device-auth-revocation-test-key"
	return &revocationEnv{
		nk: &revocationTestNK{
			RuntimeGoNakamaModule: &RuntimeGoNakamaModule{config: cfg, sessionCache: NewLocalSessionCache(3_600, 7_200)},
			store:                 newOCCTestNakamaModule(),
		},
		key:    []byte(cfg.GetSession().EncryptionKey),
		logger: NewRuntimeGoLogger(zl),
	}
}

// legacyRefreshToken mints a refresh token exactly as origin/main did: it
// carries vars["refresh"] and nothing that identifies a device session.
func (e *revocationEnv) legacyRefreshToken(t *testing.T, userID string) string {
	t.Helper()
	tok, _, err := e.nk.AuthenticateTokenGenerate(userID, "someone", time.Now().Add(deviceAuthRefreshTokenTTL).Unix(), map[string]string{"refresh": "true"})
	require.NoError(t, err)
	return tok
}

func (e *revocationEnv) refresh(t *testing.T, refreshToken string) (map[string]any, error) {
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

func (e *revocationEnv) seedRevokedBefore(userID string, unix int64) {
	v, _ := json.Marshal(map[string]int64{"revoked_before": unix})
	e.nk.store.seedObject(userID, testRevokedBeforeCollection, testRevokedBeforeKey, string(v))
}

func requireRefusal(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, StatusUnauthenticated, runtimeErrCode(t, err))
	var re *runtime.Error
	require.ErrorAs(t, err, &re)
	require.Equal(t, testRefreshRefusal, re.Message)
}

// A legacy token (no device session id) for a user whose revoked_before is newer
// than the token's iat must be refused with exactly the message nevr-runtime
// falls back to device login on.
func TestDeviceAuthRefresh_LegacyTokenOlderThanRevokedBeforeIsRefused(t *testing.T) {
	e := newRevocationEnv(t, nil)
	userID := uuid.Must(uuid.NewV4()).String()

	tok := e.legacyRefreshToken(t, userID)
	e.seedRevokedBefore(userID, time.Now().Unix()+10)

	_, err := e.refresh(t, tok)
	requireRefusal(t, err)
}

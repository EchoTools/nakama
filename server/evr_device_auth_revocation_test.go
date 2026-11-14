package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
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

// ---- helpers for the cases below ----

func newObservedRevocationEnv(t *testing.T) (*revocationEnv, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zapcore.DebugLevel)
	return newRevocationEnv(t, zap.New(core)), logs
}

func newUserID() string { return uuid.Must(uuid.NewV4()).String() }

// issuedRefreshToken mints a pair the way device/auth/issue does today and
// returns its refresh token and device session id.
func (e *revocationEnv) issuedRefreshToken(t *testing.T, userID string) (token, dsid string) {
	t.Helper()
	pair, err := mintDeviceAuthTokenPairWithNewSession(context.Background(), e.logger, e.nk, userID, "someone", "123", deviceSessionKindIssue)
	require.NoError(t, err)
	_, _, vars, _, _, _, ok := parseToken(e.key, pair.RefreshToken)
	require.True(t, ok)
	require.NotEmpty(t, vars["dsid"], "a minted refresh token must carry a device session id")
	return pair.RefreshToken, vars["dsid"]
}

func (e *revocationEnv) sessionRecord(t *testing.T, userID, dsid string) *deviceSessionRecord {
	t.Helper()
	objs, err := e.nk.StorageRead(context.Background(), []*runtime.StorageRead{{Collection: testSessionsCollection, Key: dsid, UserID: userID}})
	require.NoError(t, err)
	if len(objs) == 0 {
		return nil
	}
	var rec deviceSessionRecord
	require.NoError(t, json.Unmarshal([]byte(objs[0].Value), &rec))
	return &rec
}

func (e *revocationEnv) tokenIAT(t *testing.T, tok string) int64 {
	t.Helper()
	_, _, _, _, _, iat, ok := parseToken(e.key, tok)
	require.True(t, ok)
	return iat
}

func (e *revocationEnv) revokeAllCtx(userID string) context.Context {
	return sessionCtx(userID, "someone", map[string]string{})
}

func warnWithReason(logs *observer.ObservedLogs, reason string) []observer.LoggedEntry {
	var out []observer.LoggedEntry
	for _, l := range logs.FilterLevelExact(zapcore.WarnLevel).All() {
		if l.ContextMap()["reason"] == reason {
			out = append(out, l)
		}
	}
	return out
}

// Absent revoked_before means no revocation: a legacy token keeps refreshing,
// which is what keeps a deploy from logging everyone out. The legacy token is
// then adopted: the rotated pair carries a fresh device session id with a record.
func TestDeviceAuthRefresh_LegacyTokenRefreshesWithoutRevokedBefore_AndIsAdopted(t *testing.T) {
	e := newRevocationEnv(t, nil)
	userID := newUserID()

	body, err := e.refresh(t, e.legacyRefreshToken(t, userID))
	require.NoError(t, err)

	_, _, vars, _, _, _, ok := parseToken(e.key, body["refresh_token"].(string))
	require.True(t, ok)
	dsid := vars["dsid"]
	require.NotEmpty(t, dsid, "a refreshed legacy token is adopted into a device session")
	rec := e.sessionRecord(t, userID, dsid)
	require.NotNil(t, rec)
	require.Equal(t, deviceSessionKindLegacy, rec.Kind)
	require.Equal(t, 1, e.nk.objectCount(userID, testSessionsCollection))

	// The adopted token is itself revocable by deleting its record.
	_, err = e.refresh(t, body["refresh_token"].(string))
	require.NoError(t, err)
}

// Refresh keeps the same dsid and counts itself on the record.
func TestDeviceAuthRefresh_KeepsDsidAndCountsRefreshes(t *testing.T) {
	e := newRevocationEnv(t, nil)
	userID := newUserID()
	tok, dsid := e.issuedRefreshToken(t, userID)
	require.Zero(t, e.sessionRecord(t, userID, dsid).RefreshCount)

	body, err := e.refresh(t, tok)
	require.NoError(t, err)
	_, _, vars, _, _, _, _ := parseToken(e.key, body["refresh_token"].(string))
	require.Equal(t, dsid, vars["dsid"])
	_, err = e.refresh(t, body["refresh_token"].(string))
	require.NoError(t, err)

	require.EqualValues(t, 2, e.sessionRecord(t, userID, dsid).RefreshCount)
	require.Equal(t, 1, e.nk.objectCount(userID, testSessionsCollection), "refresh must not create more records")
}

// The same-second boundary: iat == revoked_before is refused, iat+... one second
// earlier revocation is not.
func TestDeviceAuthRefresh_RevokedBeforeBoundary(t *testing.T) {
	e := newRevocationEnv(t, nil)
	userID := newUserID()
	tok := e.legacyRefreshToken(t, userID)
	iat := e.tokenIAT(t, tok)

	e.seedRevokedBefore(userID, iat-1)
	_, err := e.refresh(t, tok)
	require.NoError(t, err, "revoked_before one second before iat must not refuse")

	e.seedRevokedBefore(userID, iat)
	_, err = e.refresh(t, tok)
	requireRefusal(t, err)

	e.seedRevokedBefore(userID, iat+1)
	_, err = e.refresh(t, tok)
	requireRefusal(t, err)
}

// revoke_all kills a legacy token (no dsid) through iat, and a token minted
// after it refreshes.
func TestDeviceSessionsRevokeAll_KillsLegacyTokenAndLaterTokenRefreshes(t *testing.T) {
	e, logs := newObservedRevocationEnv(t)
	userID := newUserID()
	legacy := e.legacyRefreshToken(t, userID)
	old, _ := e.issuedRefreshToken(t, userID)

	out, err := DeviceSessionsRevokeAllRpc(e.revokeAllCtx(userID), e.logger, nil, e.nk, "")
	require.NoError(t, err)
	var res revokeDeviceSessionsResult
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.Equal(t, userID, res.UserID)
	require.Equal(t, 1, res.SessionsDeleted)

	_, err = e.refresh(t, legacy)
	requireRefusal(t, err)
	_, err = e.refresh(t, old)
	requireRefusal(t, err)
	require.Len(t, warnWithReason(logs, "revoked_before"), 2)

	// A token minted in a later second than the revocation refreshes.
	time.Sleep(time.Until(time.Unix(res.RevokedBefore+1, 0)) + 20*time.Millisecond)
	fresh, _ := e.issuedRefreshToken(t, userID)
	_, err = e.refresh(t, fresh)
	require.NoError(t, err)
}

func TestDeviceSessionsRevokeAll_WritesInfoRecordAndDropsLiveSessions(t *testing.T) {
	e, logs := newObservedRevocationEnv(t)
	userID := newUserID()
	e.issuedRefreshToken(t, userID)
	e.issuedRefreshToken(t, userID)

	uid := uuid.FromStringOrNil(userID)
	// Issued 5s ago: the cache compares exp-tokenExpiry to the invalidation second.
	exp := time.Now().Add(time.Hour - 5*time.Second).Unix()
	e.nk.sessionCache.Add(uid, exp, "tok-1", exp, "rtok-1")
	require.True(t, e.nk.sessionCache.IsValidSession(uid, exp, "tok-1"))

	_, err := DeviceSessionsRevokeAllRpc(e.revokeAllCtx(userID), e.logger, nil, e.nk, "")
	require.NoError(t, err)

	require.False(t, e.nk.sessionCache.IsValidSession(uid, exp, "tok-1"), "live access sessions must drop")
	require.Zero(t, e.nk.objectCount(userID, testSessionsCollection))

	infos := logs.FilterMessage("Device sessions revoked").All()
	require.Len(t, infos, 1)
	require.Equal(t, zapcore.InfoLevel, infos[0].Level)
	require.Equal(t, userID, infos[0].ContextMap()["user_id"])
	require.EqualValues(t, 2, infos[0].ContextMap()["sessions_deleted"])
}

func TestDeviceSessionsRevokeAll_RequiresAuthAndRefusesImpersonation(t *testing.T) {
	e := newRevocationEnv(t, nil)
	_, err := DeviceSessionsRevokeAllRpc(context.Background(), e.logger, nil, e.nk, "")
	require.Equal(t, StatusUnauthenticated, runtimeErrCode(t, err))
	_, err = DeviceSessionsRevokeAllRpc(sessionCtx(newUserID(), "x", map[string]string{"impersonated_by": "y"}), e.logger, nil, e.nk, "")
	require.Equal(t, StatusPermissionDenied, runtimeErrCode(t, err))
}

// A dsid token whose record is gone is refused, and the refusal is logged.
func TestDeviceAuthRefresh_DsidWithoutRecordIsRefused(t *testing.T) {
	e, logs := newObservedRevocationEnv(t)
	userID := newUserID()
	tok, dsid := e.issuedRefreshToken(t, userID)
	require.NoError(t, e.nk.StorageDelete(context.Background(), []*runtime.StorageDelete{{Collection: testSessionsCollection, Key: dsid, UserID: userID}}))

	_, err := e.refresh(t, tok)
	requireRefusal(t, err)

	w := warnWithReason(logs, "missing_record")
	require.Len(t, w, 1)
	require.Equal(t, userID, w[0].ContextMap()["user_id"])
	require.Equal(t, dsid, w[0].ContextMap()["dsid"])
	require.Zero(t, e.nk.objectCount(userID, testSessionsCollection), "a refused refresh must not resurrect the record")
}

func TestDeviceAuthRefresh_DisabledAccountIsRefused(t *testing.T) {
	e, logs := newObservedRevocationEnv(t)
	userID := newUserID()
	tok, _ := e.issuedRefreshToken(t, userID)
	when := time.Now().Add(-time.Hour)
	e.nk.disableTime = &when

	_, err := e.refresh(t, tok)
	requireRefusal(t, err)
	require.Len(t, warnWithReason(logs, "disabled"), 1)
	require.Equal(t, userID, warnWithReason(logs, "disabled")[0].ContextMap()["user_id"])

	// A zero disable_time means enabled, as in the built-in refresh.
	zero := time.Unix(0, 0)
	e.nk.disableTime = &zero
	_, err = e.refresh(t, tok)
	require.NoError(t, err)
}

func TestDeviceAuthRefresh_DeletedAccountIsRefused(t *testing.T) {
	e, logs := newObservedRevocationEnv(t)
	tok := e.legacyRefreshToken(t, newUserID())
	e.nk.accountErr = ErrAccountNotFound
	_, err := e.refresh(t, tok)
	requireRefusal(t, err)
	require.Len(t, warnWithReason(logs, "account_missing"), 1)
}

// An unreadable revocation state must fail closed, with an error that is NOT the
// device-login message (an outage must not look like a revocation).
func TestDeviceAuthRefresh_StorageReadErrorFailsClosed(t *testing.T) {
	e, logs := newObservedRevocationEnv(t)
	tok := e.legacyRefreshToken(t, newUserID())
	e.nk.readErr = errors.New("db down")

	_, err := e.refresh(t, tok)
	require.Error(t, err)
	require.Equal(t, StatusInternalError, runtimeErrCode(t, err))
	var re *runtime.Error
	require.ErrorAs(t, err, &re)
	require.NotEqual(t, testRefreshRefusal, re.Message)
	require.NotEmpty(t, logs.FilterLevelExact(zapcore.ErrorLevel).All())

	e.nk.readErr = nil
	e.nk.accountErr = errors.New("db down")
	_, err = e.refresh(t, tok)
	require.Equal(t, StatusInternalError, runtimeErrCode(t, err))
}

func TestAccountRevokeDeviceSessions_RefusesNonOperator(t *testing.T) {
	e, _ := newObservedRevocationEnv(t)
	regs := buildEVRRPCRegistrations(nil, nil)
	reg := findRPCRegistration(regs, "account/revoke_device_sessions")
	require.NotNil(t, reg)
	require.NotNil(t, reg.Permission)
	require.Equal(t, []string{GroupGlobalOperators}, reg.Permission.AllowedGroups)
	wrapped := WithRPCAuthorization(reg.ID, *reg.Permission, reg.Handler)

	target := newUserID()
	e.issuedRefreshToken(t, target)
	callerCtx := WithUserPermissions(sessionCtx(newUserID(), "caller", map[string]string{}), &UserPermissions{})
	payload, _ := json.Marshal(map[string]string{"user_id": target})

	_, err := wrapped(callerCtx, e.logger, nil, e.nk, string(payload))
	require.Equal(t, StatusPermissionDenied, runtimeErrCode(t, err))
	require.Equal(t, 1, e.nk.objectCount(target, testSessionsCollection), "a refused call must change nothing")
	require.Zero(t, e.nk.objectCount(target, testRevokedBeforeCollection))
}

func TestAccountRevokeDeviceSessions_OperatorRevokesAndLogsInfo(t *testing.T) {
	e, logs := newObservedRevocationEnv(t)
	reg := findRPCRegistration(buildEVRRPCRegistrations(nil, nil), "account/revoke_device_sessions")
	require.NotNil(t, reg)
	wrapped := WithRPCAuthorization(reg.ID, *reg.Permission, reg.Handler)

	target, caller := newUserID(), newUserID()
	tok, _ := e.issuedRefreshToken(t, target)
	opCtx := WithUserPermissions(sessionCtx(caller, "op", map[string]string{}), &UserPermissions{IsGlobalOperator: true})
	payload, _ := json.Marshal(map[string]string{"user_id": target})

	out, err := wrapped(opCtx, e.logger, nil, e.nk, string(payload))
	require.NoError(t, err)
	var res revokeDeviceSessionsResult
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.Equal(t, target, res.UserID)
	require.Equal(t, 1, res.SessionsDeleted)

	_, err = e.refresh(t, tok)
	requireRefusal(t, err)

	infos := logs.FilterMessage("Device sessions revoked").All()
	require.Len(t, infos, 1)
	require.Equal(t, zapcore.InfoLevel, infos[0].Level)
	require.Equal(t, caller, infos[0].ContextMap()["caller_id"])
	require.Equal(t, target, infos[0].ContextMap()["user_id"])
	require.EqualValues(t, 1, infos[0].ContextMap()["sessions_deleted"])
}

func TestAccountRevokeDeviceSessions_ValidatesPayload(t *testing.T) {
	e := newRevocationEnv(t, nil)
	ctx := sessionCtx(newUserID(), "op", map[string]string{})
	for _, p := range []string{"", "{}", `{"user_id":"nope"}`} {
		_, err := AccountRevokeDeviceSessionsRpc(ctx, e.logger, nil, e.nk, p)
		require.Equal(t, StatusInvalidArgument, runtimeErrCode(t, err), "payload %q", p)
	}
}

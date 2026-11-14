package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

// errRefreshRefused is the one error every refresh refusal returns. nevr-runtime
// falls back to device login only on this exact message, so it must not change,
// and it must not vary by reason (the reason is logged, not returned).
func errRefreshRefused() error {
	return runtime.NewError("invalid or expired refresh token", StatusUnauthenticated)
}

// errRefreshInternal is returned when revocation state cannot be read. It is
// deliberately NOT the device-login message: an outage must not look like a
// revocation, and must not let a token through.
func errRefreshInternal() error {
	return runtime.NewError("internal error", StatusInternalError)
}

// Refusal reasons, as logged.
const (
	refusalDisabled      = "disabled"
	refusalAccountGone   = "account_missing"
	refusalRevokedBefore = "revoked_before"
	refusalMissingRecord = "missing_record"
)

func logRefusal(logger runtime.Logger, reason, userID, dsid string) {
	f := map[string]interface{}{"reason": reason, "user_id": userID}
	if dsid != "" {
		f["dsid"] = dsid
	}
	logger.WithFields(f).Warn("Device auth refresh refused")
}

// deviceSessionRecord is the stored form of one device session. The field set is
// what a future device/sessions/list returns. It holds no token and no secret.
type deviceSessionRecord struct {
	DSID          string `json:"dsid"`
	CreatedAt     int64  `json:"created_at"`
	Kind          string `json:"kind"`
	LastRefreshAt int64  `json:"last_refresh_at"`
	RefreshCount  int64  `json:"refresh_count"`
}

// revokedBeforeRecord is the stored form of a user's revoked_before.
type revokedBeforeRecord struct {
	RevokedBefore int64 `json:"revoked_before"`
}

func readRevokedBefore(ctx context.Context, nk runtime.NakamaModule, userID string) (int64, error) {
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{{Collection: deviceAuthStateCollection, Key: deviceAuthRevokedBeforeKey, UserID: userID}})
	if err != nil {
		return 0, err
	}
	if len(objs) == 0 {
		return 0, nil
	}
	var rec revokedBeforeRecord
	if err := json.Unmarshal([]byte(objs[0].Value), &rec); err != nil {
		return 0, err
	}
	return rec.RevokedBefore, nil
}

func writeRevokedBefore(ctx context.Context, nk runtime.NakamaModule, userID string, unix int64) error {
	v, _ := json.Marshal(revokedBeforeRecord{RevokedBefore: unix})
	_, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection: deviceAuthStateCollection, Key: deviceAuthRevokedBeforeKey, UserID: userID,
		Value: string(v), PermissionRead: 0, PermissionWrite: 0,
	}})
	return err
}

// createDeviceSession writes a new device session record and returns its id.
func createDeviceSession(ctx context.Context, nk runtime.NakamaModule, userID, kind string) (string, error) {
	id, err := uuid.NewV4()
	if err != nil {
		return "", err
	}
	now := time.Now().Unix()
	v, _ := json.Marshal(deviceSessionRecord{DSID: id.String(), CreatedAt: now, Kind: kind, LastRefreshAt: now})
	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection: deviceAuthSessionsCollection, Key: id.String(), UserID: userID,
		Value: string(v), Version: "*", PermissionRead: 0, PermissionWrite: 0,
	}})
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// mintDeviceAuthTokenPairWithNewSession creates a device session record and mints
// a pair whose refresh token carries its id. The record is written first, so a
// minted token always has a record; if minting fails the record is removed.
func mintDeviceAuthTokenPairWithNewSession(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID, username, discordID, kind string) (*deviceAuthTokenPair, error) {
	dsid, err := createDeviceSession(ctx, nk, userID, kind)
	if err != nil {
		logger.WithFields(map[string]interface{}{"user_id": userID, "error": err}).Error("Failed to create device session record")
		return nil, runtime.NewError("internal error", StatusInternalError)
	}
	pair, err := mintDeviceAuthTokenPair(nk, userID, username, discordID, dsid)
	if err != nil {
		deleteDeviceSessions(ctx, logger, nk, userID, []string{dsid})
		return nil, err
	}
	return pair, nil
}

func deleteDeviceSessions(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, dsids []string) {
	if len(dsids) == 0 {
		return
	}
	dels := make([]*runtime.StorageDelete, 0, len(dsids))
	for _, d := range dsids {
		dels = append(dels, &runtime.StorageDelete{Collection: deviceAuthSessionsCollection, Key: d, UserID: userID})
	}
	if err := nk.StorageDelete(ctx, dels); err != nil {
		logger.WithFields(map[string]interface{}{"user_id": userID, "error": err}).Error("Failed to delete device session records")
	}
}

// checkRefreshAllowed runs the revocation checks that follow the JWT checks:
// the account must be enabled, and the token must not predate revoked_before.
// It returns nil to allow, the exact refusal error to refuse, or an internal
// error if the state could not be read (fail closed).
//
// revoked_before boundary: a token is refused when issuedAt <= revoked_before.
// Both are whole seconds, so a token minted in the same second as a revocation
// cannot be told apart from one minted just before it; the check errs toward
// refusing. The cost is that a token minted in the very second of a revoke_all
// is refused once on refresh and the client falls back to device login.
func checkRefreshAllowed(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID, dsid string, issuedAt int64) error {
	acct, err := nk.AccountGetId(ctx, userID)
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			logRefusal(logger, refusalAccountGone, userID, dsid)
			return errRefreshRefused()
		}
		logger.WithFields(map[string]interface{}{"user_id": userID, "error": err}).Error("Failed to read account for refresh")
		return errRefreshInternal()
	}
	// Same test as the built-in session refresh (core_session.go:58): a
	// disable_time of zero seconds means enabled.
	if acct.GetDisableTime() != nil && acct.GetDisableTime().GetSeconds() != 0 {
		logRefusal(logger, refusalDisabled, userID, dsid)
		return errRefreshRefused()
	}

	revokedBefore, err := readRevokedBefore(ctx, nk, userID)
	if err != nil {
		logger.WithFields(map[string]interface{}{"user_id": userID, "error": err}).Error("Failed to read revoked_before for refresh")
		return errRefreshInternal()
	}
	if revokedBefore > 0 && issuedAt <= revokedBefore {
		logRefusal(logger, refusalRevokedBefore, userID, dsid)
		return errRefreshRefused()
	}
	return nil
}

const touchDeviceSessionAttempts = 5

// touchDeviceSession records a refresh on the token's device session
// (last_refresh_at, refresh_count). A missing record refuses the refresh. The
// write is conditional on the version read, so a record deleted by a concurrent
// revoke is never resurrected.
func touchDeviceSession(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID, dsid string) error {
	for attempt := 0; attempt < touchDeviceSessionAttempts; attempt++ {
		objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{{Collection: deviceAuthSessionsCollection, Key: dsid, UserID: userID}})
		if err != nil {
			logger.WithFields(map[string]interface{}{"user_id": userID, "dsid": dsid, "error": err}).Error("Failed to read device session record")
			return errRefreshInternal()
		}
		if len(objs) == 0 {
			logRefusal(logger, refusalMissingRecord, userID, dsid)
			return errRefreshRefused()
		}
		var rec deviceSessionRecord
		if err := json.Unmarshal([]byte(objs[0].Value), &rec); err != nil {
			logger.WithFields(map[string]interface{}{"user_id": userID, "dsid": dsid, "error": err}).Error("Corrupt device session record")
			return errRefreshInternal()
		}
		rec.DSID = dsid
		rec.LastRefreshAt = time.Now().Unix()
		rec.RefreshCount++
		v, _ := json.Marshal(rec)
		_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{{
			Collection: deviceAuthSessionsCollection, Key: dsid, UserID: userID,
			Value: string(v), Version: objs[0].Version, PermissionRead: 0, PermissionWrite: 0,
		}})
		if err == nil {
			return nil
		}
		if !errors.Is(err, runtime.ErrStorageRejectedVersion) {
			logger.WithFields(map[string]interface{}{"user_id": userID, "dsid": dsid, "error": err}).Error("Failed to update device session record")
			return errRefreshInternal()
		}
	}
	logger.WithFields(map[string]interface{}{"user_id": userID, "dsid": dsid}).Error("Device session record update kept conflicting")
	return errRefreshInternal()
}

type revokeDeviceSessionsResult struct {
	UserID          string `json:"user_id"`
	RevokedBefore   int64  `json:"revoked_before"`
	SessionsDeleted int    `json:"sessions_deleted"`
}

// revokeDeviceSessions is the core shared by both revoke RPCs. It sets the
// user's revoked_before to now (which kills every refresh token issued up to
// now, including legacy tokens that carry no dsid), deletes the user's device
// session records, and drops the user's live sessions from the session cache.
// revoked_before is written first so the revocation holds even if a later step
// fails. One INFO record is written on success.
func revokeDeviceSessions(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, rpcID, callerID, targetID string) (*revokeDeviceSessionsResult, error) {
	fail := func(step string, err error) (*revokeDeviceSessionsResult, error) {
		logger.WithFields(map[string]interface{}{"rpc_id": rpcID, "caller_id": callerID, "user_id": targetID, "step": step, "error": err}).Error("Device session revocation failed")
		return nil, runtime.NewError("internal error", StatusInternalError)
	}

	now := time.Now().Unix()
	if err := writeRevokedBefore(ctx, nk, targetID, now); err != nil {
		return fail("write_revoked_before", err)
	}

	deleted := 0
	cursor := ""
	for {
		objs, next, err := nk.StorageList(ctx, "", targetID, deviceAuthSessionsCollection, 100, cursor)
		if err != nil {
			return fail("list_sessions", err)
		}
		if len(objs) > 0 {
			dels := make([]*runtime.StorageDelete, 0, len(objs))
			for _, o := range objs {
				dels = append(dels, &runtime.StorageDelete{Collection: deviceAuthSessionsCollection, Key: o.Key, UserID: targetID})
			}
			if err := nk.StorageDelete(ctx, dels); err != nil {
				return fail("delete_sessions", err)
			}
			deleted += len(objs)
		}
		if next == "" {
			break
		}
		cursor = next
	}

	// Empty tokens mean "log out every session of this user" (core_session.go:89).
	if err := nk.SessionLogout(targetID, "", ""); err != nil {
		return fail("session_logout", err)
	}

	logger.WithFields(map[string]interface{}{
		"rpc_id": rpcID, "caller_id": callerID, "user_id": targetID,
		"sessions_deleted": deleted, "revoked_before": now,
	}).Info("Device sessions revoked")
	return &revokeDeviceSessionsResult{UserID: targetID, RevokedBefore: now, SessionsDeleted: deleted}, nil
}

// DeviceSessionsRevokeAllRpc (device/sessions/revoke_all) is self-service: it
// logs the caller out of every device. No payload. Response:
// {"user_id","revoked_before","sessions_deleted"}.
//
// Refused for impersonated sessions, which must not be able to act on the
// target's credentials.
func DeviceSessionsRevokeAllRpc(ctx context.Context, logger runtime.Logger, _ *sql.DB, nk runtime.NakamaModule, _ string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("authentication required", StatusUnauthenticated)
	}
	vars, _ := ctx.Value(runtime.RUNTIME_CTX_VARS).(map[string]string)
	if vars["impersonated_by"] != "" {
		return "", runtime.NewError("impersonated sessions cannot revoke device sessions", StatusPermissionDenied)
	}
	res, err := revokeDeviceSessions(ctx, logger, nk, "device/sessions/revoke_all", userID, userID)
	if err != nil {
		return "", err
	}
	out, _ := json.Marshal(res)
	return string(out), nil
}

// AccountRevokeDeviceSessionsRpc (account/revoke_device_sessions) is the
// operator form: payload {"user_id": "<uuid>"}, same response. Operator-only
// enforcement is the registration's AllowedGroups (WithRPCAuthorization), the
// same mechanism as account/break_alternates.
func AccountRevokeDeviceSessionsRpc(ctx context.Context, logger runtime.Logger, _ *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	callerID, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if callerID == "" {
		return "", runtime.NewError("authentication required", StatusUnauthenticated)
	}
	var req struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal([]byte(payload), &req); err != nil || req.UserID == "" {
		return "", runtime.NewError("invalid payload: user_id required", StatusInvalidArgument)
	}
	if _, err := uuid.FromString(req.UserID); err != nil {
		return "", runtime.NewError("invalid user_id", StatusInvalidArgument)
	}
	res, err := revokeDeviceSessions(ctx, logger, nk, "account/revoke_device_sessions", callerID, req.UserID)
	if err != nil {
		return "", err
	}
	out, _ := json.Marshal(res)
	return string(out), nil
}

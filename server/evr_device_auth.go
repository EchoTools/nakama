package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"math/big"
	"strings"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	DeviceAuthCollection = "DeviceAuth"
	DeviceAuthCodesKey   = "pendingCodes"
	DeviceAuthCodeExpiry = 5 * time.Minute
)

// DeviceAuthCode represents a pending device authorization code.
type DeviceAuthCode struct {
	Code         string    `json:"code"`
	Status       string    `json:"status"`        // "pending" or "verified"
	Token        string    `json:"token"`         // set when verified
	RefreshToken string    `json:"refresh_token"` // set when verified
	CreatedAt    time.Time `json:"created_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	UserID       string    `json:"user_id,omitempty"`  // set when verified
	Username     string    `json:"username,omitempty"` // set when verified
	// Absolute unix expiries of the two tokens above, set when verified.
	// ExpiresAt is the CODE's 5-minute lifetime and is a different thing --
	// deriving expires_in from it would report five minutes for a one-hour
	// token. Stored so the poll response can state RFC 6749 expires_in rather
	// than making the client decode the JWT or invent a lifetime.
	TokenExpiry        int64 `json:"token_expiry,omitempty"`
	RefreshTokenExpiry int64 `json:"refresh_token_expiry,omitempty"`
}

// loadDeviceAuthCodes loads all pending device auth codes from storage.
func loadDeviceAuthCodes(ctx context.Context, nk runtime.NakamaModule) (map[string]*DeviceAuthCode, error) {
	codes := make(map[string]*DeviceAuthCode)

	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: DeviceAuthCollection,
			Key:        DeviceAuthCodesKey,
			UserID:     SystemUserID,
		},
	})
	if err != nil {
		return nil, err
	}
	if len(objs) != 0 {
		if err := json.Unmarshal([]byte(objs[0].Value), &codes); err != nil {
			return nil, err
		}
	}

	// Purge expired codes
	now := time.Now()
	changed := false
	for k, c := range codes {
		if now.After(c.ExpiresAt) {
			delete(codes, k)
			changed = true
		}
	}
	if changed {
		_ = storeDeviceAuthCodes(ctx, nk, codes)
	}

	return codes, nil
}

// storeDeviceAuthCodes writes device auth codes to storage.
func storeDeviceAuthCodes(ctx context.Context, nk runtime.NakamaModule, codes map[string]*DeviceAuthCode) error {
	data, err := json.Marshal(codes)
	if err != nil {
		return err
	}
	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:      DeviceAuthCollection,
			Key:             DeviceAuthCodesKey,
			UserID:          SystemUserID,
			Value:           string(data),
			PermissionRead:  0,
			PermissionWrite: 0,
		},
	})
	return err
}

// generateDeviceAuthCode generates an 8-character code in XXXX-XXXX format.
// Uses crypto/rand for unpredictable codes (this is an authentication token).
// Character set excludes homoglyphs (0/O, 1/I/L, B/8).
func generateDeviceAuthCode() string {
	validChars := "ACDEFGHJKMNPRSTUXYZ2345679"
	code := make([]byte, 8)
	for i := range code {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(validChars))))
		if err != nil {
			panic("crypto/rand failed: " + err.Error())
		}
		code[i] = validChars[n.Int64()]
	}
	// Format as XXXX-XXXX
	return string(code[:4]) + "-" + string(code[4:])
}

// DeviceAuthRequestRpc generates a new device auth code.
// Public endpoint — no authentication required.
// Returns: { "code": "ABCD-EFGH", "expires_in": 300 }
func DeviceAuthRequestRpc(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	codes, err := loadDeviceAuthCodes(ctx, nk)
	if err != nil {
		logger.WithField("error", err).Error("Failed to load device auth codes")
		return "", runtime.NewError("internal error", StatusInternalError)
	}

	// Generate a unique code
	var code string
	for i := 0; i < 100; i++ {
		code = generateDeviceAuthCode()
		if _, exists := codes[code]; !exists {
			break
		}
	}

	now := time.Now()
	codes[code] = &DeviceAuthCode{
		Code:      code,
		Status:    "pending",
		CreatedAt: now,
		ExpiresAt: now.Add(DeviceAuthCodeExpiry),
	}

	if err := storeDeviceAuthCodes(ctx, nk, codes); err != nil {
		logger.WithField("error", err).Error("Failed to store device auth code")
		return "", runtime.NewError("internal error", StatusInternalError)
	}

	logger.WithField("code", code).Info("Device auth code generated")

	response, _ := json.Marshal(map[string]interface{}{
		"code":       code,
		"expires_in": int(DeviceAuthCodeExpiry.Seconds()),
	})
	return string(response), nil
}

// DeviceAuthPollRpc polls for the status of a device auth code.
// Public endpoint — no authentication required.
// Input: { "code": "ABCD-EFGH" }
// Returns: { "status": "pending" } or { "status": "verified", "access_token": "...", "refresh_token": "..." }
func DeviceAuthPollRpc(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	if payload == "" {
		return "", runtime.NewError("missing payload", StatusInvalidArgument)
	}

	var request struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", runtime.NewError("invalid payload", StatusInvalidArgument)
	}

	code := strings.ToUpper(strings.TrimSpace(request.Code))
	if code == "" {
		return "", runtime.NewError("missing code", StatusInvalidArgument)
	}

	codes, err := loadDeviceAuthCodes(ctx, nk)
	if err != nil {
		logger.WithField("error", err).Error("Failed to load device auth codes")
		return "", runtime.NewError("internal error", StatusInternalError)
	}

	entry, exists := codes[code]
	if !exists {
		response, _ := json.Marshal(map[string]string{"status": "expired"})
		return string(response), nil
	}

	if time.Now().After(entry.ExpiresAt) {
		delete(codes, code)
		_ = storeDeviceAuthCodes(ctx, nk, codes)
		response, _ := json.Marshal(map[string]string{"status": "expired"})
		return string(response), nil
	}

	if entry.Status == "verified" {
		// Return the token and clean up
		// RFC 6749 §5.1 field names, plus `status` which is this flow's own.
		// `token` is retained and deprecated so deployed clients keep working;
		// nevr-runtime reads it today.
		response, _ := json.Marshal(map[string]interface{}{
			"status":                   "verified",
			"access_token":             entry.Token,
			"token_type":               "Bearer",
			"expires_in":               int(time.Until(time.Unix(entry.TokenExpiry, 0)).Seconds()),
			"refresh_token":            entry.RefreshToken,
			"refresh_token_expires_in": int(time.Until(time.Unix(entry.RefreshTokenExpiry, 0)).Seconds()),
			"user_id":                  entry.UserID,
			"username":                 entry.Username,
			"token":                    entry.Token, // deprecated: use access_token
		})

		// Delete the code — one-time use
		delete(codes, code)
		_ = storeDeviceAuthCodes(ctx, nk, codes)

		return string(response), nil
	}

	// Still pending
	response, _ := json.Marshal(map[string]string{"status": "pending"})
	return string(response), nil
}

// DeviceAuthVerifyRpc verifies a device auth code.
// Requires authentication — the calling user's identity is used to generate the token.
// Input: { "code": "ABCD-EFGH" }
// Returns: { "status": "ok" }
func DeviceAuthVerifyRpc(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	// Get the authenticated user's ID
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("authentication required", StatusUnauthenticated)
	}
	username, _ := ctx.Value(runtime.RUNTIME_CTX_USERNAME).(string)

	if payload == "" {
		return "", runtime.NewError("missing payload", StatusInvalidArgument)
	}

	var request struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", runtime.NewError("invalid payload", StatusInvalidArgument)
	}

	code := strings.ToUpper(strings.TrimSpace(request.Code))
	// Strip dash if user entered it
	code = strings.ReplaceAll(code, "-", "")
	if len(code) == 8 {
		code = code[:4] + "-" + code[4:]
	}

	if len(code) != 9 { // XXXX-XXXX = 9 chars
		return "", runtime.NewError("invalid code format", StatusInvalidArgument)
	}

	codes, err := loadDeviceAuthCodes(ctx, nk)
	if err != nil {
		logger.WithField("error", err).Error("Failed to load device auth codes")
		return "", runtime.NewError("internal error", StatusInternalError)
	}

	entry, exists := codes[code]
	if !exists || time.Now().After(entry.ExpiresAt) {
		if exists {
			delete(codes, code)
			_ = storeDeviceAuthCodes(ctx, nk, codes)
		}
		return "", runtime.NewError("code expired or not found", StatusNotFound)
	}

	if entry.Status != "pending" {
		return "", runtime.NewError("code already used", StatusInvalidArgument)
	}

	// Look up discord ID for token vars
	discordID, err := GetDiscordIDByUserID(ctx, db, userID)
	if err != nil {
		logger.WithFields(map[string]interface{}{"user_id": userID, "error": err}).Warn("Could not look up discord ID for user")
		discordID = ""
	}

	pair, err := mintDeviceAuthTokenPair(nk, userID, username, discordID)
	if err != nil {
		logger.WithField("error", err).Error("Failed to generate tokens for device auth")
		return "", err
	}
	token, refreshToken, tokenExpiry, refreshExpiry := pair.AccessToken, pair.RefreshToken, pair.AccessExpiry, pair.RefreshExpiry

	// Mark as verified with token
	entry.Status = "verified"
	entry.Token = token
	entry.RefreshToken = refreshToken
	entry.UserID = userID
	entry.Username = username
	entry.TokenExpiry = tokenExpiry
	entry.RefreshTokenExpiry = refreshExpiry

	if err := storeDeviceAuthCodes(ctx, nk, codes); err != nil {
		logger.WithField("error", err).Error("Failed to store verified device auth")
		return "", runtime.NewError("internal error", StatusInternalError)
	}

	logger.WithFields(map[string]interface{}{"code": code, "username": username, "user_id": userID, "token_expiry": tokenExpiry}).Info("Device auth code verified")

	response, _ := json.Marshal(map[string]string{
		"status":   "ok",
		"username": username,
	})
	return string(response), nil
}

// Lifetimes of the tokens minted for device-auth-style sessions.
const (
	deviceAuthAccessTokenTTL  = 1 * time.Hour
	deviceAuthRefreshTokenTTL = 30 * 24 * time.Hour
)

// deviceAuthTokenPair is an access token and the long-lived refresh token that
// device/auth/refresh will accept for it.
type deviceAuthTokenPair struct {
	AccessToken   string
	RefreshToken  string
	AccessExpiry  int64 // absolute unix seconds
	RefreshExpiry int64 // absolute unix seconds
}

// mintDeviceAuthTokenPair is the one place that mints a device-auth token pair.
// The refresh token carries vars["refresh"]="true", which is exactly what
// device/auth/refresh requires; a token without it is refused there. Device
// verify, device refresh and device/auth/issue all mint through here so the
// three cannot drift apart.
func mintDeviceAuthTokenPair(nk runtime.NakamaModule, userID, username, discordID string) (*deviceAuthTokenPair, error) {
	accessVars := map[string]string{}
	refreshVars := map[string]string{"refresh": "true"}
	if discordID != "" {
		accessVars["did"] = discordID
		refreshVars["did"] = discordID
	}

	now := time.Now()
	accessExpiry := now.Add(deviceAuthAccessTokenTTL).Unix()
	accessToken, _, err := nk.AuthenticateTokenGenerate(userID, username, accessExpiry, accessVars)
	if err != nil {
		return nil, runtime.NewError("failed to generate token", StatusInternalError)
	}
	refreshExpiry := now.Add(deviceAuthRefreshTokenTTL).Unix()
	refreshToken, _, err := nk.AuthenticateTokenGenerate(userID, username, refreshExpiry, refreshVars)
	if err != nil {
		return nil, runtime.NewError("failed to generate refresh token", StatusInternalError)
	}
	return &deviceAuthTokenPair{
		AccessToken:   accessToken,
		RefreshToken:  refreshToken,
		AccessExpiry:  accessExpiry,
		RefreshExpiry: refreshExpiry,
	}, nil
}

// discordIDLookup resolves a Nakama user ID to its Discord ID.
type discordIDLookup func(ctx context.Context, userID string) (string, error)

// deviceAuthTokenResponse renders a token pair as the RFC 6749 §5.1 body shared
// by device/auth/refresh and device/auth/issue.
//
// `expires_in` is SECONDS FROM NOW, not an absolute time -- without it a
// client has to decode the JWT or invent a lifetime, and nevr-runtime
// currently invents 30 days for the refresh token
// (nevr-runtime plugins/common/include/auth_token_refresh.h:88). A client
// guessing at a server's expiry policy is a defect waiting for the policy
// to change.
//
// `token` is retained, deprecated, so deployed clients keep working.
func deviceAuthTokenResponse(pair *deviceAuthTokenPair, userID, username string) string {
	response, _ := json.Marshal(map[string]interface{}{
		"access_token":             pair.AccessToken,
		"token_type":               "Bearer",
		"expires_in":               int(time.Until(time.Unix(pair.AccessExpiry, 0)).Seconds()),
		"refresh_token":            pair.RefreshToken,
		"refresh_token_expires_in": int(time.Until(time.Unix(pair.RefreshExpiry, 0)).Seconds()),
		"user_id":                  userID,
		"username":                 username,
		"token":                    pair.AccessToken, // deprecated: use access_token
	})
	return string(response)
}

// DeviceAuthRefreshRpc validates a device-auth refresh token (by JWT signature,
// NOT session cache) and issues a new token pair. This survives Nakama restarts
// because it only checks the JWT itself, not the in-memory session cache.
func DeviceAuthRefreshRpc(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	config := nk.(*RuntimeGoNakamaModule).config
	encryptionKey := []byte(config.GetSession().EncryptionKey)
	lookup := func(ctx context.Context, userID string) (string, error) {
		return GetDiscordIDByUserID(ctx, db, userID)
	}
	return deviceAuthRefresh(ctx, logger, nk, encryptionKey, lookup, payload)
}

func deviceAuthRefresh(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, encryptionKey []byte, lookupDiscordID discordIDLookup, payload string) (string, error) {
	if payload == "" {
		return "", runtime.NewError("missing payload", StatusInvalidArgument)
	}

	// RFC 6749 §6 names this field `refresh_token`. This RPC originally took
	// `token`, which is why nevr-runtime sends that; both are accepted so no
	// deployed client breaks, and `refresh_token` is preferred when both are
	// present. New clients SHALL send `refresh_token`.
	var request struct {
		RefreshToken string `json:"refresh_token"`
		Token        string `json:"token"` // deprecated: pre-RFC field name
	}
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", runtime.NewError("invalid payload: refresh_token required", StatusInvalidArgument)
	}
	refreshToken := request.RefreshToken
	if refreshToken == "" {
		refreshToken = request.Token
	}
	if refreshToken == "" {
		return "", runtime.NewError("invalid payload: refresh_token required", StatusInvalidArgument)
	}

	// Validate the refresh token JWT directly (signature + expiry)
	userID, username, tokenVars, exp, _, _, ok := parseToken(encryptionKey, refreshToken)
	if !ok {
		return "", runtime.NewError("invalid or expired refresh token", StatusUnauthenticated)
	}
	if userID.IsNil() || exp <= time.Now().UTC().Unix() {
		return "", runtime.NewError("refresh token expired", StatusUnauthenticated)
	}
	if tokenVars["refresh"] != "true" {
		return "", runtime.NewError("not a refresh token", StatusInvalidArgument)
	}

	// Look up discord ID for new token vars
	discordID, err := lookupDiscordID(ctx, userID.String())
	if err != nil {
		logger.WithField("error", err).Warn("Could not look up discord ID for refresh")
		discordID = ""
	}

	pair, err := mintDeviceAuthTokenPair(nk, userID.String(), username, discordID)
	if err != nil {
		return "", err
	}

	logger.WithFields(map[string]interface{}{"username": username, "user_id": userID.String()}).Info("Device auth token refreshed")
	return deviceAuthTokenResponse(pair, userID.String(), username), nil
}

// DeviceAuthIssueRpc (device/auth/issue) exchanges the caller's authenticated
// session for a device-auth token pair, so a session that did not come from
// device login can refresh through device/auth/refresh.
//
// Why it exists: the built-in POST /v2/account/authenticate/custom (how the web
// app signs Discord users in) returns a refresh token that device/auth/refresh
// refuses -- it lacks vars["refresh"]=="true" -- and that the built-in refresh
// expires after session.refresh_token_expiry_sec (3600s by default). Nakama's
// after-authenticate hook cannot substitute for this: it receives the same
// *api.Session that is returned, but it fires for every custom-auth login
// (Steam, Apple, ...) and cannot tell a Discord login from those. The caller
// asks for this explicitly instead.
//
// Refused for callers whose session is itself a refresh token (it is not an
// access credential) or an impersonation (issuing would launder the 8-hour
// impersonation cap into 30 days).
//
// Request payload: none ("" or "{}"). Response: same body as device/auth/refresh.
func DeviceAuthIssueRpc(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	lookup := func(ctx context.Context, userID string) (string, error) {
		return GetDiscordIDByUserID(ctx, db, userID)
	}
	return deviceAuthIssue(ctx, logger, nk, lookup)
}

func deviceAuthIssue(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, lookupDiscordID discordIDLookup) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("authentication required", StatusUnauthenticated)
	}
	username, _ := ctx.Value(runtime.RUNTIME_CTX_USERNAME).(string)

	vars, _ := ctx.Value(runtime.RUNTIME_CTX_VARS).(map[string]string)
	if vars["refresh"] == "true" {
		return "", runtime.NewError("a refresh token cannot be exchanged for a session", StatusPermissionDenied)
	}
	if vars["impersonated_by"] != "" {
		return "", runtime.NewError("impersonated sessions cannot be made refreshable", StatusPermissionDenied)
	}

	discordID, err := lookupDiscordID(ctx, userID)
	if err != nil {
		logger.WithFields(map[string]interface{}{"user_id": userID, "error": err}).Warn("Could not look up discord ID for user")
		discordID = ""
	}

	pair, err := mintDeviceAuthTokenPair(nk, userID, username, discordID)
	if err != nil {
		logger.WithField("error", err).Error("Failed to generate tokens for device auth issue")
		return "", err
	}

	logger.WithFields(map[string]interface{}{"username": username, "user_id": userID}).Info("Device auth token pair issued")
	return deviceAuthTokenResponse(pair, userID, username), nil
}

// Status codes are defined in evr_runtime_rpc.go:
// StatusInvalidArgument, StatusNotFound, StatusUnauthenticated, StatusInternalError

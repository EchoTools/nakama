package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"math/rand"
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
	Code      string `json:"code"`
	Status    string `json:"status"`     // "pending" or "verified"
	Token     string `json:"token"`      // set when verified
	RefreshToken string `json:"refresh_token"` // set when verified
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	UserID    string `json:"user_id,omitempty"`   // set when verified
	Username  string `json:"username,omitempty"`  // set when verified
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
// Uses a character set that excludes homoglyphs (0/O, 1/I/L, B/8).
func generateDeviceAuthCode() string {
	validChars := "ACDEFGHJKMNPRSTUXYZ2345679"
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	code := make([]byte, 8)
	for i := range code {
		code[i] = validChars[rng.Intn(len(validChars))]
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
		logger.Error("Failed to load device auth codes: %v", err)
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
		logger.Error("Failed to store device auth code: %v", err)
		return "", runtime.NewError("internal error", StatusInternalError)
	}

	logger.Info("Device auth code generated: %s", code)

	response, _ := json.Marshal(map[string]interface{}{
		"code":       code,
		"expires_in": int(DeviceAuthCodeExpiry.Seconds()),
	})
	return string(response), nil
}

// DeviceAuthPollRpc polls for the status of a device auth code.
// Public endpoint — no authentication required.
// Input: { "code": "ABCD-EFGH" }
// Returns: { "status": "pending" } or { "status": "verified", "token": "...", "refresh_token": "..." }
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
		logger.Error("Failed to load device auth codes: %v", err)
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
		response, _ := json.Marshal(map[string]interface{}{
			"status":        "verified",
			"token":         entry.Token,
			"refresh_token": entry.RefreshToken,
			"user_id":       entry.UserID,
			"username":      entry.Username,
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
		logger.Error("Failed to load device auth codes: %v", err)
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

	// Generate a session token for the authenticated user
	// Token expires in 1 hour
	tokenExpiry := time.Now().Add(1 * time.Hour).Unix()
	token, tokenExpiry2, err := nk.AuthenticateTokenGenerate(userID, username, tokenExpiry, nil)
	if err != nil {
		logger.Error("Failed to generate token for device auth: %v", err)
		return "", runtime.NewError("failed to generate token", StatusInternalError)
	}

	// Generate refresh token (longer expiry — 30 days)
	refreshExpiry := time.Now().Add(30 * 24 * time.Hour).Unix()
	refreshToken, _, err := nk.AuthenticateTokenGenerate(userID, username, refreshExpiry, map[string]string{"refresh": "true"})
	if err != nil {
		logger.Error("Failed to generate refresh token: %v", err)
		return "", runtime.NewError("failed to generate refresh token", StatusInternalError)
	}

	// Mark as verified with token
	entry.Status = "verified"
	entry.Token = token
	entry.RefreshToken = refreshToken
	entry.UserID = userID
	entry.Username = username

	if err := storeDeviceAuthCodes(ctx, nk, codes); err != nil {
		logger.Error("Failed to store verified device auth: %v", err)
		return "", runtime.NewError("internal error", StatusInternalError)
	}

	logger.Info("Device auth code %s verified by user %s (%s), token expires at %d", code, username, userID, tokenExpiry2)

	response, _ := json.Marshal(map[string]string{
		"status":   "ok",
		"username": username,
	})
	return string(response), nil
}

// Status codes are defined in evr_runtime_rpc.go:
// StatusInvalidArgument, StatusNotFound, StatusUnauthenticated, StatusInternalError

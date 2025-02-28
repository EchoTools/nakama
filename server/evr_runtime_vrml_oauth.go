package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/echotools/vrmlgo/v3"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	StorageCollectionSocial = "Social"
	StorageKeyVRMLUser      = "VRMLUser"
)

var (
	ErrDiscordIDMismatch = errors.New("Discord ID mismatch")
)

var oauthFlows = &MapOf[string, *VRMLOAuth]{}

type VRMLOAuth struct {
	url      string
	verifier string
	tokenCh  chan string
}

func NewVRMLOAuthFlow(clientID, redirectURL string, timeout time.Duration) (*VRMLOAuth, error) {

	// Generate a random verifier
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(b)

	// Generate the challenge from the verifier
	hash := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(hash[:])

	// Channel used by the RPC to return the code
	key := uuid.Must(uuid.NewV4()).String()

	oauthData := &VRMLOAuth{
		url:      fmt.Sprintf("https://vrmasterleague.com/OAuth?scope=identify&response_type=code&redirect_uri=%s&client_id=%s&state=%s&code_challenge=%s&code_challenge_method=S256", redirectURL, clientID, key, challenge),
		verifier: verifier,
		tokenCh:  make(chan string),
	}
	// Store the verifier and channel for the RPC to use
	oauthFlows.Store(key, oauthData)

	go func() {
		select {
		case <-time.After(timeout):
			oauthFlows.Delete(key)
		}
	}()

	return oauthData, nil
}

func (v *VRMLOAuth) checkCurrentOwner(ctx context.Context, nk runtime.NakamaModule, vrmlUserID string) (string, error) {
	// Check if the account is already owned by another user
	objs, _, err := nk.StorageIndexList(ctx, SystemUserID, StorageIndexVRMLUserID, fmt.Sprintf("+value.userID:%s", vrmlUserID), 100, nil, "")
	if err != nil {
		return "", fmt.Errorf("error checking ownership: %w", err)
	}

	if len(objs.Objects) == 0 {
		return "", nil
	}

	return objs.Objects[0].UserId, nil
}

// VerifyOwnership verifies that the user owns the VRML account by checking the Discord ID
func (v *VRMLOAuth) linkAccounts(ctx context.Context, nk runtime.NakamaModule, vg *vrmlgo.Session, userID string) error {

	vrmlUser, err := vg.Me(vrmlgo.WithUseCache(false))
	if err != nil {
		return runtime.NewError("Failed to get user data", StatusInternalError)
	}

	data, err := json.Marshal(vrmlUser)
	if err != nil {
		return runtime.NewError("Failed to marshal user data", StatusInternalError)
	}

	// Store the VRML user data in the database (effectively linking the account)
	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:      StorageCollectionSocial,
			Key:             StorageKeyVRMLUser,
			UserID:          userID,
			Value:           string(data),
			PermissionRead:  0,
			PermissionWrite: 0,
		},
	}); err != nil {
		return runtime.NewError("Failed to store user", StatusInternalError)
	}

	// Set the VRML player ID in the account metadata
	metadata, err := AccountMetadataLoad(ctx, nk, userID)
	metadata.VRMLPlayerID = vrmlUser.ID
	if err := AccountMetadataUpdate(ctx, nk, userID, metadata); err != nil {
		return runtime.NewError("Failed to save account metadata", StatusInternalError)
	}

	// Queue the event to count matches and assign entitlements
	if err := nk.Event(ctx, &api.Event{
		Name: EventVRMLAccountLinked,
		Properties: map[string]string{
			"user_id": userID,
			"token":   vg.Token,
		},
		External: true,
	}); err != nil {
		return runtime.NewError("Failed to queue event", StatusInternalError)
	}
	return nil
}

// RedirectRPC is called by the client after they have authenticated with VRML
func (v *VRMLVerifier) RedirectRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	envVars, _ := ctx.Value(runtime.RUNTIME_CTX_ENV).(map[string]string)

	if envVars == nil || envVars["VRML_OAUTH_CLIENT_ID"] == "" || envVars["VRML_OAUTH_REDIRECT_URL"] == "" {
		return "", runtime.NewError("Missing VRML_OAUTH_CLIENT_ID in server config", StatusInternalError)
	}

	queryParameters := ctx.Value(runtime.RUNTIME_CTX_QUERY_PARAMS).(map[string][]string)

	// A code was provided; exchange it for a token
	if _, ok := queryParameters["code"]; !ok {
		return "", runtime.NewError("No code provided", StatusInvalidArgument)
	}

	if _, ok := queryParameters["state"]; !ok {
		return "", runtime.NewError("No state token", StatusInvalidArgument)
	}

	callbackData, ok := oauthFlows.LoadAndDelete(queryParameters["state"][0])
	if !ok {
		return "", runtime.NewError("Invalid/expired state token", StatusInvalidArgument)
	}

	// Build the token exchange request
	url := "https://api.vrmasterleague.com/Users/Token"

	params := map[string]interface{}{
		"grant_type":    "authorization_code",
		"client_id":     envVars["VRML_OAUTH_CLIENT_ID"],
		"code_verifier": callbackData.verifier,
		"code":          queryParameters["code"][0],
		"redirect_uri":  envVars["VRML_OAUTH_REDIRECT_URL"],
	}

	paramsBytes, err := json.Marshal(params)
	if err != nil {
		return "", runtime.NewError("Failed to marshal request", StatusInternalError)
	}

	if res, err := http.Post(url, "application/json", io.NopCloser(bytes.NewReader(paramsBytes))); err != nil {
		return "", runtime.NewError("Failed to create request", StatusInternalError)
	} else {
		defer res.Body.Close()
		if body, err := io.ReadAll(res.Body); err != nil {
			return "", runtime.NewError("Failed to read response", StatusInternalError)
		} else {

			// Parse the response
			response := vrmlgo.UserToken{}
			if err := json.Unmarshal(body, &response); err != nil {
				logger.WithFields(map[string]interface{}{
					"response": string(body),
				}).Error("Failed to parse VRML User Token response")
				return "", runtime.NewError("Failed to parse VRML User Token response", StatusInternalError)
			}

			if response.AccessToken == "" {
				return "", runtime.NewError("No access token", StatusInternalError)
			}

			// Check if the channel is closed
			select {
			case <-callbackData.tokenCh:
				return "", runtime.NewError("Invalid/expired state token", StatusInvalidArgument)
			default:
				// Send the token to the channel
				callbackData.tokenCh <- response.AccessToken
			}

		}
	}
	return "VRML Account Linked, you can close this window", nil
}

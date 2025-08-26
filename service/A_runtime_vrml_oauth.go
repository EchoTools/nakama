package service

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

	"github.com/echotools/vrmlgo/v5"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server"
)

var (
	ErrOAuthClientIDMissing = errors.New("Missing VRML_OAUTH_CLIENT_ID in server config")
)
var oauthFlows = &server.MapOf[string, *VRMLOAuth]{}

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

	// Set a timeout to delete the flow after the specified duration
	time.AfterFunc(timeout, func() { oauthFlows.Delete(key) })

	return oauthData, nil
}

// RedirectRPC is called by the client after they have authenticated with VRML
func (v *VRMLScanQueue) RedirectRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	envVars, _ := ctx.Value(runtime.RUNTIME_CTX_ENV).(map[string]string)

	if envVars == nil || envVars["VRML_OAUTH_CLIENT_ID"] == "" || envVars["VRML_OAUTH_REDIRECT_URL"] == "" {
		return "", errors.New("Missing VRML_OAUTH_CLIENT_ID in server config")
	}

	queryParameters := ctx.Value(runtime.RUNTIME_CTX_QUERY_PARAMS).(map[string][]string)

	// A code was provided; exchange it for a token
	if _, ok := queryParameters["code"]; !ok {
		return "", errors.New("No code provided")
	}

	if _, ok := queryParameters["state"]; !ok {
		return "", errors.New("No state token")
	}

	callbackData, ok := oauthFlows.LoadAndDelete(queryParameters["state"][0])
	if !ok {
		return "", errors.New("Invalid/expired state token")
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
		return "", errors.New("Failed to marshal request")
	}

	if res, err := http.Post(url, "application/json", io.NopCloser(bytes.NewReader(paramsBytes))); err != nil {
		return "", errors.New("Failed to create request")
	} else {
		defer res.Body.Close()
		if body, err := io.ReadAll(res.Body); err != nil {
			return "", errors.New("Failed to read response")
		} else {

			// Parse the response
			response := vrmlgo.UserToken{}
			if err := json.Unmarshal(body, &response); err != nil {
				logger.WithFields(map[string]interface{}{
					"response": string(body),
				}).Error("Failed to parse VRML User Token response")
				return "", errors.New("Failed to parse VRML User Token response")
			}

			if response.AccessToken == "" {
				return "", errors.New("No access token")
			}

			// Check if the channel is closed
			select {
			case <-callbackData.tokenCh:
				return "", errors.New("Invalid/expired state token")
			default:
				// Send the token to the channel
				callbackData.tokenCh <- response.AccessToken
			}

		}
	}
	return "VRML Account Linked, you can close this window", nil
}

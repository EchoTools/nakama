package socialauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/internal/intents"
	"golang.org/x/oauth2"
)

const (
	EnvGitHubClientID     = "GITHUB_CLIENT_ID"
	EnvGitHubClientSecret = "GITHUB_CLIENT_SECRET"
	EnvGitHubRedirectURI  = "GITHUB_REDIRECT_URI"
	GitHubDeviceIDPrefix  = "github:"
)

type GitHubUser struct {
	ID            int64  `json:"id"`
	Login         string `json:"login"`
	Name          string `json:"name"`
	Email         string `json:"email"`
	AvatarURL     string `json:"avatar_url"`
	PublicRepos   int    `json:"public_repos"`
	PrivateRepos  int    `json:"total_private_repos"`
	Followers     int    `json:"followers"`
	Following     int    `json:"following"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
	Company       string `json:"company"`
	Location      string `json:"location"`
	PublicGists   int    `json:"public_gists"`
	PrivateGists  int    `json:"total_private_gists"`
	DiskUsage     int    `json:"disk_usage"`
	Collaborators int    `json:"collaborators"`
	TwoFactorAuth bool   `json:"two_factor_authentication"`
	Plan          *Plan  `json:"plan"`
}

type Plan struct {
	Name          string `json:"name"`
	Space         int64  `json:"space"`
	PrivateRepos  int64  `json:"private_repos"`
	Collaborators int64  `json:"collaborators"`
}

func (s *SocialAuthHandler) githubHttpLoginHandler(w http.ResponseWriter, r *http.Request) {
	// Redirect user to GitHub OAuth login page
	redirectURL := s.generateGitHubURL(s.githubRedirectURI)
	http.Redirect(w, r, redirectURL, http.StatusFound)
}

func LinkGitHub(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID, githubRedirectURI, githubClientID, githubClientSecret, code string) (*GitHubUser, error) {
	// Exchange code for GitHub user ID and username
	gitHubUser, token, err := AuthenticateGitHub(ctx, logger, githubRedirectURI, githubClientID, githubClientSecret, code)
	if err != nil {
		return nil, fmt.Errorf("GitHub authentication failed: %w", err)
	}

	deviceID := GitHubDeviceIDPrefix + strconv.Itoa(int(gitHubUser.ID))
	if err := nk.LinkDevice(ctx, userID, deviceID); err != nil {
		return nil, fmt.Errorf("failed to link GitHub device: %w", err)
	}

	err = StoreGitHubToken(ctx, nk, userID, token)
	if err != nil {
		logger.WithField("err", err).Warn("Failed to store GitHub tokens")
	}

	err = StoreGitHubProfile(ctx, nk, userID, gitHubUser)
	if err != nil {
		logger.WithField("err", err).Warn("Failed to store GitHub profile")
	}

	return gitHubUser, nil
}

func (s *SocialAuthHandler) githubHttpCallbackHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	queryParams := r.URL.Query()
	code, err := s.extractAndValidateOAuth2Callback(queryParams)
	if err != nil {
		http.Error(w, "Invalid OAuth2 callback: "+err.Error(), http.StatusBadRequest)
		return
	}

	userID, username, _, err := s.nk.AuthenticateCustom(ctx, code, "", true)
	if err != nil {
		http.Error(w, "GitHub authentication failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	sessionVars := intents.SessionVars{}

	// Check if they have the required intents
	sessionVars.Intents.IsGlobalOperator, err = CheckGroupMembershipByName(ctx, s.db, userID, "Global Operators", "system")
	if err != nil {
		http.Error(w, "Failed to check group membership: "+err.Error(), http.StatusInternalServerError)
		return
	}

	sessionVars.Intents.IsGlobalDeveloper, err = CheckGroupMembershipByName(ctx, s.db, userID, "Global Developers", "system")
	if err != nil {
		http.Error(w, "Failed to check group membership: "+err.Error(), http.StatusInternalServerError)
		return
	}

	vars := sessionVars.MarshalVars()
	expiration := time.Now().Add(jwtTokenLifetimeDuration)
	token, _, err := s.nk.AuthenticateTokenGenerate(userID, username, expiration.Unix(), vars)
	if err != nil {
		http.Error(w, "Failed to generate session token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Set the authenticated user cookie
	setRefreshTokenCookie(w, token)

	// If there was a redirect path specified, redirect there
	if redirectPath := queryParams.Get("redirect"); redirectPath != "" {
		http.Redirect(w, r, redirectPath, http.StatusFound)
		return
	}

	// Return the Nakama user ID
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(userID))
}

func (s *SocialAuthHandler) generateGitHubURL(redirectURI string) string {
	const githubOAuth2AuthURL = "https://github.com/login/oauth/authorize"
	responseType := "code"
	//scope := "user:email read:user"
	scope := "" // Requesting no scopes returns only the public information
	// Build GitHub authorization URL.
	u, _ := url.Parse(githubOAuth2AuthURL)
	q := u.Query()
	q.Set("client_id", s.githubClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", responseType)
	q.Set("scope", scope)
	q.Set("state", s.NewOAuth2State(5*time.Minute))
	u.RawQuery = q.Encode()
	return u.String()
}

func AuthenticateGitHub(ctx context.Context, logger runtime.Logger, redirectURL, clientID, clientSecret, code string) (*GitHubUser, *oauth2.Token, error) {
	// Exchange the code for an access token
	conf := &oauth2.Config{
		Endpoint: oauth2.Endpoint{
			AuthURL:   "https://github.com/login/oauth/authorize",
			TokenURL:  "https://github.com/login/oauth/access_token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
		Scopes:       []string{}, // Requesting no scopes returns only the public information
		RedirectURL:  redirectURL,
		ClientID:     clientID,
		ClientSecret: clientSecret,
	}

	token, err := conf.Exchange(ctx, code)
	if err != nil {
		return nil, nil, fmt.Errorf("code exchange failed: %w", err)
	}

	// Get the GitHub user
	user, err := GetGitHubUserInfo(ctx, token.AccessToken)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to get GitHub user: %w", err)
	}

	// Return the GitHub user ID and username
	return user, token, nil
}

// GetGitHubUserInfo retrieves the GitHub user information using the provided access token.
func GetGitHubUserInfo(ctx context.Context, accessToken string) (*GitHubUser, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
	if err != nil {
		return nil, fmt.Errorf("unable to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "Nakama-Server")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("unable to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var user GitHubUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("unable to decode GitHub user: %w", err)
	}

	return &user, nil
}

// isGitHubTokenValid checks if the provided GitHub access token is still valid.
func isGitHubTokenValid(ctx context.Context, accessToken string) bool {
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "Nakama-Server")

	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return false
	}
	return true
}

// ExchangeGitHubCodeForToken exchanges the authorization code for an access token and returns the oauth2 token.
func ExchangeGitHubCodeForToken(ctx context.Context, logger runtime.Logger, redirectURL, clientID, clientSecret, code string) (*oauth2.Token, error) {
	conf := &oauth2.Config{
		Endpoint: oauth2.Endpoint{
			AuthURL:   "https://github.com/login/oauth/authorize",
			TokenURL:  "https://github.com/login/oauth/access_token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
		Scopes:       []string{"user:email", "read:user"},
		RedirectURL:  redirectURL,
		ClientID:     clientID,
		ClientSecret: clientSecret,
	}
	token, err := conf.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("code exchange failed: %w", err)
	}
	return token, nil
}

// StoreGitHubToken saves the GitHub tokens securely in the database.
func StoreGitHubToken(ctx context.Context, nk runtime.NakamaModule, userID string, token *oauth2.Token) error {
	// Serialize for storage
	b, err := json.Marshal(token)
	if err != nil {
		return err
	}
	// Store in Nakama storage
	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:      StorageCollectionSocialAuth,
			Key:             StorageKeyGitHubToken,
			UserID:          userID,
			Value:           string(b),
			PermissionRead:  runtime.STORAGE_PERMISSION_NO_READ,
			PermissionWrite: runtime.STORAGE_PERMISSION_NO_WRITE,
		}}); err != nil {
		return fmt.Errorf("failed to write GitHub tokens to storage: %w", err)
	}
	return nil
}

// StoreGitHubProfile saves the GitHub user profile securely in the database.
func StoreGitHubProfile(ctx context.Context, nk runtime.NakamaModule, userID string, user *GitHubUser) error {
	// Serialize for storage
	b, err := json.Marshal(user)
	if err != nil {
		return err
	}
	// Store in Nakama storage
	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:      StorageCollectionSocialAuth,
			Key:             StorageKeyGitHubProfile,
			UserID:          userID,
			Value:           string(b),
			PermissionRead:  runtime.STORAGE_PERMISSION_NO_READ,
			PermissionWrite: runtime.STORAGE_PERMISSION_NO_WRITE,
		}}); err != nil {
		return fmt.Errorf("failed to write GitHub profile to storage: %w", err)
	}
	return nil
}

// LoadGitHubTokens loads GitHub tokens for a user from the database.
func LoadGitHubTokens(ctx context.Context, nk runtime.NakamaModule, userID string) (*oauth2.Token, error) {
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: StorageCollectionSocialAuth,
			Key:        StorageKeyGitHubToken,
			UserID:     userID,
		},
	})
	if err != nil {
		return nil, err
	}
	if len(objs) == 0 {
		return nil, errors.New("no GitHub tokens found for user")
	}
	data := oauth2.Token{}
	err = json.Unmarshal([]byte(objs[0].Value), &data)
	if err != nil {
		return nil, err
	}
	return &data, nil
}

// LoadGitHubProfile loads GitHub profile for a user from the database.
func LoadGitHubProfile(ctx context.Context, nk runtime.NakamaModule, userID string) (*GitHubUser, error) {
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: StorageCollectionSocialAuth,
			Key:        StorageKeyGitHubProfile,
			UserID:     userID,
		},
	})
	if err != nil {
		return nil, err
	}
	if len(objs) == 0 {
		return nil, errors.New("no GitHub profile found for user")
	}
	var data GitHubUser
	err = json.Unmarshal([]byte(objs[0].Value), &data)
	if err != nil {
		return nil, err
	}
	return &data, nil
}

package socialauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"golang.org/x/oauth2"
	"google.golang.org/grpc/codes"
)

const (
	EnvDiscordClientID      = "DISCORD_CLIENT_ID"
	EnvDiscordClientSecret  = "DISCORD_CLIENT_SECRET"
	EnvDiscordRedirectURI   = "DISCORD_REDIRECT_URI"
	EnvSteamCallbackURL     = "STEAM_CALLBACK_URL"
	EnvSessionEncryptionKey = "SESSION_ENCRYPTION_KEY"
)

func (s *SocialAuthHandler) discordHttpLoginHandler(w http.ResponseWriter, r *http.Request) {
	// Redirect user to Steam OpenID login page
	redirectURL := s.generateDiscordURL(s.discordRedirectURI)
	http.Redirect(w, r, redirectURL, http.StatusFound)
}

func (s *SocialAuthHandler) discordHttpCallbackHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	queryParams := r.URL.Query()
	code, err := s.extractAndValidateOAuth2Callback(queryParams)
	if err != nil {
		http.Error(w, "Invalid OAuth2 callback: "+err.Error(), http.StatusBadRequest)
		return
	}

	userID, username, _, err := s.nk.AuthenticateCustom(ctx, code, "", true)
	if err != nil {
		http.Error(w, "Discord authentication failed: "+err.Error(), http.StatusInternalServerError)
	}

	sessionVars, err := setUpSessionVars(ctx, s.db, userID)
	if err != nil {
		http.Error(w, "Failed to set up session variables: "+err.Error(), http.StatusInternalServerError)
		return
	}

	expiration := time.Now().Add(jwtTokenLifetimeDuration)
	token, _, err := s.nk.AuthenticateTokenGenerate(userID, username, expiration.Unix(), sessionVars)
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

func (s *SocialAuthHandler) generateDiscordURL(redirectURI string) string {
	const discordOAuth2AuthURL = "https://discord.com/api/oauth2/authorize"
	responseType := "code"
	scope := "identify email"
	// Build Discord authorization URL.
	u, _ := url.Parse(discordOAuth2AuthURL)
	q := u.Query()
	q.Set("client_id", s.discordClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", responseType)
	q.Set("scope", scope)
	q.Set("state", s.NewOAuth2State(5*time.Minute))
	u.RawQuery = q.Encode()
	return u.String()
}

func (s *SocialAuthHandler) NewOAuth2State(expiry time.Duration) string {
	state := uuid.Must(uuid.NewV4()).String()
	s.stateStore.Store(state, true)
	go func() {
		// Automatically delete the state after 5 minutes to prevent memory bloat
		<-time.After(expiry)
		s.stateStore.Delete(state)
	}()
	return state
}

func (s *SocialAuthHandler) ValidateState(state string) bool {
	_, found := s.stateStore.LoadAndDelete(state)
	return found
}

func (s *SocialAuthHandler) extractAndValidateOAuth2Callback(urlValues url.Values) (code string, err error) {
	// Extract query parameters from the context
	// Extract the authorization code from the query parameters
	if v := urlValues.Get("code"); v == "" {
		return "", runtime.NewError("code parameter missing", int(codes.InvalidArgument))
	} else {
		code = v
	}
	// Extract the state parameter from the query parameters
	if v := urlValues.Get("state"); v == "" {
		return "", runtime.NewError("state parameter missing", int(codes.InvalidArgument))
	} else if !s.ValidateState(v) {
		return "", runtime.NewError("invalid state parameter", int(codes.InvalidArgument))
	}
	return
}

func AuthenticateDiscord(ctx context.Context, logger runtime.Logger, redirectURL, clientID, clientSecret, code string) (string, string, error) {

	// Exchange the code for an access token
	conf := &oauth2.Config{
		Endpoint: oauth2.Endpoint{
			AuthURL:   "https://discord.com/api/oauth2/authorize",
			TokenURL:  "https://discord.com/api/oauth2/token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
		Scopes:       []string{"identify"},
		RedirectURL:  redirectURL,
		ClientID:     clientID,
		ClientSecret: clientSecret,
	}

	token, err := conf.Exchange(ctx, code)
	if err != nil {
		return "", "", fmt.Errorf("code exchange failed: %w", err)
	}

	// Create a Discord client
	discord, err := discordgo.New("Bearer " + token.AccessToken)
	if err != nil {
		return "", "", fmt.Errorf("unable to create Discord client: %w", err)
	}

	// Get the Discord user
	user, err := discord.User("@me")
	if err != nil {
		return "", "", fmt.Errorf("unable to get Discord user: %w", err)
	}
	// Return the Discord user ID and username
	return user.ID, user.Username, nil
}

// ExchangeDiscordCodeForToken exchanges the authorization code for an access token and returns the oauth2 token.
func ExchangeDiscordCodeForToken(ctx context.Context, logger runtime.Logger, redirectURL, clientID, clientSecret, code string) (*oauth2.Token, error) {
	conf := &oauth2.Config{
		Endpoint: oauth2.Endpoint{
			AuthURL:   "https://discord.com/api/oauth2/authorize",
			TokenURL:  "https://discord.com/api/oauth2/token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
		Scopes:       []string{"identify"},
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

// GetDiscordUserInfo retrieves the Discord user information using the provided access token.
func GetDiscordUserInfo(ctx context.Context, accessToken string) (*discordgo.User, error) {
	discord, err := discordgo.New("Bearer " + accessToken)
	if err != nil {
		return nil, fmt.Errorf("unable to create Discord client: %w", err)
	}
	user, err := discord.User("@me", discordgo.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("unable to get Discord user: %w", err)
	}
	return user, nil
}

// CheckDiscordSession checks token validity and refreshes if needed, logs out if revoked.
func CheckDiscordSession(ctx context.Context, nk runtime.NakamaModule, userID string, conf *oauth2.Config) (*oauth2.Token, error) {
	query := fmt.Sprintf(`+value.user_id:%s +value.expiry<="%s"`, userID, time.Now().Format("2006-01-02T15:04:05Z"))

	result, _, err := nk.StorageIndexList(ctx, uuid.Nil.String(), StorageIndexDiscordToken, query, 1, nil, "")
	if err != nil {
		return nil, fmt.Errorf("failed to query Discord tokens: %w", err)
	}

	if len(result.Objects) == 0 {
		// No tokens found, user must authenticate
		return nil, errors.New("no Discord tokens found, user must authenticate")
	}

	token := &oauth2.Token{}
	err = json.Unmarshal([]byte(result.Objects[0].Value), token)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal Discord token: %w", err)
	}

	// If token is expired, attempt to refresh
	if !token.Valid() {
		tokenSource := conf.TokenSource(ctx, token)
		newToken, err := tokenSource.Token()
		if err != nil {
			// Token refresh failed, treat as revoked and log out user
			return nil, errors.New("discord token expired or revoked, user must reauthenticate")
		}
		// Store new token
		if err := StoreDiscordTokens(ctx, nk, userID, newToken); err != nil {
			return nil, fmt.Errorf("failed to update Discord token: %w", err)
		}
		token = newToken
	}
	// Optionally, check token with isDiscordTokenValid
	if !isDiscordTokenValid(ctx, token.AccessToken) {
		return nil, errors.New("discord token invalid, user must reauthenticate")
	}
	return token, nil
}

// StoreDiscordTokens saves the Discord tokens securely in the database.
func StoreDiscordTokens(ctx context.Context, nk runtime.NakamaModule, userID string, token *oauth2.Token) error {
	// Example: encrypt before storing in production!

	// Serialize for storage (replace with your DB logic)
	b, err := json.Marshal(token)
	if err != nil {
		return err
	}
	// Store in Nakama storage
	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:      StorageCollectionSocialAuth,
			Key:             StorageKeyDiscordToken,
			UserID:          userID,
			Value:           string(b),
			PermissionRead:  runtime.STORAGE_PERMISSION_NO_READ,
			PermissionWrite: runtime.STORAGE_PERMISSION_NO_WRITE,
		}}); err != nil {
		return fmt.Errorf("failed to write Discord tokens to storage: %w", err)
	}
	return nil
}

// LoadDiscordTokens loads Discord tokens for a user from the database.
func LoadDiscordTokens(ctx context.Context, nk runtime.NakamaModule, userID string) (*oauth2.Token, error) {
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: StorageCollectionSocialAuth,
			Key:        StorageKeyDiscordToken,
			UserID:     userID,
		},
	})
	if err != nil {
		return nil, err
	}
	if len(objs) == 0 {
		return nil, errors.New("no Discord tokens found for user")
	}
	data := oauth2.Token{}
	err = json.Unmarshal([]byte(objs[0].Value), &data)
	if err != nil {
		return nil, err
	}
	return &data, nil
}

// isDiscordTokenValid checks if the provided Discord access token is still valid.
func isDiscordTokenValid(ctx context.Context, accessToken string) bool {
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://discord.com/api/users/@me", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return false
	}
	return true
}

package socialauth

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/internal/intents"
	"golang.org/x/oauth2"
	"google.golang.org/grpc/codes"
)

func (s *SocialAuthHandler) InitializeDiscordOAuth2(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) error {
	if err := initializer.RegisterHttp("/v2/auth/discord/login", s.discordHttpLoginHandler, http.MethodGet); err != nil {
		return err
	}

	if err := initializer.RegisterHttp("/v2/auth/discord/callback", s.discordHttpCallbackHandler, http.MethodGet); err != nil {
		return err
	}
	return nil
}

func (s *SocialAuthHandler) discordHttpLoginHandler(w http.ResponseWriter, r *http.Request) {
	// Redirect user to Steam OpenID login page
	redirectURL := s.generateDiscordURL(s.discordRedirectURI)
	http.Redirect(w, r, redirectURL, http.StatusFound)
}

func (s *SocialAuthHandler) discordHttpCallbackHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := s.logger

	queryParams := r.URL.Query()
	code, err := s.extractAndValidateOAuth2Callback(queryParams)
	if err != nil {
		http.Error(w, "Invalid OAuth2 callback: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Link or create a Nakama user account based on the Discord user ID
	userID, username, _, err := s.AuthenticateDiscord(ctx, logger, code)
	if err != nil {
		http.Error(w, "Discord authentication failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	sessionVars := intents.SessionVars{}

	// Check if they have the required intents
	sessionVars.IsGlobalOperator, err = CheckGroupMembershipByName(ctx, s.db, userID, "Global Operators", "system")
	if err != nil {
		http.Error(w, "Failed to check group membership: "+err.Error(), http.StatusInternalServerError)
		return
	}

	sessionVars.IsGlobalOperator, err = CheckGroupMembershipByName(ctx, s.db, userID, "Global Developers", "system")
	if err != nil {
		http.Error(w, "Failed to check group membership: "+err.Error(), http.StatusInternalServerError)
		return
	}

	vars := sessionVars.MarshalVars()
	expiration := time.Now().Add(24 * time.Hour * 7) // 7 days
	token, _, err := s.nk.AuthenticateTokenGenerate(userID, username, expiration.Unix(), vars)

	// Set the authenticated user cookie
	setJWTAuthCookie(w, token)
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

func (s *SocialAuthHandler) AuthenticateDiscord(ctx context.Context, logger runtime.Logger, code string) (string, string, bool, error) {
	// Exchange the code for an access token
	conf := &oauth2.Config{
		Endpoint: oauth2.Endpoint{
			AuthURL:   "https://discord.com/api/oauth2/authorize",
			TokenURL:  "https://discord.com/api/oauth2/token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
		Scopes:       []string{"identify"},
		RedirectURL:  s.discordRedirectURI,
		ClientID:     s.discordClientID,
		ClientSecret: s.discordClientSecret,
	}

	token, err := conf.Exchange(context.Background(), code)
	if err != nil {
		return "", "", false, runtime.NewError("code exchange failed: "+err.Error(), int(codes.Internal))
	}

	// Create a Discord client
	discord, err := discordgo.New("Bearer " + token.AccessToken)
	if err != nil {
		logger.WithField("err", err).Error("Unable to create Discord client")
		return "", "", false, runtime.NewError("Unable to create Discord client", int(codes.Internal))
	}

	// Get the Discord user
	user, err := discord.User("@me")
	if err != nil {
		logger.WithField("err", err).Error("Unable to get Discord user")
		return "", "", false, runtime.NewError("Unable to get Discord user", int(codes.Internal))
	}

	// Authenticate/create an account.
	userID, username, created, err := s.nk.AuthenticateCustom(ctx, user.ID, user.Username, true)
	if err != nil {
		return "", "", false, runtime.NewError("Unable to create user", int(codes.Internal))
	}
	return userID, username, created, nil
}

package socialauth

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/internal/intents"
	"google.golang.org/grpc/codes"
)

const (
	jwtTokenLifetimeDuration = 24 * time.Hour * 14
	RefreshTokenCookieName   = "refresh_token"
	APIPathPrefix            = "/v2/api/#"
)

type SocialAuthHandler struct {
	ctx        context.Context
	logger     runtime.Logger
	db         *sql.DB
	nk         runtime.NakamaModule
	stateStore sync.Map

	discordClientID     string
	discordClientSecret string
	discordRedirectURI  string
	githubClientID      string
	githubClientSecret  string
	githubRedirectURI   string
	// Set this to your public callback URL in production
	steamCallbackURL string

	sessionEncryptionKey string
}

func NewAPIAuthHandler(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) (*SocialAuthHandler, error) {
	env := ctx.Value(runtime.RUNTIME_CTX_ENV).(map[string]string)

	discordClientID := env["DISCORD_CLIENT_ID"]
	discordClientSecret := env["DISCORD_CLIENT_SECRET"]
	discordRedirectURI := env["DISCORD_REDIRECT_URI"]
	githubClientID := env["GITHUB_CLIENT_ID"]
	githubClientSecret := env["GITHUB_CLIENT_SECRET"]
	githubRedirectURI := env["GITHUB_REDIRECT_URI"]
	steamCallbackURL := env["STEAM_CALLBACK_URL"]
	sessionEncryptionKey := env["SESSION_ENCRYPTION_KEY"]

	s := &SocialAuthHandler{
		ctx:                  ctx,
		logger:               logger,
		db:                   db,
		nk:                   nk,
		discordClientID:      discordClientID,
		discordClientSecret:  discordClientSecret,
		discordRedirectURI:   discordRedirectURI,
		githubClientID:       githubClientID,
		githubClientSecret:   githubClientSecret,
		githubRedirectURI:    githubRedirectURI,
		steamCallbackURL:     steamCallbackURL,
		sessionEncryptionKey: sessionEncryptionKey,
	}
	if discordClientID == "" || discordClientSecret == "" || discordRedirectURI == "" {
		logger.Warn("Discord OAuth2 environment variables are not set. Discord authentication will not be available.")
	}

	if githubClientID == "" || githubClientSecret == "" || githubRedirectURI == "" {
		logger.Warn("GitHub OAuth2 environment variables are not set. GitHub authentication will not be available.")
	}

	/*
		if err := initializer.RegisterHttp("/v2/api/#", http.StripPrefix("/v2/api/#", http.HandlerFunc(s.authRootHandler)).ServeHTTP, http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete); err != nil {
			return nil, fmt.Errorf("unable to register /static/ file server: %w", err)
		}
	*/
	return s, nil
}

func (s *SocialAuthHandler) authRootHandler(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	_ = path
	// Route the request based on the path
	switch {
	/*
		case path == "/auth/login":
			fallthrough
		case path == "/auth/discord/login":
			s.discordHttpLoginHandler(w, r)
		case path == "/auth/discord/callback":
			s.discordHttpCallbackHandler(w, r)
		case path == "/auth/github/login":
			s.githubHttpLoginHandler(w, r)
		case path == "/auth/github/callback":
			s.githubHttpCallbackHandler(w, r)
		case path == "/auth/github/link":
			s.githubLinkHttpHandler(w, r)
		//case path == "/auth/steam/login":
		//	s.steamLoginHttpHandler(w, r)
		//case path == "/auth/steam/callback":
		//	s.authSteamHttpHandler(w, r)
		case path == "/auth/logout":
			s.authLogoutHttpHandler(w, r)
	*/
	default:
		http.NotFound(w, r)
	}
}

// Logout handler: blacklist JWT token
func (s *SocialAuthHandler) authLogoutHttpHandler(w http.ResponseWriter, r *http.Request) {

	// Get the auth token from the Bearer header, and the refresh token from the cookie
	token, refreshToken, err := s.extractAuthTokens(r)
	if err != nil {
		http.Error(w, "User not authenticated", http.StatusUnauthorized)
		return
	}

	// Verify the session token and extract the user ID
	jwtToken, err := verifySignedJWT(token, s.sessionEncryptionKey)
	if err != nil {
		http.Error(w, "Unable to verify session token: "+err.Error(), http.StatusUnauthorized)
		return
	}
	userID := jwtToken.Claims.(jwt.MapClaims)["uid"].(string)

	// Revoke the tokens
	if err := s.nk.SessionLogout(userID, token, refreshToken); err != nil {
		http.Error(w, "Failed to revoke tokens: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Clear the auth cookie
	setRefreshTokenCookie(w, "")

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Logged out"))
}

func setRefreshTokenCookie(w http.ResponseWriter, refreshToken string) {
	// Set a cookie to indicate the user is authenticated
	http.SetCookie(w, &http.Cookie{
		Name:     "refresh_token",
		Value:    refreshToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   60 * 60 * 24 * 7, // 7 days
	})
}

func (s *SocialAuthHandler) extractAuthTokens(r *http.Request) (token string, refreshToken string, err error) {
	// Get the JWT auth cookie or the Bearer token from the Authorization header
	if authHeader := r.Header.Get("Authorization"); authHeader != "" {
		const bearerPrefix = "Bearer "
		if len(authHeader) < len(bearerPrefix) || authHeader[:len(bearerPrefix)] != bearerPrefix {
			return "", "", fmt.Errorf("invalid Authorization header format")
		}
		token = authHeader[len(bearerPrefix):]
	}
	// Fallback to cookie if no Authorization header is present
	if cookie, err := r.Cookie(RefreshTokenCookieName); err != nil {
		return token, "", fmt.Errorf("no refresh token cookie found")
	} else {
		refreshToken = cookie.Value
	}
	return token, refreshToken, nil
}

func validateAuthenticatedUserCookie(logger runtime.Logger, r *http.Request, sessionEncryptionKey string) (string, error) {
	cookie, err := r.Cookie(RefreshTokenCookieName)
	if err != nil {
		return "", nil
	}

	// Verify the session token and extract the user ID
	token, err := verifySignedJWT(cookie.Value, sessionEncryptionKey)
	if err != nil {
		logger.WithField("err", err).Error("Unable to verify session token")
		return "", runtime.NewError("Unable to verify session token", int(codes.Unauthenticated))
	}

	userID := token.Claims.(jwt.MapClaims)["uid"].(string)
	return userID, nil
}

func verifySignedJWT(rawToken string, secret string) (*jwt.Token, error) {
	token, err := jwt.Parse(rawToken, func(token *jwt.Token) (interface{}, error) {
		// Don't forget to validate the alg is what you expect:
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	return token, nil
}

func CheckGroupMembershipByName(ctx context.Context, db *sql.DB, userID, groupName, groupType string) (bool, error) {
	query := `
SELECT ge.state FROM groups g, group_edge ge WHERE g.id = ge.destination_id AND g.lang_tag = $1 AND g.name = $2 
AND ge.source_id = $3 AND ge.state >= 0 AND ge.state <= 2;
`

	params := make([]interface{}, 0, 4)
	params = append(params, groupType)
	params = append(params, groupName)
	params = append(params, userID)
	rows, err := db.QueryContext(ctx, query, params...)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return false, nil
	}
	return true, nil
}

func setUpSessionVars(ctx context.Context, db *sql.DB, userID string) (map[string]string, error) {
	// Clear the session vars and set any needed ones
	if userID != "" {
		// Clear and set any session vars if needed
		sessionVars := intents.SessionVars{}
		// Ensure they have the permissions they are requesting in their vars.
		var err error
		sessionVars.Intents.IsGlobalOperator, err = CheckGroupMembershipByName(ctx, db, userID, "Global Operators", "system")
		if err != nil {
			return nil, fmt.Errorf("failed to check group membership: %w", err)
		}
		sessionVars.Intents.IsGlobalDeveloper, err = CheckGroupMembershipByName(ctx, db, userID, "Global Developers", "system")
		if err != nil {
			return nil, fmt.Errorf("failed to check group membership: %w", err)
		}
		return sessionVars.MarshalVars(), nil
	}
	return nil, nil
}

package socialauth

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/yohcop/openid-go"
)

func (s *SocialAuthHandler) InitializeSteamOpenID(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) error {
	if err := initializer.RegisterHttp("/v2/auth/steam/login", s.steamLoginHttpHandler, http.MethodGet); err != nil {
		return err
	}

	if err := initializer.RegisterHttp("/v2/auth/steam/callback", s.authSteamHttpHandler, http.MethodGet); err != nil {
		return err
	}

	return nil
}

func (s *SocialAuthHandler) steamLoginHttpHandler(w http.ResponseWriter, r *http.Request) {
	const steamOpenIDProvider = "https://steamcommunity.com/openid"
	// Redirect user to Steam OpenID login page
	redirectURL, err := openid.RedirectURL(steamOpenIDProvider, s.steamCallbackURL, s.steamCallbackURL)
	if err != nil {
		http.Error(w, "Failed to get redirect URL: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, redirectURL, http.StatusFound)
}

func (s *SocialAuthHandler) authSteamHttpHandler(w http.ResponseWriter, r *http.Request) {
	userID, err := validateAuthenticatedUserCookie(s.logger, r, s.sessionEncryptionKey)
	if err != nil {
		http.Error(w, "User not authenticated", http.StatusUnauthorized)
		return
	}

	fullURL := s.steamCallbackURL + "?" + r.URL.RawQuery

	// Verify OpenID response
	id, err := openid.Verify(fullURL, openid.NewSimpleDiscoveryCache(), openid.NewSimpleNonceStore())
	if err != nil {
		http.Error(w, "OpenID verification failed: "+err.Error(), http.StatusUnauthorized)
		return
	}

	steamID := extractSteamID(id)
	if steamID == "" {
		http.Error(w, "Invalid SteamID", http.StatusUnauthorized)
		return
	}

	// Link the SteamID with user account in your system here
	if err := s.nk.LinkSteam(s.ctx, userID, "", steamID, false); err != nil {
		http.Error(w, "Failed to link Steam account: "+err.Error(), http.StatusInternalServerError)
	}
	// Display SteamID for demonstration
	fmt.Fprintf(w, "Authenticated! Your SteamID is: %s", steamID)
}

// extractSteamID parses the SteamID from OpenID identity URL
func extractSteamID(identityURL string) string {
	// Steam OpenID returns something like: https://steamcommunity.com/openid/id/76561198012345678
	parts := strings.Split(identityURL, "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

package socialauth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"golang.org/x/oauth2"
)

const (
	StorageCollectionSocialAuth = "SocialAuth"
	StorageKeyDiscordToken      = "discordAauth2Token"
	StorageIndexDiscordToken    = "discordTokenIdx"
	StorageKeyGitHubToken       = "githubOAuth2Token"
	StorageIndexGitHubToken     = "githubTokenIdx"
	StorageKeyGitHubProfile     = "githubProfile"

	GithubAuthPrefix  = "github:"
	DiscordAuthPrefix = "discord:"
)

var (
	ErrUserMustReauthenticate = errors.New("user must reauthenticate with Discord")
)

func InitializeSocialAuth(ctx context.Context, logger runtime.Logger, initializer runtime.Initializer) error {
	// Register the storage index for Discord tokens
	if err := initializer.RegisterStorageIndex(StorageIndexDiscordToken, StorageCollectionSocialAuth, StorageKeyDiscordToken, []string{"expiry"}, nil, 100000, false); err != nil {
		return fmt.Errorf("failed to register Discord storage index: %w", err)
	}
	if err := initializer.RegisterStorageIndexFilter(StorageIndexDiscordToken, func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, write *runtime.StorageWrite) bool {
		// Only index entries with a valid expiry field
		var token oauth2.Token
		if err := json.Unmarshal([]byte(write.Value), &token); err != nil {
			return false
		}
		return token.Expiry.After(time.Now())
	}); err != nil {
		return fmt.Errorf("failed to register Discord storage index filter: %w", err)
	}

	// Register the storage index for GitHub tokens
	if err := initializer.RegisterStorageIndex(StorageIndexGitHubToken, StorageCollectionSocialAuth, StorageKeyGitHubToken, []string{"expiry"}, nil, 100000, false); err != nil {
		return fmt.Errorf("failed to register GitHub storage index: %w", err)
	}
	if err := initializer.RegisterStorageIndexFilter(StorageIndexGitHubToken, func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, write *runtime.StorageWrite) bool {
		// Only index entries with a valid expiry field
		var token oauth2.Token
		if err := json.Unmarshal([]byte(write.Value), &token); err != nil {
			return false
		}
		return token.Expiry.After(time.Now())
	}); err != nil {
		return fmt.Errorf("failed to register GitHub storage index filter: %w", err)
	}

	// Register the before authenticate hook
	if err := initializer.RegisterBeforeAuthenticateCustom(BeforeAuthenticateCustom); err != nil {
		return fmt.Errorf("failed to register BeforeAuthenticateCustom hook: %w", err)
	}
	// Register the before link device hook
	if err := initializer.RegisterBeforeLinkDevice(BeforeLinkDevice); err != nil {
		return fmt.Errorf("failed to register BeforeLinkDevice hook: %w", err)
	}
	// Register the before unlink device hook
	if err := initializer.RegisterBeforeUnlinkDevice(BeforeUnlinkDevice); err != nil {
		return fmt.Errorf("failed to register BeforeUnlinkDevice hook: %w", err)
	}
	if err := initializer.RegisterRpc("github_status", RpcGetGitHubStatus); err != nil {
		return err
	}
	return nil
}

// BeforeAuthenticateCustom handles Discord and GitHub OAuth2 login for Nakama custom auth.
func BeforeAuthenticateCustom(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.AuthenticateCustomRequest) (*api.AuthenticateCustomRequest, error) {
	env := ctx.Value(runtime.RUNTIME_CTX_ENV).(map[string]string)
	discordClientID := env["DISCORD_CLIENT_ID"]
	discordClientSecret := env["DISCORD_CLIENT_SECRET"]
	discordRedirectURL := env["DISCORD_REDIRECT_URI"]

	// clear the user provided vars
	in.Account.Vars = map[string]string{}

	code := in.Account.Id
	// Require a prefix for device IDs to avoid conflicts
	switch {
	case strings.HasPrefix(code, DiscordAuthPrefix) && discordClientID != "" && discordClientSecret != "":
		code = strings.TrimPrefix(code, DiscordAuthPrefix)

		// Try Discord authentication
		token, err := ExchangeDiscordCodeForToken(ctx, logger, discordRedirectURL, discordClientID, discordClientSecret, code)
		if err != nil {
			return nil, fmt.Errorf("discord authentication failed: %w", err)
		}
		// Discord authentication successful
		user, err := GetDiscordUserInfo(ctx, token.AccessToken)
		if err != nil {
			return nil, err
		}

		// Set user ID and username for Nakama
		in.Account.Id = user.ID
		in.Username = user.Username

		// Get the User ID for this user
		users, err := nk.UsersGetUsername(ctx, []string{user.Username})
		if err != nil {
			return nil, fmt.Errorf("failed to get user by username: %w", err)
		}
		var userID string
		if len(users) > 0 {
			userID = users[0].Id
		}

		// Store Discord tokens securely
		err = StoreDiscordTokens(ctx, nk, userID, token)
		if err != nil {
			return nil, fmt.Errorf("failed to store Discord tokens: %w", err)
		}

		vars, err := setUpSessionVars(ctx, db, userID)
		if err != nil {
			return nil, err
		}
		in.Account.Vars = vars
		return in, nil
	default:
		return nil, fmt.Errorf("invalid code prefix, must start with %s or %s", DiscordAuthPrefix, GithubAuthPrefix)
	}
}

func BeforeLinkDevice(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.AccountDevice) (*api.AccountDevice, error) {
	env := ctx.Value(runtime.RUNTIME_CTX_ENV).(map[string]string)
	githubClientID := env["GITHUB_CLIENT_ID"]
	githubClientSecret := env["GITHUB_CLIENT_SECRET"]
	githubRedirectURL := env["GITHUB_REDIRECT_URI"]
	userID := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if githubClientID == "" || githubClientSecret == "" {
		return nil, fmt.Errorf("GitHub OAuth2 not configured on server")
	}
	if userID == "" {
		return nil, fmt.Errorf("user ID not found in context")
	}

	// Clear the session vars and set any needed ones
	in.Vars = map[string]string{}

	vars, err := setUpSessionVars(ctx, db, userID)
	if err != nil {
		return nil, err
	}
	in.Vars = vars

	// Require a prefix for device IDs to avoid conflicts
	switch {
	case strings.HasPrefix(in.Id, GithubAuthPrefix):
		// Valid GitHub device code
		code := strings.TrimPrefix(in.Id, GithubAuthPrefix)
		user, token, err := AuthenticateGitHub(ctx, logger, githubRedirectURL, githubClientID, githubClientSecret, code)
		if err != nil {
			return nil, fmt.Errorf("GitHub authentication failed: %w", err)
		}

		// Store the tokens securely (optional, depending on your use case)
		err = StoreGitHubToken(ctx, nk, userID, token)
		if err != nil {
			logger.WithField("err", err).Warn("Failed to store GitHub tokens")
		}

		// Store the profile securely (optional, depending on your use case)
		err = StoreGitHubProfile(ctx, nk, userID, user)
		if err != nil {
			logger.WithField("err", err).Warn("Failed to store GitHub profile")
		}

		in.Id = GithubAuthPrefix + fmt.Sprintf("%d", user.ID)
	default:
		return nil, fmt.Errorf("invalid device ID, must start with a valid prefix")
	}

	return in, nil
}

func BeforeUnlinkDevice(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.AccountDevice) (*api.AccountDevice, error) {
	// Limit unlinking to only GitHub for now
	switch {
	case strings.HasPrefix(in.Id, GithubAuthPrefix):
		// Valid GitHub device code
	default:
		return nil, fmt.Errorf("invalid device ID")
	}
	// Clear the session vars and set any needed ones
	in.Vars = map[string]string{}
	return in, nil
}

package socialauth

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

func InitializeSocialAuth(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) error {
	s := NewSocialAuthHandler(ctx, logger, db, nk, initializer)

	if err := s.InitializeSteamOpenID(ctx, logger, db, nk, initializer); err != nil {
		return err
	}

	if err := s.InitializeDiscordOAuth2(ctx, logger, db, nk, initializer); err != nil {
		return err
	}
	return nil
}

type SocialAuthHandler struct {
	ctx        context.Context
	logger     runtime.Logger
	db         *sql.DB
	nk         runtime.NakamaModule
	stateStore sync.Map

	discordClientID     string
	discordClientSecret string
	discordRedirectURI  string
	// Set this to your public callback URL in production
	steamCallbackURL string

	sessionEncryptionKey string
}

func NewSocialAuthHandler(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) *SocialAuthHandler {
	env := ctx.Value(runtime.RUNTIME_CTX_ENV).(map[string]string)

	discordClientID := env["DISCORD_CLIENT_ID"]
	discordClientSecret := env["DISCORD_CLIENT_SECRET"]
	discordRedirectURI := env["DISCORD_REDIRECT_URI"]
	steamCallbackURL := env["STEAM_CALLBACK_URL"]
	sessionEncryptionKey := env["SESSION_ENCRYPTION_KEY"]

	if discordClientID == "" || discordClientSecret == "" || discordRedirectURI == "" {
		logger.Warn("Discord OAuth2 environment variables are not set. Discord authentication will not be available.")
	}

	return &SocialAuthHandler{
		ctx:                  ctx,
		logger:               logger,
		db:                   db,
		nk:                   nk,
		discordClientID:      discordClientID,
		discordClientSecret:  discordClientSecret,
		discordRedirectURI:   discordRedirectURI,
		steamCallbackURL:     steamCallbackURL,
		sessionEncryptionKey: sessionEncryptionKey,
	}
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

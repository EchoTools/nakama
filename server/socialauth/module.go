package socialauth

import (
	"context"
	"database/sql"

	"github.com/heroiclabs/nakama-common/runtime"
)

func InitModule(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) error {
	// Initialize social authentication system
	if err := InitializeSocialAuth(ctx, logger, initializer); err != nil {
		return err
	}

	logger.Info("Social authentication module initialized successfully")
	return nil
}

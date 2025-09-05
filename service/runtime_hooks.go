package service

import (
	"context"
	"database/sql"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

func registerHooks(initializer runtime.Initializer) {

	initializer.RegisterAfterAuthenticateDevice(func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, out *api.Session, in *api.AuthenticateDeviceRequest) error {
		// Give the player 10 coins for logging in
		userId, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
		if !ok {
			return runtime.NewError("error getting userId", 13)
		}

		changeset := map[string]int64{
			"coins": 10,
		}

		nk.WalletUpdate(ctx, userId, changeset, nil, true)
		return nil
	})
}

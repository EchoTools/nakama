package main

import (
	"context"
	"database/sql"

	"github.com/echotools/nakama/v3/service"
	"github.com/heroiclabs/nakama-common/runtime"
)

func InitModule(ctx context.Context, runtimeLogger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) error {

	return service.InitModule(ctx, runtimeLogger, db, nk, initializer)
}

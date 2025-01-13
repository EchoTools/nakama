package server

import (
	"context"
	"database/sql"

	"github.com/heroiclabs/nakama-common/runtime"
)

type APIServer struct{}

func (s *APIServer) RPCHandler(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	// Unmarshal the request payload.
	return "", nil
}

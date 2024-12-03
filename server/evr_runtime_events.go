package server

import (
	"context"
	"database/sql"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

func afterUpdateAccount(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.UpdateAccountRequest) error {
	evt := &api.Event{
		Name:       "account_updated",
		Properties: map[string]string{},
		External:   false,
	}
	return nk.Event(context.Background(), evt)
}

func processEvent(ctx context.Context, logger runtime.Logger, evt *api.Event) {
	switch evt.GetName() {
	case "account_updated":
		logger.Debug("process evt: %+v", evt)
		// Send event to an analytics service.
	default:
		logger.Error("unrecognised evt: %+v", evt)
	}
}

func eventSessionEnd(ctx context.Context, logger runtime.Logger, evt *api.Event) {
	logger.Debug("process event session end: %+v", evt)
}

func eventSessionStart(ctx context.Context, logger runtime.Logger, evt *api.Event) {
	logger.Debug("process event session start: %+v", evt)
}

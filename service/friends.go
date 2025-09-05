package service

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server"
	"go.uber.org/zap"
)

func ListPlayerFriends(ctx context.Context, logger *zap.Logger, db *sql.DB, statusRegistry server.StatusRegistry, userID uuid.UUID) ([]*api.Friend, error) {

	cursor := ""
	friends := make([]*api.Friend, 0)
	for {
		users, err := server.ListFriends(ctx, logger, db, statusRegistry, userID, 100, nil, cursor)
		if err != nil {
			return nil, fmt.Errorf("failed to list friends: %w", err)
		}

		friends = append(friends, users.Friends...)

		cursor = users.Cursor
		if users.Cursor == "" {
			break
		}
	}
	return friends, nil
}

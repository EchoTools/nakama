package service

import (
	"context"
	"database/sql"

	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	StorageCollectionGameFiles = "GameFiles"
	uuidNilStr                 = "00000000-0000-0000-0000-000000000000"
)

func StorageLoadingTipsRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	// Retrieve the file from storage
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: StorageCollectionGameFiles,
			Key:        "loading_tips.json",
			UserID:     uuidNilStr,
		},
	})
	if err != nil {
		logger.Error("Error reading loading tips: %v", err)
		return "", err
	}

	if len(objs) == 0 {
		logger.Warn("No loading tips found")
		return "", runtime.NewError("No loading tips found", StatusNotFound)
	}

	return objs[0].Value, nil
}

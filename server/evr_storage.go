package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Storable interface {
	GetStorageID() StorageID
}

type StorageID struct {
	Collection string
	Key        string
}

func (s StorageID) String() string {
	return fmt.Sprintf("%s:%s", s.Collection, s.Key)
}
func LoadFromStorage(ctx context.Context, nk runtime.NakamaModule, userID string, dst Storable, create bool) error {
	storageID := dst.GetStorageID()

	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: storageID.Collection,
			Key:        storageID.Key,
			UserID:     userID,
		},
	})
	if err != nil {
		return status.Errorf(codes.Internal, "failed to read %s/%s: %s", userID, storageID.String(), err)
	}

	if len(objs) == 0 {
		if create {
			if err := StoreToStorage(ctx, nk, userID, dst); err != nil {
				return status.Errorf(codes.Internal, "failed to create %s/%s: %s", userID, storageID.String(), err)
			}

		} else {
			return status.Errorf(codes.NotFound, "no %s/%s found", userID, storageID.String())
		}
	}

	if err = json.Unmarshal([]byte(objs[0].Value), dst); err != nil {
		return status.Errorf(codes.Internal, "failed to unmarshal %s/%s: %s", userID, storageID.String(), err)
	}

	return nil
}

func StoreToStorage(ctx context.Context, nk runtime.NakamaModule, userID string, src Storable) error {
	storageID := src.GetStorageID()
	data, err := json.Marshal(src)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to marshal %s/%s: %w", userID, storageID.String(), err)
	}

	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			UserID:          userID,
			Collection:      storageID.Collection,
			Key:             storageID.Key,
			Value:           string(data),
			PermissionRead:  0,
			PermissionWrite: 0,
		},
	})
	if err != nil {
		return status.Errorf(codes.Internal, "failed to write %s/%s: %w", userID, storageID.String(), err)
	}
	return nil
}

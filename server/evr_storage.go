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
	StorageID() StorageID
}

type IndexedStorable interface {
	Storable
	StorageIndex() *StorageIndexMeta
}

// initializer.StorageIndex
type StorageIndexMeta struct {
	Name           string
	Collection     string
	Key            string
	Fields         []string
	SortableFields []string
	MaxEntries     int
	IndexOnly      bool
}

type StorageID struct {
	Collection string
	Key        string
}

func (s StorageID) String() string {
	return fmt.Sprintf("%s:%s", s.Collection, s.Key)
}
func StorageRead(ctx context.Context, nk runtime.NakamaModule, userID string, dst Storable, create bool) (string, error) {
	if dst == nil {
		return "", status.Errorf(codes.InvalidArgument, "dst is nil")
	}
	storageID := dst.StorageID()

	var version string
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: storageID.Collection,
			Key:        storageID.Key,
			UserID:     userID,
		},
	})
	if err != nil {
		return "", status.Errorf(codes.Internal, "failed to read %s/%s: %s", userID, storageID.String(), err)
	}

	var data string
	if len(objs) != 0 {
		data = objs[0].Value
		version = objs[0].Version
		if err = json.Unmarshal([]byte(data), dst); err != nil {
			return "", status.Errorf(codes.Internal, "failed to unmarshal %s/%s: %s", userID, storageID.String(), err)
		}
	} else {
		if create {
			if version, err = StorageWrite(ctx, nk, userID, dst); err != nil {
				return "", status.Errorf(codes.Internal, "failed to create %s/%s: %s", userID, storageID.String(), err)
			}
		} else {
			return "", status.Errorf(codes.NotFound, "no %s/%s found", userID, storageID.String())
		}
	}

	return version, nil
}

func StorageWrite(ctx context.Context, nk runtime.NakamaModule, userID string, src Storable) (string, error) {
	storageID := src.StorageID()
	data, err := json.Marshal(src)
	if err != nil {
		return "", status.Errorf(codes.Internal, "failed to marshal %s/%s: %s", userID, storageID.String(), err.Error())
	}

	acks, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{
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
		return "", status.Errorf(codes.Internal, "failed to write %s/%s: %s", userID, storageID.String(), err.Error())
	}
	return acks[0].Version, nil
}

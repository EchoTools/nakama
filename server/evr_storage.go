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
	StorageMeta() StorageMeta
}

type IndexedStorable interface {
	Storable
	StorageIndex() *StorageIndexMeta
}

type VersionedStorable interface {
	Storable
	SetStorageVersion(version string)
}

type IndexedVersionedStorable interface {
	IndexedStorable
	VersionedStorable
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

type StorageMeta struct {
	Collection      string
	Key             string
	PermissionRead  int
	PermissionWrite int
	Version         string
}

func (s StorageMeta) String() string {
	return fmt.Sprintf("%s:%s", s.Collection, s.Key)
}
func StorageRead(ctx context.Context, nk runtime.NakamaModule, userID string, dst Storable, create bool) error {
	if dst == nil {
		return status.Errorf(codes.InvalidArgument, "dst is nil")
	}
	storageID := dst.StorageMeta()

	var version string
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

	if len(objs) != 0 {

		if err = json.Unmarshal([]byte(objs[0].GetValue()), dst); err != nil {
			return status.Errorf(codes.Internal, "failed to unmarshal %s/%s: %s", userID, storageID.String(), err)
		}

		version = objs[0].GetVersion()

	} else {
		if create {

			// Ensure that the object does not exist
			if obj, ok := dst.(VersionedStorable); ok {
				obj.SetStorageVersion("*")
			}

			if version, err = StorageWrite(ctx, nk, userID, dst); err != nil {
				return status.Errorf(codes.Internal, "failed to create %s/%s: %s", userID, storageID.String(), err)
			}

		} else {
			return status.Errorf(codes.NotFound, "no %s/%s found", userID, storageID.String())
		}
	}

	// Set the new object version
	if obj, ok := dst.(VersionedStorable); ok {
		obj.SetStorageVersion(version)
	}

	return nil
}

func StorageWrite(ctx context.Context, nk runtime.NakamaModule, userID string, src Storable) (string, error) {
	meta := src.StorageMeta()
	data, err := json.Marshal(src)
	if err != nil {
		return "", status.Errorf(codes.Internal, "failed to marshal %s/%s: %s", userID, meta.String(), err.Error())
	}

	acks, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:      meta.Collection,
			Key:             meta.Key,
			UserID:          userID,
			Value:           string(data),
			Version:         meta.Version,
			PermissionRead:  meta.PermissionRead,
			PermissionWrite: meta.PermissionWrite,
		},
	})
	if err != nil {
		return "", status.Errorf(codes.Internal, "failed to write %s/%s: %s", userID, meta.String(), err.Error())
	}

	if obj, ok := src.(VersionedStorable); ok {
		obj.SetStorageVersion(acks[0].GetVersion())
	}

	return acks[0].Version, nil
}

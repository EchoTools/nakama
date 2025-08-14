package server

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// DEPRECATED: Legacy storage interfaces and functions.
// Use StorableAdapter from evr_storable.go instead.

// StorageIndexMeta is kept for compatibility with remaining old interface usage
type StorageIndexMeta struct {
	Name           string
	Collection     string
	Key            string
	Fields         []string
	SortableFields []string
	MaxEntries     int
	IndexOnly      bool
}

// StorageMeta is kept for compatibility with remaining old interface usage
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

// DEPRECATED: These interfaces are deprecated. Use StorableAdapter instead.
type Storable interface {
	StorageMeta() StorageMeta
}

type IndexedStorable interface {
	Storable
	StorageIndexes() []StorageIndexMeta
}

type VersionedStorable interface {
	Storable
	SetStorageVersion(userID string, version string)
}

type IndexedVersionedStorable interface {
	IndexedStorable
	VersionedStorable
}

// DEPRECATED: These functions are deprecated. Use StorableRead/StorableWrite from evr_storable.go instead.
// They are kept temporarily for compatibility with remaining old interface usage.

func StorageRead(ctx context.Context, nk runtime.NakamaModule, userID string, dst Storable, create bool) error {
	if dst == nil {
		return status.Errorf(codes.InvalidArgument, "dst is nil")
	}

	// Check if the object is a pointer.
	if dstValue := reflect.ValueOf(dst); dstValue.Kind() != reflect.Ptr {
		return status.Errorf(codes.InvalidArgument, "dst is not a pointer")
	}

	meta := dst.StorageMeta()

	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: meta.Collection,
			Key:        meta.Key,
			UserID:     userID,
		},
	})
	if err != nil {
		return status.Errorf(codes.Internal, "failed to read %s/%s: %v", userID, meta.String(), err)
	}

	if len(objs) != 0 {

		if err = json.Unmarshal([]byte(objs[0].GetValue()), dst); err != nil {
			return status.Errorf(codes.Internal, "failed to unmarshal %s/%s: %v", userID, meta.String(), err)
		}

		// Ensure that the object does not exist
		if obj, ok := dst.(VersionedStorable); ok {
			obj.SetStorageVersion(userID, objs[0].GetVersion())
		}

	} else {
		if create {

			// Ensure that the object does not exist
			if obj, ok := dst.(VersionedStorable); ok {
				obj.SetStorageVersion(userID, "*")
			}

			if err = StorageWrite(ctx, nk, userID, dst); err != nil {
				return status.Errorf(codes.Internal, "failed to create %s/%s: %v", userID, meta.String(), err)
			}

		} else {
			return status.Errorf(codes.NotFound, "no %s/%s found", userID, meta.String())
		}
	}

	return nil
}

func StorageWrite(ctx context.Context, nk runtime.NakamaModule, userID string, src Storable) error {
	meta := src.StorageMeta()
	data, err := json.Marshal(src)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to marshal %s/%s: %s", userID, meta.String(), err.Error())
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
		return status.Errorf(codes.Internal, "failed to write %s/%s: %v", userID, meta.String(), err.Error())
	}

	if obj, ok := src.(VersionedStorable); ok {
		obj.SetStorageVersion(userID, acks[0].GetVersion())
	}

	return nil
}

package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// StorageObjectAdapter defines methods for converting to a storage object.
type StorableAdapter interface {
	StorageMeta() StorableMetadata
	SetStorageMeta(meta StorableMetadata)
}

// StorableMetadata defines the metadata for a storable object.
type StorableMetadata struct {
	UserID          string
	Collection      string
	Key             string
	PermissionRead  int
	PermissionWrite int
	Version         string
}

// String returns a string representation of the metadata path
func (s StorableMetadata) String() string {
	return fmt.Sprintf("%s/%s/%s/%s", s.UserID, s.Collection, s.Key, s.Version)
}

// Storable defines the interface for objects that can be indexed within the storage system.
type StorableIndexer interface {
	StorageIndexes() []StorableIndexMeta
}

// StorableIndexMeta defines the metadata for an index on a storable object. (initializer.StorageIndex)
type StorableIndexMeta struct {
	Name           string
	Collection     string
	Key            string
	Fields         []string
	SortableFields []string
	MaxEntries     int
	IndexOnly      bool
}

func storableErrorf(m StorableMetadata, c codes.Code, format string, a ...any) error {
	return fmt.Errorf("storable error on %s/%s/%s/%s: %v", m.UserID, m.Collection, m.Key, m.Version, status.Errorf(c, format, a...))
}

func StorableReadNk(ctx context.Context, nk runtime.NakamaModule, userID string, dst StorableAdapter, create bool) error {
	// Validate the destination object.
	if dst == nil {
		return status.Error(codes.InvalidArgument, "dst is nil")
	} else if dstValue := reflect.ValueOf(dst); dstValue.Kind() != reflect.Ptr {
		return status.Error(codes.InvalidArgument, "dst is not a pointer")
	}
	if userID == "" {
		return status.Error(codes.InvalidArgument, "userID is empty")
	}
	meta := dst.StorageMeta()
	meta.UserID = userID
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: meta.Collection,
		Key:        meta.Key,
		UserID:     meta.UserID,
	}})
	if err != nil {
		return storableErrorf(meta, codes.Internal, "failed to read: %v", err)
	}
	switch len(objs) {
	case 0:
		// No objects found
		if create {
			meta.Version = "*"                           // Disallow overwriting existing objects.
			return StorableWriteNk(ctx, nk, userID, dst) // Attempt to write the object if it doesn't exist.
		}
		return status.Errorf(codes.NotFound, "no %s/%s found", userID, meta.String())
	case 1:
		// One object found, proceed to unmarshal.
		if err = json.Unmarshal([]byte(objs[0].Value), dst); err != nil {
			return storableErrorf(meta, codes.Internal, "failed to unmarshal: %v", err)
		}
		meta.Version = objs[0].GetVersion()
		meta.PermissionRead = int(objs[0].GetPermissionRead())
		meta.PermissionWrite = int(objs[0].GetPermissionWrite())
		dst.SetStorageMeta(meta)
		return nil
	default:
		// More than one object found, which is unexpected.
		return storableErrorf(meta, codes.Internal, "multiple objects returned")
	}
}

func StorableWriteNk(ctx context.Context, nk runtime.NakamaModule, userID string, src StorableAdapter) error {
	meta := src.StorageMeta()
	meta.UserID = userID
	data, err := json.Marshal(src)
	if err != nil {
		return storableErrorf(meta, codes.Internal, "failed to marshal: %v", err)
	}
	if acks, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      meta.Collection,
		Key:             meta.Key,
		UserID:          meta.UserID,
		Value:           string(data),
		Version:         meta.Version,
		PermissionRead:  meta.PermissionRead,
		PermissionWrite: meta.PermissionWrite,
	}}); err != nil {
		return storableErrorf(meta, codes.Internal, "failed to write: %v", err)
	} else if len(acks) > 0 {
		// Update the metadata with the version from the write acknowledgment.
		meta.Version = acks[0].GetVersion()
		src.SetStorageMeta(meta)
	}
	return nil
}

func StorableRead(ctx context.Context, logger *zap.Logger, db *sql.DB, storageIndex server.StorageIndex, metrics server.Metrics, userID string, dst StorableAdapter, create bool) error {
	// Validate the destination object.
	if dst == nil {
		return status.Error(codes.InvalidArgument, "dst is nil")
	} else if dstValue := reflect.ValueOf(dst); dstValue.Kind() != reflect.Ptr {
		return status.Error(codes.InvalidArgument, "dst is not a pointer")
	}
	if userID == "" {
		return status.Error(codes.InvalidArgument, "userID is empty")
	}
	meta := dst.StorageMeta()
	meta.UserID = userID

	reads := []*api.ReadStorageObjectId{{
		Collection: meta.Collection,
		Key:        meta.Key,
		UserId:     meta.UserID,
	}}

	result, err := server.StorageReadObjects(ctx, logger, db, uuid.Nil, reads)
	if err != nil {
		return err
	}
	objs := result.Objects
	switch len(objs) {
	case 0:
		// No objects found
		if create {
			meta.Version = "*"                                                        // Disallow overwriting existing objects.
			return StorableWrite(ctx, logger, db, storageIndex, metrics, userID, dst) // Attempt to write the object if it doesn't exist.
		}
		return status.Errorf(codes.NotFound, "no %s/%s found", userID, meta.String())
	case 1:
		// One object found, proceed to unmarshal.
		if err = json.Unmarshal([]byte(objs[0].Value), dst); err != nil {
			return storableErrorf(meta, codes.Internal, "failed to unmarshal: %v", err)
		}
		meta.Version = objs[0].GetVersion()
		meta.PermissionRead = int(objs[0].GetPermissionRead())
		meta.PermissionWrite = int(objs[0].GetPermissionWrite())
		dst.SetStorageMeta(meta)
		return nil
	default:
		// More than one object found, which is unexpected.
		return storableErrorf(meta, codes.Internal, "multiple objects returned")
	}
}

func StorableWrite(ctx context.Context, logger *zap.Logger, db *sql.DB, storageIndex server.StorageIndex, metrics server.Metrics, userID string, src StorableAdapter) error {
	if userID == "" {
		return status.Error(codes.InvalidArgument, "userID is empty")
	}

	meta := src.StorageMeta()
	meta.UserID = userID
	data, err := json.Marshal(src)
	if err != nil {
		return storableErrorf(meta, codes.Internal, "failed to marshal: %v", err)
	}

	ops := []*server.StorageOpWrite{{
		OwnerID: userID,
		Object: &api.WriteStorageObject{
			Collection:      meta.Collection,
			Key:             meta.Key,
			Value:           string(data),
			Version:         meta.Version,
			PermissionRead:  &wrapperspb.Int32Value{Value: int32(meta.PermissionRead)},
			PermissionWrite: &wrapperspb.Int32Value{Value: int32(meta.PermissionWrite)},
		},
	}}

	acks, _, err := server.StorageWriteObjects(ctx, logger, db, metrics, storageIndex, true, ops)
	if err != nil {
		return err
	} else if len(acks.Acks) > 0 {
		// Update the metadata with the version from the write acknowledgment.
		meta.Version = acks.Acks[0].GetVersion()
		src.SetStorageMeta(meta)
	}

	return nil
}

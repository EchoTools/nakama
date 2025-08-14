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
	StorableAdapter
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

func StorableRead(ctx context.Context, nk runtime.NakamaModule, userID string, dst StorableAdapter, create bool) error {
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
			meta.Version = "*"                 // Disallow overwriting existing objects.
			return StorableWrite(ctx, nk, dst) // Attempt to write the object if it doesn't exist.
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

func StorableWrite(ctx context.Context, nk runtime.NakamaModule, src StorableAdapter) error {
	meta := src.StorageMeta()
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

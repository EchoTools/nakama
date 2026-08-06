package server

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

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
			meta.Version = "*"                         // Disallow overwriting existing objects.
			return StorableWrite(ctx, nk, userID, dst) // Attempt to write the object if it doesn't exist.
		}
		return status.Errorf(codes.NotFound, "no %s/%s found", userID, meta.String())
	case 1:
		// One object found, proceed to unmarshal.
		if err = json.Unmarshal([]byte(objs[0].Value), dst); err != nil {
			if !create {
				return storableErrorf(meta, codes.Internal, "failed to unmarshal: %v", err)
			}
			// Record is corrupted. Delete it and recreate with defaults so the caller recovers.
			meta.Version = objs[0].GetVersion()
			if err := nk.StorageDelete(ctx, []*runtime.StorageDelete{{
				Collection: meta.Collection,
				Key:        meta.Key,
				UserID:     meta.UserID,
				Version:    meta.Version,
			}}); err != nil {
				// Intentionally not returning here; the write below will fail with a version conflict if needed.
				_ = err
			}
			meta.Version = "*" // Disallow overwriting any concurrently-recreated object.
			return StorableWrite(ctx, nk, userID, dst)
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

// StorableWriteMany writes several storables owned by one user in a SINGLE
// nk.MultiUpdate call.
//
// This is not merely a batching convenience -- it is a transaction boundary.
// MultiUpdate runs every account update, storage write and storage delete
// inside one ExecuteInTxPgx (core_multi.go:37-66), so either every object lands
// or none of them do. Two successive StorableWrite calls are two independent
// transactions and CAN half-apply.
//
// Use this whenever two objects must not disagree with each other -- notably an
// authoritative record and a projection derived from it, where a half-applied
// write leaves the projection describing a state that no longer exists.
//
// MultiUpdate is preferred over a multi-object nk.StorageWrite even though both
// are a single transaction today, because MultiUpdate is the one entry point
// that can also carry account updates and deletes in that same transaction.
// Callers that later need to widen the atomic unit do not have to change shape.
//
// Version conflicts stay detectable: MultiUpdate returns
// runtime.ErrStorageRejectedVersion, whose text carries the "version check
// failed" substring that isVersionConflictError matches on
// (evr_server_profile_storage.go:354), and the wrapper below preserves it.
//
// Acks come back in input order (storageWriteObjects assigns them by the
// original index at core_storage.go:686), so versions are propagated back to
// the matching source object.
func StorableWriteMany(ctx context.Context, nk runtime.NakamaModule, userID string, srcs ...StorableAdapter) error {
	if len(srcs) == 0 {
		return nil
	}

	ops := make([]*runtime.StorageWrite, 0, len(srcs))
	metas := make([]StorableMetadata, 0, len(srcs))
	for _, src := range srcs {
		meta := src.StorageMeta()
		meta.UserID = userID
		data, err := json.Marshal(src)
		if err != nil {
			return storableErrorf(meta, codes.Internal, "failed to marshal: %v", err)
		}
		ops = append(ops, &runtime.StorageWrite{
			Collection:      meta.Collection,
			Key:             meta.Key,
			UserID:          meta.UserID,
			Value:           string(data),
			Version:         meta.Version,
			PermissionRead:  meta.PermissionRead,
			PermissionWrite: meta.PermissionWrite,
		})
		metas = append(metas, meta)
	}

	// Storage writes only: no account updates, no deletes, no wallet updates.
	acks, _, err := nk.MultiUpdate(ctx, nil, ops, nil, nil, false)
	if err != nil {
		// Name every object in the batch: the caller needs to know that NONE of
		// them were applied, not just the one that happened to be rejected.
		paths := make([]string, 0, len(metas))
		for _, m := range metas {
			paths = append(paths, m.String())
		}
		return storableErrorf(metas[0], codes.Internal,
			"atomic write of %d objects failed, none applied (%s): %v", len(ops), strings.Join(paths, ", "), err)
	}

	for i, ack := range acks {
		if i >= len(srcs) {
			break
		}
		meta := metas[i]
		meta.Version = ack.GetVersion()
		srcs[i].SetStorageMeta(meta)
	}
	return nil
}

func StorableWrite(ctx context.Context, nk runtime.NakamaModule, userID string, src StorableAdapter) error {
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

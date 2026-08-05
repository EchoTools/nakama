package server

import (
	"context"
	"encoding/json"
	"errors"
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

// storableError carries a gRPC status code alongside the underlying cause,
// keeping that cause reachable through errors.Is/errors.As. Formatting the
// cause away (as `%v` inside a fmt.Errorf/status.Errorf) makes sentinels such
// as runtime.ErrStorageRejectedVersion undetectable and forces callers into
// substring matching on error text.
type storableError struct {
	code codes.Code
	msg  string
	err  error
}

func (e *storableError) Error() string { return e.msg }

func (e *storableError) Unwrap() error { return e.err }

// GRPCStatus lets status.Code/status.FromError recover the code.
func (e *storableError) GRPCStatus() *status.Status { return status.New(e.code, e.msg) }

// storableErrorf builds a storage error for m. The format string is evaluated
// with fmt.Errorf, so a `%w` verb keeps the underlying storage error in the
// chain — callers can then use errors.Is(err, runtime.ErrStorageRejectedVersion).
func storableErrorf(m StorableMetadata, c codes.Code, format string, a ...any) error {
	cause := fmt.Errorf(format, a...)
	return &storableError{
		code: c,
		msg:  fmt.Sprintf("storable error on %s/%s/%s/%s: %s", m.UserID, m.Collection, m.Key, m.Version, cause.Error()),
		err:  errors.Unwrap(cause),
	}
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
		return storableErrorf(meta, codes.Internal, "failed to read: %w", err)
	}
	switch len(objs) {
	case 0:
		// No objects found
		if create {
			return storableCreate(ctx, nk, userID, dst) // Attempt to write the object if it doesn't exist.
		}
		return status.Errorf(codes.NotFound, "no %s/%s found", userID, meta.String())
	case 1:
		// One object found, proceed to unmarshal.
		if err = json.Unmarshal([]byte(objs[0].Value), dst); err != nil {
			if !create {
				return storableErrorf(meta, codes.Internal, "failed to unmarshal: %w", err)
			}
			// Record is corrupted. Delete it and recreate with defaults so the caller recovers.
			meta.Version = objs[0].GetVersion()
			if err := nk.StorageDelete(ctx, []*runtime.StorageDelete{{
				Collection: meta.Collection,
				Key:        meta.Key,
				UserID:     meta.UserID,
				Version:    meta.Version,
			}}); err != nil {
				// Intentionally not returning here; the create below is version-guarded
				// and will adopt whatever object replaced the corrupt one.
				_ = err
			}
			return storableCreate(ctx, nk, userID, dst)
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

// storableCreate writes dst as a brand new object under the create-only version
// "*", so it can never overwrite an object that another writer created between
// the caller's read and this write. If the create loses that race, the winner's
// object is read back into dst — leaving StorableRead(create=true) with
// get-or-create semantics rather than read-or-clobber.
//
// The version must be forced onto the wire here: StorableWrite re-derives its
// metadata from src.StorageMeta(), so mutating a local StorableMetadata copy has
// no effect on the write.
func storableCreate(ctx context.Context, nk runtime.NakamaModule, userID string, dst StorableAdapter) error {
	err := storableWriteVersion(ctx, nk, userID, dst, "*")
	if err == nil {
		return nil
	}
	if !errors.Is(err, runtime.ErrStorageRejectedVersion) {
		return err
	}
	// Lost the create race: adopt the concurrent winner's object.
	return StorableRead(ctx, nk, userID, dst, false)
}

func StorableWrite(ctx context.Context, nk runtime.NakamaModule, userID string, src StorableAdapter) error {
	return storableWriteVersion(ctx, nk, userID, src, src.StorageMeta().Version)
}

// storableWriteVersion writes src under an explicit storage version, which
// StorableWrite fills in from src's own metadata.
func storableWriteVersion(ctx context.Context, nk runtime.NakamaModule, userID string, src StorableAdapter, version string) error {
	meta := src.StorageMeta()
	meta.UserID = userID
	meta.Version = version
	data, err := json.Marshal(src)
	if err != nil {
		return storableErrorf(meta, codes.Internal, "failed to marshal: %w", err)
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
		return storableErrorf(meta, codes.Internal, "failed to write: %w", err)
	} else if len(acks) > 0 {
		// Update the metadata with the version from the write acknowledgment.
		meta.Version = acks[0].GetVersion()
		src.SetStorageMeta(meta)
	}
	return nil
}

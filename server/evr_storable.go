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
			// Record is corrupted for this type. Recover by replacing it with
			// dst's defaults — without ever destroying an object that another
			// writer put there in the meantime.
			//
			// The delete is version-guarded, so it lands only while the corrupt
			// record is still the current object, and its outcome selects the
			// guard for the follow-up write:
			//
			//   - delete landed: the object is gone, so write create-only ("*").
			//     A concurrent recreate wins that race and is adopted into dst.
			//   - delete did not land: either the record was replaced (its
			//     version moved on) or storage failed transiently and the
			//     corrupt record is still sitting there. Write guarded on the
			//     corrupt record's own version, which succeeds only in the
			//     second case — precisely the case this recovery path exists
			//     for. A rejection means someone else owns the object now, so
			//     fall through to the create-only path and adopt their object
			//     instead of clobbering it.
			//
			// Writing unconditionally here (as this path did before) recovers
			// the same corrupt record but also silently overwrites a healthy
			// concurrent replacement; refusing to write at all (create-only in
			// both branches) leaves the corrupt record in place forever and
			// turns every later StorableRead(create=true) for this user into a
			// hard "failed to unmarshal".
			corruptVersion := objs[0].GetVersion()
			meta.Version = corruptVersion
			if derr := nk.StorageDelete(ctx, []*runtime.StorageDelete{{
				Collection: meta.Collection,
				Key:        meta.Key,
				UserID:     meta.UserID,
				Version:    corruptVersion,
			}}); derr != nil {
				werr := storableWriteVersion(ctx, nk, userID, dst, corruptVersion)
				if werr == nil {
					return nil
				}
				if !errors.Is(werr, runtime.ErrStorageRejectedVersion) {
					return werr
				}
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

// storableCreateMaxAttempts bounds storableCreate. Two attempts covers the only
// interleaving that needs one: the create loses the race and the winner's object
// is then deleted before the fallback re-read reaches it, leaving nothing stored
// and nothing to adopt. Retrying rather than surfacing NotFound keeps
// StorableRead(create=true) honest — it is a get-or-create.
const storableCreateMaxAttempts = 2

// storableCreate writes dst as a brand new object under the create-only version
// "*", so it can never overwrite an object that another writer created between
// the caller's read and this write. If the create loses that race, the winner's
// object is read back into dst — leaving StorableRead(create=true) with
// get-or-create semantics rather than read-or-clobber.
//
// The version must be forced onto the wire here: StorableWrite re-derives its
// metadata from src.StorageMeta(), so mutating a local StorableMetadata copy has
// no effect on the write.
//
// NOTE: the fallback re-read decodes into the caller's dst without resetting it,
// so dst is a merge target, not a replacement target — encoding/json keeps
// existing keys of a non-nil map and leaves fields absent from the winner's JSON
// untouched. That is the intended shape for the common caller (a
// constructor-fresh dst holding only defaults), but on the corrupt-record path
// dst has already been through a failed json.Unmarshal and may carry residue
// from the corrupt bytes. Resetting dst generically is not safe here: several
// StorableAdapter implementations derive their storage key from their own fields
// (GuildGroupState.StorageMeta uses s.GroupID) or embed a mutex
// (LatencyHistory), so neither zeroing nor wholesale assignment is sound without
// a per-type hook. Tracked as a follow-up.
func storableCreate(ctx context.Context, nk runtime.NakamaModule, userID string, dst StorableAdapter) error {
	var lastErr error
	for attempt := 0; attempt < storableCreateMaxAttempts; attempt++ {
		err := storableWriteVersion(ctx, nk, userID, dst, "*")
		if err == nil {
			return nil
		}
		if !errors.Is(err, runtime.ErrStorageRejectedVersion) {
			return err
		}
		// Lost the create race: adopt the concurrent winner's object.
		rerr := StorableRead(ctx, nk, userID, dst, false)
		if rerr == nil {
			return nil
		}
		if status.Code(rerr) != codes.NotFound {
			return rerr
		}
		// The winner's object is already gone. Nothing is stored, so the
		// create can simply be retried.
		lastErr = rerr
	}
	return lastErr
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

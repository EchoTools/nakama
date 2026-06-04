package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// storableMaxWriteAttempts bounds StorableWriteWithRetry so a persistently
	// contended object cannot spin forever. Includes the initial attempt.
	storableMaxWriteAttempts = 5
	// storableRetryBaseBackoff is the base unit of jittered backoff between
	// retry attempts. Kept small: contention here is brief (per-user object).
	storableRetryBaseBackoff = 5 * time.Millisecond
)

// isStorableVersionConflict reports whether err is a storage optimistic-
// concurrency (version-check) conflict, as opposed to any other failure.
//
// We match on the version-conflict signature specifically. Nakama's
// StorageWrite returns runtime.ErrStorageRejectedVersion ("Storage write
// rejected - version check failed.") for OCC conflicts; StorableWrite wraps
// that with %v, so the sentinel identity is lost but its message survives.
// Matching the substring keeps detection working through that wrapper and
// mirrors the existing isVersionConflictError helper in this package.
func isStorableVersionConflict(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "version check failed")
}

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

// StorableWriteWithRetry writes src, retrying on optimistic-concurrency
// (version-check) conflicts only. On such a conflict it re-reads the current
// stored object into src (adopting the concurrent winner's version and
// contents), invokes reapply to re-apply this caller's pending mutation onto
// that fresh object, and writes again. This makes concurrent writers lossless:
// neither the winner's nor this caller's new entries are clobbered.
//
// reapply MUST be idempotent across attempts and MUST re-apply the same pending
// mutation onto the (now-refreshed) src each time it is called. It is NOT
// invoked before the first attempt — the caller is responsible for having
// applied its mutation to src once before calling. Any pruning/limit/expiry
// logic that must stay bounded after a merge should live inside reapply.
//
// Only version-conflict errors are retried; any other error (including
// non-conflict storage failures) is returned immediately without re-reading.
// Attempts are bounded by storableMaxWriteAttempts with small jittered backoff.
func StorableWriteWithRetry(ctx context.Context, nk runtime.NakamaModule, userID string, src StorableAdapter, reapply func() error) error {
	var lastErr error
	for attempt := 0; attempt < storableMaxWriteAttempts; attempt++ {
		err := StorableWrite(ctx, nk, userID, src)
		if err == nil {
			return nil
		}
		if !isStorableVersionConflict(err) {
			// Not an OCC conflict: do not re-read or retry.
			return err
		}
		lastErr = err

		// Conflict: a concurrent writer advanced the version. Re-read the fresh
		// object (picking up the winner's version + entries) then re-apply this
		// caller's pending mutation onto it before writing again.
		if rerr := StorableRead(ctx, nk, userID, src, false); rerr != nil {
			return fmt.Errorf("StorableWriteWithRetry: re-read after conflict: %w", rerr)
		}
		if rerr := reapply(); rerr != nil {
			return fmt.Errorf("StorableWriteWithRetry: re-apply after conflict: %w", rerr)
		}

		// Jittered backoff before the next attempt (skip after the final loop).
		if attempt < storableMaxWriteAttempts-1 {
			backoff := time.Duration(attempt+1) * storableRetryBaseBackoff
			// rand is fine here: this is contention jitter, not security.
			jitter := time.Duration(rand.Int63n(int64(storableRetryBaseBackoff)))
			select {
			case <-ctx.Done():
				return fmt.Errorf("StorableWriteWithRetry: %w", ctx.Err())
			case <-time.After(backoff + jitter):
			}
		}
	}
	return fmt.Errorf("StorableWriteWithRetry: exhausted %d attempts: %w", storableMaxWriteAttempts, lastErr)
}

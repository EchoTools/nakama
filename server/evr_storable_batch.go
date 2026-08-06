package server

import (
	"fmt"
	"strings"

	"github.com/heroiclabs/nakama-common/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// storableBatchErrorf reports a failure that belongs to a BATCH rather than to
// any one object in it, naming every object the batch carried.
//
// A rejection on any single object aborts the whole transaction, and the
// storage layer does not say which one it was: storageWriteObjects
// (core_storage.go) holds the offending object when it rejects but returns a
// bare runtime.ErrStorageRejectedVersion / ErrStorageRejectedPermission, and
// StorageWriteObjects passes that sentinel up unadorned. Blaming one member of
// the batch would assert an identity we do not have — and under contention it
// is the wrong one, since the first object listed is not the likeliest loser of
// the race.
//
// Like storableErrorf, the format string is evaluated with fmt.Errorf so a `%w`
// verb keeps the underlying storage error reachable through errors.Is. That is
// load-bearing, not cosmetic: isVersionConflictError is errors.Is-based, so
// formatting the cause away here would make every version conflict on this path
// look like a hard failure and defeat the retry loop in
// SyncJournalAndProfileWithRetry.
//
// It keeps storableErrorf's "storable error on …" prefix so existing log
// searches still find it.
func storableBatchErrorf(metas []StorableMetadata, c codes.Code, format string, a ...any) error {
	ids := make([]string, 0, len(metas))
	for _, m := range metas {
		ids = append(ids, fmt.Sprintf("%s/%s/%s/%s", m.UserID, m.Collection, m.Key, m.Version))
	}
	cause := fmt.Errorf(format, a...)
	return &storableError{
		code: c,
		msg:  fmt.Sprintf("storable error on one of batch [%s]: %v", strings.Join(ids, ", "), status.Error(c, cause.Error())),
		err:  cause,
	}
}

// applyStorageAcks stamps the versions returned by a batch write back onto the
// source storables.
//
// Nakama's own implementation returns acks in request order, but the runtime API
// does not promise it. Match on collection/key so a reordering implementation
// cannot silently stamp the wrong version onto a storable — a wrong version is
// not a cosmetic error, it is an optimistic-concurrency check that will now pass
// when it should have failed.
//
// An ack with no matching meta is ignored rather than treated as an error: the
// caller has already been told the write succeeded, and there is no source
// object to attribute it to.
func applyStorageAcks(acks []*api.StorageObjectAck, metas []StorableMetadata, srcs []StorableAdapter) {
	for _, ack := range acks {
		for i, meta := range metas {
			if i >= len(srcs) {
				break
			}
			if ack.GetCollection() == meta.Collection && ack.GetKey() == meta.Key {
				meta.Version = ack.GetVersion()
				srcs[i].SetStorageMeta(meta)
				break
			}
		}
	}
}

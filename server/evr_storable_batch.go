package server

import (
	"github.com/heroiclabs/nakama-common/api"
)

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

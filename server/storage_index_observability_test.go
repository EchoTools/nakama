package server

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// gaugeCapturingMetrics records the storage-index entry gauge so a test can
// assert on index occupancy without a metrics backend.
type gaugeCapturingMetrics struct {
	Metrics
	gauges map[string]float64
}

func newGaugeCapturingMetrics() *gaugeCapturingMetrics {
	return &gaugeCapturingMetrics{gauges: make(map[string]float64)}
}

func (m *gaugeCapturingMetrics) GaugeStorageIndexEntries(indexName string, value float64) {
	m.gauges[indexName] = value
}

// TestStorageIndexEviction_IsObservable covers the silent-eviction gap.
//
// Above MaxEntries + 10%, Write evicts the oldest documents
// (storage_index.go:161) and Nakama never reloads evicted entries. For an index
// backing any
// kind of negative check that is a fail-OPEN condition: the entry is simply
// absent, and absent is indistinguishable from "no such record".
//
// Eviction was entirely silent -- no log line, and the only signal was a gauge
// that an operator had to already know to watch. This asserts eviction
// announces itself, so the condition can be alarmed rather than discovered.
//
// Database-free: CreateIndex and Write are served from the in-process index.
func TestStorageIndexEviction_IsObservable(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	obsLogger := zap.New(core)
	metrics := newGaugeCapturingMetrics()

	si, err := NewLocalStorageIndex(obsLogger, nil, &StorageConfig{}, metrics)
	require.NoError(t, err)

	const indexName = "test_eviction_observability"
	const maxEntries = 10
	require.NoError(t, si.CreateIndex(context.Background(), indexName, "evict_collection", "key",
		[]string{"n"}, nil, maxEntries, true))

	// Write past MaxEntries * 1.1 so the eviction branch actually runs.
	objs := make([]*api.StorageObject, 0, 20)
	now := time.Now().UTC()
	for i := 0; i < 20; i++ {
		value, err := json.Marshal(map[string]any{"n": i})
		require.NoError(t, err)
		objs = append(objs, &api.StorageObject{
			Collection: "evict_collection",
			Key:        "key",
			UserId:     uuid.Must(uuid.NewV4()).String(),
			Value:      string(value),
			Version:    fmt.Sprintf("v%d", i),
			CreateTime: timestamppb.New(now),
			UpdateTime: timestamppb.New(now.Add(time.Duration(i) * time.Second)),
		})
	}
	si.Write(context.Background(), objs)

	assert.Equal(t, float64(20), metrics.gauges[indexName],
		"the entry gauge must reflect occupancy so headroom is visible")

	evictionLogs := logs.FilterMessageSnippet("evict").All()
	require.NotEmpty(t, evictionLogs,
		"eviction must be logged: entries dropped from a storage index are never reloaded, so silent eviction is a permanent, invisible data loss")

	found := false
	for _, entry := range evictionLogs {
		for _, f := range entry.Context {
			if f.Key == "index_name" && f.String == indexName {
				found = true
			}
		}
	}
	assert.True(t, found, "the eviction log must name the index that lost entries; got %+v", evictionLogs)
}

// TestStorageIndexFieldFilter_IndexOnlyReturnsOnlyFilteredFields is the
// generic counterpart to the SuspensionProfile regression.
//
// Every existing field-filter test in storage_index_test.go listed EVERY key
// of its test value in Fields, so none of them could observe the filter at all.
// This one deliberately omits a key and pins both consequences:
//
//	a field absent from Fields is not RETURNED, and it is not SEARCHABLE.
//
// Both follow from mapIndexStorageFields reducing the value to the filtered map
// (storage_index.go:562-570) before either storing it or walking it for terms.
//
// Database-free: with indexOnly true nothing in this path touches the db.
func TestStorageIndexFieldFilter_IndexOnlyReturnsOnlyFilteredFields(t *testing.T) {
	si, err := NewLocalStorageIndex(logger, nil, &StorageConfig{}, newGaugeCapturingMetrics())
	require.NoError(t, err)

	const indexName = "test_field_filter"
	const collection = "filter_collection"
	const key = "key"

	// "kept" is indexed; "dropped" is not.
	require.NoError(t, si.CreateIndex(context.Background(), indexName, collection, key,
		[]string{"kept"}, nil, 10, true))

	value, err := json.Marshal(map[string]any{"kept": "yes", "dropped": "no"})
	require.NoError(t, err)

	userID := uuid.Must(uuid.NewV4()).String()
	now := time.Now().UTC()
	si.Write(context.Background(), []*api.StorageObject{{
		Collection: collection,
		Key:        key,
		UserId:     userID,
		Value:      string(value),
		Version:    "v1",
		CreateTime: timestamppb.New(now),
		UpdateTime: timestamppb.New(now),
	}})

	entries, _, err := si.List(context.Background(), uuid.Nil, indexName, "+value.kept:yes", 10, nil, "")
	require.NoError(t, err)
	require.Len(t, entries.Objects, 1, "the indexed field must be queryable")

	got := entries.Objects[0].Value
	assert.Contains(t, got, "kept", "a field listed in Fields must be returned")
	assert.NotContains(t, got, "dropped",
		"a field absent from Fields is discarded before the document is stored and cannot be returned; got %s", got)

	// The same filter also removes the field from the searchable terms.
	unfiltered, _, err := si.List(context.Background(), uuid.Nil, indexName, "+value.dropped:no", 10, nil, "")
	require.NoError(t, err)
	assert.Empty(t, unfiltered.Objects,
		"a field absent from Fields is not indexed and must not be queryable")
}

// recordingStorageIndex captures what storageIndexWrite hands to the index.
type recordingStorageIndex struct {
	StorageIndex
	written []*api.StorageObject
}

func (r *recordingStorageIndex) Write(ctx context.Context, objects []*api.StorageObject) (int, int) {
	r.written = append(r.written, objects...)
	return len(objects), 0
}

// TestStorageIndexWrite_PairsAcksToTheirOwnOps covers a latent misalignment in
// storageIndexWrite that a multi-object batch makes reachable.
//
// storageWriteObjects sorts the ops for deadlock avoidance and returns the
// SORTED slice, but writes acks back at each op's ORIGINAL index
// (core_storage.go:686). Both MultiUpdate (core_multi.go:81) and
// StorageWriteObjects (core_storage.go:610) then hand that sorted slice and the
// input-ordered acks to storageIndexWrite, which pairs them BY POSITION.
//
// Whenever the sort actually reorders a batch, every indexed document gets
// another object's version and timestamps. The index then holds versions that
// never belonged to those records.
//
// A single-object write can never expose this, which is why it survived. Now
// that StorableWriteMany submits multi-object batches, it is reachable.
func TestStorageIndexWrite_PairsAcksToTheirOwnOps(t *testing.T) {
	ownerID := uuid.Must(uuid.NewV4()).String()

	newOp := func(collection, value string) *StorageOpWrite {
		return &StorageOpWrite{
			OwnerID: ownerID,
			Object: &api.WriteStorageObject{
				Collection: collection, Key: "k", Value: value,
			},
		}
	}

	// The caller submitted [Zeta, Alpha]; storageWriteObjects sorts by
	// collection, so the ops come back as [Alpha, Zeta]...
	sortedOps := StorageOpWrites{newOp("Alpha", `{"a":1}`), newOp("Zeta", `{"z":1}`)}
	// ...while the acks remain at the ORIGINAL submission index.
	acks := []*api.StorageObjectAck{
		{Collection: "Zeta", Key: "k", UserId: ownerID, Version: "version-zeta"},
		{Collection: "Alpha", Key: "k", UserId: ownerID, Version: "version-alpha"},
	}

	idx := &recordingStorageIndex{}
	storageIndexWrite(context.Background(), idx, sortedOps, acks)

	require.Len(t, idx.written, 2)
	versionByCollection := make(map[string]string, 2)
	for _, o := range idx.written {
		versionByCollection[o.Collection] = o.Version
	}

	assert.Equal(t, "version-alpha", versionByCollection["Alpha"],
		"Alpha was indexed with another object's version")
	assert.Equal(t, "version-zeta", versionByCollection["Zeta"],
		"Zeta was indexed with another object's version")
}

// TestSuspensionProfileIndex_MaxEntriesCannotBind pins the capacity decision.
//
// The SuspensionProfile collection holds one entry per user who has ever had an
// enforcement journal synced -- including users whose suspensions have all
// expired or been voided, because a zero-suspension profile still carries
// user_id and so is still indexed (storage_index.go:573). The set is
// therefore CUMULATIVE and monotonic: it never shrinks.
//
// That makes the old cap of 10,000 a ceiling on lifetime enforcement history,
// not on concurrent bans -- and hitting it degrades silently in two ways:
// eviction (never reloaded) and boot-load truncation (load stops dead at
// MaxEntries, storage_index.go:503+514).
//
// Rather than invent a peak number, this couples the cap to DisplayNameHistory,
// the closest structural analogue in this codebase: also one entry per user,
// also cumulative, sized by this project for EVR's actual population. Users who
// have been enforced are a strict SUBSET of users who have a display name
// history, so a cap at least that large provably cannot bind first.
func TestSuspensionProfileIndex_MaxEntriesCannotBind(t *testing.T) {
	suspension := (&SuspensionProfile{}).StorageIndexes()[0]
	displayNames := (&DisplayNameHistory{}).StorageIndexes()[0]

	assert.GreaterOrEqual(t, suspension.MaxEntries, displayNames.MaxEntries,
		"SuspensionProfile holds a strict subset of the users DisplayNameHistory holds, so its cap must be at least as large to be provably non-binding; suspension=%d displayNames=%d",
		suspension.MaxEntries, displayNames.MaxEntries)
}

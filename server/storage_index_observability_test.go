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
	"google.golang.org/protobuf/types/known/wrapperspb"
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

	// The gauge is a point-in-time occupancy reading, not a high-water mark: it
	// holds its last value until the next Write, so a value set before eviction
	// misreports the index for as long as the index sits idle -- and an idle
	// ban-heavy index is exactly the one whose headroom matters. Compare it
	// against the index's real document count rather than a constant, so the
	// assertion tracks the eviction arithmetic instead of restating it.
	local, ok := si.(*LocalStorageIndex)
	require.True(t, ok)
	reader, err := local.indexByName[indexName].Index.Reader()
	require.NoError(t, err)
	actual, err := reader.Count()
	require.NoError(t, err)
	require.NoError(t, reader.Close())

	require.Equal(t, uint64(maxEntries), actual,
		"precondition: eviction must have trimmed the index back to MaxEntries")
	assert.Equal(t, float64(actual), metrics.gauges[indexName],
		"the entry gauge must reflect occupancy so headroom is visible; the index holds %d entries", actual)

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

// TestStorageIndexWrite_CarriesWritePermissionFromTheWriteField pins the source
// field of each permission storageIndexWrite copies into the indexed document.
//
// PermissionWrite was populated from the object's PermissionRead, so every
// document indexed at runtime carried its READ permission in the WRITE slot.
// Nothing enforces writes from the index -- storagePrepBatch gates them in SQL
// with "AND storage.write = 1" (core_storage.go:706) against the database
// column -- so this is a fidelity bug, not an authorization hole. It still
// escapes: an indexOnly index answers StorageIndexList straight out of the
// document (storage_index.go:354), so callers and clients are handed the wrong
// PermissionWrite, and a registered index filter function receives the same
// wrong value in its StorageOpWrite (storage_index.go:109).
//
// The common read=2/write=0 shape corrupts in the permissive direction: the
// listing claims the object is client-writable when it is not.
func TestStorageIndexWrite_CarriesWritePermissionFromTheWriteField(t *testing.T) {
	ownerID := uuid.Must(uuid.NewV4()).String()

	// Public read, no client write -- the shape where read and write differ.
	ops := StorageOpWrites{{
		OwnerID: ownerID,
		Object: &api.WriteStorageObject{
			Collection:      "Coll",
			Key:             "k",
			Value:           `{"a":1}`,
			PermissionRead:  wrapperspb.Int32(2),
			PermissionWrite: wrapperspb.Int32(0),
		},
	}}
	acks := []*api.StorageObjectAck{
		{Collection: "Coll", Key: "k", UserId: ownerID, Version: "v1"},
	}

	idx := &recordingStorageIndex{}
	storageIndexWrite(context.Background(), idx, ops, acks)

	require.Len(t, idx.written, 1)
	assert.Equal(t, int32(2), idx.written[0].PermissionRead,
		"the indexed document must carry the object's read permission")
	assert.Equal(t, int32(0), idx.written[0].PermissionWrite,
		"the indexed document must carry the object's WRITE permission, not a second copy of its read permission")
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

// TestStorageIndexLoad_TruncationWarnRequiresActualTruncation pins the boot-time
// truncation warning to the condition it names.
//
// Load fills the index until count reaches MaxEntries and then stops. Reaching
// MaxEntries is not by itself truncation: a collection holding exactly
// MaxEntries rows fills the index with nothing left over. The warning
// previously fired on count >= MaxEntries, so that exactly-full collection
// reported "MaxEntries reached before the collection was exhausted" -- an
// alertable claim of permanent silent data loss -- on every process boot.
//
// This is a DB-backed test: the load path reads the storage table, so it cannot
// be exercised by the DB-free suite. It runs under `just test-db`.
func TestStorageIndexLoad_TruncationWarnRequiresActualTruncation(t *testing.T) {
	const (
		indexName  = "truncation_warn_index"
		collection = "truncation_warn_collection"
		maxEntries = 4
		warnMsg    = "Storage index truncated at load: MaxEntries reached before the collection was exhausted"
	)

	// writeN writes n objects into the collection, then builds a fresh index
	// over it and Loads. It returns the number of truncation warnings emitted.
	loadWarningsWith := func(t *testing.T, maxEntries, pageSize, n int) int {
		t.Helper()

		db := NewDB(t)
		ctx := context.Background()
		value, _ := json.Marshal(map[string]any{"one": 1})

		core, logs := observer.New(zap.WarnLevel)
		obsLogger := zap.New(core)

		writeIdx, err := NewLocalStorageIndex(obsLogger, db, &StorageConfig{}, newGaugeCapturingMetrics())
		if err != nil {
			t.Fatal(err)
		}

		ops := make(StorageOpWrites, 0, n)
		for i := 0; i < n; i++ {
			ops = append(ops, &StorageOpWrite{
				OwnerID: uuid.Nil.String(),
				Object: &api.WriteStorageObject{
					Collection: collection,
					Key:        fmt.Sprintf("key%03d", i),
					Value:      string(value),
				},
			})
		}
		if _, _, err := StorageWriteObjects(ctx, obsLogger, db, newGaugeCapturingMetrics(), writeIdx, true, ops); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			dels := make(StorageOpDeletes, 0, len(ops))
			for _, op := range ops {
				dels = append(dels, &StorageOpDelete{
					OwnerID:  uuid.Nil.String(),
					ObjectID: &api.DeleteStorageObjectId{Collection: collection, Key: op.Object.Key},
				})
			}
			_, _ = StorageDeleteObjects(context.Background(), obsLogger, db, writeIdx, true, dels)
		})

		// A second index instance, loading the collection the first one wrote.
		loadIdx, err := NewLocalStorageIndex(obsLogger, db, &StorageConfig{}, newGaugeCapturingMetrics())
		if err != nil {
			t.Fatal(err)
		}
		if pageSize > 0 {
			loadIdx.(*LocalStorageIndex).loadPageSize = pageSize
		}
		if err := loadIdx.CreateIndex(ctx, indexName, collection, "", []string{"one"}, []string{}, maxEntries, false); err != nil {
			t.Fatal(err)
		}
		if err := loadIdx.Load(ctx); err != nil {
			t.Fatal(err)
		}

		return logs.FilterMessage(warnMsg).Len()
	}

	loadWarnings := func(t *testing.T, n int) int {
		t.Helper()
		return loadWarningsWith(t, maxEntries, 0, n)
	}

	// loadWarningsPaged reproduces the production alignment: MaxEntries equal to
	// the page size, so the cap is reached on a page's last row.
	loadWarningsPaged := func(t *testing.T, size, n int) int {
		t.Helper()
		return loadWarningsWith(t, size, size, n)
	}

	t.Run("exactly MaxEntries rows is not truncation", func(t *testing.T) {
		if got := loadWarnings(t, maxEntries); got != 0 {
			t.Errorf("collection holding exactly MaxEntries (%d) rows emitted %d truncation warning(s); "+
				"the index was filled exactly and nothing was dropped", maxEntries, got)
		}
	})

	t.Run("more than MaxEntries rows is truncation", func(t *testing.T) {
		if got := loadWarnings(t, maxEntries+1); got != 1 {
			t.Errorf("collection holding MaxEntries+1 (%d) rows emitted %d truncation warning(s), want 1; "+
				"rows were genuinely dropped and the operator must be told", maxEntries+1, got)
		}
	})

	t.Run("truncation is detected when the cap lands on a page boundary", func(t *testing.T) {
		// The original fix probed for an extra row with rows.Next() on the page
		// buffer. That cannot see past the end of a page, and the load pages at
		// 10,000 rows while every index in this codebase sets MaxEntries to a
		// multiple of that -- so in production the cap is reached on a page's
		// LAST row and the probe always came back false. The warning could not
		// fire for any index that could actually truncate.
		//
		// loadWarningsPaged sets MaxEntries equal to the page size, reproducing
		// that alignment at a size a test can afford.
		if got := loadWarningsPaged(t, 4, 5); got != 1 {
			t.Errorf("collection holding one row past a page-aligned MaxEntries emitted %d truncation "+
				"warning(s), want 1; the in-page probe cannot see past the page and the database must be asked", got)
		}
	})

	t.Run("exact page-aligned fit is not truncation", func(t *testing.T) {
		if got := loadWarningsPaged(t, 4, 4); got != 0 {
			t.Errorf("collection holding exactly a page-aligned MaxEntries emitted %d truncation warning(s), "+
				"want 0; the index filled exactly and nothing was dropped", got)
		}
	})
}

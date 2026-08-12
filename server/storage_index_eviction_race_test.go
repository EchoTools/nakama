package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// --- Unsynchronized index eviction ------------------------------------------
//
// LocalStorageIndex.Write ends with a capacity check and an eviction, both
// unlocked:
//
//	count, _ := reader.Count()
//	if count > uint64(float32(idx.MaxEntries)*1.1) {
//	    deleteCount := int(count - uint64(idx.MaxEntries))
//	    ... search the oldest deleteCount docs, delete them ...
//	}
//
// Write is called concurrently. Every writer that opens a reader before any
// other writer's delete batch lands reads the SAME pre-eviction count, computes
// the SAME deleteCount, and issues its OWN full-size delete batch. Observed in
// production at 23:43:38.120-.124: eight writers each read
// entries_before_eviction=1101 and each issued a 101-delete batch.
//
// The batches select overlapping document sets, so the surviving entry count is
// still about right; the cost is N redundant sorted TopN searches over the whole
// index and N delete batches, all of it allocating, on a path that runs on every
// storage write. Measured against the unfixed code by the test below: 64 writers
// produced 64 eviction batches deleting ~36,000 documents where 564 deletions
// were called for.

// evictionLogMessage is the warning LocalStorageIndex.Write emits per eviction
// batch. One batch per over-capacity episode is correct; more than one means
// several writers acted on the same stale count.
const evictionLogMessage = "Storage index at capacity; evicted oldest entries (evicted entries are not reloaded)"

// syncGaugeMetrics is the concurrent-safe counterpart of the gauge stub in
// storage_index_observability_test.go: this test calls Write from many
// goroutines, so the stub itself must not be the thing that races.
type syncGaugeMetrics struct {
	Metrics
	mu     sync.Mutex
	gauges map[string]float64
}

func (m *syncGaugeMetrics) GaugeStorageIndexEntries(indexName string, value float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.gauges == nil {
		m.gauges = make(map[string]float64)
	}
	m.gauges[indexName] = value
}

func indexObject(collection string, n int, at time.Time) *api.StorageObject {
	value, _ := json.Marshal(map[string]any{"n": n})
	return &api.StorageObject{
		Collection: collection,
		Key:        "key",
		UserId:     uuid.Must(uuid.NewV4()).String(),
		Value:      string(value),
		Version:    fmt.Sprintf("v%d", n),
		CreateTime: timestamppb.New(at),
		UpdateTime: timestamppb.New(at.Add(time.Duration(n) * time.Millisecond)),
	}
}

func indexEntryCount(t *testing.T, si *LocalStorageIndex, name string) uint64 {
	t.Helper()
	reader, err := si.indexByName[name].Index.Reader()
	require.NoError(t, err)
	defer reader.Close()
	count, err := reader.Count()
	require.NoError(t, err)
	return count
}

// TestStorageIndexEviction_ConcurrentWritersEvictOnce mirrors the production
// incident: an index sitting exactly at its eviction threshold, then several
// writers arriving at once.
func TestStorageIndexEviction_ConcurrentWritersEvictOnce(t *testing.T) {
	const (
		indexName  = "test_eviction_race"
		collection = "evict_race_collection"
		maxEntries = 5000
		// The branch fires at count > MaxEntries*1.1, so seeding exactly the
		// threshold leaves the index armed but not yet over.
		seedCount = int(float32(maxEntries) * 1.1)
		writers   = 64
	)

	core, logs := observer.New(zap.WarnLevel)
	metrics := &syncGaugeMetrics{}

	sidx, err := NewLocalStorageIndex(zap.New(core), nil, &StorageConfig{}, metrics)
	require.NoError(t, err)
	si := sidx.(*LocalStorageIndex)

	require.NoError(t, si.CreateIndex(context.Background(), indexName, collection, "key",
		[]string{"n"}, nil, maxEntries, true))

	now := time.Now().UTC()
	seed := make([]*api.StorageObject, 0, seedCount)
	for i := 0; i < seedCount; i++ {
		seed = append(seed, indexObject(collection, i, now))
	}
	si.Write(context.Background(), seed)

	require.Equal(t, uint64(seedCount), indexEntryCount(t, si, indexName),
		"seeding should leave the index exactly at the eviction threshold")
	require.Equal(t, 0, logs.FilterMessage(evictionLogMessage).Len(), "seeding must not have evicted anything yet")

	// Every writer adds one document, so each on its own tips the index one
	// past the threshold. Only ONE eviction should result.
	start := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			obj := indexObject(collection, seedCount+w, now)
			<-start
			si.Write(context.Background(), []*api.StorageObject{obj})
		}(w)
	}
	close(start)
	wg.Wait()

	evictions := logs.FilterMessage(evictionLogMessage).All()

	totalEvicted := 0
	for _, e := range evictions {
		if n, ok := e.ContextMap()["evicted_count"].(int64); ok {
			totalEvicted += int(n)
		}
	}

	final := indexEntryCount(t, si, indexName)

	if len(evictions) != 1 {
		details := make([]string, 0, len(evictions))
		for _, e := range evictions {
			ctx := e.ContextMap()
			details = append(details, fmt.Sprintf("{before=%v deleted=%v}", ctx["entries_before_eviction"], ctx["evicted_count"]))
		}
		t.Fatalf("%d concurrent writers produced %d eviction batches deleting %d documents in total; the check-and-evict is not atomic. Batches: %v. Index left holding %d entries (MaxEntries=%d)",
			writers, len(evictions), totalEvicted, details, final, maxEntries)
	}

	// One eviction takes the index to MaxEntries; the writers that ran after it
	// add at most `writers` more.
	if final < uint64(maxEntries) || final > uint64(maxEntries+writers) {
		t.Fatalf("index holds %d entries after eviction, want between %d and %d; over-eviction discards entries that are never reloaded",
			final, maxEntries, maxEntries+writers)
	}
}

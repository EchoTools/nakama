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
// Above MaxEntries + 10%, Write evicts the oldest documents (storage_index.go
// :161-190) and Nakama never reloads evicted entries. For an index backing any
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

// TestSuspensionProfileIndex_MaxEntriesCannotBind pins the capacity decision.
//
// The SuspensionProfile collection holds one entry per user who has ever had an
// enforcement journal synced -- including users whose suspensions have all
// expired or been voided, because a zero-suspension profile still carries
// user_id and so is still indexed (storage_index.go:541-543). The set is
// therefore CUMULATIVE and monotonic: it never shrinks.
//
// That makes the old cap of 10,000 a ceiling on lifetime enforcement history,
// not on concurrent bans -- and hitting it degrades silently in two ways:
// eviction (never reloaded) and boot-load truncation (load stops dead at
// MaxEntries, storage_index.go:479+491).
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

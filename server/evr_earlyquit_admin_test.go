package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// TestLoadEarlyQuitServiceConfigOrDefault_ReadsSystemConfig proves the admin
// RPC config loader reads the system-wide config row (SystemUserID) instead of
// silently falling back to defaults.
//
// Regression: loadEarlyQuitServiceConfigOrDefault called StorableRead with an
// empty owner ID, which StorableRead rejects with InvalidArgument, so the
// production config row (EarlyQuit/config under SystemUserID) was never read
// and the defaults were always used.
//
// Requires a real Postgres (TEST_DB_URL) — the read/write path runs through
// RuntimeGoNakamaModule, which uses pgx connections directly.
func TestLoadEarlyQuitServiceConfigOrDefault_ReadsSystemConfig(t *testing.T) {
	// --- setup: real module over the test DB ---
	logger := loggerForTest(t)
	db := NewDB(t)
	cfg := NewConfig(logger)

	storageIndex, err := NewLocalStorageIndex(logger, db, &StorageConfig{}, &testMetrics{})
	if err != nil {
		t.Fatalf("failed to create storage index: %v", err)
	}
	nk := NewRuntimeGoNakamaModule(logger, db, nil, cfg,
		nil, &chargeTestLeaderboardCache{}, nil, nil,
		&testSessionRegistry{}, nil, nil, nil,
		&testTracker{}, &testMetrics{}, nil, &testMessageRouter{},
		storageIndex, nil)

	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")

	// The config row lives under the system user, not any real player.
	InsertUser(t, db, uuid.Must(uuid.FromString(SystemUserID)))

	// Custom ladder: level 1 lockout = 999s instead of the default 120s.
	custom := evr.NewDefaultSNSEarlyQuitConfig()
	custom.PenaltyLevels[1].MMLockoutSec = 999
	value, err := json.Marshal(custom)
	if err != nil {
		t.Fatalf("failed to marshal custom config: %v", err)
	}

	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      evr.StorageCollectionEarlyQuitConfig,
		Key:             evr.StorageKeyEarlyQuitConfig,
		UserID:          SystemUserID,
		Value:           string(value),
		Version:         "",
		PermissionRead:  int(runtime.STORAGE_PERMISSION_NO_READ),
		PermissionWrite: int(runtime.STORAGE_PERMISSION_NO_WRITE),
	}}); err != nil {
		t.Fatalf("failed to seed config row: %v", err)
	}

	// --- the function under test ---
	got := loadEarlyQuitServiceConfigOrDefault(ctx, nk)

	// --- assert the stored custom ladder was loaded, not the defaults ---
	for _, pl := range got.PenaltyLevels {
		if pl.PenaltyLevel == 1 {
			if pl.MMLockoutSec != 999 {
				t.Errorf("level 1 lockout = %d, want 999 (got defaults: config row not read)", pl.MMLockoutSec)
			}
			return
		}
	}
	t.Errorf("loaded config missing penalty level 1: %+v", got.PenaltyLevels)
}

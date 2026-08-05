package server

import (
	"context"
	"encoding/json"
	"testing"

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
// Runs against the in-memory evrTestNakamaModule: the loader only needs a
// storage read, so no database is required.
func TestLoadEarlyQuitServiceConfigOrDefault_ReadsSystemConfig(t *testing.T) {
	nk := newEvrTestNakamaModule()

	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")

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

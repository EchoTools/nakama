package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

// TestCheckAndApplyEarlyQuitIfStillOnline_CrashReconnectForgiven verifies that
// the DISCONNECT deferral path forgives a player who crashed and reconnected on
// a new session (BUG-1: B3's user-wide session scan regressed this).
//
// Scenario: player crashes on session S1 (S1 closes and leaves the session
// registry), then reconnects within the crash-recovery window on session S2
// (same user ID, different session UUID). When the deferred-penalty goroutine
// fires with S1's UUID, the session-specific lookup finds S1 gone and must
// forgive the quit — the reconnect reservation feature exists to protect this
// flow. A user-wide scan (the B3 behavior) would see S2 online and wrongly
// apply the penalty.
//
// Requires a real Postgres (TEST_DB_URL) — the deferred penalty path's
// StorableRead/StorableWrite run through RuntimeGoNakamaModule.
func TestCheckAndApplyEarlyQuitIfStillOnline_CrashReconnectForgiven(t *testing.T) {
	// --- setup: real module over the test DB ---
	consoleLogger := loggerForTest(t)
	logger := NewRuntimeGoLogger(consoleLogger)
	db := NewDB(t)
	cfg := NewConfig(consoleLogger)

	storageIndex, err := NewLocalStorageIndex(consoleLogger, db, &StorageConfig{}, &testMetrics{})
	if err != nil {
		t.Fatalf("failed to create storage index: %v", err)
	}
	nk := NewRuntimeGoNakamaModule(consoleLogger, db, nil, cfg,
		nil, &chargeTestLeaderboardCache{}, nil, nil,
		&testSessionRegistry{}, nil, nil, nil,
		&testTracker{}, &testMetrics{}, nil, &testMessageRouter{},
		storageIndex, nil)

	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")

	userID := uuid.Must(uuid.NewV4())
	InsertUser(t, db, userID)
	uid := userID.String()

	// --- session registry: S1 (the crashed session) closes, S2 (reconnect) stays ---
	registry := NewLocalSessionRegistry(&testMetrics{})

	s1ID := uuid.Must(uuid.NewV4())
	s1 := NewSessionNEVR(consoleLogger, cfg, s1ID, userID, "crash-reconnect-tester", nil, 0,
		"1.2.3.4", "12345", "en", nil, registry, nil, nil, nil, &testMetrics{}, nil, nil, nil)
	registry.Add(s1)

	// Reconnect on a different session UUID, same user ID.
	s2ID := uuid.Must(uuid.NewV4())
	s2 := NewSessionNEVR(consoleLogger, cfg, s2ID, userID, "crash-reconnect-tester", nil, 0,
		"1.2.3.4", "12345", "en", nil, registry, nil, nil, nil, &testMetrics{}, nil, nil, nil)
	registry.Add(s2)

	// The crashed session S1 closes (session teardown removes it from the registry).
	registry.Remove(s1ID)

	// --- pre-seed a live penalty so an incorrect penalty application would be observable ---
	penaltyTs := time.Now().Unix() + 120
	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      StorageCollectionEarlyQuit,
		Key:             StorageKeyEarlyQuit,
		UserID:          uid,
		Value:           fmt.Sprintf(`{"num_early_quits":2,"num_steady_early_quits":2,"matchmaking_tier":1,"penalty_level":1,"penalty_ts":%d}`, penaltyTs),
		Version:         "",
		PermissionRead:  int(runtime.STORAGE_PERMISSION_NO_READ),
		PermissionWrite: int(runtime.STORAGE_PERMISSION_NO_WRITE),
	}}); err != nil {
		t.Fatalf("preseed early quit state failed: %v", err)
	}

	// --- run the deferred penalty check as the goroutine would, with S1's session ID ---
	matchID, err := NewMatchID(uuid.Must(uuid.NewV4()), "test-node")
	if err != nil {
		t.Fatalf("failed to create match ID: %v", err)
	}
	CheckAndApplyEarlyQuitIfStillOnline(ctx, logger, nk, db, registry, uid, s1ID.String(), matchID, 5*time.Millisecond)

	// --- the penalty must NOT be applied: S1 is gone, the player genuinely crashed ---
	eqconfig := NewEarlyQuitPlayerState()
	if err := StorableRead(ctx, nk, uid, eqconfig, false); err != nil {
		t.Fatalf("failed to read back early quit state: %v", err)
	}
	if eqconfig.NumEarlyQuits != 2 {
		t.Errorf("crash+reconnect must forgive the quit: expected 2 early quits, got %d (user online via new session S2 must not count)", eqconfig.NumEarlyQuits)
	}
	if eqconfig.PenaltyLevel != 1 {
		t.Errorf("crash+reconnect must forgive the quit: expected penalty level 1, got %d", eqconfig.PenaltyLevel)
	}
}

// TestCheckAndApplyEarlyQuitIfStillOnline_SameSessionOnlineAppliesPenalty
// verifies the other branch of the restored session-specific check: if the
// exact session captured at quit time is still in the registry, the player
// force-closed intentionally and the deferred penalty must be applied.
func TestCheckAndApplyEarlyQuitIfStillOnline_SameSessionOnlineAppliesPenalty(t *testing.T) {
	// --- setup: real module over the test DB ---
	consoleLogger := loggerForTest(t)
	logger := NewRuntimeGoLogger(consoleLogger)
	db := NewDB(t)
	cfg := NewConfig(consoleLogger)

	storageIndex, err := NewLocalStorageIndex(consoleLogger, db, &StorageConfig{}, &testMetrics{})
	if err != nil {
		t.Fatalf("failed to create storage index: %v", err)
	}
	nk := NewRuntimeGoNakamaModule(consoleLogger, db, nil, cfg,
		nil, &chargeTestLeaderboardCache{}, nil, nil,
		&testSessionRegistry{}, nil, nil, nil,
		&testTracker{}, &testMetrics{}, nil, &testMessageRouter{},
		storageIndex, nil)

	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")

	userID := uuid.Must(uuid.NewV4())
	InsertUser(t, db, userID)
	uid := userID.String()

	// --- session registry: the quit session S1 is still online ---
	registry := NewLocalSessionRegistry(&testMetrics{})

	s1ID := uuid.Must(uuid.NewV4())
	s1 := NewSessionNEVR(consoleLogger, cfg, s1ID, userID, "force-close-tester", nil, 0,
		"1.2.3.4", "12345", "en", nil, registry, nil, nil, nil, &testMetrics{}, nil, nil, nil)
	registry.Add(s1)

	// --- pre-seed a live penalty so the increment is observable ---
	penaltyTs := time.Now().Unix() + 120
	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      StorageCollectionEarlyQuit,
		Key:             StorageKeyEarlyQuit,
		UserID:          uid,
		Value:           fmt.Sprintf(`{"num_early_quits":2,"num_steady_early_quits":2,"matchmaking_tier":1,"penalty_level":1,"penalty_ts":%d}`, penaltyTs),
		Version:         "",
		PermissionRead:  int(runtime.STORAGE_PERMISSION_NO_READ),
		PermissionWrite: int(runtime.STORAGE_PERMISSION_NO_WRITE),
	}}); err != nil {
		t.Fatalf("preseed early quit state failed: %v", err)
	}

	// --- run the deferred penalty check with S1's session ID ---
	matchID, err := NewMatchID(uuid.Must(uuid.NewV4()), "test-node")
	if err != nil {
		t.Fatalf("failed to create match ID: %v", err)
	}
	CheckAndApplyEarlyQuitIfStillOnline(ctx, logger, nk, db, registry, uid, s1ID.String(), matchID, 5*time.Millisecond)

	// --- the penalty MUST be applied: the exact quit session is still online ---
	eqconfig := NewEarlyQuitPlayerState()
	if err := StorableRead(ctx, nk, uid, eqconfig, false); err != nil {
		t.Fatalf("failed to read back early quit state: %v", err)
	}
	if eqconfig.NumEarlyQuits != 3 {
		t.Errorf("same-session online must apply the deferred penalty: expected 3 early quits, got %d", eqconfig.NumEarlyQuits)
	}
	if eqconfig.LastEarlyQuitMatchID != matchID {
		t.Errorf("expected LastEarlyQuitMatchID %s, got %s", matchID, eqconfig.LastEarlyQuitMatchID)
	}
}

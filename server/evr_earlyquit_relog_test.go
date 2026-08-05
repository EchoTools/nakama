package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

// TestCheckAndStrikeEarlyQuitIfLoggedOut_RelogKeepsPenalty verifies that relogging
// on a NEW session does not bypass the 5-minute forgiveness check.
//
// Scenario: player voluntarily quits a match on session S1 (penalty applied).
// S1 closes, then the player relogs on session S2 (same user ID, different
// session UUID). When the forgiveness goroutine fires with S1's UUID, the
// specific-session lookup finds S1 gone and would forgive the quit — but the
// player is still online via S2, so the penalty must remain.
//
// Runs against the in-memory evrTestNakamaModule: the forgiveness path only
// needs storage read/write plus a session registry, so no database is required.
func TestCheckAndStrikeEarlyQuitIfLoggedOut_RelogKeepsPenalty(t *testing.T) {
	consoleLogger := loggerForTest(t)
	logger := NewRuntimeGoLogger(consoleLogger)
	cfg := NewConfig(consoleLogger)
	nk := newEvrTestNakamaModule()

	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")

	userID := uuid.Must(uuid.NewV4())
	uid := userID.String()

	// --- session registry: S1 (the quit session) closes, S2 (relog) stays ---
	registry := NewLocalSessionRegistry(&testMetrics{})

	s1ID := uuid.Must(uuid.NewV4())
	s1 := NewSessionNEVR(consoleLogger, cfg, s1ID, userID, "relog-tester", nil, 0,
		"1.2.3.4", "12345", "en", nil, registry, nil, nil, nil, &testMetrics{}, nil, nil, nil)
	registry.Add(s1)

	// New login on a different session UUID, same user ID.
	s2ID := uuid.Must(uuid.NewV4())
	s2 := NewSessionNEVR(consoleLogger, cfg, s2ID, userID, "relog-tester", nil, 0,
		"1.2.3.4", "12345", "en", nil, registry, nil, nil, nil, &testMetrics{}, nil, nil, nil)
	registry.Add(s2)

	// The original quit session S1 closes (session teardown removes it from the registry).
	registry.Remove(s1ID)

	// --- pre-seed a live penalty so forgiveness would be observable ---
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

	// --- run the forgiveness check as the goroutine would, with S1's session ID ---
	CheckAndStrikeEarlyQuitIfLoggedOut(ctx, logger, nk, nil, registry, uid, s1ID.String(), 5*time.Millisecond)

	// --- the penalty must NOT be forgiven: the user is still online via S2 ---
	eqconfig := NewEarlyQuitPlayerState()
	if err := StorableRead(ctx, nk, uid, eqconfig, false); err != nil {
		t.Fatalf("failed to read back early quit state: %v", err)
	}
	if eqconfig.NumEarlyQuits != 2 {
		t.Errorf("relog must NOT forgive the quit: expected 2 early quits, got %d (user still online via new session)", eqconfig.NumEarlyQuits)
	}
	if eqconfig.PenaltyLevel != 1 {
		t.Errorf("relog must NOT forgive the quit: expected penalty level 1, got %d", eqconfig.PenaltyLevel)
	}
}

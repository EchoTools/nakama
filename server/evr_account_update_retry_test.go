package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/stretchr/testify/require"
)

// profileUpdateTestModule is an OCC-correct in-memory module that supports the
// exact surface EVRProfileUpdate / EVRProfileLoad need: AccountGetId,
// StorageRead, StorageDelete and MultiUpdate.
//
// MultiUpdate routes its storage writes through the shared OCC mock, so a write
// carrying a stale Version is rejected with runtime.ErrStorageRejectedVersion —
// the same signal isVersionConflictError keys on in production.
type profileUpdateTestModule struct {
	*occTestNakamaModule

	metadata map[string]map[string]any

	// conflictsRemaining injects that many artificial version conflicts before
	// letting writes through, independent of the stored version. Used to force a
	// conflict on the very first attempt.
	conflictsRemaining int

	multiUpdateCalls int
}

func newProfileUpdateTestModule() *profileUpdateTestModule {
	return &profileUpdateTestModule{
		occTestNakamaModule: newOCCTestNakamaModule(),
		metadata:            make(map[string]map[string]any),
	}
}

func (m *profileUpdateTestModule) AccountGetId(ctx context.Context, userID string) (*api.Account, error) {
	return &api.Account{User: &api.User{Id: userID, Username: "tester"}}, nil
}

func (m *profileUpdateTestModule) StorageDelete(ctx context.Context, deletes []*runtime.StorageDelete) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range deletes {
		delete(m.objects, occStorageKey(d.UserID, d.Collection, d.Key))
	}
	return nil
}

func (m *profileUpdateTestModule) MultiUpdate(ctx context.Context, accountUpdates []*runtime.AccountUpdate, storageWrites []*runtime.StorageWrite, storageDeletes []*runtime.StorageDelete, walletUpdates []*runtime.WalletUpdate, updateLedger bool) ([]*api.StorageObjectAck, []*runtime.WalletUpdateResult, error) {
	// These counters share the embedded module's mutex, which its own
	// StorageWrite/StorageDelete also take. Guard them here rather than relying on
	// the current tests being single-goroutine.
	m.mu.Lock()
	m.multiUpdateCalls++
	conflict := m.conflictsRemaining > 0
	if conflict {
		m.conflictsRemaining--
	}
	m.mu.Unlock()

	if conflict {
		return nil, nil, runtime.ErrStorageRejectedVersion
	}

	acks, err := m.StorageWrite(ctx, storageWrites)
	if err != nil {
		return nil, nil, err
	}
	if err := m.StorageDelete(ctx, storageDeletes); err != nil {
		return nil, nil, err
	}
	m.mu.Lock()
	for _, au := range accountUpdates {
		m.metadata[au.UserID] = au.Metadata
	}
	m.mu.Unlock()
	return acks, nil, nil
}

// calls returns multiUpdateCalls under the mutex.
func (m *profileUpdateTestModule) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.multiUpdateCalls
}

// storedProfile decodes the EVRProfile storage row for userID.
func (m *profileUpdateTestModule) storedProfile(t *testing.T, userID string) *EVRProfile {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[occStorageKey(userID, StorageCollectionEVRProfile, StorageKeyEVRProfile)]
	require.True(t, ok, "expected an EVRProfile storage row for %s", userID)
	p := &EVRProfile{}
	require.NoError(t, json.Unmarshal([]byte(obj.Value), p))
	return p
}

func seedStoredProfile(t *testing.T, m *profileUpdateTestModule, userID string, p *EVRProfile) string {
	t.Helper()
	b, err := json.Marshal(p)
	require.NoError(t, err)
	return m.seedObject(userID, StorageCollectionEVRProfile, StorageKeyEVRProfile, string(b))
}

// TestEVRProfileUpdateWithRetry_RetriesOnConflictWithFreshRead is the core of the
// login-hard-fail finding.
//
// PR #520 removed EVRProfileUpdate's internal retry, deliberately: a blind retry
// re-submits the caller's stale payload and silently discards whatever the
// concurrent writer committed. But the login path (and two others) then had no
// retry at all, so the FIRST version conflict rejected the login outright with
// "failed to update user profile". A concurrent writer is realistic — login fires
// QueueSyncMember for the same user a few hundred lines earlier, and that Discord
// sync path writes the same key.
//
// The correct shape is a bounded retry that RE-READS between attempts and
// re-applies the caller's mutation to the fresh profile. This test pins both
// halves: the write eventually succeeds, AND the concurrent writer's field
// survives.
func TestEVRProfileUpdateWithRetry_RetriesOnConflictWithFreshRead(t *testing.T) {
	ctx := context.Background()
	const userID = "11111111-1111-4111-8111-111111111111"

	m := newProfileUpdateTestModule()

	// The version our caller is holding.
	staleVersion := seedStoredProfile(t, m, userID, &EVRProfile{TeamName: "original"})

	// A concurrent writer commits first, bumping the stored version.
	seedStoredProfile(t, m, userID, &EVRProfile{TeamName: "original", MatchmakingDivision: "gold"})

	// Our caller's in-hand profile still carries the stale version and knows
	// nothing about MatchmakingDivision.
	mine := &EVRProfile{TeamName: "mine"}
	mine.SetStorageMeta(StorableMetadata{Version: staleVersion})

	updated, err := evrProfileUpdateWithRetry(ctx, m, userID, mine, func(p *EVRProfile) error {
		p.TeamName = "mine"
		return nil
	})
	require.NoError(t, err, "a version conflict must not be fatal; retry with a fresh read")
	require.NotNil(t, updated)

	stored := m.storedProfile(t, userID)
	require.Equal(t, "mine", stored.TeamName, "the caller's mutation must be applied")
	require.Equal(t, "gold", stored.MatchmakingDivision,
		"the concurrent writer's field must survive: the retry must re-read, not re-submit a stale payload")
}

// TestEVRProfileUpdateWithRetry_SucceedsFirstTryWithoutReload is the control: no
// conflict means exactly one write and no reload.
func TestEVRProfileUpdateWithRetry_SucceedsFirstTryWithoutReload(t *testing.T) {
	ctx := context.Background()
	const userID = "22222222-2222-4222-8222-222222222222"

	m := newProfileUpdateTestModule()
	version := seedStoredProfile(t, m, userID, &EVRProfile{TeamName: "original"})

	mine := &EVRProfile{TeamName: "mine"}
	mine.SetStorageMeta(StorableMetadata{Version: version})

	applyCalls := 0
	updated, err := evrProfileUpdateWithRetry(ctx, m, userID, mine, func(p *EVRProfile) error {
		applyCalls++
		p.TeamName = "mine"
		return nil
	})
	require.NoError(t, err)
	require.Same(t, mine, updated, "no conflict means the caller's own profile object is kept")
	require.Equal(t, 1, m.calls())
	require.Zero(t, applyCalls, "apply must not be re-run when the first attempt succeeds")
}

// TestEVRProfileUpdateWithRetry_GivesUpAfterBoundedAttempts pins that the retry is
// bounded: a permanently conflicting key must not spin forever.
func TestEVRProfileUpdateWithRetry_GivesUpAfterBoundedAttempts(t *testing.T) {
	ctx := context.Background()
	const userID = "33333333-3333-4333-8333-333333333333"

	m := newProfileUpdateTestModule()
	seedStoredProfile(t, m, userID, &EVRProfile{TeamName: "original"})
	m.conflictsRemaining = 1000 // every attempt conflicts

	mine := &EVRProfile{TeamName: "mine"}
	_, err := evrProfileUpdateWithRetry(ctx, m, userID, mine, func(p *EVRProfile) error { return nil })
	require.Error(t, err)
	require.True(t, isVersionConflictError(err), "the surfaced error must still be recognisable as a conflict: %v", err)
	// Assert against a literal, not against evrProfileUpdateMaxAttempts itself:
	// comparing the constant to itself would keep this test green for ANY value,
	// including 1 (no retry at all).
	require.Equal(t, 3, m.calls(),
		"retry must make exactly 3 bounded attempts")
	require.Equal(t, 3, evrProfileUpdateMaxAttempts,
		"evrProfileUpdateMaxAttempts changed; update this test deliberately")
}

// TestEVRProfileUpdateWithRetry_DoesNotRetryNonConflictErrors pins that an
// unrelated failure is surfaced immediately rather than amplified into N writes.
func TestEVRProfileUpdateWithRetry_DoesNotRetryNonConflictErrors(t *testing.T) {
	ctx := context.Background()
	const userID = "44444444-4444-4444-8444-444444444444"

	m := newProfileUpdateTestModule()
	seedStoredProfile(t, m, userID, &EVRProfile{TeamName: "original"})
	m.failNonVersion = errors.New("database is on fire")

	mine := &EVRProfile{TeamName: "mine"}
	_, err := evrProfileUpdateWithRetry(ctx, m, userID, mine, func(p *EVRProfile) error { return nil })
	require.Error(t, err)
	require.False(t, isVersionConflictError(err))
	require.Equal(t, 1, m.calls(), "a non-conflict error must not be retried")
}

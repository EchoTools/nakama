package server

import (
	"context"
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
)

// TestPersistClientProfileUpdate_ClearedNewUnlocksSurvivesConflict pins the
// new_unlocks report: a client clears its new unlocks while another writer has
// just committed the same profile key. The clear must land, and the other
// writer's field must survive the re-read.
func TestPersistClientProfileUpdate_ClearedNewUnlocksSurvivesConflict(t *testing.T) {
	ctx := context.Background()
	const userID = "22222222-2222-4222-8222-222222222222"

	m := newProfileUpdateTestModule()

	// The profile the handler loaded: three new unlocks, at the version it holds.
	staleVersion := seedStoredProfile(t, m, userID, &EVRProfile{NewUnlocks: []int64{1, 2, 3}})
	held := &EVRProfile{NewUnlocks: []int64{1, 2, 3}}
	meta := held.StorageMeta()
	meta.UserID = userID
	meta.Version = staleVersion
	held.SetStorageMeta(meta)

	// A concurrent writer (an equip, say) commits first and bumps the version.
	seedStoredProfile(t, m, userID, &EVRProfile{NewUnlocks: []int64{1, 2, 3}, MatchmakingDivision: "gold"})

	update := evr.ClientProfile{NewUnlocks: []int64{}}
	updated, err := persistClientProfileUpdate(ctx, m, userID, held, update)
	require.NoError(t, err, "a version conflict must be retried, not returned")
	require.NotNil(t, updated)

	stored := m.storedProfile(t, userID)
	require.Empty(t, stored.NewUnlocks, "the client's cleared new_unlocks must be what is stored")
	require.Equal(t, "gold", stored.MatchmakingDivision, "the concurrent writer's field must survive the retry")
	require.Equal(t, 2, m.calls(), "one rejected write, then one committed write")
}

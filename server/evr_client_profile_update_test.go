package server

import (
	"context"
	"errors"
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
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
	updated, err := persistClientProfileUpdate(ctx, zap.NewNop(), m, userID, held, update)
	require.NoError(t, err, "a version conflict must be retried, not returned")
	require.NotNil(t, updated)

	stored := m.storedProfile(t, userID)
	require.Empty(t, stored.NewUnlocks, "the client's cleared new_unlocks must be what is stored")
	require.Equal(t, "gold", stored.MatchmakingDivision, "the concurrent writer's field must survive the retry")
	require.Equal(t, 2, m.calls(), "one rejected write, then one committed write")
}

// TestPersistClientProfileUpdate_LogsEveryReplaceWithCountsAndEntries pins the
// operator record for a persisted client profile update (AGENTS.md logging rule
// 1): exactly one INFO line carrying the account user id, the new_unlocks count
// before and after the replace, the full entry list the client sent, the clear
// flag as the request sent it, and the outcome.
func TestPersistClientProfileUpdate_LogsEveryReplaceWithCountsAndEntries(t *testing.T) {
	ctx := context.Background()
	const userID = "55555555-5555-4555-8555-555555555555"

	m := newProfileUpdateTestModule()
	version := seedStoredProfile(t, m, userID, &EVRProfile{NewUnlocks: []int64{1, 2, 3}})
	held := &EVRProfile{NewUnlocks: []int64{1, 2, 3}}
	meta := held.StorageMeta()
	meta.UserID = userID
	meta.Version = version
	held.SetStorageMeta(meta)

	core, logs := observer.New(zapcore.DebugLevel)
	update := evr.ClientProfile{
		NewUnlocks:    []int64{9, 10},
		Customization: &evr.Customization{ClearNewUnlocksVersion: 5},
	}
	_, err := persistClientProfileUpdate(ctx, zap.New(core), m, userID, held, update)
	require.NoError(t, err)

	recs := logs.FilterMessage(clientProfileUpdateLogMessage).All()
	require.Len(t, recs, 1, "exactly one %q record; all records: %v", clientProfileUpdateLogMessage, logs.All())
	require.Equal(t, zapcore.InfoLevel, recs[0].Level)
	fields := recs[0].ContextMap()
	require.Equal(t, userID, fields["user_id"])
	require.EqualValues(t, 3, fields["new_unlocks_before"])
	require.EqualValues(t, 2, fields["new_unlocks_after"])
	require.Equal(t, []any{int64(9), int64(10)}, fields["new_unlocks_entries"])
	require.EqualValues(t, 5, fields["clear_new_unlocks_version"])
	require.Equal(t, "persisted", fields["outcome"])
}

// TestPersistClientProfileUpdate_LogsCountsOfTheAttemptThatCommitted pins that
// the before count is read from the profile the replace last ran against. A
// conflict reloads the stored profile, so the before count is the reloaded
// list's, not the handler's stale copy's, and there is still one record.
func TestPersistClientProfileUpdate_LogsCountsOfTheAttemptThatCommitted(t *testing.T) {
	ctx := context.Background()
	const userID = "66666666-6666-4666-8666-666666666666"

	m := newProfileUpdateTestModule()
	staleVersion := seedStoredProfile(t, m, userID, &EVRProfile{NewUnlocks: []int64{1, 2, 3}})
	held := &EVRProfile{NewUnlocks: []int64{1, 2, 3}}
	meta := held.StorageMeta()
	meta.UserID = userID
	meta.Version = staleVersion
	held.SetStorageMeta(meta)

	// A concurrent writer commits first, with five unlocks.
	seedStoredProfile(t, m, userID, &EVRProfile{NewUnlocks: []int64{1, 2, 3, 4, 5}})

	core, logs := observer.New(zapcore.DebugLevel)
	update := evr.ClientProfile{NewUnlocks: []int64{9}}
	_, err := persistClientProfileUpdate(ctx, zap.New(core), m, userID, held, update)
	require.NoError(t, err)
	require.Equal(t, 2, m.calls(), "one rejected write, then one committed write")

	recs := logs.FilterMessage(clientProfileUpdateLogMessage).All()
	require.Len(t, recs, 1, "a retried update still logs one record; all records: %v", logs.All())
	fields := recs[0].ContextMap()
	require.EqualValues(t, 5, fields["new_unlocks_before"], "before must be the reloaded list the replace ran against")
	require.EqualValues(t, 1, fields["new_unlocks_after"])
	require.Equal(t, []any{int64(9)}, fields["new_unlocks_entries"])
	require.Equal(t, "persisted", fields["outcome"])
}

// TestPersistClientProfileUpdate_LogsFailedUpdateWithOutcome pins that a update
// the store refuses still logs its record, with the error and outcome
// "not_persisted", so no client update leaves the record without a line.
func TestPersistClientProfileUpdate_LogsFailedUpdateWithOutcome(t *testing.T) {
	ctx := context.Background()
	const userID = "77777777-7777-4777-8777-777777777777"

	m := newProfileUpdateTestModule()
	version := seedStoredProfile(t, m, userID, &EVRProfile{NewUnlocks: []int64{1}})
	held := &EVRProfile{NewUnlocks: []int64{1}}
	meta := held.StorageMeta()
	meta.UserID = userID
	meta.Version = version
	held.SetStorageMeta(meta)
	m.failNonVersion = errors.New("database is on fire")

	core, logs := observer.New(zapcore.DebugLevel)
	update := evr.ClientProfile{NewUnlocks: []int64{4, 5}}
	_, err := persistClientProfileUpdate(ctx, zap.New(core), m, userID, held, update)
	require.Error(t, err)

	recs := logs.FilterMessage(clientProfileUpdateLogMessage).All()
	require.Len(t, recs, 1, "a failed update still logs one record; all records: %v", logs.All())
	require.Equal(t, zapcore.InfoLevel, recs[0].Level)
	fields := recs[0].ContextMap()
	require.Equal(t, "not_persisted", fields["outcome"])
	require.NotEmpty(t, fields["error"])
	require.EqualValues(t, 1, fields["new_unlocks_before"])
	require.EqualValues(t, 2, fields["new_unlocks_after"])
	require.Equal(t, []any{int64(4), int64(5)}, fields["new_unlocks_entries"])
}

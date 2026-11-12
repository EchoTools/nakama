package server

import (
	"context"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

// noopWriteStorageIndex accepts index writes and deletes without indexing. The
// profile path writes through StorageWriteObjects, which calls the index; this
// test is about the profile record, not the index, so the index is inert. The
// other methods are never reached and panic if they are, because they are
// promoted from a nil interface.
type noopWriteStorageIndex struct{ StorageIndex }

func (noopWriteStorageIndex) Write(context.Context, []*api.StorageObject) (int, int) { return 0, 0 }

func (noopWriteStorageIndex) Delete(context.Context, StorageOpDeletes) int { return 0 }

// TestUpdateClientProfileRequest_LogsDiffThroughHandler drives two client
// profile updates through the real handler, updateClientProfileRequest, against
// the repo's test database (NewDB, configured by TEST_DB_URL like the other
// DB-backed tests). The guild group is registered by fixture in
// GuildGroupRegistry, which is what handleClientProfileUpdate needs before it
// persists. Nothing in the path is stubbed.
//
// The first update seeds the stored list [1 2 3]. The second sends [2 3 9] with
// clear_new_unlocks_version 5. The second update's record must carry the diff
// against what the first one stored: added [9], removed [1].
//
// Run with TEST_DB_URL pointing at the local test database and TEST_DB_REQUIRED=1
// so the test cannot skip.
func TestUpdateClientProfileRequest_LogsDiffThroughHandler(t *testing.T) {
	db := NewDB(t)

	userID := uuid.Must(uuid.NewV4())
	InsertUser(t, db, userID)
	groupID := uuid.Must(uuid.NewV4())

	nk := &RuntimeGoNakamaModule{logger: zap.NewNop(), metrics: &testMetrics{}, storageIndex: noopWriteStorageIndex{}, db: db}
	registry := &GuildGroupRegistry{
		guildGroups:    atomic.NewPointer(&map[string]*GuildGroup{groupID.String(): {}}),
		inheritanceMap: atomic.NewPointer(&map[string][]string{}),
	}
	p := &EvrPipeline{nk: nk, db: db, guildGroupRegistry: registry}

	session, _ := newNilIdentityConfigSession(t, p)
	session.userID = userID

	core, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)

	// update runs one client profile update through the handler with a fresh
	// session context, the way a real request arrives.
	update := func(t *testing.T, payload evr.ClientProfile) {
		t.Helper()
		params := &SessionParameters{profile: &EVRProfile{ActiveGroupID: groupID.String()}}
		ctx := context.WithValue(session.ctx, ctxSessionParametersKey{}, atomic.NewPointer(params))
		require.NoError(t, p.updateClientProfileRequest(ctx, logger, session, &evr.UpdateClientProfile{Payload: payload}))
	}

	update(t, evr.ClientProfile{NewUnlocks: []int64{1, 2, 3}})
	update(t, evr.ClientProfile{
		NewUnlocks:    []int64{2, 3, 9},
		Customization: &evr.Customization{ClearNewUnlocksVersion: 5},
	})

	recs := logs.FilterMessage(clientProfileUpdateLogMessage).All()
	require.Len(t, recs, 2, "one record per update; all records: %v", logs.All())
	fields := recs[1].ContextMap()
	require.Equal(t, zapcore.InfoLevel, recs[1].Level)
	require.Equal(t, userID.String(), fields["user_id"])
	require.EqualValues(t, 3, fields["new_unlocks_sent"])
	require.EqualValues(t, 3, fields["new_unlocks_stored_before"])
	require.Equal(t, []any{int64(9)}, fields["new_unlocks_added"])
	require.Equal(t, []any{int64(1)}, fields["new_unlocks_removed"])
	require.EqualValues(t, 5, fields["clear_new_unlocks_version"])
	require.Equal(t, "persisted", fields["outcome"])
}

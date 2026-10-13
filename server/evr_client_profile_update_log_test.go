package server

import (
	"context"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

// TestUpdateClientProfileRequest_DroppedUpdateLogsWarn pins the operator record
// for a client profile update that was not persisted (AGENTS.md logging rule 6):
// exactly one WARN carrying the error, version_conflict, the request's
// new_unlocks count and its clear_new_unlocks_version, and the client is sent
// UpdateProfileFailure. The failure is driven through updateClientProfileRequest.
//
// EvrPipeline.nk is the concrete *RuntimeGoNakamaModule, so no in-memory fake can
// stand in for it here. The module is real and its database is unreachable
// (stubDB), so the profile load fails: a non-conflict failure, version_conflict
// false. The conflict=true value is not asserted through the handler; the
// conflict flag itself is isVersionConflictError, covered in
// evr_account_update_retry_test.go.
func TestUpdateClientProfileRequest_DroppedUpdateLogsWarn(t *testing.T) {
	const msg = "Client profile update was not persisted"
	groupID := uuid.Must(uuid.NewV4())

	nk := &RuntimeGoNakamaModule{logger: zap.NewNop(), metrics: &testMetrics{}, storageIndex: emptyStorageIndex{}, db: stubDB(t)}
	registry := &GuildGroupRegistry{
		guildGroups:    atomic.NewPointer(&map[string]*GuildGroup{groupID.String(): {}}),
		inheritanceMap: atomic.NewPointer(&map[string][]string{}),
	}
	p := &EvrPipeline{nk: nk, db: nk.db, guildGroupRegistry: registry}

	session, sent := newNilIdentityConfigSession(t, p)
	session.userID = uuid.Must(uuid.NewV4())

	params := &SessionParameters{profile: &EVRProfile{ActiveGroupID: groupID.String()}}
	ctx := context.WithValue(session.ctx, ctxSessionParametersKey{}, atomic.NewPointer(params))

	core, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)

	request := &evr.UpdateClientProfile{
		Payload: evr.ClientProfile{
			NewUnlocks:    []int64{7, 8},
			Customization: &evr.Customization{ClearNewUnlocksVersion: 5},
		},
	}

	require.NoError(t, p.updateClientProfileRequest(ctx, logger, session, request))

	warns := logs.FilterMessage(msg).All()
	require.Len(t, warns, 1, "exactly one %q record; all records: %v", msg, logs.All())
	require.Equal(t, zapcore.WarnLevel, warns[0].Level)
	fields := warns[0].ContextMap()
	require.NotEmpty(t, fields["error"], "the record must carry the error")
	require.Equal(t, false, fields["version_conflict"])
	require.EqualValues(t, 2, fields["new_unlocks"])
	require.EqualValues(t, 5, fields["clear_new_unlocks_version"])

	// sendEvrHook makes the handler's sends observable: the failure first, then
	// the existing success message.
	require.Len(t, *sent, 2)
	failure, ok := (*sent)[0].(*evr.UpdateProfileFailure)
	require.True(t, ok, "first message sent must be UpdateProfileFailure, got %T", (*sent)[0])
	require.NotEmpty(t, failure.Message)
	require.IsType(t, &evr.UpdateProfileSuccess{}, (*sent)[1])
}

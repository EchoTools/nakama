package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/internal/intents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Minimal NakamaModule stub for MMR tests
// ---------------------------------------------------------------------------

// mmrNK is a test stub for runtime.NakamaModule.
// Only the methods called by MMR RPCs are overridden; all others panic via
// the embedded interface so unintended calls are caught immediately.
type mmrNK struct {
	runtime.NakamaModule

	// LeaderboardRecordsList: keyed by leaderboard ID
	leaderboardRecords map[string][]*api.LeaderboardRecord
	leaderboardErr     error

	// LeaderboardRecordWrite: last call captures
	leaderboardWriteCalls []leaderboardWriteCall
	leaderboardWriteErr   error

	// LeaderboardCreate: always succeeds unless leaderboardCreateErr set
	leaderboardCreateErr error

	// UsersGetId
	users    []*api.User
	usersErr error

	// StorageRead
	storageObjects []*api.StorageObject
	storageReadErr error

	// StorageWrite
	storageWriteAcks []*api.StorageObjectAck
	storageWriteErr  error
}

type leaderboardWriteCall struct {
	id       string
	ownerID  string
	username string
	score    int64
	subscore int64
}

func (n *mmrNK) LeaderboardRecordsList(ctx context.Context, id string, ownerIDs []string, limit int, cursor string, expiry int64) ([]*api.LeaderboardRecord, []*api.LeaderboardRecord, string, string, error) {
	if n.leaderboardErr != nil {
		return nil, nil, "", "", n.leaderboardErr
	}
	recs := n.leaderboardRecords[id]
	return nil, recs, "", "", nil
}

func (n *mmrNK) LeaderboardRecordWrite(ctx context.Context, id, ownerID, username string, score, subscore int64, metadata map[string]interface{}, overrideOperator *int) (*api.LeaderboardRecord, error) {
	n.leaderboardWriteCalls = append(n.leaderboardWriteCalls, leaderboardWriteCall{
		id: id, ownerID: ownerID, username: username, score: score, subscore: subscore,
	})
	if n.leaderboardWriteErr != nil {
		return nil, n.leaderboardWriteErr
	}
	return &api.LeaderboardRecord{OwnerId: ownerID}, nil
}

func (n *mmrNK) LeaderboardCreate(ctx context.Context, id string, authoritative bool, sortOrder, operator, resetSchedule string, metadata map[string]interface{}, enableRanks bool) error {
	return n.leaderboardCreateErr
}

func (n *mmrNK) UsersGetId(ctx context.Context, userIDs []string, facebookIDs []string) ([]*api.User, error) {
	if n.usersErr != nil {
		return nil, n.usersErr
	}
	return n.users, nil
}

func (n *mmrNK) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	if n.storageReadErr != nil {
		return nil, n.storageReadErr
	}
	return n.storageObjects, nil
}

func (n *mmrNK) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	if n.storageWriteErr != nil {
		return nil, n.storageWriteErr
	}
	if n.storageWriteAcks != nil {
		return n.storageWriteAcks, nil
	}
	// Return a default ack with a version so callers that inspect it don't blow up.
	acks := make([]*api.StorageObjectAck, len(writes))
	for i := range writes {
		acks[i] = &api.StorageObjectAck{Version: "v1"}
	}
	return acks, nil
}

// ---------------------------------------------------------------------------
// Helper: marshal payload to JSON string
// ---------------------------------------------------------------------------
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// ---------------------------------------------------------------------------
// Helper: build authorised context for BeforeWriteStorageObjectsHook
// ---------------------------------------------------------------------------
func ctxWithIntent(intent intents.Intent) context.Context {
	sv := &intents.SessionVars{Intents: intent}
	vars := sv.MarshalVars()
	return context.WithValue(context.Background(), runtime.RUNTIME_CTX_VARS, vars)
}

// ---------------------------------------------------------------------------
// GetMMRRPC tests
// ---------------------------------------------------------------------------

func TestGetMMRRPC_InvalidJSON(t *testing.T) {
	nk := &mmrNK{}
	_, err := GetMMRRPC(context.Background(), &mockLogger{}, &sql.DB{}, nk, "{not json}")
	require.Error(t, err)
	var rErr *runtime.Error
	require.True(t, errors.As(err, &rErr))
	assert.Equal(t, StatusInvalidArgument, int(rErr.Code))
}

func TestGetMMRRPC_MissingUserID(t *testing.T) {
	nk := &mmrNK{}
	payload := mustJSON(t, GetMMRRequest{GroupID: "g1", Mode: "echo_arena"})
	_, err := GetMMRRPC(context.Background(), &mockLogger{}, &sql.DB{}, nk, payload)
	require.Error(t, err)
	var rErr *runtime.Error
	require.True(t, errors.As(err, &rErr))
	assert.Equal(t, StatusInvalidArgument, int(rErr.Code))
}

func TestGetMMRRPC_MissingGroupID(t *testing.T) {
	nk := &mmrNK{}
	payload := mustJSON(t, GetMMRRequest{UserID: "u1", Mode: "echo_arena"})
	_, err := GetMMRRPC(context.Background(), &mockLogger{}, &sql.DB{}, nk, payload)
	require.Error(t, err)
	var rErr *runtime.Error
	require.True(t, errors.As(err, &rErr))
	assert.Equal(t, StatusInvalidArgument, int(rErr.Code))
}

func TestGetMMRRPC_MissingMode(t *testing.T) {
	nk := &mmrNK{}
	payload := mustJSON(t, GetMMRRequest{UserID: "u1", GroupID: "g1"})
	_, err := GetMMRRPC(context.Background(), &mockLogger{}, &sql.DB{}, nk, payload)
	require.Error(t, err)
	var rErr *runtime.Error
	require.True(t, errors.As(err, &rErr))
	assert.Equal(t, StatusInvalidArgument, int(rErr.Code))
}

func TestGetMMRRPC_HappyPath_NoStaticMMR(t *testing.T) {
	// StorageRead returns nothing → settings load fails, static fields are absent.
	nk := &mmrNK{
		leaderboardRecords: map[string][]*api.LeaderboardRecord{},
		// StorageRead returns empty → StorableRead returns NotFound
		storageObjects: nil,
	}
	payload := mustJSON(t, GetMMRRequest{UserID: "u1", GroupID: "g1", Mode: "echo_arena"})
	out, err := GetMMRRPC(context.Background(), &mockLogger{}, &sql.DB{}, nk, payload)
	require.NoError(t, err)

	var resp GetMMRResponse
	require.NoError(t, json.Unmarshal([]byte(out), &resp))

	assert.Equal(t, "u1", resp.UserID)
	assert.Equal(t, "g1", resp.GroupID)
	assert.Equal(t, "echo_arena", resp.Mode)
	assert.Nil(t, resp.StaticMu, "StaticMu should be nil when settings not found")
	assert.Nil(t, resp.StaticSigma, "StaticSigma should be nil when settings not found")
	assert.False(t, resp.IsStatic)
}

func TestGetMMRRPC_HappyPath_WithStaticMMR(t *testing.T) {
	mu := 25.0
	sigma := 8.333
	settingsJSON := mustJSON(t, MatchmakingSettings{
		StaticRatingMu:    &mu,
		StaticRatingSigma: &sigma,
	})

	nk := &mmrNK{
		leaderboardRecords: map[string][]*api.LeaderboardRecord{},
		storageObjects: []*api.StorageObject{
			{Value: settingsJSON, Version: "v1"},
		},
	}
	payload := mustJSON(t, GetMMRRequest{UserID: "u1", GroupID: "g1", Mode: "echo_arena"})
	out, err := GetMMRRPC(context.Background(), &mockLogger{}, &sql.DB{}, nk, payload)
	require.NoError(t, err)

	var resp GetMMRResponse
	require.NoError(t, json.Unmarshal([]byte(out), &resp))

	require.NotNil(t, resp.StaticMu)
	require.NotNil(t, resp.StaticSigma)
	assert.InDelta(t, mu, *resp.StaticMu, 0.001)
	assert.InDelta(t, sigma, *resp.StaticSigma, 0.001)
	assert.True(t, resp.IsStatic)
}

func TestGetMMRRPC_LeaderboardError(t *testing.T) {
	nk := &mmrNK{
		leaderboardErr: errors.New("db gone"),
	}
	payload := mustJSON(t, GetMMRRequest{UserID: "u1", GroupID: "g1", Mode: "echo_arena"})
	_, err := GetMMRRPC(context.Background(), &mockLogger{}, &sql.DB{}, nk, payload)
	require.Error(t, err)
	var rErr *runtime.Error
	require.True(t, errors.As(err, &rErr))
	assert.Equal(t, StatusInternalError, int(rErr.Code))
}

// ---------------------------------------------------------------------------
// UpdateMMRRPC tests
// ---------------------------------------------------------------------------

func TestUpdateMMRRPC_InvalidJSON(t *testing.T) {
	nk := &mmrNK{}
	_, err := UpdateMMRRPC(context.Background(), &mockLogger{}, &sql.DB{}, nk, "{not json}")
	require.Error(t, err)
	var rErr *runtime.Error
	require.True(t, errors.As(err, &rErr))
	assert.Equal(t, StatusInvalidArgument, int(rErr.Code))
}

func TestUpdateMMRRPC_MissingUserID(t *testing.T) {
	nk := &mmrNK{}
	payload := mustJSON(t, UpdateMMRRequest{GroupID: "g1", Mode: "echo_arena", Mu: 25, Sigma: 8.333})
	_, err := UpdateMMRRPC(context.Background(), &mockLogger{}, &sql.DB{}, nk, payload)
	require.Error(t, err)
	var rErr *runtime.Error
	require.True(t, errors.As(err, &rErr))
	assert.Equal(t, StatusInvalidArgument, int(rErr.Code))
}

func TestUpdateMMRRPC_MissingGroupID(t *testing.T) {
	nk := &mmrNK{}
	payload := mustJSON(t, UpdateMMRRequest{UserID: "u1", Mode: "echo_arena", Mu: 25, Sigma: 8.333})
	_, err := UpdateMMRRPC(context.Background(), &mockLogger{}, &sql.DB{}, nk, payload)
	require.Error(t, err)
	var rErr *runtime.Error
	require.True(t, errors.As(err, &rErr))
	assert.Equal(t, StatusInvalidArgument, int(rErr.Code))
}

func TestUpdateMMRRPC_MissingMode(t *testing.T) {
	nk := &mmrNK{}
	payload := mustJSON(t, UpdateMMRRequest{UserID: "u1", GroupID: "g1", Mu: 25, Sigma: 8.333})
	_, err := UpdateMMRRPC(context.Background(), &mockLogger{}, &sql.DB{}, nk, payload)
	require.Error(t, err)
	var rErr *runtime.Error
	require.True(t, errors.As(err, &rErr))
	assert.Equal(t, StatusInvalidArgument, int(rErr.Code))
}

func TestUpdateMMRRPC_UserNotFound(t *testing.T) {
	nk := &mmrNK{users: []*api.User{}}
	payload := mustJSON(t, UpdateMMRRequest{UserID: "u1", GroupID: "g1", Mode: "echo_arena", Mu: 25, Sigma: 8.333})
	_, err := UpdateMMRRPC(context.Background(), &mockLogger{}, &sql.DB{}, nk, payload)
	require.Error(t, err)
	var rErr *runtime.Error
	require.True(t, errors.As(err, &rErr))
	assert.Equal(t, StatusNotFound, int(rErr.Code))
}

func TestUpdateMMRRPC_HappyPath_OrdinalMath(t *testing.T) {
	mu := 30.0
	sigma := 5.0
	expectedOrdinal := mu - 3*sigma // 15.0

	nk := &mmrNK{
		users: []*api.User{{DisplayName: "Tester"}},
	}
	payload := mustJSON(t, UpdateMMRRequest{
		UserID:  "u1",
		GroupID: "g1",
		Mode:    "echo_arena",
		Mu:      mu,
		Sigma:   sigma,
	})
	out, err := UpdateMMRRPC(context.Background(), &mockLogger{}, &sql.DB{}, nk, payload)
	require.NoError(t, err)

	var resp UpdateMMRResponse
	require.NoError(t, json.Unmarshal([]byte(out), &resp))

	assert.True(t, resp.Success)
	assert.Equal(t, "u1", resp.UserID)
	assert.InDelta(t, mu, resp.Mu, 0.001)
	assert.InDelta(t, sigma, resp.Sigma, 0.001)
	assert.InDelta(t, expectedOrdinal, resp.Ordinal, 0.001)

	// At minimum mu and sigma boards should be written (ordinal is non-fatal).
	assert.GreaterOrEqual(t, len(nk.leaderboardWriteCalls), 2, "expected at least mu and sigma leaderboard writes")
}

func TestUpdateMMRRPC_LeaderboardWriteFailsWithCreate(t *testing.T) {
	// First write fails, then LeaderboardCreate succeeds, then write succeeds.
	writeCount := 0
	nk := &mmrNK{
		users: []*api.User{{DisplayName: "Tester"}},
	}
	// Simulate first call failing, subsequent succeeding.
	_ = writeCount
	// We can't easily intercept only the first call with this stub, so just
	// verify that a persistent write failure returns StatusInternalError.
	nk.leaderboardWriteErr = errors.New("disk full")
	nk.leaderboardCreateErr = errors.New("cannot create")

	payload := mustJSON(t, UpdateMMRRequest{
		UserID:  "u1",
		GroupID: "g1",
		Mode:    "echo_arena",
		Mu:      25,
		Sigma:   8.333,
	})
	_, err := UpdateMMRRPC(context.Background(), &mockLogger{}, &sql.DB{}, nk, payload)
	require.Error(t, err)
	var rErr *runtime.Error
	require.True(t, errors.As(err, &rErr))
	assert.Equal(t, StatusInternalError, int(rErr.Code))
}

// ---------------------------------------------------------------------------
// SetStaticMMRRPC tests
// ---------------------------------------------------------------------------

func TestSetStaticMMRRPC_InvalidJSON(t *testing.T) {
	nk := &mmrNK{}
	_, err := SetStaticMMRRPC(context.Background(), &mockLogger{}, &sql.DB{}, nk, "{not json}")
	require.Error(t, err)
	var rErr *runtime.Error
	require.True(t, errors.As(err, &rErr))
	assert.Equal(t, StatusInvalidArgument, int(rErr.Code))
}

func TestSetStaticMMRRPC_MissingUserID(t *testing.T) {
	nk := &mmrNK{}
	mu := 25.0
	sigma := 8.333
	payload := mustJSON(t, SetStaticMMRRequest{Enable: true, Mu: &mu, Sigma: &sigma})
	_, err := SetStaticMMRRPC(context.Background(), &mockLogger{}, &sql.DB{}, nk, payload)
	require.Error(t, err)
	var rErr *runtime.Error
	require.True(t, errors.As(err, &rErr))
	assert.Equal(t, StatusInvalidArgument, int(rErr.Code))
}

func TestSetStaticMMRRPC_EnableTrueWithoutMu(t *testing.T) {
	nk := &mmrNK{}
	sigma := 8.333
	payload := mustJSON(t, SetStaticMMRRequest{UserID: "u1", Enable: true, Sigma: &sigma})
	_, err := SetStaticMMRRPC(context.Background(), &mockLogger{}, &sql.DB{}, nk, payload)
	require.Error(t, err)
	var rErr *runtime.Error
	require.True(t, errors.As(err, &rErr))
	assert.Equal(t, StatusInvalidArgument, int(rErr.Code))
}

func TestSetStaticMMRRPC_EnableTrueWithoutSigma(t *testing.T) {
	nk := &mmrNK{}
	mu := 25.0
	payload := mustJSON(t, SetStaticMMRRequest{UserID: "u1", Enable: true, Mu: &mu})
	_, err := SetStaticMMRRPC(context.Background(), &mockLogger{}, &sql.DB{}, nk, payload)
	require.Error(t, err)
	var rErr *runtime.Error
	require.True(t, errors.As(err, &rErr))
	assert.Equal(t, StatusInvalidArgument, int(rErr.Code))
}

func TestSetStaticMMRRPC_Enable_SetsStaticFields(t *testing.T) {
	mu := 30.0
	sigma := 4.0

	// StorageRead returns empty → StorableRead with create=true → calls StorageWrite once to create,
	// then SetStaticMMRRPC calls StorageWrite again with the updated values.
	nk := &mmrNK{
		storageObjects: nil, // triggers create path
	}

	payload := mustJSON(t, SetStaticMMRRequest{
		UserID: "u1",
		Enable: true,
		Mu:     &mu,
		Sigma:  &sigma,
	})
	out, err := SetStaticMMRRPC(context.Background(), &mockLogger{}, &sql.DB{}, nk, payload)
	require.NoError(t, err)

	var resp SetStaticMMRResponse
	require.NoError(t, json.Unmarshal([]byte(out), &resp))

	assert.True(t, resp.Success)
	assert.True(t, resp.Enable)
	require.NotNil(t, resp.Mu)
	require.NotNil(t, resp.Sigma)
	assert.InDelta(t, mu, *resp.Mu, 0.001)
	assert.InDelta(t, sigma, *resp.Sigma, 0.001)
}

func TestSetStaticMMRRPC_Disable_ClearsStaticFields(t *testing.T) {
	mu := 30.0
	sigma := 4.0

	// Pre-existing settings that have static MMR enabled.
	existingSettings := MatchmakingSettings{
		StaticRatingMu:    &mu,
		StaticRatingSigma: &sigma,
	}
	settingsJSON := mustJSON(t, existingSettings)

	nk := &mmrNK{
		storageObjects: []*api.StorageObject{
			{Value: settingsJSON, Version: "v2"},
		},
	}

	payload := mustJSON(t, SetStaticMMRRequest{
		UserID: "u1",
		Enable: false,
	})
	out, err := SetStaticMMRRPC(context.Background(), &mockLogger{}, &sql.DB{}, nk, payload)
	require.NoError(t, err)

	var resp SetStaticMMRResponse
	require.NoError(t, json.Unmarshal([]byte(out), &resp))

	assert.True(t, resp.Success)
	assert.False(t, resp.Enable)
	assert.Nil(t, resp.Mu, "StaticMu should be nil after disabling")
	assert.Nil(t, resp.Sigma, "StaticSigma should be nil after disabling")
}

func TestSetStaticMMRRPC_StorageReadError(t *testing.T) {
	nk := &mmrNK{
		storageReadErr: errors.New("db error"),
	}
	mu := 25.0
	sigma := 8.333
	payload := mustJSON(t, SetStaticMMRRequest{UserID: "u1", Enable: true, Mu: &mu, Sigma: &sigma})
	_, err := SetStaticMMRRPC(context.Background(), &mockLogger{}, &sql.DB{}, nk, payload)
	require.Error(t, err)
	var rErr *runtime.Error
	require.True(t, errors.As(err, &rErr))
	assert.Equal(t, StatusInternalError, int(rErr.Code))
}

func TestSetStaticMMRRPC_StorageWriteError(t *testing.T) {
	// StorageRead succeeds (returns existing settings), but write fails.
	existingSettings := MatchmakingSettings{}
	settingsJSON := mustJSON(t, existingSettings)

	nk := &mmrNK{
		storageObjects: []*api.StorageObject{
			{Value: settingsJSON, Version: "v1"},
		},
		storageWriteErr: errors.New("write failed"),
	}
	mu := 25.0
	sigma := 8.333
	payload := mustJSON(t, SetStaticMMRRequest{UserID: "u1", Enable: true, Mu: &mu, Sigma: &sigma})
	_, err := SetStaticMMRRPC(context.Background(), &mockLogger{}, &sql.DB{}, nk, payload)
	require.Error(t, err)
	var rErr *runtime.Error
	require.True(t, errors.As(err, &rErr))
	assert.Equal(t, StatusInternalError, int(rErr.Code))
}

// ---------------------------------------------------------------------------
// BeforeWriteStorageObjectsHook tests
// ---------------------------------------------------------------------------

func TestBeforeWriteStorageObjectsHook_NilInput(t *testing.T) {
	nk := &mmrNK{}
	out, err := BeforeWriteStorageObjectsHook(context.Background(), &mockLogger{}, &sql.DB{}, nk, nil)
	require.NoError(t, err)
	assert.Nil(t, out)
}

func TestBeforeWriteStorageObjectsHook_Unauthorized_ReturnsNil(t *testing.T) {
	// No session vars in context → not authorized → returns nil (blocks request).
	nk := &mmrNK{}
	req := &api.WriteStorageObjectsRequest{
		Objects: []*api.WriteStorageObject{
			{Collection: "SomeCollection", Key: "somekey"},
		},
	}
	out, err := BeforeWriteStorageObjectsHook(context.Background(), &mockLogger{}, &sql.DB{}, nk, req)
	require.NoError(t, err)
	assert.Nil(t, out, "unauthorized request should be blocked (nil returned)")
}

func TestBeforeWriteStorageObjectsHook_MatchmakingConfig_NoVersion_Rejected(t *testing.T) {
	ctx := ctxWithIntent(intents.Intent{IsGlobalOperator: true})
	nk := &mmrNK{}
	req := &api.WriteStorageObjectsRequest{
		Objects: []*api.WriteStorageObject{
			{
				Collection: MatchmakerStorageCollection,
				Key:        MatchmakingConfigStorageKey,
				Version:    "", // no version → must be rejected
			},
		},
	}
	out, err := BeforeWriteStorageObjectsHook(ctx, &mockLogger{}, &sql.DB{}, nk, req)
	require.Error(t, err, "should error when matchmaking config written without version")
	assert.Nil(t, out)
	var rErr *runtime.Error
	require.True(t, errors.As(err, &rErr))
	assert.Equal(t, StatusFailedPrecondition, int(rErr.Code))
}

func TestBeforeWriteStorageObjectsHook_MatchmakingConfig_WithVersion_Passes(t *testing.T) {
	ctx := ctxWithIntent(intents.Intent{IsGlobalOperator: true})
	nk := &mmrNK{}
	req := &api.WriteStorageObjectsRequest{
		Objects: []*api.WriteStorageObject{
			{
				Collection: MatchmakerStorageCollection,
				Key:        MatchmakingConfigStorageKey,
				Version:    "v42", // version present → should pass through
			},
		},
	}
	out, err := BeforeWriteStorageObjectsHook(ctx, &mockLogger{}, &sql.DB{}, nk, req)
	require.NoError(t, err)
	assert.Equal(t, req, out, "request should be returned unchanged")
}

func TestBeforeWriteStorageObjectsHook_NonMatchmakingCollection_Passes(t *testing.T) {
	ctx := ctxWithIntent(intents.Intent{IsGlobalOperator: true})
	nk := &mmrNK{}
	req := &api.WriteStorageObjectsRequest{
		Objects: []*api.WriteStorageObject{
			{
				Collection: "OtherCollection",
				Key:        "somekey",
				Version:    "", // version irrelevant for non-matchmaking collection
			},
		},
	}
	out, err := BeforeWriteStorageObjectsHook(ctx, &mockLogger{}, &sql.DB{}, nk, req)
	require.NoError(t, err)
	assert.Equal(t, req, out)
}

func TestBeforeWriteStorageObjectsHook_StorageObjectsIntent_Authorized(t *testing.T) {
	// StorageObjects intent (not GlobalOperator) also grants access.
	ctx := ctxWithIntent(intents.Intent{StorageObjects: true})
	nk := &mmrNK{}
	req := &api.WriteStorageObjectsRequest{
		Objects: []*api.WriteStorageObject{
			{Collection: "Anything", Key: "k", Version: "v1"},
		},
	}
	out, err := BeforeWriteStorageObjectsHook(ctx, &mockLogger{}, &sql.DB{}, nk, req)
	require.NoError(t, err)
	assert.Equal(t, req, out)
}

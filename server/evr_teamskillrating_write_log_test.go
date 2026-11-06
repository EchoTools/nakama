package server

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

const ratingLogMsg = "TeamSkillRating board write"

// flakyWriteNK fails the first failFirst LeaderboardRecordWrite calls, then
// succeeds; failAll makes every write fail. Leaf methods on a stub that embeds
// mmrNK: the code under test calls through runtime.NakamaModule, which
// dispatches to this outermost type.
type flakyWriteNK struct {
	*mmrNK
	failFirst int
	failAll   bool
	writes    int
}

func (n *flakyWriteNK) LeaderboardRecordWrite(ctx context.Context, id, ownerID, username string, score, subscore int64, md map[string]any, op *int) (*api.LeaderboardRecord, error) {
	n.writes++
	if n.failAll || n.writes <= n.failFirst {
		return nil, errors.New("write refused")
	}
	return &api.LeaderboardRecord{OwnerId: ownerID}, nil
}

func ratingEntry(t *testing.T) *StatisticsQueueEntry {
	t.Helper()
	score, sub, err := Float64ToScore(31.5)
	require.NoError(t, err)
	return &StatisticsQueueEntry{
		BoardMeta: LeaderboardMeta{GroupID: "g1", Mode: evr.ToSymbol("echo_arena"), StatName: TeamSkillRatingMuStatisticID, Operator: OperatorSet, ResetSchedule: evr.ResetScheduleAllTime},
		UserID:    "u1", DisplayName: "synthetic", Score: score, Subscore: sub,
		Writer: StatisticsWriterRemoteLog, MatchID: "m1.node",
	}
}

func observed() (runtime.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(zap.InfoLevel)
	return NewRuntimeGoLogger(zap.New(core)), logs
}

func TestTeamSkillRatingWrite_FailedWriteIsNotLoggedAsWritten(t *testing.T) {
	logger, logs := observed()
	nk := &flakyWriteNK{mmrNK: &mmrNK{}, failAll: true}
	processSingleEntry(context.Background(), logger, nk, ratingEntry(t))

	require.Zero(t, logs.FilterMessage(ratingLogMsg).Len(), "a failed write must not read as a landed write: %v", logs.All())
	errs := logs.FilterMessage("Failed to write leaderboard record").All()
	require.Len(t, errs, 1, "logged: %v", logs.All())
	m := errs[0].ContextMap()
	require.Equal(t, "u1", m["user_id"])
	require.Contains(t, m, "leaderboard_id")
	require.Equal(t, "write refused", m["error"])
}

func TestTeamSkillRatingWrite_LoggedAfterCreateAndRetry(t *testing.T) {
	logger, logs := observed()
	nk := &flakyWriteNK{mmrNK: &mmrNK{}, failFirst: 1}
	processSingleEntry(context.Background(), logger, nk, ratingEntry(t))

	require.Equal(t, 2, nk.writes)
	recs := logs.FilterMessage(ratingLogMsg).All()
	require.Len(t, recs, 1, "logged: %v", logs.All())
	t.Logf("record: %v", recs[0].ContextMap())
	require.Equal(t, StatisticsWriterRemoteLog, recs[0].ContextMap()["writer"])
}

func TestUpdateMMRRPC_TeamSkillRatingWritesAreLogged(t *testing.T) {
	logger, logs := observed()
	nk := &mmrNK{users: []*api.User{{DisplayName: "synthetic"}}}
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, "operator-1") //nolint:staticcheck // runtime.RUNTIME_CTX_USER_ID is a string key by the nakama-common API
	payload := mustJSON(t, UpdateMMRRequest{UserID: "u1", GroupID: "g1", Mode: "echo_arena", Mu: 30, Sigma: 5})
	_, err := UpdateMMRRPC(ctx, logger, &sql.DB{}, nk, payload)
	require.NoError(t, err)

	byStat := map[string]map[string]any{}
	for _, e := range logs.FilterMessage(ratingLogMsg).All() {
		m := e.ContextMap()
		t.Logf("record: %v", m)
		byStat[m["stat"].(string)] = m
	}
	for stat, want := range map[string]float64{TeamSkillRatingMuStatisticID: 30, TeamSkillRatingSigmaStatisticID: 5, TeamSkillRatingOrdinalStatisticID: 15} {
		m := byStat[stat]
		require.NotNil(t, m, "no record for %s; logged: %v", stat, logs.All())
		require.Equal(t, StatisticsWriterMMRRPC, m["writer"])
		require.Equal(t, "operator-1", m["operator_id"])
		require.Equal(t, "u1", m["user_id"])
		require.Contains(t, m, "leaderboard_id")
		require.InDelta(t, want, m["new_value"], 0.01)
	}
}

func TestUpdateMMRRPC_FailedWriteIsNotLogged(t *testing.T) {
	logger, logs := observed()
	nk := &mmrNK{users: []*api.User{{DisplayName: "synthetic"}}, leaderboardWriteErr: errors.New("disk full"), leaderboardCreateErr: errors.New("nope")}
	payload := mustJSON(t, UpdateMMRRequest{UserID: "u1", GroupID: "g1", Mode: "echo_arena", Mu: 30, Sigma: 5})
	_, err := UpdateMMRRPC(context.Background(), logger, &sql.DB{}, nk, payload)
	require.Error(t, err)
	require.Zero(t, logs.FilterMessage(ratingLogMsg).Len(), "logged: %v", logs.All())
}

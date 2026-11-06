package server

import (
	"context"
	"database/sql"
	"errors"
	"strings"
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

	// logs, when set, makes the stub note how many rating records existed at
	// each write attempt.
	logs           *observer.ObservedLogs
	recordsAtWrite []int
}

func (n *flakyWriteNK) LeaderboardRecordWrite(ctx context.Context, id, ownerID, username string, score, subscore int64, md map[string]any, op *int) (*api.LeaderboardRecord, error) {
	n.writes++
	if n.logs != nil {
		n.recordsAtWrite = append(n.recordsAtWrite, n.logs.FilterMessage(ratingLogMsg).Len())
	}
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
	nk := &flakyWriteNK{mmrNK: &mmrNK{}, failFirst: 1, logs: logs}
	e := ratingEntry(t)
	processSingleEntry(context.Background(), logger, nk, e)

	require.Equal(t, 2, nk.writes)
	// No record may exist at any write attempt: it is emitted only after the
	// write that lands.
	require.Equal(t, []int{0, 0}, nk.recordsAtWrite, "the record was emitted before the write landed")
	recs := logs.FilterMessage(ratingLogMsg).All()
	require.Len(t, recs, 1, "logged: %v", logs.All())
	m := recs[0].ContextMap()
	t.Logf("record: %v", m)
	require.Equal(t, StatisticsWriterRemoteLog, m["writer"])
	require.InDelta(t, 31.5, m["new_value"], 1e-9)
	require.Equal(t, e.BoardMeta.ID(), m["leaderboard_id"])
}

func TestTeamSkillRatingWrite_UndecodablePreviousIsNotKnown(t *testing.T) {
	logger, logs := observed()
	e := ratingEntry(t)
	nk := &flakyWriteNK{mmrNK: &mmrNK{leaderboardRecords: map[string][]*api.LeaderboardRecord{
		e.BoardMeta.ID(): {{OwnerId: "u1", Score: -1, Subscore: 0}},
	}}}
	processSingleEntry(context.Background(), logger, nk, e)

	recs := logs.FilterMessage(ratingLogMsg).All()
	require.Len(t, recs, 1, "logged: %v", logs.All())
	m := recs[0].ContextMap()
	t.Logf("record: %v", m)
	require.Equal(t, false, m["previous_known"], "an undecodable previous record must not read as 'no record'")
	require.NotContains(t, m, "previous_value")
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

// boardWriteNK fails writes per board (matched by substring of the board id):
// failures[sub] is how many writes to a matching board fail before one lands.
type boardWriteNK struct {
	*mmrNK
	failures map[string]int
}

func (n *boardWriteNK) LeaderboardRecordWrite(ctx context.Context, id, ownerID, username string, score, subscore int64, md map[string]any, op *int) (*api.LeaderboardRecord, error) {
	for sub, left := range n.failures {
		if left > 0 && strings.Contains(id, sub) {
			n.failures[sub]--
			return nil, errors.New("write refused")
		}
	}
	return &api.LeaderboardRecord{OwnerId: ownerID}, nil
}

func ratingRecordStats(logs *observer.ObservedLogs) []string {
	var out []string
	for _, e := range logs.FilterMessage(ratingLogMsg).All() {
		out = append(out, e.ContextMap()["stat"].(string))
	}
	return out
}

func runUpdateMMR(t *testing.T, nk runtime.NakamaModule) (*observer.ObservedLogs, error) {
	t.Helper()
	logger, logs := observed()
	payload := mustJSON(t, UpdateMMRRequest{UserID: "u1", GroupID: "g1", Mode: "echo_arena", Mu: 30, Sigma: 5})
	_, err := UpdateMMRRPC(context.Background(), logger, &sql.DB{}, nk, payload)
	return logs, err
}

func TestUpdateMMRRPC_SigmaFailsAfterMuLands(t *testing.T) {
	nk := &boardWriteNK{
		mmrNK:    &mmrNK{users: []*api.User{{DisplayName: "synthetic"}}, leaderboardCreateErr: errors.New("nope")},
		failures: map[string]int{TeamSkillRatingSigmaStatisticID: 99},
	}
	logs, err := runUpdateMMR(t, nk)
	require.Error(t, err)
	// Create failed, so the Sigma error line is the create failure.
	require.Len(t, logs.FilterMessage("Failed to create Sigma leaderboard").All(), 1, "logged: %v", logs.All())
	// Mu landed and is recorded; Sigma and Ordinal never landed and are not.
	require.Equal(t, []string{TeamSkillRatingMuStatisticID}, ratingRecordStats(logs), "logged: %v", logs.All())
}

func TestUpdateMMRRPC_SigmaRetryFailsAfterMuLands(t *testing.T) {
	nk := &boardWriteNK{
		mmrNK:    &mmrNK{users: []*api.User{{DisplayName: "synthetic"}}},
		failures: map[string]int{TeamSkillRatingSigmaStatisticID: 99},
	}
	logs, err := runUpdateMMR(t, nk)
	require.Error(t, err)
	require.Len(t, logs.FilterMessage("Failed to write Sigma leaderboard record").All(), 1, "logged: %v", logs.All())
	require.Equal(t, []string{TeamSkillRatingMuStatisticID}, ratingRecordStats(logs), "logged: %v", logs.All())
}

func TestUpdateMMRRPC_OrdinalWriteFailsIsNotLogged(t *testing.T) {
	nk := &boardWriteNK{
		mmrNK:    &mmrNK{users: []*api.User{{DisplayName: "synthetic"}}, leaderboardCreateErr: errors.New("nope")},
		failures: map[string]int{TeamSkillRatingOrdinalStatisticID: 99},
	}
	logs, err := runUpdateMMR(t, nk)
	require.NoError(t, err, "the ordinal write is non-fatal")
	require.Equal(t, []string{TeamSkillRatingMuStatisticID, TeamSkillRatingSigmaStatisticID}, ratingRecordStats(logs), "logged: %v", logs.All())
}

func TestUpdateMMRRPC_OrdinalCreateAndRetryIsLogged(t *testing.T) {
	nk := &boardWriteNK{
		mmrNK:    &mmrNK{users: []*api.User{{DisplayName: "synthetic"}}},
		failures: map[string]int{TeamSkillRatingOrdinalStatisticID: 1},
	}
	logs, err := runUpdateMMR(t, nk)
	require.NoError(t, err)
	require.Equal(t, []string{TeamSkillRatingMuStatisticID, TeamSkillRatingSigmaStatisticID, TeamSkillRatingOrdinalStatisticID}, ratingRecordStats(logs), "logged: %v", logs.All())
}

func TestUpdateMMRRPC_OrdinalRetryFailsIsNotLogged(t *testing.T) {
	nk := &boardWriteNK{
		mmrNK:    &mmrNK{users: []*api.User{{DisplayName: "synthetic"}}},
		failures: map[string]int{TeamSkillRatingOrdinalStatisticID: 2},
	}
	logs, err := runUpdateMMR(t, nk)
	require.NoError(t, err)
	require.Equal(t, []string{TeamSkillRatingMuStatisticID, TeamSkillRatingSigmaStatisticID}, ratingRecordStats(logs), "logged: %v", logs.All())
}

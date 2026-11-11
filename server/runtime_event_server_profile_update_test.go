package server

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// Synthetic identities only.
const (
	periodRowUserID  = "00000000-0000-4000-8000-0000000d0001"
	periodRowGroupID = "00000000-0000-4000-8000-0000000d0002"
)

// periodRowNK is an in-memory leaderboard store. Its leaderboard methods are
// leaf methods: the code under test calls them through runtime.NakamaModule,
// which dispatches to this outermost type, so they shadow evrTestNakamaModule's
// inert stubs.
type periodRowNK struct {
	*evrTestNakamaModule

	boardsMu sync.Mutex
	boards   map[string]map[string]*api.LeaderboardRecord // board ID -> owner -> record
}

func (n *periodRowNK) LeaderboardCreate(context.Context, string, bool, string, string, string, map[string]any, bool) error {
	return nil
}

func (n *periodRowNK) LeaderboardRecordWrite(_ context.Context, id, ownerID, username string, score, subscore int64, _ map[string]any, operator *int) (*api.LeaderboardRecord, error) {
	if operator == nil || *operator != 2 {
		return nil, errors.New("periodRowNK only models SET writes, which is all the statistics queue issues")
	}
	n.boardsMu.Lock()
	defer n.boardsMu.Unlock()
	if n.boards[id] == nil {
		n.boards[id] = map[string]*api.LeaderboardRecord{}
	}
	rec := &api.LeaderboardRecord{LeaderboardId: id, OwnerId: ownerID, Username: wrapperspb.String(username), Score: score, Subscore: subscore}
	n.boards[id][ownerID] = rec
	return rec, nil
}

func (n *periodRowNK) LeaderboardRecordsList(_ context.Context, id string, ownerIDs []string, _ int, _ string, _ int64) ([]*api.LeaderboardRecord, []*api.LeaderboardRecord, string, string, error) {
	n.boardsMu.Lock()
	defer n.boardsMu.Unlock()
	var owners []*api.LeaderboardRecord
	for _, o := range ownerIDs {
		if rec, ok := n.boards[id][o]; ok {
			owners = append(owners, rec)
		}
	}
	return nil, owners, "", "", nil
}

func (n *periodRowNK) seed(t *testing.T, stat string, r evr.ResetSchedule, v float64) {
	t.Helper()
	score, subscore, err := Float64ToScore(v)
	if err != nil {
		t.Fatal(err)
	}
	set := 2
	if _, err := n.LeaderboardRecordWrite(context.Background(), StatisticBoardID(periodRowGroupID, evr.ModeCombatPublic, stat, r), periodRowUserID, "", score, subscore, nil, &set); err != nil {
		t.Fatal(err)
	}
}

func (n *periodRowNK) row(t *testing.T, stat string, r evr.ResetSchedule) (float64, bool) {
	t.Helper()
	n.boardsMu.Lock()
	defer n.boardsMu.Unlock()
	rec, ok := n.boards[StatisticBoardID(periodRowGroupID, evr.ModeCombatPublic, stat, r)][periodRowUserID]
	if !ok {
		return 0, false
	}
	v, err := ScoreToFloat64(rec.Score, rec.Subscore)
	if err != nil {
		t.Fatal(err)
	}
	return v, true
}

// periodRowDB answers PlayerStatisticsGetID's one query -- the owner's rows on
// a list of boards -- from the same in-memory store. Expiry is not modelled: a
// seeded daily or weekly row is one from the current period.
type periodRowDB struct{ nk *periodRowNK }

func (c periodRowDB) Connect(context.Context) (driver.Conn, error) { return periodRowConn(c), nil }
func (c periodRowDB) Driver() driver.Driver                        { return nil }

type periodRowConn struct{ nk *periodRowNK }

func (c periodRowConn) Prepare(query string) (driver.Stmt, error) {
	if !strings.Contains(query, "leaderboard_record") {
		return nil, errors.New("periodRowDB only answers the leaderboard_record read: " + query)
	}
	return periodRowStmt(c), nil
}
func (c periodRowConn) Close() error              { return nil }
func (c periodRowConn) Begin() (driver.Tx, error) { return nil, errors.New("no transactions") }

// CheckNamedValue lets the []string board-ID list through as-is, the way the
// pgx driver accepts it for `= ANY($2)`.
func (c periodRowConn) CheckNamedValue(*driver.NamedValue) error { return nil }

type periodRowStmt struct{ nk *periodRowNK }

func (s periodRowStmt) Close() error  { return nil }
func (s periodRowStmt) NumInput() int { return -1 }
func (s periodRowStmt) Exec([]driver.Value) (driver.Result, error) {
	return nil, errors.New("read only")
}
func (s periodRowStmt) Query(args []driver.Value) (driver.Rows, error) {
	owner, _ := args[0].(string)
	ids, _ := args[1].([]string)
	rows := &periodRowRows{}
	s.nk.boardsMu.Lock()
	defer s.nk.boardsMu.Unlock()
	for _, id := range ids {
		if rec, ok := s.nk.boards[id][owner]; ok {
			rows.data = append(rows.data, []driver.Value{id, rec.Score, rec.Subscore})
		}
	}
	return rows, nil
}

type periodRowRows struct{ data [][]driver.Value }

func (r *periodRowRows) Columns() []string { return []string{"leaderboard_id", "score", "subscore"} }
func (r *periodRowRows) Close() error      { return nil }
func (r *periodRowRows) Next(dest []driver.Value) error {
	if len(r.data) == 0 {
		return io.EOF
	}
	copy(dest, r.data[0])
	r.data = r.data[1:]
	return nil
}

// TestServerProfileUpdate_PeriodRowsHoldOnlyThatPeriod sends one combat match
// through the real EventServerProfileUpdate.Process for a player with history,
// and checks each leaderboard row. A daily row holds today's total, a weekly
// row this week's, the all-time row everything: each is the match merged into
// that same row's current value. Merging into the all-time value and writing
// the result to all three makes every daily and weekly board an all-time board.
func TestServerProfileUpdate_PeriodRowsHoldOnlyThatPeriod(t *testing.T) {
	nk := &periodRowNK{evrTestNakamaModule: newEvrTestNakamaModule(), boards: map[string]map[string]*api.LeaderboardRecord{}}

	// History: 100 matches all-time (50-50, 100 kills), 3 this week (2-1, 20
	// kills), none today.
	nk.seed(t, "CombatKills", evr.ResetScheduleAllTime, 100)
	nk.seed(t, "CombatWins", evr.ResetScheduleAllTime, 50)
	nk.seed(t, "CombatLosses", evr.ResetScheduleAllTime, 50)
	nk.seed(t, "CombatKills", evr.ResetScheduleWeekly, 20)
	nk.seed(t, "CombatWins", evr.ResetScheduleWeekly, 2)
	nk.seed(t, "CombatLosses", evr.ResetScheduleWeekly, 1)

	db := sql.OpenDB(periodRowDB{nk: nk})
	t.Cleanup(func() { db.Close() })

	core, logs := observer.New(zap.DebugLevel)
	logger := NewRuntimeGoLogger(zap.New(core))
	queue := &StatisticsQueue{logger: logger, ch: make(chan []*StatisticsQueueEntry, 8)}

	// The match: a win with 7 kills.
	update := evr.ServerProfileUpdate{Statistics: &evr.ServerProfileUpdateStatistics{Combat: &evr.CombatStatistics{
		CombatKills: &evr.StatisticValue{Value: 7, Count: 1},
		CombatWins:  &evr.StatisticValue{Value: 1, Count: 1},
	}}}
	ev := NewEventServerProfileUpdate(periodRowUserID, periodRowGroupID, "synthetic-player", evr.ModeCombatPublic, update)
	if err := ev.Process(context.Background(), logger, &EventDispatcher{db: db, nk: nk, statisticsQueue: queue}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	// The statistics queue worker's loop body, run synchronously.
	for len(queue.ch) > 0 {
		for _, e := range <-queue.ch {
			processSingleEntry(context.Background(), logger, nk, e)
		}
	}

	for _, tc := range []struct {
		stat                   string
		daily, weekly, alltime float64
	}{
		{"CombatKills", 7, 27, 107},
		{"CombatWins", 1, 3, 51},
		{"GamesPlayed", 1, 4, 101},                         // wins + losses
		{"CombatWinPercentage", 100, 75, 51.0 / 101 * 100}, // derived per period
	} {
		for r, want := range map[evr.ResetSchedule]float64{
			evr.ResetScheduleDaily:   tc.daily,
			evr.ResetScheduleWeekly:  tc.weekly,
			evr.ResetScheduleAllTime: tc.alltime,
		} {
			got, ok := nk.row(t, tc.stat, r)
			if !ok {
				t.Errorf("%s %s: no row, want %v", tc.stat, r, want)
			} else if math.Abs(got-want) > 1e-6 {
				t.Errorf("%s %s = %v, want %v", tc.stat, r, got, want)
			}
		}
	}

	entries := logs.FilterMessage("Server profile update queued").All()
	if len(entries) != 1 {
		t.Fatalf("%d \"Server profile update queued\" records, want 1", len(entries))
	}
	if f := entries[0].ContextMap(); f["uid"] != periodRowUserID || f["gid"] != periodRowGroupID || f["entries"] == nil {
		t.Errorf("log record fields = %v", f)
	}
}

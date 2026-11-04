package server

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// Synthetic identities. The fixture's own userid is the all-zero XPID, which is
// not IsValid() and would send the remote-log path down its game-server branch
// instead of the self-report branch a real client takes.
const (
	combatTestXPID      = "OVR-ORG-1000000000000001"
	combatTestUserID    = "00000000-0000-4000-8000-00000000c0b1"
	combatTestSessionID = "00000000-0000-4000-8000-00000000c0b2"
	combatTestGroupID   = "00000000-0000-4000-8000-00000000c0b3"
	combatTestMatchUUID = "00000000-0000-4000-8000-00000000c0b4"
	combatTestNode      = "combattestnode"
)

// readServerRemoteLogFixture reads one of the remote log fixtures that live
// with the evr package's parse tests, with the fixture's anonymised userid
// replaced by combatTestXPID.
func readServerRemoteLogFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("evr", "testdata", "remotelog", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return []byte(strings.ReplaceAll(string(data), "OVR-ORG-0000000000000000", combatTestXPID))
}

// combatBoardNK is an in-memory leaderboard store plus the one match both stat
// writers report on. Its leaderboard and MatchGet methods are leaf methods:
// the code under test calls them through runtime.NakamaModule, which dispatches
// to this outermost type, so they shadow evrTestNakamaModule's inert stubs.
type combatBoardNK struct {
	*evrTestNakamaModule

	label *MatchLabel

	boardsMu sync.Mutex
	boards   map[string]map[string]*api.LeaderboardRecord // board ID -> owner -> record
}

func newCombatBoardNK(label *MatchLabel) *combatBoardNK {
	return &combatBoardNK{
		evrTestNakamaModule: newEvrTestNakamaModule(),
		label:               label,
		boards:              map[string]map[string]*api.LeaderboardRecord{},
	}
}

func (n *combatBoardNK) MatchGet(_ context.Context, id string) (*api.Match, error) {
	if id != n.label.ID.String() {
		return nil, nil
	}
	data, err := json.Marshal(n.label)
	if err != nil {
		return nil, err
	}
	return &api.Match{MatchId: id, Label: wrapperspb.String(string(data))}, nil
}

func (n *combatBoardNK) LeaderboardCreate(context.Context, string, bool, string, string, string, map[string]any, bool) error {
	return nil
}

func (n *combatBoardNK) LeaderboardRecordWrite(_ context.Context, id, ownerID, username string, score, subscore int64, _ map[string]any, operator *int) (*api.LeaderboardRecord, error) {
	if operator == nil || *operator != 2 {
		return nil, errors.New("combatBoardNK only models SET writes, which is all the statistics queue issues")
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

func (n *combatBoardNK) LeaderboardRecordsList(_ context.Context, id string, ownerIDs []string, _ int, _ string, _ int64) ([]*api.LeaderboardRecord, []*api.LeaderboardRecord, string, string, error) {
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

// rows returns every leaderboard row owned by ownerID, decoded.
func (n *combatBoardNK) rows(t *testing.T, ownerID string) map[string]float64 {
	t.Helper()
	n.boardsMu.Lock()
	defer n.boardsMu.Unlock()
	out := map[string]float64{}
	for id, owners := range n.boards {
		rec, ok := owners[ownerID]
		if !ok {
			continue
		}
		v, err := ScoreToFloat64(rec.Score, rec.Subscore)
		if err != nil {
			t.Fatalf("decode %s: %v", id, err)
		}
		out[id] = v
	}
	return out
}

// combatBoardDB answers PlayerStatisticsGetID's one query -- the owner's rows
// on a list of boards -- from the same in-memory store, so the profile-update
// path reads exactly what the remote-log path wrote.
type combatBoardDB struct{ nk *combatBoardNK }

func (c combatBoardDB) Connect(context.Context) (driver.Conn, error) { return combatBoardConn(c), nil }
func (c combatBoardDB) Driver() driver.Driver                        { return nil }

type combatBoardConn struct{ nk *combatBoardNK }

func (c combatBoardConn) Prepare(query string) (driver.Stmt, error) {
	if !strings.Contains(query, "FROM") || !strings.Contains(query, "leaderboard_record") {
		return nil, errors.New("combatBoardDB only answers the leaderboard_record read: " + query)
	}
	return combatBoardStmt(c), nil
}
func (c combatBoardConn) Close() error              { return nil }
func (c combatBoardConn) Begin() (driver.Tx, error) { return nil, errors.New("no transactions") }

// CheckNamedValue lets the []string board-ID list through as-is, the way the
// pgx driver accepts it for `= ANY($2)`.
func (c combatBoardConn) CheckNamedValue(*driver.NamedValue) error { return nil }

type combatBoardStmt struct{ nk *combatBoardNK }

func (s combatBoardStmt) Close() error  { return nil }
func (s combatBoardStmt) NumInput() int { return -1 }
func (s combatBoardStmt) Exec([]driver.Value) (driver.Result, error) {
	return nil, errors.New("read only")
}
func (s combatBoardStmt) Query(args []driver.Value) (driver.Rows, error) {
	owner, _ := args[0].(string)
	ids, _ := args[1].([]string)
	rows := &combatBoardRows{}
	s.nk.boardsMu.Lock()
	defer s.nk.boardsMu.Unlock()
	for _, id := range ids {
		if rec, ok := s.nk.boards[id][owner]; ok {
			rows.data = append(rows.data, []driver.Value{id, rec.Score, rec.Subscore})
		}
	}
	return rows, nil
}

type combatBoardRows struct{ data [][]driver.Value }

func (r *combatBoardRows) Columns() []string { return []string{"leaderboard_id", "score", "subscore"} }
func (r *combatBoardRows) Close() error      { return nil }
func (r *combatBoardRows) Next(dest []driver.Value) error {
	if len(r.data) == 0 {
		return io.EOF
	}
	copy(dest, r.data[0])
	r.data = r.data[1:]
	return nil
}

// combatStatsHarness is one public combat match with one blue player, and the
// pieces both stat writers need to run for real against it.
type combatStatsHarness struct {
	nk      *combatBoardNK
	db      *sql.DB
	queue   *StatisticsQueue
	logger  runtime.Logger
	matchID MatchID
	xpid    evr.EvrId
}

func newCombatStatsHarness(t *testing.T) *combatStatsHarness {
	t.Helper()
	xpid, err := evr.ParseEvrId(combatTestXPID)
	if err != nil {
		t.Fatal(err)
	}
	matchID := MatchID{UUID: uuid.Must(uuid.FromString(combatTestMatchUUID)), Node: combatTestNode}
	groupID := uuid.Must(uuid.FromString(combatTestGroupID))
	label := &MatchLabel{
		ID:      matchID,
		Mode:    evr.ModeCombatPublic,
		GroupID: &groupID,
		Players: []PlayerInfo{{
			UserID:      combatTestUserID,
			SessionID:   combatTestSessionID,
			EvrID:       *xpid,
			Team:        BlueTeam,
			DisplayName: "synthetic-player",
		}},
	}
	nk := newCombatBoardNK(label)
	db := sql.OpenDB(combatBoardDB{nk: nk})
	t.Cleanup(func() { db.Close() })
	logger := NewRuntimeGoLogger(zap.NewNop())
	return &combatStatsHarness{
		nk:      nk,
		db:      db,
		queue:   &StatisticsQueue{logger: logger, ch: make(chan []*StatisticsQueueEntry, 64)},
		logger:  logger,
		matchID: matchID,
		xpid:    *xpid,
	}
}

// drain runs every queued batch through processSingleEntry, which is the body
// of the statistics queue's worker loop, synchronously.
func (h *combatStatsHarness) drain(t *testing.T) {
	t.Helper()
	for {
		select {
		case entries := <-h.queue.ch:
			for _, e := range entries {
				processSingleEntry(context.Background(), h.logger, h.nk, e)
			}
		default:
			return
		}
	}
}

// remoteLog runs the client's POST_MATCH_MATCH_TYPE_STATS through the
// remote-log writer: EventRemoteLogSet.processPostMatchMessages.
func (h *combatStatsHarness) remoteLog(t *testing.T, msg evr.RemoteLog) {
	t.Helper()
	s := &EventRemoteLogSet{Node: combatTestNode, UserID: combatTestUserID, SessionID: combatTestSessionID, XPID: h.xpid}
	if err := s.processPostMatchMessages(context.Background(), h.logger, nil, h.nk, nil, h.queue, h.matchID, []evr.RemoteLog{msg}); err != nil {
		t.Fatalf("processPostMatchMessages: %v", err)
	}
	h.drain(t)
}

// profileUpdate runs the game server's combat stats through the profile-update
// writer: EventServerProfileUpdate.Process.
func (h *combatStatsHarness) profileUpdate(t *testing.T, combat *evr.CombatStatistics) {
	t.Helper()
	ev := NewEventServerProfileUpdate(combatTestUserID, combatTestGroupID, "synthetic-player", evr.ModeCombatPublic,
		evr.ServerProfileUpdate{Statistics: &evr.ServerProfileUpdateStatistics{Combat: combat}})
	if err := ev.Process(context.Background(), h.logger, &EventDispatcher{db: h.db, nk: h.nk, statisticsQueue: h.queue}); err != nil {
		t.Fatalf("EventServerProfileUpdate.Process: %v", err)
	}
	h.drain(t)
}

// combatProfileUpdateFromSample builds the game server's per-match combat
// update from the same CR15NetProfileStats object the client logged, one
// StatisticValue per key CombatStatistics names.
//
// XP is left out on purpose. The client's type stats carry XP (see the
// fixture), so the remote-log path writes combat XP today. Whether the game
// server's update carries it too is not recorded anywhere in this repository,
// and if it does, combat XP has had two writers since before this change. This
// test does not settle that; it settles the Combat* stats.
func combatProfileUpdateFromSample(t *testing.T, sample map[string]float64) *evr.CombatStatistics {
	t.Helper()
	out := &evr.CombatStatistics{}
	v := reflect.ValueOf(out).Elem()
	for i := 0; i < v.NumField(); i++ {
		name, _, _ := strings.Cut(v.Type().Field(i).Tag.Get("json"), ",")
		val, ok := sample[name]
		if !ok || !strings.HasPrefix(name, "Combat") {
			continue
		}
		v.Field(i).Set(reflect.ValueOf(&evr.StatisticValue{Value: val, Count: 1}))
	}
	return out
}

func fixtureObject(t *testing.T, raw []byte, key string) map[string]float64 {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	var obj map[string]float64
	if err := json.Unmarshal(top[key], &obj); err != nil {
		t.Fatal(err)
	}
	return obj
}

// TestCombatStats_OneWriterPerLeaderboardRow runs one public combat match
// through both paths that report it -- the client's POST_MATCH_MATCH_TYPE_STATS
// remote log and the game server's profile update -- in both arrival orders,
// and asserts every leaderboard row the player ends up with.
//
// A leaderboard ID is group:mode:stat:schedule (StatisticBoardID); the operator
// is not part of it. So if the remote log wrote CombatKills it would land on
// the same row the profile update writes: the remote log increments it, the
// profile update sets it to what it read plus the match, and either order ends
// at twice the match.
func TestCombatStats_OneWriterPerLeaderboardRow(t *testing.T) {
	raw := readServerRemoteLogFixture(t, "post_match_match_type_stats_combat.json")
	sample := fixtureObject(t, raw, "stats")

	// What one match is worth on a player with no history. Counters are the
	// match's value; the rest are what CombatStatistics.CalculateFields
	// derives from them (core_account_statistics.go:639).
	want := map[string]float64{
		"GamesPlayed":                     1, // CombatWins 1 + CombatLosses 0
		"Level":                           1, // ensureLevelDefaults
		"CombatWinPercentage":             100,
		"CombatPointCaptureWinPercentage": 100,
		"XP":                              sample["XP"], // remote log only; see combatProfileUpdateFromSample
	}
	for key, v := range sample {
		switch key {
		case "XP", "CombatWinPercentage", "CombatPointCaptureWinPercentage":
			continue // set above
		}
		// Counters, and CombatAverageEliminationDeathRatio, which
		// CalculateFields derives as eliminations/deaths -- 7/4, the same
		// 1.75 the game sent.
		want[key] = v
	}

	for _, order := range []string{"remote log first", "profile update first"} {
		t.Run(order, func(t *testing.T) {
			h := newCombatStatsHarness(t)

			msg, err := evr.UnmarshalRemoteLog(raw)
			if err != nil {
				t.Fatalf("UnmarshalRemoteLog: %v", err)
			}
			update := combatProfileUpdateFromSample(t, sample)

			if order == "remote log first" {
				h.remoteLog(t, msg)
				h.profileUpdate(t, update)
			} else {
				h.profileUpdate(t, update)
				h.remoteLog(t, msg)
			}

			got := h.nk.rows(t, combatTestUserID)
			var problems []string
			for _, r := range []evr.ResetSchedule{evr.ResetScheduleDaily, evr.ResetScheduleWeekly, evr.ResetScheduleAllTime} {
				for stat, w := range want {
					id := StatisticBoardID(combatTestGroupID, evr.ModeCombatPublic, stat, r)
					g, ok := got[id]
					switch {
					case !ok:
						problems = append(problems, id+": no row, want "+ftoa(w))
					case math.Abs(g-w) > 1e-6:
						problems = append(problems, id+": "+ftoa(g)+", want "+ftoa(w))
					}
				}
			}
			for id, g := range got {
				_, mode, stat, _, err := ParseStatisticBoardID(id)
				if err != nil || mode != evr.ModeCombatPublic {
					problems = append(problems, id+": row outside combat_public")
					continue
				}
				if _, ok := want[stat]; !ok && g != 0 {
					problems = append(problems, id+": "+ftoa(g)+", want no value")
				}
			}
			sort.Strings(problems)
			for _, p := range slices.Compact(problems) {
				t.Error(p)
			}
		})
	}
}

func ftoa(f float64) string { return strings.TrimRight(strings.TrimRight(jsonNumber(f), "0"), ".") }

func jsonNumber(f float64) string {
	b, _ := json.Marshal(f)
	if !strings.Contains(string(b), ".") {
		return string(b) + ".0"
	}
	return string(b)
}

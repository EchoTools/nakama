package server

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// This file characterizes the behaviour of server/storage_index.go
// (LocalStorageIndex) as it actually is, so that the question "can a
// suspension/ban check be served from a Nakama storage index?" can be settled
// by test output rather than by argument.
//
// Every test here is DB-free unless its name ends in _RequiresDB, in which case
// it uses NewDB(t) and skips with a clear reason when no database is reachable.

// --- infrastructure ------------------------------------------------------

// errCharNoDatabase is returned by charFailingConnector for every connection
// attempt. It is deliberately NOT driver.ErrBadConn, so database/sql returns it
// immediately instead of retrying, which keeps the attempt count exact.
var errCharNoDatabase = errors.New("characterization: database is unavailable")

// charFailingConnector is a database/sql connector that never yields a usable
// connection and counts how many times one was requested. It lets a test prove
// *whether* a code path reaches the database without needing a database.
type charFailingConnector struct {
	attempts atomic.Int64
}

func (c *charFailingConnector) Connect(context.Context) (driver.Conn, error) {
	c.attempts.Add(1)
	return nil, errCharNoDatabase
}

func (c *charFailingConnector) Driver() driver.Driver { return nil }

// charCountingDB returns a *sql.DB that fails every query, along with the
// connector so the test can read the attempt count.
func charCountingDB(t *testing.T) (*sql.DB, *charFailingConnector) {
	t.Helper()
	conn := &charFailingConnector{}
	db := sql.OpenDB(conn)
	t.Cleanup(func() { _ = db.Close() })
	return db, conn
}

// charIndexSpec describes one index to register on a node.
type charIndexSpec struct {
	name       string
	collection string
	key        string
	fields     []string
	maxEntries int
	indexOnly  bool
}

// charNode builds an independent LocalStorageIndex ("node") with the given
// indexes registered. db may be nil.
func charNode(t *testing.T, db *sql.DB, specs ...charIndexSpec) StorageIndex {
	t.Helper()
	si, err := NewLocalStorageIndex(zap.NewNop(), db, &StorageConfig{}, &testMetrics{})
	if err != nil {
		t.Fatalf("NewLocalStorageIndex: %v", err)
	}
	for _, s := range specs {
		if err := si.CreateIndex(context.Background(), s.name, s.collection, s.key, s.fields, nil, s.maxEntries, s.indexOnly); err != nil {
			t.Fatalf("CreateIndex(%s): %v", s.name, err)
		}
	}
	return si
}

// charObject builds a storage object suitable for indexing. PermissionRead is 2
// (public) so that caller filtering in the index-only branch never drops it.
func charObject(userID, collection, key, value string, at time.Time) *api.StorageObject {
	return &api.StorageObject{
		Collection:      collection,
		Key:             key,
		UserId:          userID,
		Value:           value,
		Version:         "v1",
		PermissionRead:  2,
		PermissionWrite: 0,
		CreateTime:      timestamppb.New(at),
		UpdateTime:      timestamppb.New(at),
	}
}

// charJournalJSON marshals a GuildEnforcementJournal exactly the way a real
// storage write would, so the indexed value is byte-identical to production.
func charJournalJSON(t *testing.T, j *GuildEnforcementJournal) string {
	t.Helper()
	b, err := json.Marshal(j)
	if err != nil {
		t.Fatalf("marshal journal: %v", err)
	}
	return string(b)
}

// charSuspendedJournal builds a journal holding one active suspension in
// groupID that expires in 24h.
func charSuspendedJournal(userID, groupID, notice string) *GuildEnforcementJournal {
	j := NewGuildEnforcementJournal(userID)
	j.RecordsByGroupID = map[string][]GuildEnforcementRecord{
		groupID: {{
			ID:             uuid.Must(uuid.NewV4()).String(),
			UserID:         userID,
			GroupID:        groupID,
			CreatedAt:      time.Now().UTC().Add(-time.Hour),
			Expiry:         time.Now().UTC().Add(24 * time.Hour),
			UserNoticeText: notice,
		}},
	}
	return j
}

// charAllJournalFields is every top-level field of a marshalled
// GuildEnforcementJournal. Registering all of them with indexOnly:true gives the
// "serve the ban check from the index" proposal its BEST possible case: the
// index-only payload is then byte-complete and the partial-data problem
// demonstrated by TestStorageIndex_IndexOnlyTrue_ReturnsPartialObject does not
// apply. Tests that want to isolate a different failure mode (eviction, node
// locality) use this field list so that partiality cannot be the cause.
var charAllJournalFields = []string{
	"community_values_completed_at",
	"records",
	"voids",
	"user_id",
	"guild_ids",
}

// charListAll returns every entry in an index, using a match-all query.
func charListAll(t *testing.T, si StorageIndex, indexName string, limit int) (*api.StorageObjects, error) {
	t.Helper()
	objs, _, err := si.List(context.Background(), uuid.Nil, indexName, "*", limit, nil, "")
	return objs, err
}

// charUserIDs extracts and sorts the user IDs from a result set.
func charUserIDs(objs *api.StorageObjects) []string {
	if objs == nil {
		return nil
	}
	out := make([]string, 0, len(objs.Objects))
	for _, o := range objs.Objects {
		out = append(out, o.UserId)
	}
	sort.Strings(out)
	return out
}

// --- Group A: index semantics -------------------------------------------

// A1. indexOnly:true stores ONLY the registered fields. Everything else in the
// storage value is gone from the returned object.
//
// Applied to the enforcement journal (registered fields: ["guild_ids"]) this
// means the returned journal has NO enforcement records at all.
func TestStorageIndex_IndexOnlyTrue_ReturnsPartialObject(t *testing.T) {
	t.Parallel()

	const idxName = "charIndexOnlyTrue"
	const groupID = "guildA"
	userID := uuid.Must(uuid.NewV4()).String()

	si := charNode(t, nil, charIndexSpec{
		name:       idxName,
		collection: StorageCollectionEnforcementJournal,
		key:        StorageKeyEnforcementJournal,
		fields:     []string{"guild_ids"}, // same fields as the production journal index
		maxEntries: 100,
		indexOnly:  true,
	})

	journal := charSuspendedJournal(userID, groupID, "banned for cheating")
	value := charJournalJSON(t, journal)

	// Sanity: the value that went in really does carry the suspension.
	if !strings.Contains(value, "banned for cheating") {
		t.Fatalf("precondition failed: indexed value has no suspension notice: %s", value)
	}

	si.Write(context.Background(), []*api.StorageObject{
		charObject(userID, StorageCollectionEnforcementJournal, StorageKeyEnforcementJournal, value, time.Now()),
	})

	objs, err := charListAll(t, si, idxName, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objs.Objects) != 1 {
		t.Fatalf("expected 1 index hit, got %d", len(objs.Objects))
	}

	got := objs.Objects[0].Value

	var asMap map[string]any
	if err := json.Unmarshal([]byte(got), &asMap); err != nil {
		t.Fatalf("returned value is not JSON (%q): %v", got, err)
	}

	if _, ok := asMap["guild_ids"]; !ok {
		t.Errorf("registered field guild_ids is MISSING from the index-only result: %s", got)
	}
	for _, absent := range []string{"records", "voids", "user_id", "community_values_completed_at"} {
		if _, ok := asMap[absent]; ok {
			t.Errorf("non-registered field %q was returned by an index-only read: %s", absent, got)
		}
	}

	// The consequence, stated in production terms: parse the index-only result
	// with the real production parser and the suspension has vanished.
	roundTripped, err := GuildEnforcementJournalFromStorageObject(objs.Objects[0])
	if err != nil {
		t.Fatalf("GuildEnforcementJournalFromStorageObject: %v", err)
	}
	if n := len(roundTripped.RecordsByGroupID); n != 0 {
		t.Errorf("expected an index-only journal to carry 0 record groups, got %d", n)
	}
	if n := len(roundTripped.ActiveSuspensions()); n != 0 {
		t.Errorf("expected 0 active suspensions from an index-only journal, got %d", n)
	}

	t.Logf("indexed value  (%d bytes): %s", len(value), value)
	t.Logf("index-only read (%d bytes): %s", len(got), got)
	t.Logf("active suspensions: in DB value=%d, from index-only read=%d",
		len(journal.ActiveSuspensions()), len(roundTripped.ActiveSuspensions()))
}

// A3 (structural half). indexOnly:true never touches the database; indexOnly:false
// cannot produce a result without one. Both indexes see the identical write and
// the identical query -- only the retrieval strategy differs.
func TestStorageIndex_IndexOnlyTrue_SucceedsWithoutDatabase_FalseCannot(t *testing.T) {
	t.Parallel()

	const (
		idxOnly  = "charTwinIndexOnly"
		idxFull  = "charTwinFullRead"
		groupID  = "guildA"
		theQuery = "*"
	)
	userID := uuid.Must(uuid.NewV4()).String()

	db, conn := charCountingDB(t)

	// Two indexes over the SAME collection, identical fields, differing ONLY in
	// indexOnly. A single Write populates both.
	si := charNode(t, db,
		charIndexSpec{idxOnly, StorageCollectionEnforcementJournal, StorageKeyEnforcementJournal, []string{"guild_ids"}, 100, true},
		charIndexSpec{idxFull, StorageCollectionEnforcementJournal, StorageKeyEnforcementJournal, []string{"guild_ids"}, 100, false},
	)

	value := charJournalJSON(t, charSuspendedJournal(userID, groupID, "banned"))
	si.Write(context.Background(), []*api.StorageObject{
		charObject(userID, StorageCollectionEnforcementJournal, StorageKeyEnforcementJournal, value, time.Now()),
	})

	// indexOnly:true -- no database needed.
	before := conn.attempts.Load()
	onlyObjs, _, errOnly := si.List(context.Background(), uuid.Nil, idxOnly, theQuery, 10, nil, "")
	afterOnly := conn.attempts.Load()

	if errOnly != nil {
		t.Fatalf("indexOnly:true List returned an error with no usable database: %v", errOnly)
	}
	if len(onlyObjs.Objects) != 1 {
		t.Fatalf("indexOnly:true expected 1 hit, got %d", len(onlyObjs.Objects))
	}
	if n := afterOnly - before; n != 0 {
		t.Errorf("indexOnly:true made %d database connection attempts, expected 0", n)
	}

	// indexOnly:false -- the same match, but it must go to the database.
	fullObjs, _, errFull := si.List(context.Background(), uuid.Nil, idxFull, theQuery, 10, nil, "")
	afterFull := conn.attempts.Load()

	if errFull == nil {
		t.Fatalf("indexOnly:false unexpectedly succeeded without a database: %+v", fullObjs)
	}
	if !errors.Is(errFull, errCharNoDatabase) {
		t.Errorf("indexOnly:false failed for an unexpected reason: %v", errFull)
	}
	if n := afterFull - afterOnly; n < 1 {
		t.Errorf("indexOnly:false made %d database connection attempts, expected >= 1", n)
	}

	t.Logf("db connection attempts: indexOnly:true=%d indexOnly:false=%d",
		afterOnly-before, afterFull-afterOnly)
	t.Logf("indexOnly:false error: %v", errFull)
}

// A2. indexOnly:false returns the COMPLETE object, read fresh from the database.
func TestStorageIndex_IndexOnlyFalse_ReturnsCompleteObject_RequiresDB(t *testing.T) {
	db := NewDB(t) // skips when no database is reachable
	defer db.Close()

	ctx := context.Background()

	const idxName = "charFullReadCompleteObject"
	const groupID = "guildA"
	uid := uuid.Must(uuid.NewV4())
	userID := uid.String()
	InsertUser(t, db, uid)

	collection := "CharEnforcement" + uuid.Must(uuid.NewV4()).String()[:8]

	si, err := NewLocalStorageIndex(zap.NewNop(), db, &StorageConfig{}, &testMetrics{})
	if err != nil {
		t.Fatalf("NewLocalStorageIndex: %v", err)
	}
	if err := si.CreateIndex(ctx, idxName, collection, StorageKeyEnforcementJournal, []string{"guild_ids"}, nil, 100, false); err != nil {
		t.Fatalf("CreateIndex: %v", err)
	}

	value := charJournalJSON(t, charSuspendedJournal(userID, groupID, "banned for cheating"))

	acks, _, err := StorageWriteObjects(ctx, zap.NewNop(), db, metrics, si, true, StorageOpWrites{{
		OwnerID: userID,
		Object: &api.WriteStorageObject{
			Collection: collection,
			Key:        StorageKeyEnforcementJournal,
			Value:      value,
		},
	}})
	if err != nil {
		t.Fatalf("StorageWriteObjects: %v", err)
	}
	if len(acks.Acks) != 1 {
		t.Fatalf("expected 1 ack, got %d", len(acks.Acks))
	}

	objs, _, err := si.List(ctx, uuid.Nil, idxName, "*", 10, nil, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objs.Objects) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(objs.Objects))
	}

	got := objs.Objects[0].Value
	var asMap map[string]any
	if err := json.Unmarshal([]byte(got), &asMap); err != nil {
		t.Fatalf("returned value is not JSON: %v", err)
	}
	for _, present := range []string{"guild_ids", "records", "voids", "user_id"} {
		if _, ok := asMap[present]; !ok {
			t.Errorf("indexOnly:false result is MISSING field %q -- expected the complete object: %s", present, got)
		}
	}

	roundTripped, err := GuildEnforcementJournalFromStorageObject(objs.Objects[0])
	if err != nil {
		t.Fatalf("GuildEnforcementJournalFromStorageObject: %v", err)
	}
	if n := len(roundTripped.ActiveSuspensions()); n != 1 {
		t.Errorf("expected 1 active suspension from an indexOnly:false read, got %d", n)
	}

	t.Logf("indexOnly:false returned %d bytes: %s", len(got), got)
}

// --- Group B: staleness --------------------------------------------------

// B4. Under indexOnly:true the index serves whatever was last written INTO THE
// INDEX. A change that reached the database but not this index is invisible:
// there is no read-through to correct it.
func TestStorageIndex_IndexOnlyTrue_ServesStaleValueAfterUnindexedChange(t *testing.T) {
	t.Parallel()

	const idxName = "charStaleIndexOnly"
	const groupID = "guildA"
	userID := uuid.Must(uuid.NewV4()).String()

	si := charNode(t, nil, charIndexSpec{
		name:       idxName,
		collection: StorageCollectionEnforcementJournal,
		key:        StorageKeyEnforcementJournal,
		fields:     []string{"guild_ids"},
		maxEntries: 100,
		indexOnly:  true,
	})

	// v1: user has a record in guildA. Indexed.
	v1 := charJournalJSON(t, charSuspendedJournal(userID, groupID, "banned"))
	si.Write(context.Background(), []*api.StorageObject{
		charObject(userID, StorageCollectionEnforcementJournal, StorageKeyEnforcementJournal, v1, time.Now()),
	})

	// v2: the ban is lifted -- guild_ids is now empty. This write lands in the
	// database. It does NOT reach this index (another node wrote it, or the
	// index write was dropped, or this node restarted after eviction).
	cleared := NewGuildEnforcementJournal(userID)
	v2 := charJournalJSON(t, cleared)
	_ = v2 // v2 is what the database now holds; the index was never told.

	objs, err := charListAll(t, si, idxName, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objs.Objects) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(objs.Objects))
	}

	served := objs.Objects[0].Value
	if served != stripJSONToFields(t, v1, "guild_ids") {
		t.Logf("note: served value %q", served)
	}
	if !strings.Contains(served, groupID) {
		t.Errorf("expected the index to serve the STALE v1 (containing %q), got: %s", groupID, served)
	}

	t.Logf("database now holds : %s", v2)
	t.Logf("index still serves : %s", served)
	t.Logf("indexOnly:true has no read-through, so the index result cannot converge on its own")
}

// stripJSONToFields reduces a JSON object to the given top-level fields, which
// is what mapIndexStorageFields does before storing the index-only payload.
func stripJSONToFields(t *testing.T, value string, fields ...string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(value), &m); err != nil {
		t.Fatalf("stripJSONToFields: %v", err)
	}
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		if v, ok := m[f]; ok {
			out[f] = v
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("stripJSONToFields: %v", err)
	}
	return string(b)
}

// B5. Under indexOnly:false the same scenario returns CURRENT data, because the
// index result is only a list of IDs and the values come from the database.
func TestStorageIndex_IndexOnlyFalse_ReturnsCurrentValueAfterChange_RequiresDB(t *testing.T) {
	db := NewDB(t)
	defer db.Close()

	ctx := context.Background()

	const idxName = "charFreshFullRead"
	const groupID = "guildA"
	uid := uuid.Must(uuid.NewV4())
	userID := uid.String()
	InsertUser(t, db, uid)

	collection := "CharFresh" + uuid.Must(uuid.NewV4()).String()[:8]

	si, err := NewLocalStorageIndex(zap.NewNop(), db, &StorageConfig{}, &testMetrics{})
	if err != nil {
		t.Fatalf("NewLocalStorageIndex: %v", err)
	}
	if err := si.CreateIndex(ctx, idxName, collection, StorageKeyEnforcementJournal, []string{"guild_ids"}, nil, 100, false); err != nil {
		t.Fatalf("CreateIndex: %v", err)
	}

	write := func(value string) {
		t.Helper()
		if _, _, err := StorageWriteObjects(ctx, zap.NewNop(), db, metrics, si, true, StorageOpWrites{{
			OwnerID: userID,
			Object: &api.WriteStorageObject{
				Collection: collection,
				Key:        StorageKeyEnforcementJournal,
				Value:      value,
			},
		}}); err != nil {
			t.Fatalf("StorageWriteObjects: %v", err)
		}
	}

	// v1: no suspension, but a (voided-away) record keeps guild_ids populated so
	// the query still matches after the update.
	j := charSuspendedJournal(userID, groupID, "banned")
	write(charJournalJSON(t, j))

	// v2: the record is voided in the database.
	rec := j.RecordsByGroupID[groupID][0]
	j.VoidRecord(groupID, rec.ID, userID, "", "lifted")
	write(charJournalJSON(t, j))

	objs, _, err := si.List(ctx, uuid.Nil, idxName, "*", 10, nil, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objs.Objects) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(objs.Objects))
	}

	roundTripped, err := GuildEnforcementJournalFromStorageObject(objs.Objects[0])
	if err != nil {
		t.Fatalf("GuildEnforcementJournalFromStorageObject: %v", err)
	}
	if n := len(roundTripped.ActiveSuspensions()); n != 0 {
		t.Errorf("expected the voided record to be reflected (0 active), got %d -- the read was stale", n)
	}

	t.Logf("indexOnly:false returned the current (post-void) value: %s", objs.Objects[0].Value)
}

// B6. The concrete consequence: a journal whose index entry is stale/partial
// produces a "not suspended" verdict from the REAL production verdict function.
func TestEnforcementVerdict_StaleIndexEntry_YieldsNotSuspended(t *testing.T) {
	t.Parallel()

	const idxName = "charVerdictStale"
	groupID := uuid.Must(uuid.NewV4()).String()
	userID := uuid.Must(uuid.NewV4()).String()

	si := charNode(t, nil, charIndexSpec{
		name:       idxName,
		collection: StorageCollectionEnforcementJournal,
		key:        StorageKeyEnforcementJournal,
		fields:     []string{"guild_ids"},
		maxEntries: 100,
		indexOnly:  true,
	})

	truth := charSuspendedJournal(userID, groupID, "banned for cheating")
	si.Write(context.Background(), []*api.StorageObject{
		charObject(userID, StorageCollectionEnforcementJournal, StorageKeyEnforcementJournal,
			charJournalJSON(t, truth), time.Now()),
	})

	// Ground truth: the user IS suspended.
	truthEnforcements, err := CheckEnforcementSuspensions(
		GuildEnforcementJournalList{userID: truth}, map[string][]string{})
	if err != nil {
		t.Fatalf("CheckEnforcementSuspensions(truth): %v", err)
	}
	if _, ok := truthEnforcements[groupID][evr.ModeArenaPublic]; !ok {
		t.Fatalf("precondition failed: the user is not suspended in the ground-truth journal")
	}

	// Now build the journal list the way an index-only implementation would.
	objs, err := charListAll(t, si, idxName, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	fromIndex := make(GuildEnforcementJournalList, len(objs.Objects))
	for _, o := range objs.Objects {
		j, err := GuildEnforcementJournalFromStorageObject(o)
		if err != nil {
			t.Fatalf("GuildEnforcementJournalFromStorageObject: %v", err)
		}
		fromIndex[o.UserId] = j
	}

	indexEnforcements, err := CheckEnforcementSuspensions(fromIndex, map[string][]string{})
	if err != nil {
		t.Fatalf("CheckEnforcementSuspensions(index): %v", err)
	}

	if _, ok := indexEnforcements[groupID][evr.ModeArenaPublic]; ok {
		t.Fatalf("index-only path DID find the suspension -- claim is false")
	}

	t.Logf("ground truth  : suspended in %d group(s)", len(truthEnforcements))
	t.Logf("index-only    : suspended in %d group(s)  <-- FAIL OPEN", len(indexEnforcements))
}

// --- Group C: eviction ---------------------------------------------------

// C7. Writing past MaxEntries + the 10% threshold evicts the OLDEST entries by
// update_time.
func TestStorageIndex_ExceedingMaxEntriesPlusThreshold_EvictsOldest(t *testing.T) {
	t.Parallel()

	const (
		idxName    = "charEviction"
		collection = "CharEvictionCollection"
		key        = "k"
		maxEntries = 10
		total      = 12 // 12 > 10 * 1.1, so eviction fires and removes 12-10 = 2
	)

	si := charNode(t, nil, charIndexSpec{idxName, collection, key, []string{"n"}, maxEntries, true})

	base := time.Now().Add(-time.Hour)
	users := make([]string, 0, total)
	objects := make([]*api.StorageObject, 0, total)
	for i := 0; i < total; i++ {
		uid := uuid.Must(uuid.NewV4()).String()
		users = append(users, uid)
		objects = append(objects, charObject(uid, collection, key,
			fmt.Sprintf(`{"n":%d}`, i), base.Add(time.Duration(i)*time.Minute)))
	}

	updates, _ := si.Write(context.Background(), objects)
	if updates != total {
		t.Fatalf("expected %d index updates, got %d", total, updates)
	}

	objs, err := charListAll(t, si, idxName, 50)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	remaining := len(objs.Objects)
	if remaining != maxEntries {
		t.Errorf("expected %d entries to remain after eviction, got %d", maxEntries, remaining)
	}

	present := make(map[string]bool, remaining)
	for _, o := range objs.Objects {
		present[o.UserId] = true
	}

	evicted := make([]int, 0, 2)
	for i, uid := range users {
		if !present[uid] {
			evicted = append(evicted, i)
		}
	}

	// The two oldest (index 0 and 1, the earliest update_time) must be the ones gone.
	want := []int{0, 1}
	if !reflect.DeepEqual(evicted, want) {
		t.Errorf("expected the oldest entries %v to be evicted, got %v", want, evicted)
	}

	t.Logf("wrote %d entries into an index with MaxEntries=%d", total, maxEntries)
	t.Logf("remaining=%d, evicted (by write order, oldest first)=%v", remaining, evicted)
}

// C8. Evicted entries are NOT reloaded. Nothing in the read path or in
// subsequent writes brings them back; the only repopulation path is
// LocalStorageIndex.Load, which requires a database and is a startup operation.
func TestStorageIndex_EvictedEntry_IsNotReloaded(t *testing.T) {
	t.Parallel()

	const (
		idxName    = "charNoReload"
		collection = "CharNoReloadCollection"
		key        = "k"
		maxEntries = 10
		total      = 12
	)

	db, conn := charCountingDB(t)
	si := charNode(t, db, charIndexSpec{idxName, collection, key, []string{"n"}, maxEntries, true})

	base := time.Now().Add(-time.Hour)
	users := make([]string, 0, total)
	objects := make([]*api.StorageObject, 0, total)
	for i := 0; i < total; i++ {
		uid := uuid.Must(uuid.NewV4()).String()
		users = append(users, uid)
		objects = append(objects, charObject(uid, collection, key,
			fmt.Sprintf(`{"n":%d}`, i), base.Add(time.Duration(i)*time.Minute)))
	}
	si.Write(context.Background(), objects)

	victim := users[0] // the oldest, therefore evicted

	stillGone := func(stage string) {
		t.Helper()
		objs, err := charListAll(t, si, idxName, 50)
		if err != nil {
			t.Fatalf("List(%s): %v", stage, err)
		}
		for _, o := range objs.Objects {
			if o.UserId == victim {
				t.Fatalf("%s: evicted entry reappeared in the index", stage)
			}
		}
	}

	stillGone("immediately after eviction")

	// Repeated reads do not repopulate.
	for i := 0; i < 3; i++ {
		stillGone(fmt.Sprintf("after read #%d", i+1))
	}

	// An unrelated write -- which runs the whole Write/eviction path again --
	// does not repopulate it either.
	si.Write(context.Background(), []*api.StorageObject{
		charObject(uuid.Must(uuid.NewV4()).String(), collection, key, `{"n":999}`, time.Now()),
	})
	stillGone("after an unrelated write")

	// The read path never touched the database.
	if n := conn.attempts.Load(); n != 0 {
		t.Errorf("expected the indexOnly read path to make 0 database attempts, got %d", n)
	}

	// The ONLY repopulation path is Load, which needs a database.
	loadErr := si.Load(context.Background())
	if loadErr == nil {
		t.Errorf("expected Load to fail without a usable database, got nil")
	}
	if conn.attempts.Load() == 0 {
		t.Errorf("expected Load to attempt a database read")
	}

	t.Logf("evicted entry stayed evicted across reads and a subsequent write")
	t.Logf("Load (the only repopulation path) failed without a database: %v", loadErr)
	t.Logf("database attempts: read path=0, Load=%d", conn.attempts.Load())
}

// C9-control. Before eviction is even in play: with the field list the journal
// index ACTUALLY registers today (["guild_ids"]), an index-only ban check fails
// open on a freshly-indexed, fully-current entry. Partiality alone is fatal;
// eviction is a second, independent failure mode.
func TestEnforceJoinSuspension_IndexOnlyWithRegisteredFields_ReturnsAllowedEvenWhenFresh(t *testing.T) {
	t.Parallel()

	const idxName = "charPartialFailOpen"
	groupID := uuid.Must(uuid.NewV4()).String()
	bannedUserID := uuid.Must(uuid.NewV4()).String()

	// Exactly the production registration, except indexOnly flipped to true --
	// which is the change under debate.
	meta := (&GuildEnforcementJournal{}).StorageIndexes()[0]
	si := charNode(t, nil, charIndexSpec{
		name:       idxName,
		collection: meta.Collection,
		key:        meta.Key,
		fields:     meta.Fields, // ["guild_ids"]
		maxEntries: meta.MaxEntries,
		indexOnly:  true,
	})

	truth := charSuspendedJournal(bannedUserID, groupID, "banned for cheating")
	si.Write(context.Background(), []*api.StorageObject{
		charObject(bannedUserID, meta.Collection, meta.Key, charJournalJSON(t, truth), time.Now()),
	})

	if n := len(truth.ActiveSuspensions()); n != 1 {
		t.Fatalf("precondition failed: expected 1 active suspension, got %d", n)
	}

	nk := &charIndexBackedNK{si: si, indexName: idxName}
	ggReg := seatTestGuildGroupRegistry(map[string]*GuildGroup{
		groupID: seatTestGuildGroup(groupID, "TestGuild", false),
	})
	session := newSeatTestSession(uuid.FromStringOrNil(bannedUserID), []string{bannedUserID})

	err := enforceJoinSuspension(context.Background(), zap.NewNop(), nk, ggReg,
		makeLabel(groupID, evr.ModeArenaPublic), session)

	if err != nil {
		t.Fatalf("the index-only check refused the join (%v) -- partiality is not fatal after all", err)
	}

	// The very same check against the complete journal refuses it.
	full := &seatTestNK{objects: map[string]*api.StorageObject{
		seatKey(bannedUserID, meta.Collection, meta.Key): charObject(
			bannedUserID, meta.Collection, meta.Key, charJournalJSON(t, truth), time.Now()),
	}}
	if fullErr := enforceJoinSuspension(context.Background(), zap.NewNop(), full, ggReg,
		makeLabel(groupID, evr.ModeArenaPublic), session); fullErr == nil {
		t.Fatalf("control failed: the complete-object check also allowed the join")
	} else {
		t.Logf("complete-object check : join REFUSED (%v)", fullErr)
	}

	t.Logf("index-only check      : join ALLOWED (err=nil)  <-- FAIL OPEN, entry is fresh and uncontended")
	t.Logf("registered fields %v do not carry records, so no verdict is possible", meta.Fields)
}

// C9. THE FAIL-OPEN. An evicted suspension makes the real, fail-closed
// enforceJoinSuspension check return "allowed" for a genuinely banned user.
//
// This drives production code end to end: the only substitution is that
// EnforcementJournalsLoad's nk.StorageRead is served from the storage index
// (what "serve the ban check from the index" means) instead of from SQL.
//
// The index here registers EVERY journal field, so index-only partiality cannot
// be the cause. Eviction alone produces the fail-open.
func TestEnforceJoinSuspension_EvictedSuspension_ReturnsAllowed(t *testing.T) {
	t.Parallel()

	const (
		idxName    = "charFailOpen"
		maxEntries = 10
		filler     = 12
	)

	groupID := uuid.Must(uuid.NewV4()).String()
	bannedUserID := uuid.Must(uuid.NewV4()).String()

	si := charNode(t, nil, charIndexSpec{
		name:       idxName,
		collection: StorageCollectionEnforcementJournal,
		key:        StorageKeyEnforcementJournal,
		fields:     charAllJournalFields,
		maxEntries: maxEntries,
		indexOnly:  true,
	})

	base := time.Now().Add(-time.Hour)

	// 1. The ban is issued first, so it is the OLDEST entry in the index.
	truth := charSuspendedJournal(bannedUserID, groupID, "banned for cheating")
	si.Write(context.Background(), []*api.StorageObject{
		charObject(bannedUserID, StorageCollectionEnforcementJournal, StorageKeyEnforcementJournal,
			charJournalJSON(t, truth), base),
	})

	// Before the flood, the index-backed check correctly REFUSES the join.
	nk := &charIndexBackedNK{si: si, indexName: idxName}
	ggReg := seatTestGuildGroupRegistry(map[string]*GuildGroup{
		groupID: seatTestGuildGroup(groupID, "TestGuild", false),
	})
	session := newSeatTestSession(uuid.FromStringOrNil(bannedUserID), []string{bannedUserID})
	label := makeLabel(groupID, evr.ModeArenaPublic)

	if err := enforceJoinSuspension(context.Background(), zap.NewNop(), nk, ggReg, label, session); err == nil {
		t.Fatalf("precondition failed: the index-backed check did not refuse a freshly-indexed ban")
	} else {
		t.Logf("before eviction: join REFUSED (%v)", err)
	}

	// 2. Normal traffic: other players get journals written. The index fills up.
	flood := make([]*api.StorageObject, 0, filler)
	for i := 0; i < filler; i++ {
		uid := uuid.Must(uuid.NewV4()).String()
		clean := NewGuildEnforcementJournal(uid)
		clean.RecordsByGroupID = map[string][]GuildEnforcementRecord{
			groupID: {{
				ID:        uuid.Must(uuid.NewV4()).String(),
				UserID:    uid,
				GroupID:   groupID,
				CreatedAt: base,
				Expiry:    base.Add(-time.Minute), // already expired: not a suspension
			}},
		}
		flood = append(flood, charObject(uid, StorageCollectionEnforcementJournal, StorageKeyEnforcementJournal,
			charJournalJSON(t, clean), base.Add(time.Duration(i+1)*time.Minute)))
	}
	si.Write(context.Background(), flood)

	// 3. The ban is still 100% valid in the source of truth.
	if n := len(truth.ActiveSuspensions()); n != 1 {
		t.Fatalf("precondition failed: the ban is no longer active in the source of truth (%d)", n)
	}

	// 4. But the index has evicted it -- and the fail-closed check now ALLOWS the join.
	err := enforceJoinSuspension(context.Background(), zap.NewNop(), nk, ggReg, label, session)

	if err != nil {
		t.Fatalf("after eviction the join was still refused (%v) -- the fail-open claim is FALSE", err)
	}

	t.Logf("after eviction : join ALLOWED (err=nil)")
	t.Logf("ground truth   : %d active suspension(s) for the user", len(truth.ActiveSuspensions()))
	t.Logf("FAIL-OPEN: a valid, unexpired ban stopped being enforced with no error and no log line")
}

// charIndexBackedNK serves StorageRead out of a storage index instead of SQL.
// This is precisely the proposal under evaluation: "serve the suspension check
// from the index".
type charIndexBackedNK struct {
	runtime.NakamaModule
	si        StorageIndex
	indexName string
}

func (m *charIndexBackedNK) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	objs, _, err := m.si.List(ctx, uuid.Nil, m.indexName, "*", 100, nil, "")
	if err != nil {
		return nil, err
	}
	wanted := make(map[string]bool, len(reads))
	for _, r := range reads {
		wanted[r.UserID+":"+r.Collection+":"+r.Key] = true
	}
	out := make([]*api.StorageObject, 0, len(reads))
	for _, o := range objs.Objects {
		if wanted[o.UserId+":"+o.Collection+":"+o.Key] {
			out = append(out, o)
		}
	}
	return out, nil
}

// --- Group D: node locality ----------------------------------------------

// D10. Two independent index instances ("nodes") do not share entries. A ban
// indexed on node A is invisible to node B.
func TestStorageIndex_TwoNodes_WriteOnOneIsInvisibleToTheOther(t *testing.T) {
	t.Parallel()

	const idxName = "charNodeLocality"
	groupID := uuid.Must(uuid.NewV4()).String()
	userID := uuid.Must(uuid.NewV4()).String()

	// Every field registered, so index-only partiality cannot be the cause;
	// node locality alone produces the divergence.
	spec := charIndexSpec{
		name:       idxName,
		collection: StorageCollectionEnforcementJournal,
		key:        StorageKeyEnforcementJournal,
		fields:     charAllJournalFields,
		maxEntries: 100,
		indexOnly:  true,
	}

	nodeA := charNode(t, nil, spec)
	nodeB := charNode(t, nil, spec)

	value := charJournalJSON(t, charSuspendedJournal(userID, groupID, "banned on node A"))
	obj := charObject(userID, StorageCollectionEnforcementJournal, StorageKeyEnforcementJournal, value, time.Now())

	// The ban is issued on node A only.
	nodeA.Write(context.Background(), []*api.StorageObject{obj})

	aObjs, err := charListAll(t, nodeA, idxName, 10)
	if err != nil {
		t.Fatalf("nodeA List: %v", err)
	}
	bObjs, err := charListAll(t, nodeB, idxName, 10)
	if err != nil {
		t.Fatalf("nodeB List: %v", err)
	}

	if len(aObjs.Objects) != 1 {
		t.Errorf("node A should see its own write, got %d entries", len(aObjs.Objects))
	}
	if len(bObjs.Objects) != 0 {
		t.Errorf("node B unexpectedly saw node A's write (%d entries) -- propagation exists, prior claim is wrong",
			len(bObjs.Objects))
	}

	// And the consequence, through the production check.
	ggReg := seatTestGuildGroupRegistry(map[string]*GuildGroup{
		groupID: seatTestGuildGroup(groupID, "TestGuild", false),
	})
	session := newSeatTestSession(uuid.FromStringOrNil(userID), []string{userID})
	label := makeLabel(groupID, evr.ModeArenaPublic)

	errA := enforceJoinSuspension(context.Background(), zap.NewNop(),
		&charIndexBackedNK{si: nodeA, indexName: idxName}, ggReg, label, session)
	errB := enforceJoinSuspension(context.Background(), zap.NewNop(),
		&charIndexBackedNK{si: nodeB, indexName: idxName}, ggReg, label, session)

	if errA == nil {
		t.Errorf("node A should refuse the join, got nil")
	}
	if errB != nil {
		t.Errorf("node B refused the join (%v) -- it somehow saw node A's write", errB)
	}

	t.Logf("node A entries=%d  join=%v", len(aObjs.Objects), errA)
	t.Logf("node B entries=%d  join=%v  <-- ban not enforced on this node", len(bObjs.Objects), errB)
}

// D11. No propagation mechanism exists. LocalStorageIndex carries no peer,
// broadcast or pub/sub collaborator, and its source references none. If
// propagation is ever added this test fails and the conclusion above must be
// re-derived.
func TestStorageIndex_HasNoCrossNodePropagationMechanism(t *testing.T) {
	t.Parallel()

	// (a) Structural: enumerate the collaborators of LocalStorageIndex.
	typ := reflect.TypeOf(LocalStorageIndex{})
	fields := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		fields = append(fields, typ.Field(i).Name)
	}
	sort.Strings(fields)

	// loadPageSize is a tuning knob for load()'s row batch size, not a
	// collaborator: it is an int with a production default, touches no peer and
	// carries no state between nodes. It exists so a test can reproduce the
	// page-boundary case that the truncation probe has to handle.
	want := []string{"config", "customFilterFunctions", "db", "indexByName", "indicesByCollection", "loadPageSize", "logger", "metrics"}
	if !reflect.DeepEqual(fields, want) {
		t.Errorf("LocalStorageIndex collaborators changed.\n got: %v\nwant: %v\n"+
			"If a peer/broadcast collaborator was added, cross-node propagation may now exist and "+
			"TestStorageIndex_TwoNodes_WriteOnOneIsInvisibleToTheOther must be re-evaluated.", fields, want)
	}

	// (b) Textual: the source mentions no propagation primitive.
	src, err := os.ReadFile("storage_index.go")
	if err != nil {
		t.Skipf("cannot read storage_index.go from the test working directory: %v", err)
	}
	lower := strings.ToLower(string(src))
	for _, token := range []string{"peer", "broadcast", "pubsub", "pub/sub", "gossip", "replicat", "cluster"} {
		if strings.Contains(lower, token) {
			t.Errorf("storage_index.go now mentions %q -- a propagation mechanism may exist; re-evaluate node locality", token)
		}
	}

	t.Logf("LocalStorageIndex collaborators: %v", fields)
	t.Logf("no peer/broadcast/pubsub/gossip/replication/cluster reference in storage_index.go")
	t.Logf("each node's index is a process-local in-memory bluge writer (BlugeInMemoryConfig)")
}

// --- Group E: query shape ------------------------------------------------

// E12. The production journal index registers ONLY guild_ids. That cannot answer
// "is user X suspended in guild G for mode M": it returns every journal that has
// ever held a record in G, expired and voided ones included, and a Go-side pass
// over the full journals is required to reach a verdict.
func TestEnforcementJournalIndex_GuildIDsOnly_CannotAnswerPointLookup(t *testing.T) {
	t.Parallel()

	// The registered shape, read from production code.
	idxMeta := (&GuildEnforcementJournal{}).StorageIndexes()
	if len(idxMeta) != 1 {
		t.Fatalf("expected 1 registered index, got %d", len(idxMeta))
	}
	meta := idxMeta[0]
	if !reflect.DeepEqual(meta.Fields, []string{"guild_ids"}) {
		t.Fatalf("journal index fields changed: %v", meta.Fields)
	}
	t.Logf("registered: name=%s collection=%s key=%s fields=%v maxEntries=%d indexOnly=%v",
		meta.Name, meta.Collection, meta.Key, meta.Fields, meta.MaxEntries, meta.IndexOnly)

	const (
		idxName = "charQueryShape"
		groupID = "guildA"
	)

	si := charNode(t, nil, charIndexSpec{
		name:       idxName,
		collection: StorageCollectionEnforcementJournal,
		key:        StorageKeyEnforcementJournal,
		fields:     meta.Fields,
		maxEntries: 100,
		indexOnly:  true,
	})

	now := time.Now().UTC()
	mk := func(userID string, expiry time.Time, void bool) *GuildEnforcementJournal {
		j := NewGuildEnforcementJournal(userID)
		rec := GuildEnforcementRecord{
			ID:             uuid.Must(uuid.NewV4()).String(),
			UserID:         userID,
			GroupID:        groupID,
			CreatedAt:      now.Add(-time.Hour),
			Expiry:         expiry,
			UserNoticeText: "n",
		}
		j.RecordsByGroupID = map[string][]GuildEnforcementRecord{groupID: {rec}}
		if void {
			j.VoidRecord(groupID, rec.ID, userID, "", "lifted")
		}
		return j
	}

	activeUser := uuid.Must(uuid.NewV4()).String()
	expiredUser := uuid.Must(uuid.NewV4()).String()
	voidedUser := uuid.Must(uuid.NewV4()).String()

	journals := GuildEnforcementJournalList{
		activeUser:  mk(activeUser, now.Add(24*time.Hour), false),
		expiredUser: mk(expiredUser, now.Add(-24*time.Hour), false),
		voidedUser:  mk(voidedUser, now.Add(24*time.Hour), true),
	}

	objects := make([]*api.StorageObject, 0, len(journals))
	for uid, j := range journals {
		objects = append(objects, charObject(uid, StorageCollectionEnforcementJournal,
			StorageKeyEnforcementJournal, charJournalJSON(t, j), now))
	}
	si.Write(context.Background(), objects)

	// The only question the index can be asked.
	hits, _, err := si.List(context.Background(), uuid.Nil, idxName,
		fmt.Sprintf("+value.guild_ids:%s", groupID), 50, nil, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(hits.Objects) != 3 {
		t.Errorf("expected the guild_ids query to match all 3 journals with any record in %s, got %d",
			groupID, len(hits.Objects))
	}

	// The actual answer requires the full journals and a Go-side pass.
	enforcements, err := CheckEnforcementSuspensions(journals, map[string][]string{})
	if err != nil {
		t.Fatalf("CheckEnforcementSuspensions: %v", err)
	}
	if _, ok := enforcements[groupID][evr.ModeArenaPublic]; !ok {
		t.Errorf("expected exactly the active user to be suspended in %s", groupID)
	}
	if rec := enforcements[groupID][evr.ModeArenaPublic]; rec.UserID != activeUser {
		t.Errorf("expected the suspension to belong to the active user, got %q", rec.UserID)
	}

	// There is no indexed field that could narrow this: no expiry, no void, no mode.
	for _, absent := range []string{"expiry", "voids", "records", "mode", "user_id"} {
		q := fmt.Sprintf("+value.%s:anything", absent)
		got, _, err := si.List(context.Background(), uuid.Nil, idxName, q, 50, nil, "")
		if err != nil {
			t.Fatalf("List(%s): %v", q, err)
		}
		if len(got.Objects) != 0 {
			t.Errorf("query %q unexpectedly matched %d entries -- the field IS indexed", q, len(got.Objects))
		}
	}

	t.Logf("guild_ids query matched %d journals; only 1 is actually an active suspension",
		len(hits.Objects))
	t.Logf("index fan-out for a single point lookup: %dx", len(hits.Objects))
	t.Logf("no expiry/void/mode field is indexed, so the narrowing must happen in Go on full journals")
}

// TestSuspensionProfileIndex_CannotAnswerPointLookup was removed when these
// characterizations were ported. It asserted that StorageIndexSuspensionProfile
// indexes only "user_id" and that an index-only read carries no suspensions --
// true when it was written, and deliberately no longer true. #527 added
// "suspensions" to the indexed fields precisely so the index could serve that
// data (evr_suspension_profile.go:174, whose comment calls the field
// mandatory), and main already pins the current behaviour in
// TestSuspensionProfileIndex_ServesSuspensionData. Keeping the old assertion
// would have made a fixed bug look like a regression.

func TestSuspensionProfile_CompiledObject_IsWriteOnlyToday(t *testing.T) {
	t.Parallel()

	// The join path reads the JOURNAL, not the compiled profile.
	src, err := os.ReadFile("evr_lobby_joinentrant_enforce.go")
	if err != nil {
		t.Skipf("cannot read evr_lobby_joinentrant_enforce.go: %v", err)
	}
	if !strings.Contains(string(src), "EnforcementJournalsLoad") {
		t.Errorf("the seat enforcement path no longer calls EnforcementJournalsLoad -- re-derive the characterization")
	}
	if strings.Contains(string(src), "SuspensionProfile") {
		t.Errorf("the seat enforcement path now references SuspensionProfile -- the compiled object is no longer unread")
	}

	// SyncFromJournal compiles ALL records, not just active suspensions --
	// so the compiled object is not "active bans", it is a full mirror.
	userID := uuid.Must(uuid.NewV4()).String()
	groupID := uuid.Must(uuid.NewV4()).String()
	j := NewGuildEnforcementJournal(userID)
	now := time.Now().UTC()
	expired := GuildEnforcementRecord{
		ID: uuid.Must(uuid.NewV4()).String(), UserID: userID, GroupID: groupID,
		CreatedAt: now.Add(-48 * time.Hour), Expiry: now.Add(-24 * time.Hour),
	}
	active := GuildEnforcementRecord{
		ID: uuid.Must(uuid.NewV4()).String(), UserID: userID, GroupID: groupID,
		CreatedAt: now.Add(-time.Hour), Expiry: now.Add(24 * time.Hour),
	}
	j.RecordsByGroupID = map[string][]GuildEnforcementRecord{groupID: {expired, active}}

	p := NewSuspensionProfile(userID)
	p.SyncFromJournal(j)

	if len(p.Suspensions) != 2 {
		t.Errorf("expected SyncFromJournal to compile ALL 2 records, got %d", len(p.Suspensions))
	}
	if n := len(j.ActiveSuspensions()); n != 1 {
		t.Errorf("expected 1 active suspension in the journal, got %d", n)
	}

	t.Logf("journal: 2 records, %d active", len(j.ActiveSuspensions()))
	t.Logf("compiled profile: %d records -- it mirrors everything, it does not distil active bans",
		len(p.Suspensions))
	t.Logf("a consumer of the compiled object must still evaluate expiry/voids in Go")
}

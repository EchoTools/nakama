package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// altClearTestModule drives MigrationClearAlternateMatches against in-memory
// storage.
//
// It embeds occTestNakamaModule for StorageRead/seedObject, but deliberately
// OVERRIDES StorageWrite: the embedded one applies writes as it walks the
// slice and returns mid-loop on the first conflict, so earlier rows in a
// rejected batch stay committed. Production does the opposite --
// StorageWriteObjects (server/core_storage.go:583-613) runs the whole batch
// inside ExecuteInTxPgx and converts runtime.ErrStorageRejectedVersion into a
// returned error, so the transaction rolls back and NONE of the rows commit,
// and the acks slice comes back nil. Nor is the batch retried:
// executeInTxPostgresPgx (server/db.go:418-447) retries only when errors.As
// finds a *pgconn.PgError with SQLSTATE class 40, and a version rejection is a
// Go sentinel wrapped in a statusError -- it is terminal on the first attempt.
// Testing P1-3 against the embedded write would test the wrong machine.
type altClearTestModule struct {
	*occTestNakamaModule

	// listed is what StorageList hands back, in one page.
	listed []*api.StorageObject

	// indexErr, when set, fails StorageIndexList. That is the real failure
	// mode of the rebuild: UpdateAlternates -> LoginAlternateSearch ->
	// LoginAlternatePatternSearch -> nk.StorageIndexList
	// (server/evr_authenticate_alts.go:139-141), which is the first of the
	// three I/O-backed error returns in UpdateAlternates
	// (server/evr_authenticate_history.go:481+). A context deadline, a reset
	// connection or an unavailable index all surface here.
	indexErr error

	// conflictUserIDs model a racing login: that row's stored version moved
	// on, so an OCC write carrying the version the migration read is
	// rejected -- and with it the entire batch.
	conflictUserIDs map[string]bool

	// writeBatches records the user IDs submitted in each StorageWrite call.
	writeBatches [][]string
}

func newAltClearTestModule() *altClearTestModule {
	return &altClearTestModule{
		occTestNakamaModule: newOCCTestNakamaModule(),
		conflictUserIDs:     map[string]bool{},
	}
}

func (m *altClearTestModule) StorageList(ctx context.Context, callerID, userID, collection string, limit int, cursor string) ([]*api.StorageObject, string, error) {
	if cursor != "" {
		return nil, "", nil
	}
	return m.listed, "", nil
}

func (m *altClearTestModule) StorageIndexList(ctx context.Context, callerID, indexName, query string, limit int, order []string, cursor string) (*api.StorageObjects, string, error) {
	if m.indexErr != nil {
		return nil, "", m.indexErr
	}
	// No alternates found. This is the migration's whole point: the fixed
	// detection code no longer links these accounts, so the cleared (empty)
	// map is the correct rebuild result and must still be persisted.
	return &api.StorageObjects{Objects: nil}, "", nil
}

func (m *altClearTestModule) AccountsGetId(ctx context.Context, userIDs []string) ([]*api.Account, error) {
	return nil, nil
}

// StorageWrite models the production transaction: all-or-nothing, nil acks on
// rejection, no retry. See the type comment for the citations.
func (m *altClearTestModule) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writeAttempts++

	batch := make([]string, 0, len(writes))
	for _, w := range writes {
		batch = append(batch, w.UserID)
	}
	m.writeBatches = append(m.writeBatches, batch)

	// Pass 1: validate every row. Any rejection aborts the whole batch
	// before a single row is applied.
	for _, w := range writes {
		if m.conflictUserIDs[w.UserID] {
			return nil, runtime.ErrStorageRejectedVersion
		}
		existing, ok := m.objects[occStorageKey(w.UserID, w.Collection, w.Key)]
		if w.Version != "" && w.Version != "*" && (!ok || existing.Version != w.Version) {
			return nil, runtime.ErrStorageRejectedVersion
		}
	}

	// Pass 2: commit.
	acks := make([]*api.StorageObjectAck, 0, len(writes))
	for _, w := range writes {
		k := occStorageKey(w.UserID, w.Collection, w.Key)
		ver := m.versionLocked()
		m.objects[k] = &api.StorageObject{
			Collection: w.Collection,
			Key:        w.Key,
			UserId:     w.UserID,
			Value:      w.Value,
			Version:    ver,
		}
		acks = append(acks, &api.StorageObjectAck{
			Collection: w.Collection, Key: w.Key, UserId: w.UserID, Version: ver,
		})
	}
	return acks, nil
}

// seedLinkedAccount stores a login history that carries a genuine alternate
// link plus enough real login entries for AltSearchPatterns to return a
// non-empty pattern set -- without patterns, LoginAlternateSearch returns
// early at server/evr_authenticate_alts.go:120 and never reaches the index,
// so the failure under test would never fire.
func (m *altClearTestModule) seedLinkedAccount(t *testing.T, userID, linkedUserID string) {
	t.Helper()

	h := NewLoginHistory(userID)
	h.History = map[string]*LoginHistoryEntry{
		"entry": {
			CreatedAt: time.Now().Add(-time.Hour),
			UpdatedAt: time.Now(),
			XPID:      evr.EvrId{PlatformCode: 4, AccountId: 1000},
			ClientIP:  "203.0.113.7",
			LoginData: &evr.LoginProfile{HMDSerialNumber: "HMD-" + userID},
		},
	}
	h.AlternateMatches = map[string][]*AlternateSearchMatch{
		linkedUserID: {{OtherUserID: linkedUserID}},
	}
	h.SecondDegreeAlternates = []string{linkedUserID}

	data, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal seed history: %v", err)
	}
	version := m.seedObject(userID, LoginStorageCollection, LoginHistoryStorageKey, string(data))

	m.listed = append(m.listed, &api.StorageObject{
		Collection: LoginStorageCollection,
		Key:        LoginHistoryStorageKey,
		UserId:     userID,
		Value:      string(data),
		Version:    version,
	})

	// Guard the fixture itself: if these patterns were empty the rebuild
	// would short-circuit and every assertion below would pass vacuously.
	h.SetStorageMeta(StorableMetadata{UserID: userID, Version: version})
	if got := len(h.AltSearchPatterns()); got == 0 {
		t.Fatalf("fixture is inert: AltSearchPatterns() is empty for %s, so the rebuild never reaches the alt index", userID)
	}
}

// storedHistory reads back what the migration actually persisted.
func (m *altClearTestModule) storedHistory(t *testing.T, userID string) *LoginHistory {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[occStorageKey(userID, LoginStorageCollection, LoginHistoryStorageKey)]
	if !ok {
		t.Fatalf("no stored login history for %s", userID)
	}
	h := NewLoginHistory(userID)
	if err := json.Unmarshal([]byte(obj.Value), h); err != nil {
		t.Fatalf("unmarshal stored history for %s: %v", userID, err)
	}
	return h
}

func runAltClearMigration(t *testing.T, nk runtime.NakamaModule) *captureLogger {
	t.Helper()
	logger := newCaptureLogger()
	m := &MigrationClearAlternateMatches{}
	if err := m.MigrateSystem(context.Background(), logger, nil, nk); err != nil {
		t.Fatalf("MigrateSystem returned an error: %v", err)
	}
	return logger
}

// completionField pulls one field off the migration's final summary line.
func completionField(t *testing.T, logger *captureLogger, field string) int {
	t.Helper()
	event, ok := logger.find("info", "alt-clear migration complete")
	if !ok {
		t.Fatalf("migration never logged its completion line")
	}
	raw, ok := event.fields[field]
	if !ok {
		t.Fatalf("completion line has no %q field; fields were %v", field, event.fields)
	}
	n, ok := raw.(int)
	if !ok {
		t.Fatalf("completion field %q is %T, want int", field, raw)
	}
	return n
}

// TestClearAltsMigration_SuccessfulRebuildStillClears is the positive control
// for P1-2: the fix must change the failure path ONLY.
//
// Here the index is healthy and reports no alternates -- the exact situation
// the migration exists to correct, an account whose stored links were the
// v3.27.2-evr.321 false positives. The cleared empty map is the correct
// rebuild result and must still be persisted.
//
// This test passes both before and after the fix. If it ever fails, the fix
// has changed success-path behaviour and must be rejected.
func TestClearAltsMigration_SuccessfulRebuildStillClears(t *testing.T) {
	nk := newAltClearTestModule()
	nk.seedLinkedAccount(t, "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222")

	logger := runAltClearMigration(t, nk)

	stored := nk.storedHistory(t, "11111111-1111-1111-1111-111111111111")
	if len(stored.AlternateMatches) != 0 {
		t.Errorf("AlternateMatches = %v, want empty: a successful rebuild that finds nothing must persist the clear", stored.AlternateMatches)
	}
	if len(stored.SecondDegreeAlternates) != 0 {
		t.Errorf("SecondDegreeAlternates = %v, want empty", stored.SecondDegreeAlternates)
	}
	if got := completionField(t, logger, "cleared"); got != 1 {
		t.Errorf("cleared = %d, want 1", got)
	}
}

// TestClearAltsMigration_TestDoubleModelsProductionRollback is the positive
// control for the double itself.
//
// P1-3's whole premise is that production rolls the batch back. If the double
// committed the non-conflicting rows of a rejected batch -- which the
// occTestNakamaModule it embeds does -- the P1-3 test would pass against
// unfixed code and prove nothing. This asserts the double has the semantics
// the citations describe.
func TestClearAltsMigration_TestDoubleModelsProductionRollback(t *testing.T) {
	nk := newAltClearTestModule()
	goodVer := nk.seedObject("11111111-1111-1111-1111-111111111111", LoginStorageCollection, LoginHistoryStorageKey, `{"a":1}`)
	nk.seedObject("22222222-2222-2222-2222-222222222222", LoginStorageCollection, LoginHistoryStorageKey, `{"a":1}`)
	nk.conflictUserIDs["22222222-2222-2222-2222-222222222222"] = true

	acks, err := nk.StorageWrite(context.Background(), []*runtime.StorageWrite{
		{Collection: LoginStorageCollection, Key: LoginHistoryStorageKey, UserID: "11111111-1111-1111-1111-111111111111", Value: `{"a":2}`, Version: goodVer},
		{Collection: LoginStorageCollection, Key: LoginHistoryStorageKey, UserID: "22222222-2222-2222-2222-222222222222", Value: `{"a":2}`, Version: "stale"},
	})

	if err == nil {
		t.Fatal("want a rejection error from the batch")
	}
	if acks != nil {
		t.Errorf("acks = %v, want nil: StorageWriteObjects returns nil acks when the transaction fails", acks)
	}
	nk.mu.Lock()
	got := nk.objects[occStorageKey("11111111-1111-1111-1111-111111111111", LoginStorageCollection, LoginHistoryStorageKey)].Value
	nk.mu.Unlock()
	if got != `{"a":1}` {
		t.Errorf("non-conflicting row = %s, want the original {\"a\":1}: the double committed a row that production would have rolled back", got)
	}
}

// TestClearAltsMigration_FixtureReachesTheRebuild is the positive control for
// every zero above: it proves the migration actually walked the seeded
// account and reached UpdateAlternates, rather than filtering it out at the
// key check or the pattern short-circuit and passing vacuously.
func TestClearAltsMigration_FixtureReachesTheRebuild(t *testing.T) {
	nk := newAltClearTestModule()
	nk.seedLinkedAccount(t, "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222")

	var indexQueries int
	probe := &indexProbeModule{altClearTestModule: nk, calls: &indexQueries}

	logger := runAltClearMigration(t, probe)

	if got := completionField(t, logger, "walked"); got != 1 {
		t.Errorf("walked = %d, want 1: the migration did not examine the seeded account", got)
	}
	if indexQueries == 0 {
		t.Error("the rebuild never queried the alt index; the failure-injection point is unreachable and every P1-2 assertion would be vacuous")
	}

	// Sanity: the pages the migration submitted are the accounts we seeded.
	var submitted []string
	for _, batch := range nk.writeBatches {
		submitted = append(submitted, batch...)
	}
	sort.Strings(submitted)
	if fmt.Sprint(submitted) != "[11111111-1111-1111-1111-111111111111]" {
		t.Errorf("submitted writes = %v, want the one seeded account", submitted)
	}
}

type indexProbeModule struct {
	*altClearTestModule
	calls *int
}

func (m *indexProbeModule) StorageIndexList(ctx context.Context, callerID, indexName, query string, limit int, order []string, cursor string) (*api.StorageObjects, string, error) {
	*m.calls++
	return m.altClearTestModule.StorageIndexList(ctx, callerID, indexName, query, limit, order, cursor)
}

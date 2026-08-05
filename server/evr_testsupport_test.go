package server

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// requireCharacterizationFixture skips the test when a developer-local
// characterization fixture is missing.
//
// These fixtures are captures of live production matchmaker state. They live
// under _local/ (which .gitignore excludes) or in /tmp, are produced ad hoc by
// whoever is investigating a matchmaking issue, and have never been committed —
// `git log --all -- _local` is empty. A clean checkout and CI therefore never
// have them, and the tests that read them previously failed with a bare
// "Error opening file".
func requireCharacterizationFixture(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Skipf("skipping: characterization fixture %q is not present (%v). These fixtures are developer-local captures of live matchmaker state; they are gitignored and are not part of the repo.", path, err)
	}
}

// evrTestNakamaModule is an in-memory runtime.NakamaModule for EVR tests that
// only need storage plus a handful of fire-and-forget side-effect calls
// (metrics, streams, events, leaderboards).
//
// It exists so EVR behaviour tests do not require a live CockroachDB. The
// storage half is the OCC-correct mock from evr_latencyhistory_test.go; the rest
// are inert stubs, which is faithful enough because the code under test treats
// all of them as best-effort (failures are logged, not propagated).
//
// Fidelity caveat: production passes a *RuntimeGoNakamaModule, and several
// branches in MatchLeave are guarded by `nk.(*RuntimeGoNakamaModule)` type
// assertions (session-lifecycle transitions, party-registry reservation
// clearing). Those branches are NOT exercised when this double is used. Tests
// asserting on those behaviours need the real module and a database.
type evrTestNakamaModule struct {
	*occTestNakamaModule
}

func newEvrTestNakamaModule() *evrTestNakamaModule {
	return &evrTestNakamaModule{occTestNakamaModule: newOCCTestNakamaModule()}
}

func (m *evrTestNakamaModule) StorageList(ctx context.Context, callerID, userID, collection string, limit int, cursor string) ([]*api.StorageObject, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	objects := make([]*api.StorageObject, 0, len(m.objects))
	for k, obj := range m.objects {
		if !strings.HasPrefix(k, userID+":"+collection+":") {
			continue
		}
		objects = append(objects, obj)
	}
	return objects, "", nil
}

func (m *evrTestNakamaModule) StorageDelete(ctx context.Context, deletes []*runtime.StorageDelete) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range deletes {
		delete(m.objects, occStorageKey(d.UserID, d.Collection, d.Key))
	}
	return nil
}

// --- inert side-effect stubs ---

func (m *evrTestNakamaModule) MetricsCounterAdd(name string, tags map[string]string, delta int64) {}
func (m *evrTestNakamaModule) MetricsGaugeSet(name string, tags map[string]string, value float64) {}
func (m *evrTestNakamaModule) MetricsTimerRecord(name string, tags map[string]string, value time.Duration) {
}

func (m *evrTestNakamaModule) Event(ctx context.Context, evt *api.Event) error { return nil }

func (m *evrTestNakamaModule) StreamUserList(mode uint8, subject, subcontext, label string, includeHidden, includeNotHidden bool) ([]runtime.Presence, error) {
	return nil, nil
}

func (m *evrTestNakamaModule) StreamUserLeave(mode uint8, subject, subcontext, label, userID, sessionID string) error {
	return nil
}

func (m *evrTestNakamaModule) StreamUserJoin(mode uint8, subject, subcontext, label, userID, sessionID string, hidden, persistence bool, status string) (bool, error) {
	return true, nil
}

func (m *evrTestNakamaModule) StreamSend(mode uint8, subject, subcontext, label, data string, presences []runtime.Presence, reliable bool) error {
	return nil
}

func (m *evrTestNakamaModule) NotificationSend(ctx context.Context, userID, subject string, content map[string]interface{}, code int, sender string, persistent bool) error {
	return nil
}

// LeaderboardCreate / LeaderboardRecordWrite / LeaderboardRecordsList are inert:
// the EVR stat-accumulation path treats leaderboard failures as warnings, so the
// tests using this double never assert on leaderboard state.
func (m *evrTestNakamaModule) LeaderboardCreate(ctx context.Context, id string, authoritative bool, sortOrder, operator, resetSchedule string, metadata map[string]interface{}, enableRanks bool) error {
	return nil
}

func (m *evrTestNakamaModule) LeaderboardRecordWrite(ctx context.Context, id, ownerID, username string, score, subscore int64, metadata map[string]interface{}, overrideOperator *int) (*api.LeaderboardRecord, error) {
	return &api.LeaderboardRecord{LeaderboardId: id, OwnerId: ownerID}, nil
}

func (m *evrTestNakamaModule) LeaderboardRecordsList(ctx context.Context, id string, ownerIDs []string, limit int, cursor string, expiry int64) ([]*api.LeaderboardRecord, []*api.LeaderboardRecord, string, string, error) {
	return nil, nil, "", "", nil
}

func (m *evrTestNakamaModule) GroupsGetId(ctx context.Context, groupIDs []string) ([]*api.Group, error) {
	return nil, nil
}

func (m *evrTestNakamaModule) UsersGetId(ctx context.Context, userIDs []string, facebookIDs []string) ([]*api.User, error) {
	return nil, nil
}

// disableEvrRuntimeModules temporarily clears EvrRuntimeModuleFns and returns a
// function that restores the previous value.
//
// Why this exists: runtime_go.go's NewRuntime calls every function in
// EvrRuntimeModuleFns and escalates any error to startupLogger.Fatal, which is a
// zap fatal — it calls os.Exit(1) and kills the entire test binary, taking every
// test that had not yet run down with it. InitializeEvrRuntimeModule returns an
// error whenever DISCORD_BOT_TOKEN is unset, which is always the case in CI and
// on a developer machine without production secrets.
//
// Supplying a dummy token is not a safe alternative: InitializeEvrRuntimeModule
// would then run to completion and spawn background goroutines (notably
// `go MigrateSystem(...)`, which sleeps 20s and then issues storage queries)
// against the nil *sql.DB these tests pass in, panicking mid-run.
//
// Tests that need a *Runtime only need the generic Nakama runtime, so the EVR
// module initialisers are disabled for the duration of the NewRuntime call.
//
// Not safe for use from parallel tests: it mutates a package-level variable.
// Call it synchronously around NewRuntime and restore immediately.
func disableEvrRuntimeModules() (restore func()) {
	saved := EvrRuntimeModuleFns
	EvrRuntimeModuleFns = nil
	return func() {
		EvrRuntimeModuleFns = saved
	}
}

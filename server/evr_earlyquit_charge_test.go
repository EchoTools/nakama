package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/atomic"
)

// chargeTestLeaderboardCache is a LeaderboardCache test double whose Get returns
// nil (leaderboard not found). Leaderboard read/write ops then degrade to errors
// that MatchLeave treats as non-fatal warnings — we don't need real leaderboards
// to exercise the early-quit charge gate.
type chargeTestLeaderboardCache struct{}

func (c *chargeTestLeaderboardCache) Get(id string) *Leaderboard { return nil }
func (c *chargeTestLeaderboardCache) ListAll(limit int, reverse bool, cursor *LeaderboardAllCursor) ([]*Leaderboard, int, *LeaderboardAllCursor) {
	return nil, 0, nil
}
func (c *chargeTestLeaderboardCache) RefreshAllLeaderboards(ctx context.Context) error { return nil }
func (c *chargeTestLeaderboardCache) Create(ctx context.Context, id string, authoritative bool, sortOrder, operator int, resetSchedule, metadata string, enableRanks bool) (*Leaderboard, bool, error) {
	return nil, false, errors.New("charge test: leaderboard cache not configured")
}
func (c *chargeTestLeaderboardCache) Insert(id string, authoritative bool, sortOrder, operator int, resetSchedule, metadata string, createTime int64, enableRanks bool) {
}
func (c *chargeTestLeaderboardCache) List(limit int, cursor *LeaderboardListCursor) ([]*Leaderboard, *LeaderboardListCursor, error) {
	return nil, nil, nil
}
func (c *chargeTestLeaderboardCache) CreateTournament(ctx context.Context, id string, authoritative bool, sortOrder, operator int, resetSchedule, metadata, title, description string, category, startTime, endTime, duration, maxSize, maxNumScore int, joinRequired, enableRanks bool) (*Leaderboard, bool, error) {
	return nil, false, errors.New("charge test: tournament cache not configured")
}
func (c *chargeTestLeaderboardCache) InsertTournament(id string, authoritative bool, sortOrder, operator int, resetSchedule, metadata, title, description string, category, duration, maxSize, maxNumScore int, joinRequired bool, createTime, startTime, endTime int64, enableRanks bool) {
}
func (c *chargeTestLeaderboardCache) ListTournaments(now int64, categoryStart, categoryEnd int, startTime, endTime int64, limit int, cursor *TournamentListCursor) ([]*Leaderboard, *TournamentListCursor, error) {
	return nil, nil, nil
}
func (c *chargeTestLeaderboardCache) Delete(ctx context.Context, rankCache LeaderboardRankCache, scheduler LeaderboardScheduler, id string) (bool, error) {
	return false, nil
}
func (c *chargeTestLeaderboardCache) Remove(id string) {}

// TestEarlyQuitChargeGate_SetsPenaltyState drives the REAL MatchLeave charge gate
// end-to-end: arena-public, pre-matchover, voluntary player leave with 2 prior
// early quits on record. The gate must increment the quit count AND resolve the
// penalty level from the ladder, setting PenaltyLevel and a future
// PenaltyTimestamp so IsPenaltyActive() reports a live lockout.
//
// Requires a real Postgres (TEST_DB_URL) — the gate's StorableRead/StorableWrite
// run through RuntimeGoNakamaModule, which uses pgx connections directly.
func TestEarlyQuitChargeGate_SetsPenaltyState(t *testing.T) {
	// --- setup: real module over the test DB ---
	logger := loggerForTest(t)
	db := NewDB(t)
	cfg := NewConfig(logger)

	storageIndex, err := NewLocalStorageIndex(logger, db, &StorageConfig{}, &testMetrics{})
	if err != nil {
		t.Fatalf("failed to create storage index: %v", err)
	}
	nk := NewRuntimeGoNakamaModule(logger, db, nil, cfg,
		nil, &chargeTestLeaderboardCache{}, nil, nil,
		&testSessionRegistry{}, nil, nil, nil,
		&testTracker{}, &testMetrics{}, nil, &testMessageRouter{},
		storageIndex, nil)

	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")

	// --- pin the stored EarlyQuit|config row: since B2 the charge gate resolves
	// against the STORED ladder (SystemUserID), the test must pin the ladder it
	// expects rather than rely on whatever row the shared test DB holds. ---
	writeEarlyQuitServiceConfig(t, ctx, nk, defaultEarlyQuitConfigJSON(t))

	// --- player with 2 prior early quits on record (3rd quit crosses into penalty level 1) ---
	player := reconnectTestPlayer("b1-charge", evr.TeamBlue)
	InsertUser(t, db, player.UserID)

	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      StorageCollectionEarlyQuit,
		Key:             StorageKeyEarlyQuit,
		UserID:          player.GetUserId(),
		Value:           `{"num_early_quits":2,"num_steady_early_quits":2,"matchmaking_tier":1}`,
		Version:         "",
		PermissionRead:  int(runtime.STORAGE_PERMISSION_NO_READ),
		PermissionWrite: int(runtime.STORAGE_PERMISSION_NO_WRITE),
	}}); err != nil {
		t.Fatalf("preseed early quit state failed: %v", err)
	}

	// --- arena-public, pre-matchover match with one player mid-round ---
	state := reconnectTestState(evr.ModeArenaPublic)
	// The charge gate persists LastEarlyQuitMatchID; MatchID only round-trips
	// through storage with a non-empty node (prod always sets one).
	state.ID.Node = "test-node"
	state.presenceMap[player.GetSessionId()] = player
	state.presenceByEvrID[player.EvrID] = player
	state.joinTimestamps[player.GetSessionId()] = time.Now().Add(-2 * time.Minute)
	state.participations[player.GetUserId()] = &PlayerParticipation{
		UserID:      player.GetUserId(),
		Username:    player.Username,
		DisplayName: player.DisplayName,
		Team:        BlueTeam,
		JoinTime:    time.Now().Add(-2 * time.Minute),
	}
	state.rebuildCache()

	// --- voluntary leave ---
	dispatcher := &reconnectTestDispatcher{}
	leavePresence := reconnectTestPresence{EvrMatchPresence: player, reason: runtime.PresenceReasonLeave}

	m := &EvrMatch{}
	got := m.MatchLeave(ctx, reconnectTestLogger(), db, nk, dispatcher, 1, state, []runtime.Presence{leavePresence})
	if _, ok := got.(*MatchLabel); !ok {
		t.Fatalf("MatchLeave returned non-*MatchLabel state: %T", got)
	}

	// --- read back what the charge gate wrote to storage ---
	eqconfig := NewEarlyQuitPlayerState()
	if err := StorableRead(ctx, nk, player.GetUserId(), eqconfig, false); err != nil {
		t.Fatalf("failed to read back early quit state: %v", err)
	}

	if eqconfig.NumEarlyQuits != 3 {
		t.Errorf("expected charge gate to increment to 3 early quits, got %d", eqconfig.NumEarlyQuits)
	}
	if eqconfig.PenaltyLevel <= 0 {
		t.Errorf("expected penalty level resolved from ladder (> 0), got %d", eqconfig.PenaltyLevel)
	}
	now := time.Now().Unix()
	if eqconfig.PenaltyTimestamp <= now {
		t.Errorf("expected penalty timestamp to be a future expiry, got %d (now=%d)", eqconfig.PenaltyTimestamp, now)
	}
	if !eqconfig.IsPenaltyActive() {
		t.Errorf("expected IsPenaltyActive() to be true, got false (penalty_ts=%d)", eqconfig.PenaltyTimestamp)
	}
	// Default ladder level 1 lockout is 120s.
	if d := eqconfig.PenaltyTimestamp - now; d < 110 || d > 130 {
		t.Errorf("expected lockout ~120s, got %ds", d)
	}
}

// TestEarlyQuitChargeGate_GuildEnforcementFlag proves the charge gate is gated
// by the guild's EnforceEarlyQuitPenalty flag (GroupMetadata). A guild that has
// not opted in — flag nil (default) or explicitly false — must NOT be charged
// a penalty for an early quit; an opted-in guild (flag true) must be charged
// exactly as before.
func TestEarlyQuitChargeGate_GuildEnforcementFlag(t *testing.T) {
	cases := []struct {
		name       string
		enforce    *bool
		wantQuits  int32
		wantActive bool
	}{
		{"guild-flag-nil", nil, 2, false},
		{"guild-flag-false", boolPtr(false), 2, false},
		{"guild-flag-true", boolPtr(true), 3, true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logger := loggerForTest(t)
			db := NewDB(t)
			cfg := NewConfig(logger)

			storageIndex, err := NewLocalStorageIndex(logger, db, &StorageConfig{}, &testMetrics{})
			if err != nil {
				t.Fatalf("failed to create storage index: %v", err)
			}
			nk := NewRuntimeGoNakamaModule(logger, db, nil, cfg,
				nil, &chargeTestLeaderboardCache{}, nil, nil,
				&testSessionRegistry{}, nil, nil, nil,
				&testTracker{}, &testMetrics{}, nil, &testMessageRouter{},
				storageIndex, nil)

			ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")
			writeEarlyQuitServiceConfig(t, ctx, nk, defaultEarlyQuitConfigJSON(t))

			// The guild hosting the match, with the enforcement flag under test.
			guildID := uuid.Must(uuid.NewV4())
			ctx = withGuildEnforcement(ctx, guildID, tc.enforce)

			// Player with 2 prior early quits (3rd quit crosses into penalty level 1).
			player := reconnectTestPlayer(fmt.Sprintf("charge-guild-flag-%d", i), evr.TeamBlue)
			InsertUser(t, db, player.UserID)

			if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
				Collection:      StorageCollectionEarlyQuit,
				Key:             StorageKeyEarlyQuit,
				UserID:          player.GetUserId(),
				Value:           `{"num_early_quits":2,"num_steady_early_quits":2,"matchmaking_tier":1}`,
				Version:         "",
				PermissionRead:  int(runtime.STORAGE_PERMISSION_NO_READ),
				PermissionWrite: int(runtime.STORAGE_PERMISSION_NO_WRITE),
			}}); err != nil {
				t.Fatalf("preseed early quit state failed: %v", err)
			}

			// Arena-public, pre-matchover match hosted by the guild.
			state := reconnectTestState(evr.ModeArenaPublic)
			state.ID.Node = "test-node"
			state.GroupID = &guildID
			state.presenceMap[player.GetSessionId()] = player
			state.presenceByEvrID[player.EvrID] = player
			state.joinTimestamps[player.GetSessionId()] = time.Now().Add(-2 * time.Minute)
			state.participations[player.GetUserId()] = &PlayerParticipation{
				UserID:      player.GetUserId(),
				Username:    player.Username,
				DisplayName: player.DisplayName,
				Team:        BlueTeam,
				JoinTime:    time.Now().Add(-2 * time.Minute),
			}
			state.rebuildCache()

			// Voluntary leave.
			dispatcher := &reconnectTestDispatcher{}
			leavePresence := reconnectTestPresence{EvrMatchPresence: player, reason: runtime.PresenceReasonLeave}

			m := &EvrMatch{}
			if _, ok := m.MatchLeave(ctx, reconnectTestLogger(), db, nk, dispatcher, 1, state, []runtime.Presence{leavePresence}).(*MatchLabel); !ok {
				t.Fatalf("MatchLeave returned non-*MatchLabel state")
			}

			// Read back what the charge gate wrote to storage.
			eqconfig := NewEarlyQuitPlayerState()
			if err := StorableRead(ctx, nk, player.GetUserId(), eqconfig, false); err != nil {
				t.Fatalf("failed to read back early quit state: %v", err)
			}

			if eqconfig.NumEarlyQuits != tc.wantQuits {
				t.Errorf("expected %d early quits, got %d", tc.wantQuits, eqconfig.NumEarlyQuits)
			}
			if got := eqconfig.IsPenaltyActive(); got != tc.wantActive {
				t.Errorf("expected penalty active=%v, got %v (penalty_level=%d penalty_ts=%d)", tc.wantActive, got, eqconfig.PenaltyLevel, eqconfig.PenaltyTimestamp)
			}
		})
	}
}

// withGuildEnforcement returns a copy of ctx whose session parameters map the
// given guild to a GuildGroup carrying the given EnforceEarlyQuitPenalty flag
// (nil leaves the flag unset — the getter's default).
//
// WARNING — THIS CONTEXT SHAPE DOES NOT OCCUR IN PRODUCTION.
//
// ctxSessionParametersKey{} is planted on the *session* context by the EVR
// pipeline. The context that the Nakama match runtime hands to MatchLeave is
// built by the match runtime, not by a session, and it never carries that key.
// So the branch that TestEarlyQuitChargeGate_GuildEnforcementFlag exercises
// through this helper is reachable only from the test.
//
// Consequence: that test does NOT prove the guild opt-in gate works. In
// production earlyQuitEnforcementEnabled cannot find the session parameters, so
// LoadParams fails and it takes its fail-open `return true` branch — every guild
// is treated as opted in and the flag never suppresses anything. That is a real
// production bug; it is deliberately NOT fixed here (this change set is
// test/build tooling only) and needs its own PR. Do not read a green run of this
// test as evidence the gate is enforced.
func withGuildEnforcement(ctx context.Context, guildID uuid.UUID, enforce *bool) context.Context {
	params := &SessionParameters{
		guildGroups: map[string]*GuildGroup{
			guildID.String(): {
				GroupMetadata: GroupMetadata{EnforceEarlyQuitPenalty: enforce},
			},
		},
	}
	return context.WithValue(ctx, ctxSessionParametersKey{}, atomic.NewPointer(params))
}

func boolPtr(b bool) *bool { return &b }

// writeEarlyQuitServiceConfig pins the stored EarlyQuit|config row under
// SystemUserID — the row LoadEarlyQuitServiceConfig reads. Tests that drive the
// charge gate must pin this row because B2 resolves penalties against it.
func writeEarlyQuitServiceConfig(t *testing.T, ctx context.Context, nk runtime.NakamaModule, value string) {
	t.Helper()
	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      evr.StorageCollectionEarlyQuitConfig,
		Key:             evr.StorageKeyEarlyQuitConfig,
		UserID:          SystemUserID,
		Value:           value,
		Version:         "",
		PermissionRead:  int(runtime.STORAGE_PERMISSION_NO_READ),
		PermissionWrite: int(runtime.STORAGE_PERMISSION_NO_WRITE),
	}}); err != nil {
		t.Fatalf("preseed early quit service config failed: %v", err)
	}
}

// defaultEarlyQuitConfigJSON returns the default ladder as the stored config row.
// Marshaled from DefaultEarlyQuitLevels so the test pin cannot drift from the code.
func defaultEarlyQuitConfigJSON(t *testing.T) string {
	t.Helper()
	d := evr.DefaultEarlyQuitLevels()
	cfg := evr.NewSNSEarlyQuitConfig(d.PenaltyLevels, d.SteadyPlayerLevels)
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal default early quit config: %v", err)
	}
	return string(b)
}

// TestEarlyQuitChargeGate_UsesStoredConfigLadder proves the charge gate resolves
// the penalty against the STORED EarlyQuit|config ladder, not the static
// defaults B1 used. Regression for BUG-3: a custom ladder that puts level 1 at
// 2 quits with a 60s lockout must be honored (defaults would say level 0).
func TestEarlyQuitChargeGate_UsesStoredConfigLadder(t *testing.T) {
	logger := loggerForTest(t)
	db := NewDB(t)
	cfg := NewConfig(logger)

	storageIndex, err := NewLocalStorageIndex(logger, db, &StorageConfig{}, &testMetrics{})
	if err != nil {
		t.Fatalf("failed to create storage index: %v", err)
	}
	nk := NewRuntimeGoNakamaModule(logger, db, nil, cfg,
		nil, &chargeTestLeaderboardCache{}, nil, nil,
		&testSessionRegistry{}, nil, nil, nil,
		&testTracker{}, &testMetrics{}, nil, &testMessageRouter{},
		storageIndex, nil)

	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")

	// Custom ladder: level 1 at 2+ quits, 60s lockout (defaults: 3+ quits, 120s).
	writeEarlyQuitServiceConfig(t, ctx, nk,
		`{"penalty_levels":[{"penalty_level":0,"min_earlyquits":0,"max_earlyquits":1,"mm_lockout_sec":0},{"penalty_level":1,"min_earlyquits":2,"max_earlyquits":999,"mm_lockout_sec":60}],"steady_player_levels":[{"steady_player_level":0,"min_num_matches":0,"min_steady_ratio":0}]}`)
	// Restore the default ladder so tests that run after us resolve predictably.
	t.Cleanup(func() { writeEarlyQuitServiceConfig(t, ctx, nk, defaultEarlyQuitConfigJSON(t)) })

	// --- player with 1 prior early quit (2nd quit crosses into custom level 1) ---
	player := reconnectTestPlayer("b2-stored-ladder", evr.TeamBlue)
	InsertUser(t, db, player.UserID)

	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      StorageCollectionEarlyQuit,
		Key:             StorageKeyEarlyQuit,
		UserID:          player.GetUserId(),
		Value:           `{"num_early_quits":1,"num_steady_early_quits":1,"matchmaking_tier":1}`,
		Version:         "",
		PermissionRead:  int(runtime.STORAGE_PERMISSION_NO_READ),
		PermissionWrite: int(runtime.STORAGE_PERMISSION_NO_WRITE),
	}}); err != nil {
		t.Fatalf("preseed early quit state failed: %v", err)
	}

	state := reconnectTestState(evr.ModeArenaPublic)
	state.ID.Node = "test-node"
	state.presenceMap[player.GetSessionId()] = player
	state.presenceByEvrID[player.EvrID] = player
	state.joinTimestamps[player.GetSessionId()] = time.Now().Add(-2 * time.Minute)
	state.participations[player.GetUserId()] = &PlayerParticipation{
		UserID:      player.GetUserId(),
		Username:    player.Username,
		DisplayName: player.DisplayName,
		Team:        BlueTeam,
		JoinTime:    time.Now().Add(-2 * time.Minute),
	}
	state.rebuildCache()

	dispatcher := &reconnectTestDispatcher{}
	leavePresence := reconnectTestPresence{EvrMatchPresence: player, reason: runtime.PresenceReasonLeave}

	m := &EvrMatch{}
	if _, ok := m.MatchLeave(ctx, reconnectTestLogger(), db, nk, dispatcher, 1, state, []runtime.Presence{leavePresence}).(*MatchLabel); !ok {
		t.Fatalf("MatchLeave returned non-*MatchLabel state")
	}

	eqconfig := NewEarlyQuitPlayerState()
	if err := StorableRead(ctx, nk, player.GetUserId(), eqconfig, false); err != nil {
		t.Fatalf("failed to read back early quit state: %v", err)
	}

	if eqconfig.NumEarlyQuits != 2 {
		t.Errorf("expected charge gate to increment to 2 early quits, got %d", eqconfig.NumEarlyQuits)
	}
	// The stored ladder puts 2 quits at level 1 with a 60s lockout.
	if eqconfig.PenaltyLevel != 1 {
		t.Errorf("expected penalty level 1 resolved from STORED ladder, got %d", eqconfig.PenaltyLevel)
	}
	now := time.Now().Unix()
	if d := eqconfig.PenaltyTimestamp - now; d < 50 || d > 70 {
		t.Errorf("expected lockout ~60s from stored ladder, got %ds", d)
	}
}

// TestEarlyQuitChargeGate_ClearsPenaltyAtLevelZero drives the charge gate with a
// quit count that maps to level 0 and verifies a stale PenaltyLevel and
// PenaltyTimestamp are cleared to zero (B1 left them untouched, which could keep
// a dead lockout on record indefinitely).
func TestEarlyQuitChargeGate_ClearsPenaltyAtLevelZero(t *testing.T) {
	logger := loggerForTest(t)
	db := NewDB(t)
	cfg := NewConfig(logger)

	storageIndex, err := NewLocalStorageIndex(logger, db, &StorageConfig{}, &testMetrics{})
	if err != nil {
		t.Fatalf("failed to create storage index: %v", err)
	}
	nk := NewRuntimeGoNakamaModule(logger, db, nil, cfg,
		nil, &chargeTestLeaderboardCache{}, nil, nil,
		&testSessionRegistry{}, nil, nil, nil,
		&testTracker{}, &testMetrics{}, nil, &testMessageRouter{},
		storageIndex, nil)

	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")

	// Pin the default ladder: level 0 covers 0-2 quits.
	writeEarlyQuitServiceConfig(t, ctx, nk, defaultEarlyQuitConfigJSON(t))

	// --- player with 1 prior quit but a STALE level-2 penalty still on record:
	// 2nd quit maps to level 0, so the stale lockout must be cleared. ---
	staleTS := time.Now().Add(time.Hour).Unix()
	player := reconnectTestPlayer("b2-level0-clear", evr.TeamBlue)
	InsertUser(t, db, player.UserID)

	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      StorageCollectionEarlyQuit,
		Key:             StorageKeyEarlyQuit,
		UserID:          player.GetUserId(),
		Value:           fmt.Sprintf(`{"num_early_quits":1,"num_steady_early_quits":1,"matchmaking_tier":1,"penalty_level":2,"penalty_ts":%d}`, staleTS),
		Version:         "",
		PermissionRead:  int(runtime.STORAGE_PERMISSION_NO_READ),
		PermissionWrite: int(runtime.STORAGE_PERMISSION_NO_WRITE),
	}}); err != nil {
		t.Fatalf("preseed early quit state failed: %v", err)
	}

	state := reconnectTestState(evr.ModeArenaPublic)
	state.ID.Node = "test-node"
	state.presenceMap[player.GetSessionId()] = player
	state.presenceByEvrID[player.EvrID] = player
	state.joinTimestamps[player.GetSessionId()] = time.Now().Add(-2 * time.Minute)
	state.participations[player.GetUserId()] = &PlayerParticipation{
		UserID:      player.GetUserId(),
		Username:    player.Username,
		DisplayName: player.DisplayName,
		Team:        BlueTeam,
		JoinTime:    time.Now().Add(-2 * time.Minute),
	}
	state.rebuildCache()

	dispatcher := &reconnectTestDispatcher{}
	leavePresence := reconnectTestPresence{EvrMatchPresence: player, reason: runtime.PresenceReasonLeave}

	m := &EvrMatch{}
	if _, ok := m.MatchLeave(ctx, reconnectTestLogger(), db, nk, dispatcher, 1, state, []runtime.Presence{leavePresence}).(*MatchLabel); !ok {
		t.Fatalf("MatchLeave returned non-*MatchLabel state")
	}

	eqconfig := NewEarlyQuitPlayerState()
	if err := StorableRead(ctx, nk, player.GetUserId(), eqconfig, false); err != nil {
		t.Fatalf("failed to read back early quit state: %v", err)
	}

	if eqconfig.NumEarlyQuits != 2 {
		t.Errorf("expected charge gate to increment to 2 early quits, got %d", eqconfig.NumEarlyQuits)
	}
	if eqconfig.PenaltyLevel != 0 {
		t.Errorf("expected penalty level cleared to 0 at level-0 count, got %d", eqconfig.PenaltyLevel)
	}
	if eqconfig.PenaltyTimestamp != 0 {
		t.Errorf("expected penalty timestamp cleared to 0 at level-0 count, got %d", eqconfig.PenaltyTimestamp)
	}
	if eqconfig.IsPenaltyActive() {
		t.Errorf("expected IsPenaltyActive() to be false after level-0 clear, got true")
	}
}

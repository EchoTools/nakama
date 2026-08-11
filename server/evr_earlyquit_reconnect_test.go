package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// captureLogger is a runtime.Logger that records every log call (level,
// message, fields) so tests can assert that specific production log lines were
// produced.
type captureLogger struct {
	mu     sync.Mutex
	fields map[string]any
	events *[]captureLogEvent
}

type captureLogEvent struct {
	level  string
	msg    string
	fields map[string]any
}

func newCaptureLogger() *captureLogger {
	events := make([]captureLogEvent, 0, 32)
	return &captureLogger{fields: map[string]any{}, events: &events}
}

func (l *captureLogger) record(level, format string, v ...any) {
	fields := make(map[string]any, len(l.fields))
	for k, val := range l.fields {
		fields[k] = val
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	*l.events = append(*l.events, captureLogEvent{level: level, msg: fmt.Sprintf(format, v...), fields: fields})
}

func (l *captureLogger) Debug(format string, v ...any) { l.record("debug", format, v...) }
func (l *captureLogger) Info(format string, v ...any)  { l.record("info", format, v...) }
func (l *captureLogger) Warn(format string, v ...any)  { l.record("warn", format, v...) }
func (l *captureLogger) Error(format string, v ...any) { l.record("error", format, v...) }

func (l *captureLogger) WithField(key string, v any) runtime.Logger {
	return l.WithFields(map[string]any{key: v})
}

func (l *captureLogger) WithFields(fields map[string]any) runtime.Logger {
	merged := make(map[string]any, len(l.fields)+len(fields))
	for k, v := range l.fields {
		merged[k] = v
	}
	for k, v := range fields {
		merged[k] = v
	}
	return &captureLogger{fields: merged, events: l.events}
}

func (l *captureLogger) Fields() map[string]any {
	return l.fields
}

func (l *captureLogger) find(level, msg string) (captureLogEvent, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range *l.events {
		if e.level == level && e.msg == msg {
			return e, true
		}
	}
	return captureLogEvent{}, false
}

// reconnectLeaderboardSpyModule wraps the standard test module and records every
// leaderboard write so tests can assert that the early-quit stat was
// accumulated.
type reconnectLeaderboardSpyModule struct {
	*reconnectTestNakamaModule
	leaderboardWrites []string
}

func (m *reconnectLeaderboardSpyModule) LeaderboardRecordWrite(ctx context.Context, leaderboardId, ownerID, username string, score, subscore int64, metadata map[string]any, operator *int) (*api.LeaderboardRecord, error) {
	m.leaderboardWrites = append(m.leaderboardWrites, leaderboardId)
	return &api.LeaderboardRecord{}, nil
}

// TestReconnectReservationExpiry_RecordsEarlyQuitEffects drives the REAL
// MatchLoop reconnect-reservation expiry path (evr_match.go:1331-1361): a
// crashed player's reservation expires with DeferPenalty, and the deferred
// penalty must produce the same side effects as an immediate early quit in
// MatchLeave — the "Incrementing early quit for player." log line, the
// leaderboard stat accumulation, and the quit-history record — in addition to
// the counter increment itself.
//
// Uses the recording test module (reconnectLeaderboardSpyModule): no DB needed.
func TestReconnectReservationExpiry_RecordsEarlyQuitEffects(t *testing.T) {
	logger := newCaptureLogger()
	nk := &reconnectLeaderboardSpyModule{reconnectTestNakamaModule: &reconnectTestNakamaModule{}}
	dispatcher := &reconnectTestDispatcher{}
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")

	state := reconnectTestState(evr.ModeArenaPublic)
	state.ID.Node = "test-node" // MatchID round-trips through storage JSON only with a node set
	player := reconnectTestPlayer("b7-expiry", evr.TeamBlue)
	state.participations[player.GetUserId()] = &PlayerParticipation{
		UserID:      player.GetUserId(),
		Username:    player.Username,
		DisplayName: player.DisplayName,
		Team:        BlueTeam,
		JoinTime:    time.Now().Add(-2 * time.Minute),
		LeaveTime:   time.Now(),
	}
	// The crashed player is gone; only their reconnect reservation remains.
	state.reconnectReservations[player.GetUserId()] = &reconnectReservation{
		Presence:     player,
		Expiry:       time.Now().Add(-time.Second), // already expired
		UserID:       player.GetUserId(),
		DeferPenalty: true,
	}

	m := &EvrMatch{}
	got := m.MatchLoop(ctx, logger, nil, nk, dispatcher, 1, state, nil)
	if got == nil {
		t.Fatal("MatchLoop returned nil state")
	}
	if _, ok := got.(*MatchLabel); !ok {
		t.Fatalf("MatchLoop returned non-*MatchLabel state: %T", got)
	}

	// 1. The expiry block logged the increment line.
	if ev, ok := logger.find("debug", "Incrementing early quit for player."); !ok {
		t.Error(`expected log line "Incrementing early quit for player." to be produced on reservation expiry`)
	} else if uid := ev.fields["uid"]; uid != player.GetUserId() {
		t.Errorf("expected log line uid field %s, got %v", player.GetUserId(), uid)
	}

	// 2. The leaderboard stat was accumulated for all three reset schedules.
	wantBoards := []string{
		StatisticBoardID(state.GetGroupID().String(), evr.ModeArenaPublic, EarlyQuitStatisticID, evr.ResetScheduleDaily),
		StatisticBoardID(state.GetGroupID().String(), evr.ModeArenaPublic, EarlyQuitStatisticID, evr.ResetScheduleWeekly),
		StatisticBoardID(state.GetGroupID().String(), evr.ModeArenaPublic, EarlyQuitStatisticID, evr.ResetScheduleAllTime),
	}
	if len(nk.leaderboardWrites) != len(wantBoards) {
		t.Errorf("expected %d leaderboard writes (one per reset schedule), got %d: %v", len(wantBoards), len(nk.leaderboardWrites), nk.leaderboardWrites)
	} else {
		for i, want := range wantBoards {
			if nk.leaderboardWrites[i] != want {
				t.Errorf("expected leaderboard write %d to be %s, got %s", i, want, nk.leaderboardWrites[i])
			}
		}
	}

	// 3. The quit-history record was written with the reservation-expiry reason.
	var historyWrite *runtime.StorageWrite
	for _, w := range nk.storageWrites {
		if w.Collection == StorageCollectionEarlyQuitHistory && w.Key == StorageKeyEarlyQuitHistory {
			historyWrite = w
			break
		}
	}
	if historyWrite == nil {
		t.Fatal("expected an EarlyQuitHistory storage write on reservation expiry")
	}
	history := NewEarlyQuitHistory(player.GetUserId())
	if err := json.Unmarshal([]byte(historyWrite.Value), history); err != nil {
		t.Fatalf("failed to unmarshal history write: %v", err)
	}
	if len(history.Records) != 1 {
		t.Fatalf("expected 1 quit record in history, got %d", len(history.Records))
	}
	if gotReason := history.Records[0].LeaveReason; gotReason != LeaveReasonReservationExp {
		t.Errorf("expected quit record leave_reason %q, got %q", LeaveReasonReservationExp, gotReason)
	}

	// 4. The counter increment itself must also still happen.
	var eqWrite *runtime.StorageWrite
	for _, w := range nk.storageWrites {
		if w.Collection == StorageCollectionEarlyQuit && w.Key == StorageKeyEarlyQuit {
			eqWrite = w
			break
		}
	}
	if eqWrite == nil {
		t.Fatal("expected an EarlyQuit config storage write on reservation expiry")
	}
	if !strings.Contains(eqWrite.Value, `"num_early_quits":1`) {
		t.Errorf("expected num_early_quits 1 in config write, got: %s", eqWrite.Value)
	}
}

// TestReconnectReservationExpiry_ExemptPlayerIsNotCharged pins the moderator
// exemption on the DEFERRED charge path.
//
// MatchLeave's immediate charge honours EarlyQuitPlayerState.IsExempt. The
// reservation-expiry charge in MatchLoop did not, and MatchLeave skips its
// entire charge block -- exemption check included -- when a reconnect
// reservation exists. So an exempt player who crashed and did not get back in
// before the window closed was charged anyway: counter incremented, lockout
// resolved, leaderboard stat and quit-history record written.
//
// That is the worst population to get wrong. The exemption is most often
// granted to players with known-bad connections, which is exactly who fails to
// reconnect inside the window.
func TestReconnectReservationExpiry_ExemptPlayerIsNotCharged(t *testing.T) {
	logger := newCaptureLogger()
	nk := &reconnectLeaderboardSpyModule{reconnectTestNakamaModule: &reconnectTestNakamaModule{}}
	dispatcher := &reconnectTestDispatcher{}
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")

	state := reconnectTestState(evr.ModeArenaPublic)
	state.ID.Node = "test-node"
	player := reconnectTestPlayer("b7-expiry-exempt", evr.TeamBlue)
	groupID := state.GetGroupID().String()

	// The player carries a moderator exemption for this guild, as the
	// earlyquit/modify RPC would have written.
	nk.earlyQuitStateJSON = fmt.Sprintf(
		`{"matchmaking_tier":1,"guild_overrides":{%q:{"exempt":true}}}`, groupID)

	state.participations[player.GetUserId()] = &PlayerParticipation{
		UserID:      player.GetUserId(),
		Username:    player.Username,
		DisplayName: player.DisplayName,
		Team:        BlueTeam,
		JoinTime:    time.Now().Add(-2 * time.Minute),
		LeaveTime:   time.Now(),
	}
	state.reconnectReservations[player.GetUserId()] = &reconnectReservation{
		Presence:     player,
		Expiry:       time.Now().Add(-time.Second),
		UserID:       player.GetUserId(),
		DeferPenalty: true,
	}

	m := &EvrMatch{}
	if got := m.MatchLoop(ctx, logger, nil, nk, dispatcher, 1, state, nil); got == nil {
		t.Fatal("MatchLoop returned nil state")
	}

	if _, ok := logger.find("info", "Deferred early quit penalty skipped: player is exempt in this guild"); !ok {
		t.Error("expected the deferred charge to log that it skipped an exempt player")
	}

	if _, ok := logger.find("debug", "Incrementing early quit for player."); ok {
		t.Error("an exempt player must not have their early quit counter incremented on reservation expiry")
	}

	if len(nk.leaderboardWrites) != 0 {
		t.Errorf("expected no leaderboard writes for an exempt player, got %v", nk.leaderboardWrites)
	}

	for _, w := range nk.storageWrites {
		if w.Collection == StorageCollectionEarlyQuitHistory && w.Key == StorageKeyEarlyQuitHistory {
			t.Error("an exempt player must not get a quit-history record; it feeds the moderator-facing " +
				"quit stats and would re-punish them by another route")
		}
	}
}

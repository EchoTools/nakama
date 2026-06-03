package server

// Regression tests for the social-lobby allocation bug introduced after v3.27.2-evr.245.
//
// Root cause:
//   allocatePostMatchSocialLobby is called synchronously inside MatchLoop.  It calls
//   nk.MatchSignal on an unassigned-lobby match.  Nakama's match handler goroutine
//   processes ticks AND signals on the same select loop, so if the target match's
//   signalCh queue (default size 10) is full — which happens when many arena matches
//   end simultaneously and all pile MatchSignal calls onto the same small pool of
//   unassigned servers — QueueSignal returns false immediately and the registry
//   returns ErrMatchBusy.  allocatePostMatchSocialLobby then fails, players have no
//   post-match social lobby, and repeated allocation attempts can spawn runaway
//   matches, explaining the "700 matches when we should have <50" symptom.
//
// Additionally, even a single slow MatchSignal (e.g. the SignalPrepareSession handler
// doing a DB query via CheckSystemGroupMembership) blocks the calling match's tick
// goroutine for up to 10 seconds, freezing that match entirely.

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap/zapcore"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// blockingMatchSignalNK is a NakamaModule stub whose MatchSignal blocks until the test
// releases it, simulating a slow or hung target match.
type blockingMatchSignalNK struct {
	runtime.NakamaModule
	entered        chan struct{}
	unblock        chan struct{}
	fakeMatchLabel string
}

func newBlockingMatchSignalNK(fakeTargetLabel string) *blockingMatchSignalNK {
	return &blockingMatchSignalNK{
		entered:        make(chan struct{}),
		unblock:        make(chan struct{}),
		fakeMatchLabel: fakeTargetLabel,
	}
}

func (m *blockingMatchSignalNK) MatchSignal(_ context.Context, _ string, _ string) (string, error) {
	select {
	case <-m.entered:
	default:
		close(m.entered)
	}
	<-m.unblock
	return `{"success":false}`, nil
}

func (m *blockingMatchSignalNK) MatchList(_ context.Context, _ int, _ bool, _ string, _, _ *int, _ string) ([]*api.Match, error) {
	return []*api.Match{
		{
			MatchId:       uuid.Must(uuid.NewV4()).String() + ".testnode",
			Authoritative: true,
			Label:         &wrapperspb.StringValue{Value: m.fakeMatchLabel},
		},
	}, nil
}

func (m *blockingMatchSignalNK) MetricsCounterAdd(_ string, _ map[string]string, _ int64)          {}
func (m *blockingMatchSignalNK) MetricsTimerRecord(_ string, _ map[string]string, _ time.Duration) {}
func (m *blockingMatchSignalNK) MetricsGaugeSet(_ string, _ map[string]string, _ float64)          {}
func (m *blockingMatchSignalNK) StorageWrite(_ context.Context, _ []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	return nil, nil
}
func (m *blockingMatchSignalNK) StorageRead(_ context.Context, _ []*runtime.StorageRead) ([]*api.StorageObject, error) {
	return nil, nil
}

type noopDispatcher struct{}

func (d *noopDispatcher) BroadcastMessage(_ int64, _ []byte, _ []runtime.Presence, _ runtime.Presence, _ bool) error {
	return nil
}
func (d *noopDispatcher) BroadcastMessageDeferred(_ int64, _ []byte, _ []runtime.Presence, _ runtime.Presence, _ bool) error {
	return nil
}
func (d *noopDispatcher) MatchKick(_ []runtime.Presence) error { return nil }
func (d *noopDispatcher) MatchLabelUpdate(_ string) error      { return nil }

// TestMatchLoop_AllocatePostMatchSocialLobby_DoesNotBlock is the regression guard
// for the social-lobby allocation bug. The original bug called
// allocatePostMatchSocialLobby (which calls nk.MatchSignal) synchronously inside
// MatchLoop, so a slow or hung target match froze the calling match's tick
// goroutine for up to 10 seconds.
//
// The fix (commit "Queue async side effects from MatchTerminate") moved the
// allocation off the MatchLoop path entirely: it now runs in the asynchronous
// match-termination worker (processMatchTerminationTask), scheduled by
// MatchTerminate. MatchLoop must therefore NEVER call nk.MatchSignal, even when
// a private match has just ended, so a blocking signal cannot freeze the tick.
//
// This test drives MatchLoop with the exact conditions that used to trigger the
// synchronous allocation and asserts that MatchLoop returns promptly without ever
// entering nk.MatchSignal.
func TestMatchLoop_AllocatePostMatchSocialLobby_DoesNotBlock(t *testing.T) {
	// Mutates the process-global ServiceSettings, so it must not run in
	// parallel with other tests that touch it.
	settings := &ServiceSettingsData{}
	settings.Matchmaking.QueryAddons.Create = "+label.open:T"
	settings.Matchmaking.EnablePostMatchSocialLobby = true
	ServiceSettingsUpdate(settings)
	t.Cleanup(func() { ServiceSettingsUpdate(nil) })

	targetMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	fakeServerLabel, err := json.Marshal(&MatchLabel{
		ID:        targetMatchID,
		LobbyType: UnassignedLobby,
		GameServer: &GameServerPresence{
			SessionID:  uuid.Must(uuid.NewV4()),
			EndpointID: "testendpoint",
			Endpoint: evr.Endpoint{
				ExternalIP: net.ParseIP("127.0.0.1"),
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal fake server label: %v", err)
	}

	// A blocking MatchSignal: if MatchLoop ever calls it, nk.entered closes and we
	// fail. The unblock channel is closed in cleanup so a stray goroutine cannot leak.
	nk := newBlockingMatchSignalNK(string(fakeServerLabel))
	t.Cleanup(func() {
		select {
		case <-nk.unblock:
		default:
			close(nk.unblock)
		}
	})

	groupID := uuid.Must(uuid.NewV4())
	player := &EvrMatchPresence{
		SessionID:     uuid.Must(uuid.NewV4()),
		UserID:        uuid.Must(uuid.NewV4()),
		Node:          "testnode",
		RoleAlignment: evr.TeamBlue,
	}
	state := &MatchLabel{
		ID:               MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"},
		Mode:             evr.ModeArenaPrivate,
		GroupID:          &groupID,
		tickRate:         10,
		GameState:        &GameState{MatchOver: true},
		matchSummarySent: false,
		presenceMap: map[string]*EvrMatchPresence{
			player.SessionID.String(): player,
		},
		presenceByEvrID: make(map[evr.EvrId]*EvrMatchPresence),
		joinTimestamps:  make(map[string]time.Time),
		participations:  make(map[string]*PlayerParticipation),
		reservationMap:  make(map[string]*slotReservation),
	}

	m := &EvrMatch{}
	logger := NewRuntimeGoLogger(NewJSONLogger(os.Stdout, zapcore.ErrorLevel, JSONFormat))
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_MATCH_ID, state.ID.String())
	ctx = context.WithValue(ctx, runtime.RUNTIME_CTX_NODE, "testnode")

	// tick=20 satisfies the former trigger 20 % (2*10) == 0.
	const testTick int64 = 20

	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		m.MatchLoop(ctx, logger, nil, nk, &noopDispatcher{}, testTick, state, nil)
	}()

	select {
	case <-nk.entered:
		t.Fatal("MatchLoop called nk.MatchSignal synchronously — the blocking allocation regression is back")
	case <-loopDone:
		// MatchLoop returned without ever signalling: the allocation is off the
		// tick path (now handled asynchronously by the termination worker).
	case <-time.After(3 * time.Second):
		t.Fatal("MatchLoop neither returned nor signalled within the timeout")
	}
}

// busyMatchSignalNK is a NakamaModule stub whose MatchSignal returns ErrMatchBusy
// immediately, simulating an exhausted signalCh queue on the target match.
type busyMatchSignalNK struct {
	runtime.NakamaModule
	fakeMatchLabel string
	callCount      int
}

func newBusyMatchSignalNK(fakeTargetLabel string) *busyMatchSignalNK {
	return &busyMatchSignalNK{fakeMatchLabel: fakeTargetLabel}
}

func (m *busyMatchSignalNK) MatchSignal(_ context.Context, _ string, _ string) (string, error) {
	m.callCount++
	return "", runtime.ErrMatchBusy
}

func (m *busyMatchSignalNK) MatchList(_ context.Context, _ int, _ bool, _ string, _, _ *int, _ string) ([]*api.Match, error) {
	return []*api.Match{
		{
			MatchId:       uuid.Must(uuid.NewV4()).String() + ".testnode",
			Authoritative: true,
			Label:         &wrapperspb.StringValue{Value: m.fakeMatchLabel},
		},
	}, nil
}

func (m *busyMatchSignalNK) MetricsCounterAdd(_ string, _ map[string]string, _ int64)          {}
func (m *busyMatchSignalNK) MetricsTimerRecord(_ string, _ map[string]string, _ time.Duration) {}
func (m *busyMatchSignalNK) MetricsGaugeSet(_ string, _ map[string]string, _ float64)          {}
func (m *busyMatchSignalNK) StorageWrite(_ context.Context, _ []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	return nil, nil
}
func (m *busyMatchSignalNK) StorageRead(_ context.Context, _ []*runtime.StorageRead) ([]*api.StorageObject, error) {
	return nil, nil
}

// TestAllocatePostMatchSocialLobby_ErrMatchBusy confirms that when every candidate
// unassigned server returns ErrMatchBusy (signalCh queue full), the allocation
// fails with an error rather than silently succeeding or spinning forever.
//
// This is the production failure mode: with 700 matches and only a handful of
// unassigned servers, all servers are perpetually busy signalling each other, so
// every post-match social lobby allocation fails and players are left stranded.
func TestAllocatePostMatchSocialLobby_ErrMatchBusy(t *testing.T) {
	// Mutates the process-global ServiceSettings, so it must not run in
	// parallel with other tests that touch it.
	settings := &ServiceSettingsData{}
	settings.Matchmaking.QueryAddons.Create = "+label.open:T"
	ServiceSettingsUpdate(settings)
	t.Cleanup(func() { ServiceSettingsUpdate(nil) })

	targetMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	fakeServerLabel, err := json.Marshal(&MatchLabel{
		ID:        targetMatchID,
		LobbyType: UnassignedLobby,
		GameServer: &GameServerPresence{
			SessionID:  uuid.Must(uuid.NewV4()),
			EndpointID: "testendpoint",
			Endpoint: evr.Endpoint{
				ExternalIP: net.ParseIP("127.0.0.1"),
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal fake server label: %v", err)
	}

	nk := newBusyMatchSignalNK(string(fakeServerLabel))

	groupID := uuid.Must(uuid.NewV4())
	player := &EvrMatchPresence{
		SessionID:     uuid.Must(uuid.NewV4()),
		UserID:        uuid.Must(uuid.NewV4()),
		Node:          "testnode",
		RoleAlignment: evr.TeamBlue,
	}
	state := &MatchLabel{
		ID:      MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"},
		Mode:    evr.ModeArenaPrivate,
		GroupID: &groupID,
		presenceMap: map[string]*EvrMatchPresence{
			player.SessionID.String(): player,
		},
	}

	logger := NewRuntimeGoLogger(NewJSONLogger(os.Stdout, zapcore.ErrorLevel, JSONFormat))
	ctx := context.Background()

	// allocatePostMatchSocialLobby must return an error — not nil — when every
	// candidate server is busy.  Before the fix this would silently succeed or
	// block; after the fix it must surface the error so callers can decide.
	err = allocatePostMatchSocialLobby(ctx, logger, nk, state)
	if err == nil {
		t.Fatal("expected an error when all unassigned servers are busy, got nil")
	}

	// MatchSignal must have been called exactly once (no silent retry loop that
	// would spin and create runaway matches).
	if nk.callCount != 1 {
		t.Errorf("expected MatchSignal called 1 time, got %d — a retry loop would spawn runaway matches", nk.callCount)
	}
}

// TestProcessMatchTerminationTask_FailedAllocationDoesNotRetryStorm confirms the
// no-runaway-match property the original "FailedAllocationPreventsRetry" test
// guarded, expressed against the current architecture.
//
// The post-match social lobby is now allocated asynchronously by the match
// termination worker (processMatchTerminationTask), not synchronously in
// MatchLoop. When every candidate unassigned server is busy (MatchSignal returns
// ErrMatchBusy), a single termination task must:
//   - call MatchSignal exactly once (no internal retry loop that would spin and
//     spawn runaway matches — the "700 matches" symptom), and
//   - complete without blocking or panicking despite the failure.
//
// Each end-of-match enqueues exactly one task, so "one task -> at most one signal"
// is the invariant that prevents the retry storm.
func TestProcessMatchTerminationTask_FailedAllocationDoesNotRetryStorm(t *testing.T) {
	// Mutates the process-global ServiceSettings, so it must not run in
	// parallel with other tests that touch it.
	settings := &ServiceSettingsData{}
	settings.Matchmaking.QueryAddons.Create = "+label.open:T"
	settings.Matchmaking.EnablePostMatchSocialLobby = true
	ServiceSettingsUpdate(settings)
	t.Cleanup(func() { ServiceSettingsUpdate(nil) })

	targetMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	fakeServerLabel, err := json.Marshal(&MatchLabel{
		ID:        targetMatchID,
		LobbyType: UnassignedLobby,
		GameServer: &GameServerPresence{
			SessionID:  uuid.Must(uuid.NewV4()),
			EndpointID: "testendpoint",
			Endpoint: evr.Endpoint{
				ExternalIP: net.ParseIP("127.0.0.1"),
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal fake server label: %v", err)
	}

	// busyMatchSignalNK returns ErrMatchBusy on every MatchSignal and records the
	// call count, plus no-ops the SessionDisconnect/StoreMatchLabel side effects.
	nk := newBusyMatchSignalNK(string(fakeServerLabel))

	groupID := uuid.Must(uuid.NewV4())
	player := &EvrMatchPresence{
		SessionID:     uuid.Must(uuid.NewV4()),
		UserID:        uuid.Must(uuid.NewV4()),
		Node:          "testnode",
		RoleAlignment: evr.TeamBlue,
	}
	state := &MatchLabel{
		ID:      MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"},
		Mode:    evr.ModeArenaPrivate,
		GroupID: &groupID,
		presenceMap: map[string]*EvrMatchPresence{
			player.SessionID.String(): player,
		},
	}

	logger := NewRuntimeGoLogger(NewJSONLogger(os.Stdout, zapcore.ErrorLevel, JSONFormat))

	task := matchTerminationTask{
		logger:                       logger,
		nk:                           nk,
		stateSnapshot:                state,
		matchID:                      state.ID.String(),
		schedulePostMatchSocialLobby: true,
		labelAlreadyStored:           true,
	}

	// Process the task synchronously (bypassing the worker pool) so we can assert
	// the signal count deterministically.
	done := make(chan struct{})
	go func() {
		defer close(done)
		processMatchTerminationTask(task)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("processMatchTerminationTask blocked on a busy MatchSignal")
	}

	if nk.callCount != 1 {
		t.Errorf("expected MatchSignal called exactly 1 time per termination task, got %d — a retry loop would spawn runaway matches", nk.callCount)
	}
}

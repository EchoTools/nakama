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

// TestMatchLoop_AllocatePostMatchSocialLobby_Blocks confirms that MatchLoop blocks the
// calling goroutine inside nk.MatchSignal when allocatePostMatchSocialLobby is triggered.
//
// Trigger conditions (evr_match.go):
//
//	state.Mode == evr.ModeArenaPrivate
//	tick % (2*tickRate) == 0
//	state.GameState != nil && state.GameState.MatchOver
//	!state.matchSummarySent
func TestMatchLoop_AllocatePostMatchSocialLobby_Blocks(t *testing.T) {
	t.Parallel()

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

	nk := newBlockingMatchSignalNK(string(fakeServerLabel))

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

	// tick=20 satisfies 20 % (2*10) == 0
	const testTick int64 = 20

	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		m.MatchLoop(ctx, logger, nil, nk, &noopDispatcher{}, testTick, state, nil)
	}()

	select {
	case <-nk.entered:

	case <-loopDone:
		t.Fatal("MatchLoop returned before blocking in nk.MatchSignal: trigger conditions not met or bug is fixed")
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for MatchLoop to enter nk.MatchSignal")
	}

	close(nk.unblock)
	select {
	case <-loopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("MatchLoop did not return after nk.MatchSignal was unblocked")
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
	t.Parallel()

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


// TestAllocatePostMatchSocialLobby_FailedAllocationPreventsRetry confirms the
// matchSummarySent-before-allocation bug.
//
// Previous (buggy) code path:
//   line 1117: state.matchSummarySent = true      ← set BEFORE allocation
//   line 1118: allocatePostMatchSocialLobby(...)   ← returns error (ErrMatchBusy)
//   line 1119: logger.Error(...)                  ← error logged, flag already set
//
// Because the flag is set before the call, a second tick (tick=40) finds
// state.matchSummarySent==true and skips the block entirely — MatchSignal is
// never called again.  With the fix (set flag only on success) the second tick
// retries the allocation and MatchSignal is called a second time.
//
// This test FAILS against the current code and PASSES after the fix.
func TestAllocatePostMatchSocialLobby_FailedAllocationPreventsRetry(t *testing.T) {
	t.Parallel()

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

	// First tick: allocation fails with ErrMatchBusy.
	const tick1 int64 = 20 // 20 % (2*10) == 0
	m.MatchLoop(ctx, logger, nil, nk, &noopDispatcher{}, tick1, state, nil)

	if nk.callCount != 1 {
		t.Fatalf("first tick: expected MatchSignal called 1 time, got %d", nk.callCount)
	}

	// After a failed allocation the flag must NOT be set — the next eligible tick
	// must retry.
	if state.matchSummarySent {
		t.Fatal("matchSummarySent must be false after a failed allocation (BUG: flag set before allocation)")
	}

	// Second tick: the retry should call MatchSignal again.
	const tick2 int64 = 40 // 40 % (2*10) == 0
	m.MatchLoop(ctx, logger, nil, nk, &noopDispatcher{}, tick2, state, nil)

	if nk.callCount != 2 {
		t.Errorf("second tick: expected MatchSignal called 2 times total, got %d — failed allocation silently prevents retries (matchSummarySent set before allocation)", nk.callCount)
	}
}
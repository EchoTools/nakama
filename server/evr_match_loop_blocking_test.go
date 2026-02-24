package server

// Regression test for the bug introduced after v3.27.2-evr.245:
// allocatePostMatchSocialLobby calls nk.MatchSignal synchronously inside MatchLoop,
// blocking the single match goroutine for up to 10 seconds per tick.

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

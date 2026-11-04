package server

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// ambassadorQueueNK is the in-memory module with the leaderboard read the
// lobby-parameters path makes for games played.
type ambassadorQueueNK struct {
	*evrTestNakamaModule
	// failAmbassadorRead makes reads of the Ambassador collection fail. The
	// call reaches this override because the code under test takes the module as
	// an interface, which dispatches to the outermost type.
	failAmbassadorRead bool
}

func (n *ambassadorQueueNK) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	if n.failAmbassadorRead {
		for _, r := range reads {
			if r.Collection == StorageCollectionAmbassador {
				return nil, errors.New("storage unavailable")
			}
		}
	}
	return n.evrTestNakamaModule.StorageRead(ctx, reads)
}

func (n *ambassadorQueueNK) LeaderboardRecordsList(context.Context, string, []string, int, string, int64) ([]*api.LeaderboardRecord, []*api.LeaderboardRecord, string, string, error) {
	return nil, nil, "", "", nil
}

var ambassadorQueueGroup = uuid.Must(uuid.FromString("00000000-0000-4000-8000-0000000000e1"))

func ambassadorQueueRequest(mode evr.Symbol) *evr.LobbyFindSessionRequest {
	return &evr.LobbyFindSessionRequest{
		Mode:    mode,
		GroupID: ambassadorQueueGroup,
		Entrants: []evr.Entrant{{
			EvrID: evr.EvrId{PlatformCode: 4, AccountId: 3963667097037078},
			Role:  int8(evr.TeamUnassigned),
		}},
	}
}

type ambassadorQueueEnv struct {
	nk      *ambassadorQueueNK
	session *sessionWS
	logs    *observer.ObservedLogs
	logger  *zap.Logger
}

func newAmbassadorQueueEnv(t *testing.T) *ambassadorQueueEnv {
	t.Helper()
	core, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)
	chargeNK, _ := newChargeModule(t)
	nk := &ambassadorQueueNK{evrTestNakamaModule: chargeNK}
	session := newLobbyParamsTestSession(t, logger, nk, false)

	original := ServiceSettings()
	t.Cleanup(func() { ServiceSettingsUpdate(original) })
	ServiceSettingsUpdate(&ServiceSettingsData{})
	return &ambassadorQueueEnv{nk: nk, session: session, logs: logs, logger: logger}
}

func (e *ambassadorQueueEnv) setAccount(t *testing.T, a AmbassadorAccount) {
	t.Helper()
	a.version = "*"
	// A second write replaces the first: use the stored version.
	if cur, err := LoadAmbassadorAccount(context.Background(), e.nk, e.session.userID.String()); err == nil {
		a.version = cur.version
	}
	if err := StorableWrite(context.Background(), e.nk, e.session.userID.String(), &a); err != nil {
		t.Fatalf("seed account: %v", err)
	}
}

func (e *ambassadorQueueEnv) request(t *testing.T, mode evr.Symbol) *LobbySessionParameters {
	t.Helper()
	lp, err := NewLobbyParametersFromRequest(e.session.ctx, e.logger, e.nk, e.session, ambassadorQueueRequest(mode))
	if err != nil {
		t.Fatalf("NewLobbyParametersFromRequest: %v", err)
	}
	return lp
}

func (e *ambassadorQueueEnv) stamp() *ambassadorQueueStamp {
	p, _ := LoadParams(e.session.ctx)
	return p.ambassadorQueue.Load()
}

func TestQueue_AmbassadorModeStampsTheSessionNotTheTicket(t *testing.T) {
	for _, mode := range []evr.Symbol{evr.ModeArenaPublic, evr.ModeCombatPublic} {
		t.Run(mode.String(), func(t *testing.T) {
			env := newAmbassadorQueueEnv(t)
			env.setAccount(t, AmbassadorAccount{Ambassador: true, Mode: true})
			lp := env.request(t, mode)
			if !lp.AmbassadorMode {
				t.Error("an ambassador in ambassador mode was not marked")
			}
			st := env.stamp()
			if st == nil || st.Mode != mode || st.GroupID != ambassadorQueueGroup {
				t.Fatalf("session stamp = %+v, want mode %v group %v", st, mode, ambassadorQueueGroup)
			}
			if !st.ExpiresAt.After(time.Now()) {
				t.Errorf("stamp already expired: %v", st.ExpiresAt)
			}
			var queued int
			for _, e := range env.logs.FilterMessage("ambassador_event").All() {
				m := e.ContextMap()
				if m["event"] == "queue_stamped" {
					queued++
					if m["actor_user_id"] != env.session.userID.String() || m["mode"] != mode.String() {
						t.Errorf("queue_stamped lacks the player or mode: %v", m)
					}
				}
			}
			if queued != 1 {
				t.Errorf("queue_stamped events = %d, want 1", queued)
			}
		})
	}
}

func TestQueue_NormalModeAndNoRoleAreNotStamped(t *testing.T) {
	for _, tc := range []struct {
		name string
		acct AmbassadorAccount
	}{
		{"role, mode off", AmbassadorAccount{Ambassador: true, Mode: false}},
		{"mode on but the role was removed", AmbassadorAccount{Ambassador: false, Mode: true}},
		{"no stored account", AmbassadorAccount{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newAmbassadorQueueEnv(t)
			env.setAccount(t, tc.acct)
			lp := env.request(t, evr.ModeArenaPublic)
			if lp.AmbassadorMode || env.stamp() != nil {
				t.Errorf("AmbassadorMode=%v stamp=%+v, want normal matchmaking", lp.AmbassadorMode, env.stamp())
			}
		})
	}
}

// The old session flag was only ever set at queue time and never cleared by a
// later queue. The stamp is set or cleared on every request.
func TestQueue_StampIsClearedByTheNextRequest(t *testing.T) {
	env := newAmbassadorQueueEnv(t)
	env.setAccount(t, AmbassadorAccount{Ambassador: true, Mode: true})
	env.request(t, evr.ModeCombatPublic)
	if env.stamp() == nil {
		t.Fatal("first request did not stamp")
	}

	// The ambassador switches off, then queues arena: no stamp may survive.
	env.setAccount(t, AmbassadorAccount{Ambassador: true, Mode: false})
	env.request(t, evr.ModeArenaPublic)
	if st := env.stamp(); st != nil {
		t.Errorf("a combat-mode stamp survived a normal arena request: %+v", st)
	}

	// Back on; then a non-stats mode request also clears it.
	env.setAccount(t, AmbassadorAccount{Ambassador: true, Mode: true})
	env.request(t, evr.ModeArenaPublic)
	if env.stamp() == nil {
		t.Fatal("arena request in ambassador mode did not stamp")
	}
	env.request(t, evr.ModeSocialPublic)
	if st := env.stamp(); st != nil {
		t.Errorf("a social request left a stale stamp: %+v", st)
	}
}

func TestQueue_StorageFailureQueuesNormalAndWarns(t *testing.T) {
	env := newAmbassadorQueueEnv(t)
	env.setAccount(t, AmbassadorAccount{Ambassador: true, Mode: true})
	env.nk.failAmbassadorRead = true
	lp := env.request(t, evr.ModeArenaPublic)
	if lp.AmbassadorMode || env.stamp() != nil {
		t.Error("a failed account read stamped the session")
	}
	if env.logs.FilterMessageSnippet("Failed to load ambassador account").Len() == 0 {
		t.Error("a failed account read was not logged at Warn")
	}
}

// Nothing about ambassador mode may reach the matchmaker: the ticket an
// ambassador in ambassador mode submits is the ticket the same player submits
// in normal matchmaking.
func TestQueue_TicketIsIdenticalInAmbassadorMode(t *testing.T) {
	env := newAmbassadorQueueEnv(t)
	ticket := &MatchmakingTicketParameters{MinCount: 8, MaxCount: 8, CountMultiple: 2}
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

	env.setAccount(t, AmbassadorAccount{Ambassador: true, Mode: false})
	normal := env.request(t, evr.ModeArenaPublic)
	normal.MatchmakingTimestamp = at
	nq, ns, nn := normal.MatchmakingParameters(ticket)

	env.setAccount(t, AmbassadorAccount{Ambassador: true, Mode: true})
	amb := env.request(t, evr.ModeArenaPublic)
	if !amb.AmbassadorMode {
		t.Fatal("setup: not in ambassador mode")
	}
	amb.MatchmakingTimestamp = at
	aq, as, an := amb.MatchmakingParameters(ticket)

	if nq != aq || !reflect.DeepEqual(ns, as) || !reflect.DeepEqual(nn, an) {
		t.Errorf("ambassador-mode ticket differs from the normal ticket.\nquery %q vs %q\nstring %v vs %v\nnumeric %v vs %v", nq, aq, ns, as, nn, an)
	}
}

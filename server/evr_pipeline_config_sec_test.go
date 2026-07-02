package server

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
	uberatomic "go.uber.org/atomic"
	"go.uber.org/zap"
)

// --- SEC-1 regression tests -------------------------------------------------
//
// SEC-1: an unauthenticated ConfigRequest (reachable with the public ServerKey,
// see socket_ws.go:89-91 and evr_pipeline.go:548-550 where
// isAuthenticationRequired=false) forces one storage read per packet because
// the in-memory cache is keyed on the client-controlled Type+":"+ID while the
// storage read itself keys on Type only. A remote caller varying ID on each
// packet guarantees a cache miss -> a DB round-trip -> and, when a stored
// Config:<Type> object exists, one cache entry per unique ID (unbounded growth).
//
// These tests drive the real configRequest handler with the storage read
// substituted by a counting stub (configStorageRead) so no live Postgres is
// required.

// configTestStub records how many storage reads the handler performed and lets
// a test decide whether a stored object exists for the requested Type.
type configTestStub struct {
	reads      int64
	storedJSON string // non-empty => a stored Config:<Type> object is returned
}

func (c *configTestStub) read(ctx context.Context, logger *zap.Logger, db *sql.DB, caller uuid.UUID, objectIDs []*api.ReadStorageObjectId) (*api.StorageObjects, error) {
	atomic.AddInt64(&c.reads, 1)
	if c.storedJSON == "" {
		return &api.StorageObjects{}, nil
	}
	return &api.StorageObjects{Objects: []*api.StorageObject{{Value: c.storedJSON}}}, nil
}

// installConfigStub swaps configStorageRead for the counting stub and restores
// it (and clears the cache) on cleanup.
func installConfigStub(t *testing.T, stub *configTestStub) {
	t.Helper()
	orig := configStorageRead
	configStorageRead = stub.read
	clearConfigCacheForTest()
	t.Cleanup(func() {
		configStorageRead = orig
		clearConfigCacheForTest()
	})
}

// newNilIdentityConfigSession builds a minimal *sessionWS with a nil user
// identity (as produced by the ServerKey auth path in socket_ws.go) that can
// serve ConfigRequests. sendEvrHook captures responses; a buffered outgoing
// channel lets SendBytes succeed without a websocket.
func newNilIdentityConfigSession(t *testing.T, p *EvrPipeline) (*sessionWS, *[]evr.Message) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var mu sync.Mutex
	captured := &[]evr.Message{}
	s := &sessionWS{
		id:          uuid.Must(uuid.NewV4()),
		userID:      uuid.Nil, // nil identity: ServerKey session
		logger:      zap.NewNop(),
		username:    uberatomic.NewString(""),
		ctx:         ctx,
		ctxCancelFn: cancel,
		pipeline:    &Pipeline{}, // db is nil; the storage read is stubbed
		evrPipeline: p,
		outgoingCh:  make(chan []byte, 1024),
	}
	s.sendEvrHook = func(messages []evr.Message) {
		mu.Lock()
		defer mu.Unlock()
		*captured = append(*captured, messages...)
	}
	// Drain the outgoing channel so SendBytes never fills the buffer (which
	// would otherwise trigger a Close on the nil matchmaker under load).
	go func() {
		for {
			select {
			case <-s.outgoingCh:
			case <-ctx.Done():
				return
			}
		}
	}()
	return s, captured
}

// SEC-1(a): amplification. Distinct IDs for the same Type must collapse to a
// single storage read. On the vulnerable code every distinct ID is a cache
// miss -> one read per packet.
func TestSEC1_ConfigRequest_DistinctIDs_SingleDBRead(t *testing.T) {
	stub := &configTestStub{} // no stored object
	installConfigStub(t, stub)

	p := &EvrPipeline{}
	session, _ := newNilIdentityConfigSession(t, p)

	const n = 50
	for i := 0; i < n; i++ {
		msg := &evr.ConfigRequest{Type: "main_menu", ID: fmt.Sprintf("attacker-%d", i)}
		_ = p.configRequest(session.ctx, session.logger, session, msg)
	}

	got := atomic.LoadInt64(&stub.reads)
	if got != 1 {
		t.Fatalf("SEC-1 amplification: %d distinct IDs caused %d storage reads, want 1 "+
			"(cache must key on Type, not client-controlled ID)", n, got)
	}
}

// SEC-1(b): unbounded cache growth. With a stored Config:main_menu object,
// distinct IDs must not each create a cache entry.
func TestSEC1_ConfigCache_BoundedUnderDistinctIDs(t *testing.T) {
	stub := &configTestStub{storedJSON: `{"type":"main_menu","id":"main_menu"}`}
	installConfigStub(t, stub)

	p := &EvrPipeline{}
	session, _ := newNilIdentityConfigSession(t, p)

	const n = 50
	for i := 0; i < n; i++ {
		msg := &evr.ConfigRequest{Type: "main_menu", ID: fmt.Sprintf("attacker-%d", i)}
		if err := p.configRequest(session.ctx, session.logger, session, msg); err != nil {
			t.Fatalf("unexpected error serving stored config: %v", err)
		}
	}

	if entries := configCacheLenForTest(); entries != 1 {
		t.Fatalf("SEC-1 unbounded growth: %d distinct IDs produced %d cache entries, want 1", n, entries)
	}
	if got := atomic.LoadInt64(&stub.reads); got != 1 {
		t.Fatalf("SEC-1 amplification: %d distinct IDs caused %d storage reads, want 1", n, got)
	}
}

// SEC-1: the cache must be size-bounded so a flood of distinct Types cannot
// grow it without limit.
func TestSEC1_ConfigCache_SizeCapped(t *testing.T) {
	stub := &configTestStub{storedJSON: `{"ok":true}`}
	installConfigStub(t, stub)

	p := &EvrPipeline{}
	session, _ := newNilIdentityConfigSession(t, p)

	n := configCacheMaxEntries * 3
	for i := 0; i < n; i++ {
		msg := &evr.ConfigRequest{Type: fmt.Sprintf("type-%d", i), ID: fmt.Sprintf("type-%d", i)}
		_ = p.configRequest(session.ctx, session.logger, session, msg)
	}

	if entries := configCacheLenForTest(); entries > configCacheMaxEntries {
		t.Fatalf("SEC-1 cache cap: %d distinct Types produced %d cache entries, want <= %d",
			n, entries, configCacheMaxEntries)
	}
}

// Behavior preservation: the four valid Type:ID default pairs must still be
// served (as ConfigSuccess) when no stored object exists.
func TestSEC1_ValidDefaultPairs_StillServed(t *testing.T) {
	stub := &configTestStub{} // no stored object -> falls back to built-in defaults
	installConfigStub(t, stub)

	p := &EvrPipeline{}
	session, captured := newNilIdentityConfigSession(t, p)

	pairs := []string{
		"main_menu",
		"active_battle_pass_season",
		"active_store_entry",
		"active_store_featured_entry",
	}
	for _, tp := range pairs {
		*captured = (*captured)[:0]
		msg := &evr.ConfigRequest{Type: tp, ID: tp}
		if err := p.configRequest(session.ctx, session.logger, session, msg); err != nil {
			t.Fatalf("valid default pair %s: unexpected error: %v", tp, err)
		}
		if len(*captured) == 0 {
			t.Fatalf("valid default pair %s: no response sent", tp)
		}
		if _, ok := (*captured)[0].(*evr.ConfigSuccess); !ok {
			t.Fatalf("valid default pair %s: expected *evr.ConfigSuccess, got %T", tp, (*captured)[0])
		}
	}
}

// Behavior preservation: a real stored config object must be served.
func TestSEC1_StoredObjectServed(t *testing.T) {
	stub := &configTestStub{storedJSON: `{"type":"main_menu","id":"main_menu","custom":42}`}
	installConfigStub(t, stub)

	p := &EvrPipeline{}
	session, captured := newNilIdentityConfigSession(t, p)

	msg := &evr.ConfigRequest{Type: "main_menu", ID: "main_menu"}
	if err := p.configRequest(session.ctx, session.logger, session, msg); err != nil {
		t.Fatalf("stored object: unexpected error: %v", err)
	}
	if len(*captured) == 0 {
		t.Fatalf("stored object: no response sent")
	}
	if _, ok := (*captured)[0].(*evr.ConfigSuccess); !ok {
		t.Fatalf("stored object: expected *evr.ConfigSuccess, got %T", (*captured)[0])
	}
}

// SEC-1 reachability: ConfigRequest is dispatched with isAuthenticationRequired
// = false, so a nil-identity (ServerKey) session reaches the DB-touching
// handler. Proves remote-triggerability without any user token.
func TestSEC1_ConfigRequest_ReachableByNilIdentity(t *testing.T) {
	stub := &configTestStub{}
	installConfigStub(t, stub)

	p := &EvrPipeline{}
	session, _ := newNilIdentityConfigSession(t, p)
	if !session.userID.IsNil() {
		t.Fatal("test session must have a nil user identity")
	}

	msg := &evr.ConfigRequest{Type: "main_menu", ID: "reachability-probe"}
	if !p.ProcessRequestEVR(zap.NewNop(), session, msg) {
		t.Fatal("ProcessRequestEVR rejected ConfigRequest from a nil-identity session")
	}
	if got := atomic.LoadInt64(&stub.reads); got == 0 {
		t.Fatal("SEC-1 reachability: nil-identity ConfigRequest never reached the storage read")
	}
}

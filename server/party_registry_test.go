package server

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
)

func createTestPartyRegistry(t *testing.T) *LocalPartyRegistry {
	t.Helper()
	logger := loggerForTest(t)
	node := "testnode"
	tt := testTracker{}
	tsm := testStreamManager{}
	dmr := DummyMessageRouter{}
	return &LocalPartyRegistry{
		logger:        logger,
		config:        cfg,
		matchmaker:    nil,
		tracker:       &tt,
		streamManager: &tsm,
		router:        &dmr,
		node:          node,
		parties:       &MapOf[uuid.UUID, *PartyHandler]{},
	}
}

func testLeader() *rtapi.UserPresence {
	return &rtapi.UserPresence{UserId: uuid.Must(uuid.NewV4()).String(), Username: "leader"}
}

func TestGetOrCreate_NilID(t *testing.T) {
	pr := createTestPartyRegistry(t)

	ph, created, err := pr.GetOrCreate(uuid.Nil, true, 4, testLeader())
	if err == nil {
		t.Fatal("expected error for nil UUID, got nil")
	}
	if ph != nil {
		t.Fatal("expected nil handler for nil UUID")
	}
	if created {
		t.Fatal("expected created=false for nil UUID")
	}
}

func TestGetOrCreate_InvalidMaxSize(t *testing.T) {
	pr := createTestPartyRegistry(t)

	id := uuid.Must(uuid.NewV4())
	leader := testLeader()

	for _, maxSize := range []int{0, -1, -100} {
		ph, created, err := pr.GetOrCreate(id, true, maxSize, leader)
		if err == nil {
			t.Fatalf("expected error for maxSize=%d, got nil", maxSize)
		}
		if ph != nil {
			t.Fatalf("expected nil handler for maxSize=%d", maxSize)
		}
		if created {
			t.Fatalf("expected created=false for maxSize=%d", maxSize)
		}
	}
}

func TestGetOrCreate_CreatesNewParty(t *testing.T) {
	pr := createTestPartyRegistry(t)

	id := uuid.Must(uuid.NewV4())
	leader := testLeader()

	ph, created, err := pr.GetOrCreate(id, true, 4, leader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !created {
		t.Fatal("expected created=true for new party")
	}
	if ph == nil {
		t.Fatal("expected non-nil handler")
	}
	if ph.ID != id {
		t.Fatalf("expected party ID %v, got %v", id, ph.ID)
	}
}

func TestGetOrCreate_ReturnsExisting(t *testing.T) {
	pr := createTestPartyRegistry(t)

	id := uuid.Must(uuid.NewV4())
	leader := testLeader()

	ph1, created1, err := pr.GetOrCreate(id, true, 4, leader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !created1 {
		t.Fatal("expected created=true on first call")
	}

	ph2, created2, err := pr.GetOrCreate(id, true, 4, leader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created2 {
		t.Fatal("expected created=false on second call")
	}
	if ph1 != ph2 {
		t.Fatal("expected same handler returned on second call")
	}
}

func TestGetOrCreate_ConcurrentSameID(t *testing.T) {
	pr := createTestPartyRegistry(t)

	id := uuid.Must(uuid.NewV4())
	leader := testLeader()

	const goroutines = 50
	var (
		wg         sync.WaitGroup
		createdCnt atomic.Int32
		handlers   [goroutines]*PartyHandler
		errs       [goroutines]error
	)

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			ph, created, err := pr.GetOrCreate(id, true, 4, leader)
			handlers[idx] = ph
			errs[idx] = err
			if created {
				createdCnt.Add(1)
			}
		}(i)
	}
	wg.Wait()

	for i := 0; i < goroutines; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d got error: %v", i, errs[i])
		}
	}

	if createdCnt.Load() != 1 {
		t.Fatalf("expected exactly 1 goroutine to report created=true, got %d", createdCnt.Load())
	}

	// All handlers must be the same instance.
	first := handlers[0]
	for i := 1; i < goroutines; i++ {
		if handlers[i] != first {
			t.Fatalf("goroutine %d returned different handler than goroutine 0", i)
		}
	}
}

func TestGetOrCreate_DifferentIDs(t *testing.T) {
	pr := createTestPartyRegistry(t)

	leader := testLeader()
	id1 := uuid.Must(uuid.NewV4())
	id2 := uuid.Must(uuid.NewV4())

	ph1, created1, err := pr.GetOrCreate(id1, true, 4, leader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !created1 {
		t.Fatal("expected created=true for first ID")
	}

	ph2, created2, err := pr.GetOrCreate(id2, true, 8, leader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !created2 {
		t.Fatal("expected created=true for second ID")
	}

	if ph1 == ph2 {
		t.Fatal("expected different handlers for different IDs")
	}
}

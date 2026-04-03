package server

import (
	"sync"
	"testing"
	"time"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

func TestMatchmakingCredit_StoreAndRetrieve(t *testing.T) {
	matchmakingCredits = sync.Map{}
	t.Cleanup(func() { matchmakingCredits = sync.Map{} })

	ts := time.Now().UTC().Add(-30 * time.Second)
	setMatchmakingCredit("user-1", &matchmakingCredit{
		Mode:      evr.ModeArenaPublic,
		Timestamp: ts,
		Expiry:    time.Now().UTC().Add(8 * time.Minute),
	})

	got := getMatchmakingCredit("user-1", evr.ModeArenaPublic)
	if got == nil {
		t.Fatal("expected credit, got nil")
	}
	if !got.Timestamp.Equal(ts) {
		t.Errorf("expected timestamp %v, got %v", ts, got.Timestamp)
	}
}

func TestMatchmakingCredit_ExpiredCredit(t *testing.T) {
	matchmakingCredits = sync.Map{}
	t.Cleanup(func() { matchmakingCredits = sync.Map{} })

	setMatchmakingCredit("user-1", &matchmakingCredit{
		Mode:      evr.ModeArenaPublic,
		Timestamp: time.Now().UTC().Add(-10 * time.Minute),
		Expiry:    time.Now().UTC().Add(-1 * time.Minute),
	})

	got := getMatchmakingCredit("user-1", evr.ModeArenaPublic)
	if got != nil {
		t.Errorf("expected nil for expired credit, got %+v", got)
	}

	if _, loaded := matchmakingCredits.Load("user-1"); loaded {
		t.Error("expected expired credit to be removed from map")
	}
}

func TestMatchmakingCredit_ModeMismatch(t *testing.T) {
	matchmakingCredits = sync.Map{}
	t.Cleanup(func() { matchmakingCredits = sync.Map{} })

	setMatchmakingCredit("user-1", &matchmakingCredit{
		Mode:      evr.ModeArenaPublic,
		Timestamp: time.Now().UTC(),
		Expiry:    time.Now().UTC().Add(8 * time.Minute),
	})

	got := getMatchmakingCredit("user-1", evr.ModeCombatPublic)
	if got != nil {
		t.Errorf("expected nil for mode mismatch, got %+v", got)
	}

	if _, loaded := matchmakingCredits.Load("user-1"); !loaded {
		t.Error("expected arena credit to still be in map after mode mismatch lookup")
	}
}

func TestMatchmakingCredit_Clear(t *testing.T) {
	matchmakingCredits = sync.Map{}
	t.Cleanup(func() { matchmakingCredits = sync.Map{} })

	setMatchmakingCredit("user-1", &matchmakingCredit{
		Mode:      evr.ModeArenaPublic,
		Timestamp: time.Now().UTC(),
		Expiry:    time.Now().UTC().Add(8 * time.Minute),
	})

	clearMatchmakingCredit("user-1")

	got := getMatchmakingCredit("user-1", evr.ModeArenaPublic)
	if got != nil {
		t.Errorf("expected nil after clear, got %+v", got)
	}
}

func TestMatchmakingCredit_Overwrite(t *testing.T) {
	matchmakingCredits = sync.Map{}
	t.Cleanup(func() { matchmakingCredits = sync.Map{} })

	t0 := time.Now().UTC().Add(-2 * time.Minute)
	t1 := time.Now().UTC().Add(-1 * time.Minute)

	setMatchmakingCredit("user-1", &matchmakingCredit{
		Mode:      evr.ModeArenaPublic,
		Timestamp: t0,
		Expiry:    time.Now().UTC().Add(8 * time.Minute),
	})
	setMatchmakingCredit("user-1", &matchmakingCredit{
		Mode:      evr.ModeCombatPublic,
		Timestamp: t1,
		Expiry:    time.Now().UTC().Add(8 * time.Minute),
	})

	got := getMatchmakingCredit("user-1", evr.ModeCombatPublic)
	if got == nil {
		t.Fatal("expected credit, got nil")
	}
	if !got.Timestamp.Equal(t1) {
		t.Errorf("expected overwritten timestamp %v, got %v", t1, got.Timestamp)
	}

	got = getMatchmakingCredit("user-1", evr.ModeArenaPublic)
	if got != nil {
		t.Errorf("expected nil for old mode after overwrite, got %+v", got)
	}
}

func TestMatchmakingCredit_FutureTimestamp(t *testing.T) {
	matchmakingCredits = sync.Map{}
	t.Cleanup(func() { matchmakingCredits = sync.Map{} })

	setMatchmakingCredit("user-1", &matchmakingCredit{
		Mode:      evr.ModeArenaPublic,
		Timestamp: time.Now().UTC().Add(5 * time.Minute),
		Expiry:    time.Now().UTC().Add(8 * time.Minute),
	})

	got := getMatchmakingCredit("user-1", evr.ModeArenaPublic)
	if got != nil {
		t.Errorf("expected nil for future timestamp, got %+v", got)
	}
}

func TestMatchmakingCredit_DifferentUsers(t *testing.T) {
	matchmakingCredits = sync.Map{}
	t.Cleanup(func() { matchmakingCredits = sync.Map{} })

	t1 := time.Now().UTC().Add(-2 * time.Minute)
	t2 := time.Now().UTC().Add(-1 * time.Minute)

	setMatchmakingCredit("user-1", &matchmakingCredit{
		Mode:      evr.ModeArenaPublic,
		Timestamp: t1,
		Expiry:    time.Now().UTC().Add(8 * time.Minute),
	})
	setMatchmakingCredit("user-2", &matchmakingCredit{
		Mode:      evr.ModeArenaPublic,
		Timestamp: t2,
		Expiry:    time.Now().UTC().Add(8 * time.Minute),
	})

	got1 := getMatchmakingCredit("user-1", evr.ModeArenaPublic)
	got2 := getMatchmakingCredit("user-2", evr.ModeArenaPublic)

	if got1 == nil || got2 == nil {
		t.Fatal("expected both credits to exist")
	}
	if !got1.Timestamp.Equal(t1) {
		t.Errorf("user-1: expected %v, got %v", t1, got1.Timestamp)
	}
	if !got2.Timestamp.Equal(t2) {
		t.Errorf("user-2: expected %v, got %v", t2, got2.Timestamp)
	}
}

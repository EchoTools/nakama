package server

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// fakeMigrationClock replaces wall time so pacing can be asserted on SHAPE
// rather than on elapsed nanoseconds.
//
// Every Now() advances by exactly one tick, so a duration measured across a
// unit of work that consults the clock twice -- start, then finish -- is always
// one tick. That turns "how long was this pause relative to what it paused for"
// into an exact equality instead of a timing window, and the test neither
// sleeps nor flakes.
type fakeMigrationClock struct {
	mu     sync.Mutex
	now    time.Time
	tick   time.Duration
	sleeps []time.Duration
}

func newFakeMigrationClock() *fakeMigrationClock {
	return &fakeMigrationClock{
		now:  time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC),
		tick: time.Millisecond,
	}
}

func (c *fakeMigrationClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(c.tick)
	return c.now
}

func (c *fakeMigrationClock) Sleep(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
}

func (c *fakeMigrationClock) recorded() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

func (c *fakeMigrationClock) pacer() *migrationPacer {
	return &migrationPacer{now: c.Now, sleep: c.Sleep}
}

// pagingModule serves m.listed in fixed-size pages with real cursors.
//
// The base double answers every walk in one page, which cannot distinguish
// per-page pacing from per-account pacing at all: with one page there is no
// page BOUNDARY, and the old pause sat after the boundary check. Several pages
// is the only fixture in which the two shapes produce different numbers.
type pagingModule struct {
	*altClearTestModule
	pageSize int
}

func (m *pagingModule) StorageList(ctx context.Context, callerID, userID, collection string, limit int, cursor string) ([]*api.StorageObject, string, error) {
	start := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil {
			return nil, "", err
		}
		start = n
	}
	end := min(start+m.pageSize, len(m.listed))
	next := ""
	if end < len(m.listed) {
		next = strconv.Itoa(end)
	}
	return m.liveListed()[start:end], next, nil
}

// seedUnlinkedAccount stores a searchable account that carries NO alt links.
//
// No links is deliberate: this fixture exists to measure pacing across several
// pages, and an account whose link is cleared without being rebuilt feeds the
// safety floor. With enough accounts to fill three pages, a linked fixture
// would trip the floor and abort the run before the pacing could be observed.
func seedUnlinkedAccount(t *testing.T, m *altClearTestModule, userID, serial string) {
	t.Helper()

	h := NewLoginHistory(userID)
	h.History = map[string]*LoginHistoryEntry{
		"entry": {
			CreatedAt: time.Now().Add(-time.Hour),
			UpdatedAt: time.Now(),
			XPID:      evr.EvrId{PlatformCode: 4, AccountId: 1000},
			ClientIP:  "203.0.113.7",
			LoginData: &evr.LoginProfile{HMDSerialNumber: serial},
		},
	}

	data, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal seed history for %s: %v", userID, err)
	}
	if len(h.AltSearchPatterns()) == 0 {
		t.Fatalf("fixture is inert: %s has no search patterns, so phase 2 skips it and never paces it", userID)
	}

	version := m.seedObject(userID, LoginStorageCollection, LoginHistoryStorageKey, string(data))
	m.listed = append(m.listed, &api.StorageObject{
		Collection: LoginStorageCollection,
		Key:        LoginHistoryStorageKey,
		UserId:     userID,
		Value:      string(data),
		Version:    version,
	})
}

// TestClearAltsMigration_PacesPerAccountNotPerPage is the pacing gate.
//
// The old pause sat at the bottom of the page loop and slept for as long as the
// whole page took. The average duty cycle was 50%, but the PROFILE was not: a
// page is 100 accounts, so the box saw 100% load for the length of a page and
// then an equally long dead stop. Moving the pause inside the per-account work
// keeps the same average and flattens the profile.
//
// Both properties are asserted, because either alone is satisfiable by the
// wrong implementation:
//
//   - COUNT: one pause per account per phase, not one per page boundary. With
//     10 accounts in pages of 4 the two shapes are 20 pauses and 4.
//   - SIZE: every pause is one tick, the duration of the single account it
//     follows. A page-sized pause is many ticks, so this is what rules out
//     "paused the right number of times, for the wrong amount".
func TestClearAltsMigration_PacesPerAccountNotPerPage(t *testing.T) {
	const accounts = 10

	base := newAltClearTestModule()
	for i := 0; i < accounts; i++ {
		seedUnlinkedAccount(t, base, migrationTestUserID(i+1), "SERIAL-"+strconv.Itoa(i+1))
	}
	nk := &pagingModule{altClearTestModule: base, pageSize: 4}

	clock := newFakeMigrationClock()
	m := &MigrationClearAlternateMatches{pacer: clock.pacer()}
	ensureAltClearPreconditions(t)

	logger := newCaptureLogger()
	if err := m.MigrateSystem(context.Background(), logger, nil, nk); err != nil {
		t.Fatalf("MigrateSystem returned an error: %v", err)
	}

	// Positive control: without this, "20 pauses" could be 20 pauses over an
	// empty walk and the test would prove nothing about accounts.
	if got := completionField(t, logger, "walked"); got != accounts {
		t.Fatalf("walked = %d, want %d: the fixture did not reach the per-account work", got, accounts)
	}

	sleeps := clock.recorded()

	// Phase 1 repairs caches for every account and phase 2 rebuilds every
	// account, so each account is paced exactly twice.
	if want := accounts * 2; len(sleeps) != want {
		t.Errorf("paused %d times, want %d (once per account per phase). %d is one pause per page boundary -- the load profile is still a page of full load followed by a page-long stop.",
			len(sleeps), want, len(sleeps))
	}

	for i, d := range sleeps {
		if d != clock.tick {
			t.Errorf("pause %d was %v, want %v: a pause must match the duration of the ONE account it follows, not of the page around it", i, d, clock.tick)
		}
	}
}

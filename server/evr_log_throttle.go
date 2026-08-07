package server

import (
	"sync"
	"time"
)

// perKeyLogThrottle rate-limits repeated log lines by key, so a condition that
// holds for every player in a guild for minutes at a time still produces a
// readable number of log lines. Metrics counters are the right place to carry
// the true volume; this only bounds the prose.
//
// Every field is read and written under mu, including the clock, so a test may
// swap in a fake clock on a throttle that production code is also calling.
type perKeyLogThrottle struct {
	mu       sync.Mutex
	interval time.Duration
	last     map[string]time.Time
	now      func() time.Time
}

func newPerKeyLogThrottle(interval time.Duration) *perKeyLogThrottle {
	return &perKeyLogThrottle{
		interval: interval,
		last:     make(map[string]time.Time),
		now:      time.Now,
	}
}

// allow reports whether key may emit a log line now, recording the emission
// when it does. The first call for a key always passes.
func (t *perKeyLogThrottle) allow(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	if last, ok := t.last[key]; ok && now.Sub(last) < t.interval {
		return false
	}
	t.last[key] = now
	return true
}

// reset forgets every recorded emission and restores the real clock.
//
// Tests must call this instead of reassigning the package-level throttle they
// exercise: the throttle is shared process-wide, so leftover state from one
// test silently suppresses another test's log line and makes its assertion pass
// for the wrong reason, and an unsynchronized reassignment is a data race the
// moment anything runs in parallel.
func (t *perKeyLogThrottle) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.last = make(map[string]time.Time)
	t.now = time.Now
}

// setClock replaces the throttle's time source. Test-only; safe to call while
// other goroutines are in allow.
func (t *perKeyLogThrottle) setClock(now func() time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.now = now
}

package server

import (
	"sync"
	"time"
)

// logThrottle rate-limits repeated log lines by key, so a condition that holds
// for every player in a guild for minutes at a time still produces a readable
// number of log lines. Metrics counters are the right place to carry the true
// volume; this only bounds the prose.
type logThrottle struct {
	mu       sync.Mutex
	interval time.Duration
	last     map[string]time.Time
	now      func() time.Time
}

func newLogThrottle(interval time.Duration) *logThrottle {
	return &logThrottle{
		interval: interval,
		last:     make(map[string]time.Time),
		now:      time.Now,
	}
}

// allow reports whether key may emit a log line now, recording the emission
// when it does. The first call for a key always passes.
func (t *logThrottle) allow(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	if last, ok := t.last[key]; ok && now.Sub(last) < t.interval {
		return false
	}
	t.last[key] = now
	return true
}

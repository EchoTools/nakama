package server

import (
	"go.uber.org/atomic"
	"golang.org/x/time/rate"
)

// inboundRateLimiter is a per-session token-bucket limiter for inbound EVR
// messages. It gates message dispatch in the Consume loop (SEC-2): without it
// a single established WebSocket can submit messages as fast as the TCP window
// allows, turning any cheap-request/expensive-handler pair — e.g. SEC-1's
// ConfigRequest -> storage read — into a sustained database-exhaustion DoS.
// Connection-level rate limits do not throttle messages within an already-
// established socket, so the per-session cap lives here.
//
// A nil *inboundRateLimiter allows everything (limiter disabled via config).
type inboundRateLimiter struct {
	limiter *rate.Limiter
	dropped atomic.Int64 // total messages dropped, for the audit counter
	lastLog atomic.Int64 // unix-nano of the last emitted drop warn log
}

// newInboundRateLimiter builds a limiter allowing perSec messages/second with
// the given burst. If perSec <= 0 the limiter is disabled and nil is returned
// (a nil limiter allows all messages). A non-positive burst defaults to perSec.
func newInboundRateLimiter(perSec, burst int) *inboundRateLimiter {
	if perSec <= 0 {
		return nil
	}
	if burst <= 0 {
		burst = perSec
	}
	return &inboundRateLimiter{
		limiter: rate.NewLimiter(rate.Limit(perSec), burst),
	}
}

// allow reports whether one inbound message may be dispatched now. On denial it
// records the drop for the audit counter. A nil limiter always allows.
func (l *inboundRateLimiter) allow() bool {
	if l == nil {
		return true
	}
	if l.limiter.Allow() {
		return true
	}
	l.dropped.Inc()
	return false
}

// droppedTotal returns the total number of messages this limiter has dropped.
// A nil limiter has dropped nothing.
func (l *inboundRateLimiter) droppedTotal() int64 {
	if l == nil {
		return 0
	}
	return l.dropped.Load()
}

// logDue reports whether a drop warning should be emitted now, rate-limited to
// at most once per minIntervalNano, so the warn log cannot itself be turned
// into a flood. A nil limiter never logs.
func (l *inboundRateLimiter) logDue(nowNano, minIntervalNano int64) bool {
	if l == nil {
		return false
	}
	last := l.lastLog.Load()
	if nowNano-last < minIntervalNano {
		return false
	}
	return l.lastLog.CompareAndSwap(last, nowNano)
}

package server

import (
	"math"
	"testing"
	"time"
)

// --- SEC-2 regression tests -------------------------------------------------
//
// SEC-2: the Consume loop (session_ws.go IncomingLoop) dispatches every parsed
// EVR message with no per-session throttle, so a single established socket can
// submit messages as fast as the TCP window allows. Connection-level rate
// limits do not throttle messages within an already-established socket, so a
// per-message flood on one allowed socket reproduces the same DB-exhaustion
// outcome (force-multiplier for SEC-1). A per-session token-bucket limiter
// fixes it.

// SEC-2: a configured limiter must cap a burst and drop the overflow, recording
// each drop for the audit counter.
func TestSEC2_InboundRateLimiter_ThrottlesFlood(t *testing.T) {
	const perSec, burst = 50, 100
	l := newInboundRateLimiter(perSec, burst)
	if l == nil {
		t.Fatal("expected a limiter for perSec>0")
	}

	// A token bucket that starts full guarantees two timing-independent facts:
	// the first `burst` calls are always admitted, and over any wall-clock
	// window of duration d the total admitted count cannot exceed
	// burst + perSec*d (the bucket's refill rate). rate.Limiter.Allow() reads
	// time.Now() on each call, so a slow or -race loop legitimately replenishes
	// tokens as it runs; we therefore measure d around the loop and assert
	// against that exact bound instead of a hardcoded constant, which is what
	// makes this test deterministic rather than flaky under load.
	const flood = 1000
	start := time.Now()
	allowed := 0
	for i := 0; i < flood; i++ {
		if l.allow() {
			allowed++
		}
	}
	elapsed := time.Since(start)

	if allowed >= flood {
		t.Fatalf("SEC-2: limiter allowed all %d messages (no throttle)", flood)
	}
	if allowed < burst {
		t.Fatalf("SEC-2: limiter allowed %d < burst %d (would throttle legitimate bursts)", allowed, burst)
	}
	// Upper bound from the token-bucket refill guarantee, computed from the
	// measured loop duration (+1 for a token that may land exactly on the
	// window boundary). A correct limiter can never exceed this regardless of
	// machine speed, so the assertion is deterministic.
	maxAllowed := burst + int(math.Ceil(perSec*elapsed.Seconds())) + 1
	if allowed > maxAllowed {
		t.Fatalf("SEC-2: limiter allowed %d, above token-bucket bound %d (burst %d + %d/s over %v; cap ineffective)",
			allowed, maxAllowed, burst, perSec, elapsed)
	}
	dropped := int64(flood - allowed)
	if got := l.droppedTotal(); got != dropped {
		t.Fatalf("SEC-2 audit counter: droppedTotal()=%d, want %d", got, dropped)
	}
}

// SEC-2: perSec <= 0 disables the limiter (nil), and a nil limiter allows
// everything — the opt-out path must not panic and must not throttle.
func TestSEC2_InboundRateLimiter_Disabled(t *testing.T) {
	l := newInboundRateLimiter(0, 0)
	if l != nil {
		t.Fatalf("expected nil limiter when perSec<=0, got %v", l)
	}
	for i := 0; i < 1000; i++ {
		if !l.allow() {
			t.Fatal("SEC-2: disabled (nil) limiter must allow every message")
		}
	}
	if got := l.droppedTotal(); got != 0 {
		t.Fatalf("SEC-2: disabled limiter droppedTotal()=%d, want 0", got)
	}
}

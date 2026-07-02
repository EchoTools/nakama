package server

import "testing"

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

	const flood = 1000
	allowed := 0
	for i := 0; i < flood; i++ {
		if l.allow() {
			allowed++
		}
	}

	if allowed >= flood {
		t.Fatalf("SEC-2: limiter allowed all %d messages (no throttle)", flood)
	}
	if allowed < burst {
		t.Fatalf("SEC-2: limiter allowed %d < burst %d (would throttle legitimate bursts)", allowed, burst)
	}
	if allowed > burst+perSec {
		t.Fatalf("SEC-2: limiter allowed %d, well above burst %d (cap ineffective)", allowed, burst)
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

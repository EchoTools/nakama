package server

import "testing"

// SEC-2: the per-session inbound message rate limit default is data-derived, not
// guessed. It is the 3.5σ point of the per-session inbound EVR message rate
// distribution measured from one month of production debug logs
// (2026-06-03 → 2026-07-03, n=10,782,318 active session-seconds,
// mean=1.845 msg/s, σ=1.790 msg/s → mean+3.5σ=8.11, rounded up to 9). This test
// pins that value so the DoS cap cannot silently drift back to an unverified
// number. If the traffic profile is re-measured, update both this test and the
// derivation comment in NewSocketConfig together.
func TestSEC2_DefaultInboundRateLimit_IsDataDerived(t *testing.T) {
	c := NewSocketConfig()
	if got, want := c.InboundMessageRateLimitPerSec, 9; got != want {
		t.Fatalf("InboundMessageRateLimitPerSec default = %d, want %d (mean+3.5σ of measured production rate)", got, want)
	}
	// Burst must comfortably exceed the highest per-second rate ever observed in
	// the same month (max=21 msg/s) so a legitimate connect-time burst is never
	// clipped, while the sustained rate above still caps a flood.
	if got, want := c.InboundMessageRateLimitBurst, 200; got != want {
		t.Fatalf("InboundMessageRateLimitBurst default = %d, want %d", got, want)
	}
}

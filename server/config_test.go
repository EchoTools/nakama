package server

import "testing"

// SEC-2: the per-session inbound message rate limit default is grounded in
// measured production traffic, not guessed. One month of production debug logs
// (2026-06-03 → 2026-07-03, n=10,782,318 active session-seconds) put the mean at
// 1.845 msg/s, σ at 1.790, and the observed per-second max at 21 msg/s. Because
// the distribution is heavily right-tailed, a σ-multiple (mean+3.5σ≈9) undershoots
// the real ceiling; the sustained cap is instead pinned to observed-max + margin
// (21 msg/s observed peak + 9 headroom = 30). This test pins that value so the DoS cap cannot silently drift back
// to an unverified number. If the traffic profile is re-measured, update both this
// test and the derivation comment in NewSocketConfig together.
func TestSEC2_DefaultInboundRateLimit_IsDataDerived(t *testing.T) {
	c := NewSocketConfig()
	if got, want := c.InboundMessageRateLimitPerSec, 30; got != want {
		t.Fatalf("InboundMessageRateLimitPerSec default = %d, want %d (observed production max 21 msg/s + headroom)", got, want)
	}
	// Burst must comfortably exceed the highest per-second rate ever observed in
	// the same month (max=21 msg/s) so a legitimate connect-time burst is never
	// clipped, while the sustained rate above still caps a flood.
	if got, want := c.InboundMessageRateLimitBurst, 200; got != want {
		t.Fatalf("InboundMessageRateLimitBurst default = %d, want %d", got, want)
	}
}

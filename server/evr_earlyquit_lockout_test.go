package server

import (
	"testing"
	"time"
)

// TestEarlyQuitLockoutExpiryGate pins the lockout expiry direction (B4).
//
// EarlyQuitPlayerState.PenaltyTimestamp is an ABSOLUTE expiry timestamp
// (unix seconds): the scheduler's expiry gate must fire when now >= penalty_ts
// and must NOT fire for a zero timestamp (penalty never recorded). The previous
// implementation compared now - penalty_ts against the lockout duration, which
// fired a full lockout late for genuine penalties and instantly for a zero
// timestamp (discord handler never set it).
func TestEarlyQuitLockoutExpiryGate(t *testing.T) {
	// Genuine 15-minute lockout (penalty level 3): expiry 900s in the future.
	active := NewEarlyQuitPlayerState()
	active.PenaltyLevel = 3
	active.PenaltyTimestamp = time.Now().Unix() + 900

	// The penalty is active...
	if !active.IsPenaltyActive() {
		t.Errorf("IsPenaltyActive() = false for penalty_ts=%d (900s in the future)", active.PenaltyTimestamp)
	}

	// ...and the scheduler expiry gate must NOT fire: 900s remain.
	if earlyQuitPenaltyExpired(active.PenaltyTimestamp, time.Now()) {
		t.Errorf("scheduler expiry gate fired while lockout is active (penalty_ts=%d is 900s in the future)", active.PenaltyTimestamp)
	}

	// Lockout expired 1s ago: the gate MUST fire.
	expired := NewEarlyQuitPlayerState()
	expired.PenaltyLevel = 3
	expired.PenaltyTimestamp = time.Now().Unix() - 1

	if expired.IsPenaltyActive() {
		t.Errorf("IsPenaltyActive() = true for penalty_ts=%d (expired 1s ago)", expired.PenaltyTimestamp)
	}
	if !earlyQuitPenaltyExpired(expired.PenaltyTimestamp, time.Now()) {
		t.Errorf("scheduler expiry gate did not fire for penalty_ts=%d (expired 1s ago)", expired.PenaltyTimestamp)
	}

	// Zero timestamp (penalty never recorded — discord handler missed the
	// timestamp, or the level-0 clear path): the gate must never fire. A zero
	// ts must not make the lockout instantly "expired" on the next scheduler tick.
	zero := NewEarlyQuitPlayerState()
	zero.PenaltyLevel = 0
	zero.PenaltyTimestamp = 0
	if earlyQuitPenaltyExpired(zero.PenaltyTimestamp, time.Now()) {
		t.Errorf("scheduler expiry gate fired for zero penalty_ts: lockouts with no recorded timestamp must not expire")
	}
}

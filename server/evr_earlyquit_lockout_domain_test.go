package server

import (
	"testing"
	"time"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

// TestEarlyQuitLockoutDurations_CoversEveryLevel is the sensor for a drift that
// would otherwise be silent.
//
// GetLockoutDuration returns 0 for a level that is not in
// EarlyQuitLockoutDurations. Zero is not an error value here -- it is what
// level 0 legitimately returns, and what "no lockout" looks like everywhere
// downstream: IsPenaltyActive() reports no active lockout and the scheduler
// treats the penalty as already expired. So a missing entry does not fail, it
// un-penalises.
//
// Today the map and MaxEarlyQuitPenaltyLevel agree at 3, and every path that
// can reach GetLockoutDuration is bounded to that: the modify RPC rejects
// level > MaxEarlyQuitPenaltyLevel (evr_runtime_rpc_earlyquit_manage.go), the
// /earlyquit command validates 0..3, and evr_match.go clamps to it. The gap
// this test closes is the future edit that raises the ceiling and forgets the
// table.
func TestEarlyQuitLockoutDurations_CoversEveryLevel(t *testing.T) {
	for level := 0; level <= MaxEarlyQuitPenaltyLevel; level++ {
		if _, ok := EarlyQuitLockoutDurations[level]; !ok {
			t.Errorf("penalty level %d has no entry in EarlyQuitLockoutDurations, so GetLockoutDuration(%d) "+
				"returns 0 and the level silently carries no lockout. MaxEarlyQuitPenaltyLevel is %d — "+
				"raising it requires extending the table.", level, level, MaxEarlyQuitPenaltyLevel)
		}
	}
}

// TestEarlyQuitLockoutDurations_OnlyLevelZeroIsFree pins the other half: a
// non-zero level must actually cost something, or the ladder has a rung that
// does nothing.
func TestEarlyQuitLockoutDurations_OnlyLevelZeroIsFree(t *testing.T) {
	if got := GetLockoutDuration(0); got != 0 {
		t.Errorf("level 0 should carry no lockout, got %v", got)
	}
	for level := 1; level <= MaxEarlyQuitPenaltyLevel; level++ {
		if got := GetLockoutDuration(level); got <= 0 {
			t.Errorf("level %d carries no lockout (%v); a penalty rung that costs nothing is indistinguishable from level 0", level, got)
		}
	}
}

// TestGetLockoutDuration_UnmappedLevelIsZero documents the behaviour the two
// tests above exist to contain, so it is not mistaken for a bug when read in
// isolation. Out-of-domain input is the caller's contract to satisfy.
func TestGetLockoutDuration_UnmappedLevelIsZero(t *testing.T) {
	if got := GetLockoutDuration(MaxEarlyQuitPenaltyLevel + 1); got != 0 {
		t.Errorf("got %v, want 0 for an out-of-domain level", got)
	}
	if got := GetLockoutDuration(-1); got != 0 {
		t.Errorf("got %v, want 0 for a negative level", got)
	}
}

// TestGetLockoutDuration_IsKeyedOnLevelNotQuitCount is the regression guard for
// the deprecation note this change withdrew.
//
// The note said callers should use ResolvePenaltyLevel instead. The two are
// keyed on different things, and swapping one for the other is what produced
// the bug fixed in #535 — a level absent from the configured ladder borrowed a
// different level's lockout. This pins that they are not interchangeable, so
// the advice cannot quietly come back.
func TestGetLockoutDuration_IsKeyedOnLevelNotQuitCount(t *testing.T) {
	// A config whose ladder does not configure level 2 at all: one band, for
	// level 1, covering quit counts 0..99.
	cfg := &evr.SNSEarlyQuitConfig{
		PenaltyLevels: []evr.EarlyQuitPenaltyLevelConfig{
			{PenaltyLevel: 1, MinEarlyQuits: 0, MaxEarlyQuits: 99, MMLockoutSec: 120},
		},
	}

	// ResolvePenaltyLevel is asked about a QUIT COUNT and can only ever answer
	// with the band that count falls in.
	level, lockoutSec := ResolvePenaltyLevel(5, cfg)
	if level != 1 {
		t.Fatalf("precondition: quit count 5 should resolve to the only configured level, got %d", level)
	}

	// GetLockoutDuration is asked about a LEVEL. Level 2's built-in lockout is
	// its own value, not level 1's — which is exactly what ResolvePenaltyLevel
	// would have handed back for any quit count in this config.
	if got := GetLockoutDuration(2); got == time.Duration(lockoutSec)*time.Second {
		t.Errorf("level 2's lockout (%v) collapsed onto the configured band's (%ds); "+
			"if these are ever equal by construction this test stops proving the two are keyed differently", got, lockoutSec)
	}
	if got := GetLockoutDuration(2); got != EarlyQuitLockoutDurations[2] {
		t.Errorf("got %v, want level 2's own entry %v", got, EarlyQuitLockoutDurations[2])
	}
}

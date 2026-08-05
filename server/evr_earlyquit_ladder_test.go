package server

import (
	"math"
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

// TestResolvePenaltyLevel_UnderRangeAndGapDoNotGiveMaxPenalty pins the
// direction of the "no band matched" fallback.
//
// ResolvePenaltyLevel walks the ladder looking for a band that contains the
// quit count. The fallback for "no band matched" returned the LAST configured
// level — the maximum penalty — with the comment "quit count above all ranges".
// That comment only describes one of the three ways the loop can fall through:
//
//   - the count is ABOVE every band (the intended case: max penalty is right),
//   - the count is BELOW the first band (a player with fewer quits than the
//     ladder's floor got the harshest lockout in the ladder),
//   - the count lands in a GAP between two bands. validatePenaltyLevels only
//     ever raises MinEarlyQuits to close overlaps, never lowers it to close
//     gaps, and nothing forces the first band to start at 0 — so both of those
//     shapes survive validation and reach production.
//
// Correct behaviour: resolution must be total and monotonic. A count that is
// not inside any band resolves to the highest band whose floor it has already
// reached (i.e. the band below the gap), and to no penalty at all when it is
// below every band.
func TestResolvePenaltyLevel_UnderRangeAndGapDoNotGiveMaxPenalty(t *testing.T) {
	// Ladder with a gap: 3 and 4 quits belong to no band.
	gapped := &evr.SNSEarlyQuitConfig{
		PenaltyLevels: []evr.EarlyQuitPenaltyLevelConfig{
			{PenaltyLevel: 0, MinEarlyQuits: 0, MaxEarlyQuits: 2, MMLockoutSec: 0},
			{PenaltyLevel: 1, MinEarlyQuits: 5, MaxEarlyQuits: 10, MMLockoutSec: 120},
			{PenaltyLevel: 3, MinEarlyQuits: 11, MaxEarlyQuits: 999, MMLockoutSec: 900},
		},
	}

	// Ladder whose floor is above zero: 0, 1 and 2 quits are below every band.
	floored := &evr.SNSEarlyQuitConfig{
		PenaltyLevels: []evr.EarlyQuitPenaltyLevelConfig{
			{PenaltyLevel: 1, MinEarlyQuits: 3, MaxEarlyQuits: 5, MMLockoutSec: 120},
			{PenaltyLevel: 2, MinEarlyQuits: 6, MaxEarlyQuits: 999, MMLockoutSec: 300},
		},
	}

	for _, tc := range []struct {
		name           string
		cfg            *evr.SNSEarlyQuitConfig
		quits          int32
		wantLevel      int32
		wantLockoutSec int32
	}{
		// --- in-band lookups still work (guards against a fix that breaks the happy path) ---
		{"gapped: in first band", gapped, 1, 0, 0},
		{"gapped: at band floor", gapped, 5, 1, 120},
		{"gapped: at band ceiling", gapped, 10, 1, 120},
		{"gapped: in top band", gapped, 50, 3, 900},

		// --- the gap: 3 and 4 quits belong to no band ---
		{"gapped: first count in gap falls to the band below", gapped, 3, 0, 0},
		{"gapped: last count in gap falls to the band below", gapped, 4, 0, 0},

		// --- above every band: the max penalty IS correct here ---
		{"gapped: above every band gets the top level", gapped, 1000, 3, 900},
		{"floored: above every band gets the top level", floored, 5000, 2, 300},

		// --- below every band: no penalty, not the harshest one ---
		{"floored: zero quits is below the ladder floor", floored, 0, 0, 0},
		{"floored: one quit is below the ladder floor", floored, 1, 0, 0},
		{"floored: one short of the floor", floored, 2, 0, 0},
		{"floored: at the floor", floored, 3, 1, 120},
	} {
		t.Run(tc.name, func(t *testing.T) {
			level, lockoutSec := ResolvePenaltyLevel(tc.quits, tc.cfg)
			if level != tc.wantLevel || lockoutSec != tc.wantLockoutSec {
				t.Errorf("ResolvePenaltyLevel(%d) = (level %d, lockout %ds), want (level %d, lockout %ds)",
					tc.quits, level, lockoutSec, tc.wantLevel, tc.wantLockoutSec)
			}
		})
	}
}

// TestResolvePenaltyLevel_LockoutSaturatesInsteadOfWrapping pins the int32
// conversion of the config-supplied lockout.
//
// MMLockoutSec is a plain `int` (64-bit) that arrives from stored JSON.
// validatePenaltyLevels clamps only negatives, and configs read back through
// StorableRead are never re-validated at all, so an out-of-range value reaches
// ResolvePenaltyLevel intact. `int32(pl.MMLockoutSec)` then wraps: 2^31 becomes
// negative and 2^32 becomes exactly zero. Every caller gates on
// `lockoutSec > 0`, so a wrapped value takes the CLEAR branch — the penalty is
// silently disabled by a config value that asked for a longer one.
//
// Correct behaviour: saturate at math.MaxInt32. An absurdly long lockout is
// still a lockout.
func TestResolvePenaltyLevel_LockoutSaturatesInsteadOfWrapping(t *testing.T) {
	for _, tc := range []struct {
		name        string
		lockoutSec  int
		wantLockout int32
	}{
		{"ordinary value passes through", 900, 900},
		{"exactly MaxInt32 passes through", math.MaxInt32, math.MaxInt32},
		{"MaxInt32+1 saturates instead of going negative", math.MaxInt32 + 1, math.MaxInt32},
		{"2^32 saturates instead of becoming zero", 1 << 32, math.MaxInt32},
		{"negative is clamped to zero", -5, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &evr.SNSEarlyQuitConfig{
				PenaltyLevels: []evr.EarlyQuitPenaltyLevelConfig{
					{PenaltyLevel: 2, MinEarlyQuits: 0, MaxEarlyQuits: 999, MMLockoutSec: tc.lockoutSec},
				},
			}

			level, lockoutSec := ResolvePenaltyLevel(1, cfg)
			if level != 2 {
				t.Errorf("ResolvePenaltyLevel level = %d, want 2", level)
			}
			if lockoutSec != tc.wantLockout {
				t.Errorf("ResolvePenaltyLevel lockout = %d, want %d (config asked for %d)",
					lockoutSec, tc.wantLockout, tc.lockoutSec)
			}
		})
	}
}

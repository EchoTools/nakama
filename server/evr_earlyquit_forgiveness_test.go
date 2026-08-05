package server

import (
	"context"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// TestCheckAndStrikeEarlyQuitIfLoggedOut_ForgivenessLiftsTheLockout pins the
// whole point of the logout-forgiveness path: a player who quit the game
// entirely (rather than abandoning one match for another) gets the quit
// forgiven, and forgiving a quit must actually release the matchmaking lockout
// it caused.
//
// CheckAndStrikeEarlyQuitIfLoggedOut called ForgiveLastQuit — whose own doc
// comment says "PenaltyLevel and PenaltyTimestamp are re-resolved by the
// caller" — and then never re-resolved them. Its sibling
// CheckAndApplyEarlyQuitIfStillOnline does call resolveAndApplyPenaltyLockout.
// So the decremented quit count was persisted while the stale
// PenaltyLevel/PenaltyTimestamp rode along untouched: the forgiven player
// stayed locked out for the full original duration, and because UpdateTier
// reads that same stale PenaltyLevel, tierChanged was always false and the
// notification block below it was unreachable.
func TestCheckAndStrikeEarlyQuitIfLoggedOut_ForgivenessLiftsTheLockout(t *testing.T) {
	logger := NewRuntimeGoLogger(loggerForTest(t))

	// Pin service settings: the notification block dereferences a *sql.DB that
	// is nil here, and it is reached only when the penalty system is enabled.
	saved := ServiceSettings()
	ServiceSettingsUpdate(&ServiceSettingsData{})
	t.Cleanup(func() { ServiceSettingsUpdate(saved) })

	for _, tc := range []struct {
		name string

		quits        int32 // quit count BEFORE forgiveness
		penaltyLevel int32 // penalty in force before forgiveness
		lockoutSec   int64 // remaining lockout before forgiveness

		wantQuits      int32
		wantLevel      int32
		wantLockoutSec int64 // 0 means "no lockout at all"
		wantTier       int32
	}{
		{
			// Default ladder: 3 quits => level 1 / 120s, 2 quits => level 0 / none.
			name:  "forgiving the quit that caused the lockout clears it",
			quits: 3, penaltyLevel: 1, lockoutSec: 120,
			wantQuits: 2, wantLevel: 0, wantLockoutSec: 0, wantTier: MatchmakingTier1,
		},
		{
			// 6 quits => level 2 / 300s, 5 quits => level 1 / 120s. Forgiveness
			// steps the lockout DOWN a rung rather than leaving level 2 in force.
			name:  "forgiving a quit steps the lockout down a rung",
			quits: 6, penaltyLevel: 2, lockoutSec: 300,
			wantQuits: 5, wantLevel: 1, wantLockoutSec: 120, wantTier: MatchmakingTier2,
		},
		{
			// Nothing to forgive: the state must be left coherent, not corrupted.
			name:  "forgiving with no quits on record leaves a clean player clean",
			quits: 0, penaltyLevel: 0, lockoutSec: 0,
			wantQuits: 0, wantLevel: 0, wantLockoutSec: 0, wantTier: MatchmakingTier1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nk := newEvrTestNakamaModule()
			ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")
			seedEarlyQuitLadder(t, ctx, nk, evr.DefaultEarlyQuitLevels().PenaltyLevels)

			userID := uuid.Must(uuid.NewV4()).String()
			sessionID := uuid.Must(uuid.NewV4()).String()

			state := NewEarlyQuitPlayerState()
			state.NumEarlyQuits = tc.quits
			state.NumSteadyEarlyQuits = tc.quits
			state.PenaltyLevel = tc.penaltyLevel
			if tc.lockoutSec > 0 {
				state.PenaltyTimestamp = time.Now().Unix() + tc.lockoutSec
			}
			state.UpdateTier(nil)
			if err := StorableWrite(ctx, nk, userID, state); err != nil {
				t.Fatalf("seed player state: %v", err)
			}

			// The player has no live session: testSessionRegistry.Range never
			// yields one, which is exactly "logged out entirely".
			before := time.Now().Unix()
			CheckAndStrikeEarlyQuitIfLoggedOut(ctx, logger, nk, nil, &testSessionRegistry{}, userID, sessionID, 0)
			after := time.Now().Unix()

			got := NewEarlyQuitPlayerState()
			if err := StorableRead(ctx, nk, userID, got, false); err != nil {
				t.Fatalf("read back player state: %v", err)
			}

			if got.NumEarlyQuits != tc.wantQuits {
				t.Errorf("NumEarlyQuits = %d, want %d", got.NumEarlyQuits, tc.wantQuits)
			}
			if got.PenaltyLevel != tc.wantLevel {
				t.Errorf("PenaltyLevel = %d, want %d: the penalty was not re-resolved after forgiveness",
					got.PenaltyLevel, tc.wantLevel)
			}
			if tc.wantLockoutSec == 0 {
				if got.PenaltyTimestamp != 0 {
					t.Errorf("PenaltyTimestamp = %d, want 0: the forgiven player is still locked out", got.PenaltyTimestamp)
				}
				if got.IsPenaltyActive() {
					t.Errorf("IsPenaltyActive() = true after forgiveness: the lockout was never lifted")
				}
			} else {
				if got.PenaltyTimestamp < before+tc.wantLockoutSec || got.PenaltyTimestamp > after+tc.wantLockoutSec {
					t.Errorf("PenaltyTimestamp = %d, want a %ds lockout from now (between %d and %d)",
						got.PenaltyTimestamp, tc.wantLockoutSec, before+tc.wantLockoutSec, after+tc.wantLockoutSec)
				}
			}
			if got.MatchmakingTier != tc.wantTier {
				t.Errorf("MatchmakingTier = %d, want %d: the tier was computed from a stale penalty level",
					got.MatchmakingTier, tc.wantTier)
			}
		})
	}
}

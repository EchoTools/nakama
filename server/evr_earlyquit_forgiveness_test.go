package server

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync/atomic"
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

// unreachableDBDriver is a database/sql driver whose connections always fail.
// It stands in for "a real *sql.DB is wired up but the query does not succeed",
// which is the only shape of database interaction this DB-free suite can drive.
// It counts Open calls so a test can prove a code path that touches the database
// was actually entered.
type unreachableDBDriver struct{ opens atomic.Int64 }

func (d *unreachableDBDriver) Open(string) (driver.Conn, error) {
	d.opens.Add(1)
	return nil, errors.New("no database is reachable in this test")
}

var unreachableDB = func() *unreachableDBDriver {
	d := &unreachableDBDriver{}
	sql.Register("evr-earlyquit-unreachable", d)
	return d
}()

// TestCheckAndStrikeEarlyQuitIfLoggedOut_TierRestoredNotificationIsReachable
// covers a path that only became live in this PR.
//
// The tier-change notification block at the end of
// CheckAndStrikeEarlyQuitIfLoggedOut is gated on `tierChanged`. Before the
// forgiveness fix, UpdateTier there was fed a PenaltyLevel that forgiveness had
// never re-resolved, so it always matched the stored tier and `tierChanged` was
// permanently false: the whole block — GetDiscordIDByUserID against the *sql.DB,
// SendEarlyQuitUpdateNotification, the Discord DM — was dead code. Re-resolving
// the penalty makes it execute in production for the first time.
//
// Newly-live code with no coverage is exactly where a nil dereference hides, so
// this pins two things: the block IS entered (the database is touched, which
// cannot happen unless tierChanged was true), and it survives the database being
// unusable rather than taking the whole goroutine down. The player's forgiven
// state must still be persisted either way — a notification failure must not
// cost the player their lockout release.
func TestCheckAndStrikeEarlyQuitIfLoggedOut_TierRestoredNotificationIsReachable(t *testing.T) {
	logger := NewRuntimeGoLogger(loggerForTest(t))

	// The block is gated on the penalty system being enabled and not silent.
	saved := ServiceSettings()
	settings := &ServiceSettingsData{}
	settings.Matchmaking.EnableEarlyQuitPenalty = true
	settings.Matchmaking.SilentEarlyQuitSystem = false
	ServiceSettingsUpdate(settings)
	t.Cleanup(func() { ServiceSettingsUpdate(saved) })

	db, err := sql.Open("evr-earlyquit-unreachable", "")
	if err != nil {
		t.Fatalf("open stub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	nk := newEvrTestNakamaModule()
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")
	seedEarlyQuitLadder(t, ctx, nk, evr.DefaultEarlyQuitLevels().PenaltyLevels)

	userID := uuid.Must(uuid.NewV4()).String()
	sessionID := uuid.Must(uuid.NewV4()).String()

	// 3 quits => level 1 => Tier 2. Forgiving one drops it to 2 quits => level 0
	// => Tier 1, which is the tier-RESTORED transition this block reports.
	state := NewEarlyQuitPlayerState()
	state.NumEarlyQuits = 3
	state.NumSteadyEarlyQuits = 3
	state.PenaltyLevel = 1
	state.PenaltyTimestamp = time.Now().Unix() + 120
	state.UpdateTier(nil)
	if state.MatchmakingTier != MatchmakingTier2 {
		t.Fatalf("precondition: seeded tier = %d, want %d", state.MatchmakingTier, MatchmakingTier2)
	}
	if err := StorableWrite(ctx, nk, userID, state); err != nil {
		t.Fatalf("seed player state: %v", err)
	}

	opensBefore := unreachableDB.opens.Load()

	// Must not panic: this is the first execution of this block, ever.
	CheckAndStrikeEarlyQuitIfLoggedOut(ctx, logger, nk, db, &testSessionRegistry{}, userID, sessionID, 0)

	if got := unreachableDB.opens.Load(); got <= opensBefore {
		t.Errorf("the database was never touched (opens %d -> %d): the tier-change notification block was not reached, so this test is not covering it",
			opensBefore, got)
	}

	got := NewEarlyQuitPlayerState()
	if err := StorableRead(ctx, nk, userID, got, false); err != nil {
		t.Fatalf("read back player state: %v", err)
	}
	if got.NumEarlyQuits != 2 {
		t.Errorf("NumEarlyQuits = %d, want 2", got.NumEarlyQuits)
	}
	if got.PenaltyLevel != 0 || got.PenaltyTimestamp != 0 {
		t.Errorf("PenaltyLevel/PenaltyTimestamp = %d/%d, want 0/0: the forgiveness was lost",
			got.PenaltyLevel, got.PenaltyTimestamp)
	}
	if got.MatchmakingTier != MatchmakingTier1 {
		t.Errorf("MatchmakingTier = %d, want %d: an unreachable database cost the player their tier restoration",
			got.MatchmakingTier, MatchmakingTier1)
	}
}

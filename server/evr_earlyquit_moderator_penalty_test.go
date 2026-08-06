package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// earlyQuitAdminNK is an evrTestNakamaModule that also answers GroupsGetId, so
// RequireEnforcerOperatorOrBot's GuildGroupLoad succeeds without a database.
// The caller is a global operator (supplied through the permissions context),
// so the guild's own enforcer list is never consulted.
type earlyQuitAdminNK struct {
	*evrTestNakamaModule
	groupID string
}

func (m *earlyQuitAdminNK) GroupsGetId(ctx context.Context, groupIDs []string) ([]*api.Group, error) {
	for _, id := range groupIDs {
		if id == m.groupID {
			return []*api.Group{{Id: m.groupID, Name: "test-guild", Metadata: "{}"}}, nil
		}
	}
	return nil, nil
}

// newEarlyQuitAdminHarness wires up the in-memory module, an operator context
// and the global service settings the admin RPC path reads. It returns the
// module, the context, the caller and target user IDs and the group ID.
func newEarlyQuitAdminHarness(t *testing.T) (*earlyQuitAdminNK, context.Context, string, string) {
	t.Helper()

	groupID := uuid.Must(uuid.NewV4()).String()
	nk := &earlyQuitAdminNK{evrTestNakamaModule: newEvrTestNakamaModule(), groupID: groupID}

	// GuildGroupLoad reads the guild state row owned by the Discord bot user;
	// StorableRead rejects an empty owner ID, so the settings must name one.
	saved := ServiceSettings()
	settings := &ServiceSettingsData{}
	settings.DiscordBotUserID = uuid.Must(uuid.NewV4()).String()
	ServiceSettingsUpdate(settings)
	t.Cleanup(func() { ServiceSettingsUpdate(saved) })

	callerID := uuid.Must(uuid.NewV4()).String()
	targetID := uuid.Must(uuid.NewV4()).String()

	ctx := context.Background()
	ctx = context.WithValue(ctx, runtime.RUNTIME_CTX_NODE, "test-node")
	ctx = context.WithValue(ctx, runtime.RUNTIME_CTX_USER_ID, callerID)
	ctx = WithUserPermissions(ctx, &UserPermissions{IsGlobalOperator: true})

	return nk, ctx, targetID, groupID
}

// seedEarlyQuitLadder stores a custom system-wide penalty ladder.
func seedEarlyQuitLadder(t *testing.T, ctx context.Context, nk runtime.NakamaModule, levels []evr.EarlyQuitPenaltyLevelConfig) {
	t.Helper()
	cfg := &evr.SNSEarlyQuitConfig{
		PenaltyLevels:      levels,
		SteadyPlayerLevels: evr.DefaultEarlyQuitLevels().SteadyPlayerLevels,
	}
	value, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal ladder: %v", err)
	}
	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      evr.StorageCollectionEarlyQuitConfig,
		Key:             evr.StorageKeyEarlyQuitConfig,
		UserID:          SystemUserID,
		Value:           string(value),
		PermissionRead:  int(runtime.STORAGE_PERMISSION_NO_READ),
		PermissionWrite: int(runtime.STORAGE_PERMISSION_NO_WRITE),
	}}); err != nil {
		t.Fatalf("seed ladder: %v", err)
	}
}

// TestEarlyQuitModifyRPC_SetPenaltyDoesNotBorrowAnotherLevelsLockout pins the
// lockout the set_penalty action applies.
//
// The handler seeded lockoutSec from ResolvePenaltyLevel(state.NumEarlyQuits) —
// the lockout for the level the player's CURRENT quit count maps to — discarded
// it with `_ = lockoutSec`, and then overwrote it only if the requested level
// happened to appear in the ladder. When the requested level is absent from the
// ladder the seed survived, so a moderator asking for level 2 silently applied
// a DIFFERENT level's lockout (here level 3's 900s) to the player.
//
// Correct behaviour: the lockout must come from the level being set. When the
// ladder does not configure that level, fall back to the built-in duration for
// it (GetLockoutDuration) — never to whatever the quit count happened to map to.
func TestEarlyQuitModifyRPC_SetPenaltyDoesNotBorrowAnotherLevelsLockout(t *testing.T) {
	nk, ctx, targetID, groupID := newEarlyQuitAdminHarness(t)

	// Ladder with NO level 2 entry, and a distinctive level 3 lockout.
	seedEarlyQuitLadder(t, ctx, nk, []evr.EarlyQuitPenaltyLevelConfig{
		{PenaltyLevel: 0, MinEarlyQuits: 0, MaxEarlyQuits: 2, MMLockoutSec: 0},
		{PenaltyLevel: 1, MinEarlyQuits: 3, MaxEarlyQuits: 10, MMLockoutSec: 777},
		{PenaltyLevel: 3, MinEarlyQuits: 11, MaxEarlyQuits: 999, MMLockoutSec: 900},
	})

	// The player's quit count puts them in the level 3 band.
	state := NewEarlyQuitPlayerState()
	state.NumEarlyQuits = 20
	if err := StorableWrite(ctx, nk, targetID, state); err != nil {
		t.Fatalf("seed player state: %v", err)
	}

	for _, tc := range []struct {
		name        string
		level       int32
		wantLockout int64 // expected seconds of lockout; 0 means "no lockout"
	}{
		{"level absent from ladder uses its own built-in duration, not level 3's 900s", 2, int64(GetLockoutDuration(2).Seconds())},
		{"level present in ladder uses the ladder's duration for THAT level", 1, 777},
		{"level 0 clears the lockout", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			level := tc.level
			payload, err := json.Marshal(EarlyQuitModifyRequest{
				GroupID:      groupID,
				TargetUserID: targetID,
				Action:       "set_penalty",
				PenaltyLevel: &level,
			})
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}

			before := time.Now().Unix()
			out, err := EarlyQuitModifyRPC(ctx, NewRuntimeGoLogger(loggerForTest(t)), nil, nk, string(payload))
			if err != nil {
				t.Fatalf("EarlyQuitModifyRPC: %v", err)
			}
			after := time.Now().Unix()

			var resp EarlyQuitModifyResponse
			if err := json.Unmarshal([]byte(out), &resp); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			if resp.State.PenaltyLevel != tc.level {
				t.Errorf("PenaltyLevel = %d, want %d", resp.State.PenaltyLevel, tc.level)
			}

			if tc.wantLockout == 0 {
				if resp.State.PenaltyTimestamp != 0 {
					t.Errorf("PenaltyTimestamp = %d, want 0 (no lockout for level %d)", resp.State.PenaltyTimestamp, tc.level)
				}
				return
			}

			gotLockout := resp.State.PenaltyTimestamp - before
			if gotLockout < tc.wantLockout || resp.State.PenaltyTimestamp-after > tc.wantLockout {
				t.Errorf("lockout for level %d = %ds (penalty_ts %d, set between %d and %d), want %ds",
					tc.level, gotLockout, resp.State.PenaltyTimestamp, before, after, tc.wantLockout)
			}
		})
	}
}

// TestEarlyQuit_ModeratorSanctionSurvivesASubsequentQuit is the headline case
// for the sanction-wipe bug.
//
// resolveAndApplyPenaltyLockout overwrote PenaltyLevel/PenaltyTimestamp from the
// quit count unconditionally, zeroing BOTH whenever the resolved level carries
// no lockout. So a moderator sanction applied to a player with a clean record
// was erased by that player's very next early quit: one quit maps to ladder
// level 0, and level 0 takes the CLEAR branch.
//
// Correct behaviour: a moderator-applied sanction that has not yet expired is
// not lowered or erased by ladder resolution.
func TestEarlyQuit_ModeratorSanctionSurvivesASubsequentQuit(t *testing.T) {
	nk, ctx, targetID, groupID := newEarlyQuitAdminHarness(t)
	logger := NewRuntimeGoLogger(loggerForTest(t))

	// Default ladder: 0-2 quits => level 0 / no lockout; level 3 => 900s.
	seedEarlyQuitLadder(t, ctx, nk, evr.DefaultEarlyQuitLevels().PenaltyLevels)

	// A player with a completely clean record.
	state := NewEarlyQuitPlayerState()
	if err := StorableWrite(ctx, nk, targetID, state); err != nil {
		t.Fatalf("seed player state: %v", err)
	}

	// --- a moderator applies a level 3 sanction ---
	level := int32(3)
	payload, err := json.Marshal(EarlyQuitModifyRequest{
		GroupID:      groupID,
		TargetUserID: targetID,
		Action:       "set_penalty",
		PenaltyLevel: &level,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if _, err := EarlyQuitModifyRPC(ctx, logger, nil, nk, string(payload)); err != nil {
		t.Fatalf("EarlyQuitModifyRPC: %v", err)
	}

	sanctioned := NewEarlyQuitPlayerState()
	if err := StorableRead(ctx, nk, targetID, sanctioned, false); err != nil {
		t.Fatalf("read sanctioned state: %v", err)
	}
	if sanctioned.PenaltyLevel != 3 || !sanctioned.IsPenaltyActive() {
		t.Fatalf("precondition: expected an active level 3 sanction, got level %d ts %d",
			sanctioned.PenaltyLevel, sanctioned.PenaltyTimestamp)
	}
	sanctionTS := sanctioned.PenaltyTimestamp

	// --- the player then quits one match ---
	sanctioned.IncrementEarlyQuit()
	resolveAndApplyPenaltyLockout(ctx, nk, logger, sanctioned)

	if sanctioned.PenaltyLevel != 3 {
		t.Errorf("PenaltyLevel = %d after one early quit, want 3: an early quit must not erase a moderator sanction",
			sanctioned.PenaltyLevel)
	}
	if sanctioned.PenaltyTimestamp != sanctionTS {
		t.Errorf("PenaltyTimestamp = %d after one early quit, want %d (unchanged): an early quit must not shorten a moderator sanction",
			sanctioned.PenaltyTimestamp, sanctionTS)
	}
	if !sanctioned.IsPenaltyActive() {
		t.Errorf("IsPenaltyActive() = false after one early quit: the moderator sanction was erased")
	}
}

// TestResolveAndApplyPenaltyLockout_ModeratorSanctionBoundaries covers the
// edges around the sanction floor: it must hold both dimensions while it is in
// force, must not become permanent immunity, must yield to a harsher ladder
// result, and must not stop an ordinary (ladder-set) penalty from being lifted.
//
// Only the first subtest can fail from deleting the floor; the other three are
// over-correction guards, and they pass with the feature removed entirely. Read
// them as "the fix did not break the ordinary case", not as coverage of the fix.
func TestResolveAndApplyPenaltyLockout_ModeratorSanctionBoundaries(t *testing.T) {
	logger := NewRuntimeGoLogger(loggerForTest(t))

	// sanction applies a moderator penalty through the admin RPC and returns
	// the resulting stored state.
	sanction := func(t *testing.T, ctx context.Context, nk runtime.NakamaModule, groupID, targetID string, level int32) *EarlyQuitPlayerState {
		t.Helper()
		payload, err := json.Marshal(EarlyQuitModifyRequest{
			GroupID:      groupID,
			TargetUserID: targetID,
			Action:       "set_penalty",
			PenaltyLevel: &level,
		})
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		if _, err := EarlyQuitModifyRPC(ctx, logger, nil, nk, string(payload)); err != nil {
			t.Fatalf("EarlyQuitModifyRPC: %v", err)
		}
		state := NewEarlyQuitPlayerState()
		if err := StorableRead(ctx, nk, targetID, state, false); err != nil {
			t.Fatalf("read sanctioned state: %v", err)
		}
		return state
	}

	t.Run("an unexpired sanction floors both the level and the expiry", func(t *testing.T) {
		nk, ctx, targetID, groupID := newEarlyQuitAdminHarness(t)
		seedEarlyQuitLadder(t, ctx, nk, evr.DefaultEarlyQuitLevels().PenaltyLevels)

		state := sanction(t, ctx, nk, groupID, targetID, 3)
		// A week-long sanction, the shape the Discord handler's
		// duration-minutes option exists to produce.
		weekOut := time.Now().Unix() + 7*24*60*60
		state.PenaltyTimestamp = weekOut
		state.ModeratorPenaltyUntil = weekOut
		// The ladder on its own would say "no penalty at all" for this player.
		state.NumEarlyQuits = 1

		resolveAndApplyPenaltyLockout(ctx, nk, logger, state)

		if state.PenaltyLevel != 3 {
			t.Errorf("PenaltyLevel = %d, want 3: ladder resolution lowered a level the moderator set", state.PenaltyLevel)
		}
		if state.PenaltyTimestamp != weekOut {
			t.Errorf("PenaltyTimestamp = %d, want %d (delta %ds): ladder resolution shortened a moderator sanction",
				state.PenaltyTimestamp, weekOut, weekOut-state.PenaltyTimestamp)
		}
		if state.ModeratorPenaltyLevel != 3 || state.ModeratorPenaltyUntil != weekOut {
			t.Errorf("retained sanction = level %d until %d, want 3 until %d: an unexpired sanction must survive resolution",
				state.ModeratorPenaltyLevel, state.ModeratorPenaltyUntil, weekOut)
		}
	})

	t.Run("expired sanction stops blocking the ladder", func(t *testing.T) {
		nk, ctx, targetID, groupID := newEarlyQuitAdminHarness(t)
		seedEarlyQuitLadder(t, ctx, nk, evr.DefaultEarlyQuitLevels().PenaltyLevels)

		state := sanction(t, ctx, nk, groupID, targetID, 3)
		// The sanction just lapsed.
		state.PenaltyTimestamp = time.Now().Unix() - 1
		state.ModeratorPenaltyUntil = state.PenaltyTimestamp
		state.NumEarlyQuits = 1 // ladder level 0

		resolveAndApplyPenaltyLockout(ctx, nk, logger, state)

		if state.PenaltyLevel != 0 || state.PenaltyTimestamp != 0 {
			t.Errorf("after the sanction expired, ladder resolution must apply: got level %d ts %d, want 0/0",
				state.PenaltyLevel, state.PenaltyTimestamp)
		}
		if state.ModeratorPenaltyLevel != 0 || state.ModeratorPenaltyUntil != 0 {
			t.Errorf("a lapsed sanction must be dropped, not retained: got level %d until %d",
				state.ModeratorPenaltyLevel, state.ModeratorPenaltyUntil)
		}
	})

	t.Run("a harsher ladder result overrides a lighter sanction", func(t *testing.T) {
		nk, ctx, targetID, groupID := newEarlyQuitAdminHarness(t)
		seedEarlyQuitLadder(t, ctx, nk, evr.DefaultEarlyQuitLevels().PenaltyLevels)

		state := sanction(t, ctx, nk, groupID, targetID, 1)
		state.NumEarlyQuits = 20 // ladder level 3 / 900s

		resolveAndApplyPenaltyLockout(ctx, nk, logger, state)

		if state.PenaltyLevel != 3 {
			t.Errorf("PenaltyLevel = %d, want 3: a harsher ladder result must override a lighter sanction", state.PenaltyLevel)
		}
	})

	t.Run("an ordinary ladder penalty is still lifted when the count drops", func(t *testing.T) {
		nk, ctx, _, _ := newEarlyQuitAdminHarness(t)
		seedEarlyQuitLadder(t, ctx, nk, evr.DefaultEarlyQuitLevels().PenaltyLevels)

		state := NewEarlyQuitPlayerState()
		state.NumEarlyQuits = 3
		resolveAndApplyPenaltyLockout(ctx, nk, logger, state)
		if state.PenaltyLevel != 1 || !state.IsPenaltyActive() {
			t.Fatalf("precondition: expected an active level 1 ladder penalty, got level %d ts %d",
				state.PenaltyLevel, state.PenaltyTimestamp)
		}

		// The quit is forgiven; the ladder now resolves to level 0.
		state.ForgiveLastQuit()
		resolveAndApplyPenaltyLockout(ctx, nk, logger, state)

		if state.PenaltyLevel != 0 || state.PenaltyTimestamp != 0 {
			t.Errorf("ladder penalty was not lifted: got level %d ts %d, want 0/0", state.PenaltyLevel, state.PenaltyTimestamp)
		}
	})
}

// TestEarlyQuit_QuittingMoreCannotShortenAModeratorSanction is the second half
// of the sanction-wipe bug: the guard that stopped a quit ERASING a sanction
// compared levels only, so a quit could still SHORTEN one.
//
// The Discord /early-quit handler takes an explicit duration-minutes
// (evr_discord_appbot.go, "duration-minutes" option), so a moderator can hand
// out a long, deliberately light sanction — level 1 for 24 hours. The default
// ladder's harshest rung is level 3 for 900 seconds. A player who then quits
// their way to 11 quits resolves to level 3, and because level 3 > level 1 the
// guard stood aside and the ladder wrote its own 900s expiry over the
// moderator's — the player served 15 minutes instead of 24 hours. Quitting MORE
// shortened the punishment.
//
// Correct behaviour: an unexpired sanction is a floor in BOTH dimensions. The
// ladder may raise the level and it may extend the expiry; it may do neither in
// reverse.
func TestEarlyQuit_QuittingMoreCannotShortenAModeratorSanction(t *testing.T) {
	nk, ctx, _, _ := newEarlyQuitAdminHarness(t)
	logger := NewRuntimeGoLogger(loggerForTest(t))
	seedEarlyQuitLadder(t, ctx, nk, evr.DefaultEarlyQuitLevels().PenaltyLevels)

	// A moderator sanctions level 1 for 24 hours.
	state := NewEarlyQuitPlayerState()
	state.ApplyModeratorPenalty(1, 24*60*60)
	sanctionExpiry := state.PenaltyTimestamp

	// The player racks up quits until the ladder resolves to level 3 / 900s.
	state.NumEarlyQuits = 11
	resolveAndApplyPenaltyLockout(ctx, nk, logger, state)

	if state.PenaltyLevel != 3 {
		t.Errorf("PenaltyLevel = %d, want 3: the ladder must still be able to RAISE the level", state.PenaltyLevel)
	}
	if state.PenaltyTimestamp < sanctionExpiry {
		t.Errorf("PenaltyTimestamp = %d, want >= %d: quitting more shortened the moderator's sanction by %ds",
			state.PenaltyTimestamp, sanctionExpiry, sanctionExpiry-state.PenaltyTimestamp)
	}

	// ...and the sanction keeps flooring the level for its full duration, even
	// after the quits that raised it are forgiven.
	state.NumEarlyQuits = 0
	resolveAndApplyPenaltyLockout(ctx, nk, logger, state)
	if state.PenaltyLevel != 1 {
		t.Errorf("PenaltyLevel = %d after the quits were forgiven, want 1 (the sanctioned level): "+
			"the ladder's transient level 3 ratcheted into the sanction", state.PenaltyLevel)
	}
	if state.PenaltyTimestamp != sanctionExpiry {
		t.Errorf("PenaltyTimestamp = %d after the quits were forgiven, want %d (the sanction's own expiry)",
			state.PenaltyTimestamp, sanctionExpiry)
	}
}

// TestEarlyQuit_ResetLiftsAModeratorSanction pins that the admin reset action
// drops the retained sanction. Without that, the sanction would keep flooring
// ladder resolution for the rest of its original lockout even though the
// operator just wiped the player's record.
func TestEarlyQuit_ResetLiftsAModeratorSanction(t *testing.T) {
	nk, ctx, targetID, groupID := newEarlyQuitAdminHarness(t)
	logger := NewRuntimeGoLogger(loggerForTest(t))
	seedEarlyQuitLadder(t, ctx, nk, evr.DefaultEarlyQuitLevels().PenaltyLevels)

	state := NewEarlyQuitPlayerState()
	state.ApplyModeratorPenalty(3, 24*60*60)
	state.NumEarlyQuits = 20
	if err := StorableWrite(ctx, nk, targetID, state); err != nil {
		t.Fatalf("seed player state: %v", err)
	}

	payload, err := json.Marshal(EarlyQuitModifyRequest{
		GroupID:      groupID,
		TargetUserID: targetID,
		Action:       "reset",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if _, err := EarlyQuitModifyRPC(ctx, logger, nil, nk, string(payload)); err != nil {
		t.Fatalf("EarlyQuitModifyRPC: %v", err)
	}

	after := NewEarlyQuitPlayerState()
	if err := StorableRead(ctx, nk, targetID, after, false); err != nil {
		t.Fatalf("read reset state: %v", err)
	}
	if after.ModeratorPenaltyLevel != 0 || after.ModeratorPenaltyUntil != 0 {
		t.Errorf("reset left a retained sanction: level %d until %d", after.ModeratorPenaltyLevel, after.ModeratorPenaltyUntil)
	}

	// A subsequent quit must resolve purely from the ladder again.
	after.NumEarlyQuits = 1
	resolveAndApplyPenaltyLockout(ctx, nk, logger, after)
	if after.PenaltyLevel != 0 || after.PenaltyTimestamp != 0 {
		t.Errorf("after a reset the ladder must govern: got level %d ts %d, want 0/0",
			after.PenaltyLevel, after.PenaltyTimestamp)
	}
}

// TestEarlyQuit_ASanctionWithNoLockoutLeavesNoPhantomLevel pins the one
// invariant every reader of PenaltyLevel depends on: a non-zero PenaltyLevel
// always carries a non-zero PenaltyTimestamp.
//
// applyLadderPenalty already states the rule — "A level carrying no lockout is
// no penalty at all" (evr_earlyquit.go), and it zeroes the level to enforce it.
// ApplyModeratorPenalty assigned s.PenaltyLevel = level BEFORE branching, so the
// no-lockout branch cleared the timestamp and the retained sanction but left the
// level standing. Same ladder, same requested level, two different stored
// states depending on which path last touched the record.
//
// The phantom level is not inert. Both match-completion paths
// (evr_match.go MatchOver, EventRemoteLogSet.incrementCompletedMatches) read the
// stored state and call UpdateTier WITHOUT re-resolving the level first, so the
// phantom drives the player into the Tier 2 queue and fires the "Account
// flagged for early quitting" DM — for a player IsPenaltyActive() says is not
// penalized. Nothing on the completion path ever recomputes PenaltyLevel, so
// "Complete full matches to restore Tier 1 status" cannot come true; only a
// later early quit (which does re-resolve) clears it.
func TestEarlyQuit_ASanctionWithNoLockoutLeavesNoPhantomLevel(t *testing.T) {
	t.Run("the level is cleared with the lockout", func(t *testing.T) {
		state := NewEarlyQuitPlayerState()
		state.ApplyModeratorPenalty(2, 0)

		if state.PenaltyLevel != 0 || state.PenaltyTimestamp != 0 {
			t.Errorf("ApplyModeratorPenalty(2, 0) = level %d ts %d, want 0/0: a level with no lockout is no penalty at all",
				state.PenaltyLevel, state.PenaltyTimestamp)
		}
		if state.PenaltyLevel > 0 && !state.IsPenaltyActive() {
			t.Errorf("PenaltyLevel = %d while IsPenaltyActive() = false: readers that consult the level alone "+
				"(UpdateTier, the client profile's EarlyQuitFeatures.PenaltyLevel) now disagree with the lockout",
				state.PenaltyLevel)
		}
	})

	// The reachable path: a ladder that configures a level with no matchmaking
	// lockout (a coherent config — the level still carries SpawnLock/AutoReport),
	// and a moderator setting that level through earlyquit/modify. The RPC reads
	// the lockout straight off that ladder entry and hands 0 to
	// ApplyModeratorPenalty.
	t.Run("a moderator setting a lockout-less ladder level does not queue-degrade the player", func(t *testing.T) {
		nk, ctx, targetID, groupID := newEarlyQuitAdminHarness(t)
		seedEarlyQuitLadder(t, ctx, nk, []evr.EarlyQuitPenaltyLevelConfig{
			{PenaltyLevel: 0, MinEarlyQuits: 0, MaxEarlyQuits: 2, MMLockoutSec: 0},
			// Level 2 punishes with a spawn lock and an auto-report, but no
			// matchmaking lockout. validatePenaltyLevels bounds MMLockoutSec
			// only from below, and the admin RPC's loader skips Validate
			// entirely, so this ladder reaches the handler as written.
			{PenaltyLevel: 2, MinEarlyQuits: 3, MaxEarlyQuits: 999, MMLockoutSec: 0, SpawnLock: 1, AutoReport: 1},
		})

		state := NewEarlyQuitPlayerState()
		if err := StorableWrite(ctx, nk, targetID, state); err != nil {
			t.Fatalf("seed player state: %v", err)
		}

		level := int32(2)
		payload, err := json.Marshal(EarlyQuitModifyRequest{
			GroupID:      groupID,
			TargetUserID: targetID,
			Action:       "set_penalty",
			PenaltyLevel: &level,
		})
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		if _, err := EarlyQuitModifyRPC(ctx, NewRuntimeGoLogger(loggerForTest(t)), nil, nk, string(payload)); err != nil {
			t.Fatalf("EarlyQuitModifyRPC: %v", err)
		}

		// Read it back exactly as the completion paths do.
		stored := NewEarlyQuitPlayerState()
		if err := StorableRead(ctx, nk, targetID, stored, false); err != nil {
			t.Fatalf("read stored state: %v", err)
		}
		if stored.IsPenaltyActive() {
			t.Fatalf("precondition: the stored penalty must be inactive (ts %d)", stored.PenaltyTimestamp)
		}
		if stored.PenaltyLevel != 0 {
			t.Errorf("stored PenaltyLevel = %d with PenaltyTimestamp = %d: a phantom level no lockout backs",
				stored.PenaltyLevel, stored.PenaltyTimestamp)
		}

		// evr_match.go's MatchOver handler: read, credit the completion, then
		// UpdateTier — with no re-resolution of the level in between.
		threshold := int32(0)
		stored.IncrementCompletedMatches()
		oldTier, newTier, changed := stored.UpdateTier(&threshold)
		if newTier != MatchmakingTier1 {
			t.Errorf("completing a match moved the player from tier %d to tier %d (changed=%v) "+
				"while IsPenaltyActive() = false: an unpenalized player was degraded to the Tier 2 queue",
				oldTier, newTier, changed)
		}
	})
}

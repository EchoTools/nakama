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
// edges around the sanction guard: it must not become permanent immunity, must
// yield to a harsher ladder result, and must not stop an ordinary (ladder-set)
// penalty from being lifted.
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

	t.Run("expired sanction stops blocking the ladder", func(t *testing.T) {
		nk, ctx, targetID, groupID := newEarlyQuitAdminHarness(t)
		seedEarlyQuitLadder(t, ctx, nk, evr.DefaultEarlyQuitLevels().PenaltyLevels)

		state := sanction(t, ctx, nk, groupID, targetID, 3)
		state.PenaltyTimestamp = time.Now().Unix() - 1 // sanction just lapsed
		state.NumEarlyQuits = 1                        // ladder level 0

		resolveAndApplyPenaltyLockout(ctx, nk, logger, state)

		if state.PenaltyLevel != 0 || state.PenaltyTimestamp != 0 {
			t.Errorf("after the sanction expired, ladder resolution must apply: got level %d ts %d, want 0/0",
				state.PenaltyLevel, state.PenaltyTimestamp)
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

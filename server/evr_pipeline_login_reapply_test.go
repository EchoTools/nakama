package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/stretchr/testify/require"
)

// These tests cover loginProfileReapply, the closure evrProfileUpdateWithRetry
// runs when login's profile write loses a version race and has to be retried
// against a freshly read profile.
//
// The whole reason EVRProfileUpdate has no internal retry is that a blind retry
// re-submits the caller's stale payload and discards whatever the concurrent
// writer committed. A reapply closure that replays this session's pre-conflict
// snapshot reintroduces exactly that bug through the back door, so each rule it
// applies has to be re-evaluated against the fresh profile. That is what these
// tests pin.

func newReapplyTestProfile(t *testing.T) *EVRProfile {
	t.Helper()
	return &EVRProfile{
		account:     &api.Account{User: &api.User{Username: "tester"}, Wallet: "{}"},
		InGameNames: make(map[string]GroupInGameName),
	}
}

// TestLoginProfileReapply_DoesNotRevertConcurrentActiveGroupChange is finding 4.
//
// metadataUpdated can be true purely because FixBrokenCosmetics repaired a
// cosmetic, while the player's active group was never touched by this login. If
// the retry re-asserts the active group anyway, and the writer we are retrying
// around is the one that changed it, login silently reverts the player's guild.
func TestLoginProfileReapply_DoesNotRevertConcurrentActiveGroupChange(t *testing.T) {
	const (
		loginSawGroup   = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		concurrentGroup = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	)

	// This session did not choose a new active group.
	r := loginProfileReapply{
		setActiveGroupID: false,
		activeGroupID:    uuid.FromStringOrNil(loginSawGroup),
	}

	// The freshly read profile carries the concurrent writer's deliberate change.
	fresh := newReapplyTestProfile(t)
	fresh.ActiveGroupID = concurrentGroup

	require.NoError(t, r.apply(fresh))
	require.Equal(t, concurrentGroup, fresh.ActiveGroupID,
		"a retry must not revert an active group the concurrent writer just set")
}

// TestLoginProfileReapply_PersistsDeliberateActiveGroupChange is the control for
// the test above: when this login DID assign a group (a brand-new player with no
// stored group), the retry must still persist it.
func TestLoginProfileReapply_PersistsDeliberateActiveGroupChange(t *testing.T) {
	const assignedGroup = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"

	r := loginProfileReapply{
		setActiveGroupID: true,
		activeGroupID:    uuid.FromStringOrNil(assignedGroup),
	}

	fresh := newReapplyTestProfile(t)
	fresh.ActiveGroupID = ""

	require.NoError(t, r.apply(fresh))
	require.Equal(t, assignedGroup, fresh.ActiveGroupID,
		"a group this login deliberately assigned must survive the retry")
}

// TestLoginProfileReapply_ReassertsUsernameOnlyDisplayName is half of finding 2.
//
// A guild with the UsernameOnly role forces the player's display name to their
// username. That is enforcement, not preference: if the retry adopts the fresh
// profile without re-asserting it, the session runs the whole way through with a
// display name the guild does not permit.
func TestLoginProfileReapply_ReassertsUsernameOnlyDisplayName(t *testing.T) {
	const usernameOnlyGroup = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"

	r := loginProfileReapply{
		username:             "tester",
		usernameOnlyGroupIDs: []string{usernameOnlyGroup},
	}

	// Storage still holds the name the player had before login forced the username.
	fresh := newReapplyTestProfile(t)
	fresh.SetGroupDisplayName(usernameOnlyGroup, "SomeOtherName")

	require.NoError(t, r.apply(fresh))

	dn, found := fresh.GetGroupDisplayName(usernameOnlyGroup)
	require.True(t, found)
	require.Equal(t, "tester", dn,
		"the UsernameOnly role must be re-enforced against the freshly read profile")
}

// TestLoginProfileReapply_PrunesDisplayNameOwnedByAnotherAccount is the other half
// of finding 2.
//
// Login prunes any in-game name that DisplayNameOwnerSearch says belongs to a
// different account. Storage still holds that name, so a retry that simply adopts
// the fresh profile puts the duplicate name straight back — the exact condition
// the prune exists to prevent.
func TestLoginProfileReapply_PrunesDisplayNameOwnedByAnotherAccount(t *testing.T) {
	const guild = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"

	r := loginProfileReapply{
		// Stored lowercased, matching how initializeSession builds the set.
		displayNamesOwnedByOthers: map[string]struct{}{"takenname": {}},
	}

	fresh := newReapplyTestProfile(t)
	fresh.SetGroupDisplayName(guild, "TakenName")

	require.NoError(t, r.apply(fresh))

	_, found := fresh.GetGroupDisplayName(guild)
	require.False(t, found,
		"a name owned by another account must be pruned again after the re-read")
}

// TestLoginProfileReapply_KeepsUnrelatedConcurrentRename is the guard against
// over-correcting findings 2 and 4.
//
// The fix must re-assert enforcement rules WITHOUT writing this session's whole
// in-game name map over the fresh read. GuildPlayerRenameRPC writes that same
// map, so a wholesale overwrite would clobber a rename that landed mid-write.
func TestLoginProfileReapply_KeepsUnrelatedConcurrentRename(t *testing.T) {
	const (
		usernameOnlyGuild = "ffffffff-ffff-4fff-8fff-ffffffffffff"
		renamedGuild      = "11111111-2222-4333-8444-555555555555"
	)

	r := loginProfileReapply{
		username:                  "tester",
		usernameOnlyGroupIDs:      []string{usernameOnlyGuild},
		displayNamesOwnedByOthers: map[string]struct{}{"takenname": {}},
	}

	fresh := newReapplyTestProfile(t)
	fresh.SetGroupDisplayName(usernameOnlyGuild, "Whatever")
	// A rename committed by the concurrent writer we are retrying around.
	fresh.SetGroupDisplayName(renamedGuild, "FreshRename")

	require.NoError(t, r.apply(fresh))

	dn, found := fresh.GetGroupDisplayName(renamedGuild)
	require.True(t, found, "the concurrent rename must not be deleted")
	require.Equal(t, "FreshRename", dn,
		"reapply must not overwrite in-game names it has no enforcement rule for")
}

// TestLoginProfileReapply_IsIdempotent pins the contract
// evrProfileUpdateWithRetry documents: apply may run more than once, so it must
// be free of order-dependent side effects.
func TestLoginProfileReapply_IsIdempotent(t *testing.T) {
	const (
		guild             = "66666666-7777-4888-8999-aaaaaaaaaaaa"
		usernameOnlyGuild = "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff"
	)

	r := loginProfileReapply{
		setActiveGroupID:          true,
		activeGroupID:             uuid.FromStringOrNil(guild),
		fixBrokenCosmetics:        true,
		username:                  "tester",
		usernameOnlyGroupIDs:      []string{usernameOnlyGuild},
		displayNamesOwnedByOthers: map[string]struct{}{"takenname": {}},
	}

	fresh := newReapplyTestProfile(t)
	fresh.SetGroupDisplayName(usernameOnlyGuild, "Whatever")
	fresh.SetGroupDisplayName(guild, "TakenName")

	require.NoError(t, r.apply(fresh))
	first := fresh.DisplayNamesByGroupID()
	firstGroup := fresh.ActiveGroupID
	firstLoadout := fresh.LoadoutCosmetics.Loadout

	require.NoError(t, r.apply(fresh))
	require.Equal(t, first, fresh.DisplayNamesByGroupID())
	require.Equal(t, firstGroup, fresh.ActiveGroupID)
	require.Equal(t, firstLoadout, fresh.LoadoutCosmetics.Loadout)
}

// TestInitializeSession_NotificationGoroutineDoesNotReadParamsProfile guards the
// data race this PR fixes.
//
// initializeSession spawns a goroutine to send the "display name in use" Discord
// notification, and — since the retry work landed — also reassigns the
// params.profile FIELD when the profile write succeeds. A goroutine that reads
// params.profile therefore races that assignment. The window is real: the
// goroutine performs a Discord API call while the main path continues through
// several storage round-trips before reaching the write.
//
// No unit test can drive initializeSession (it needs a full session and pipeline),
// and -race cannot catch what it never executes, so this asserts the property
// structurally: nothing spawned by initializeSession may read params.profile.
// Snapshot the values into locals before the go statement instead.
func TestInitializeSession_NotificationGoroutineDoesNotReadParamsProfile(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "evr_pipeline_login.go", nil, 0)
	require.NoError(t, err)

	var fn *ast.FuncDecl
	ast.Inspect(file, func(n ast.Node) bool {
		if d, ok := n.(*ast.FuncDecl); ok && d.Name.Name == "initializeSession" {
			fn = d
			return false
		}
		return true
	})
	require.NotNil(t, fn, "initializeSession not found; this guard test is stale")

	goStmts := 0
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		gs, ok := n.(*ast.GoStmt)
		if !ok {
			return true
		}
		goStmts++
		ast.Inspect(gs, func(m ast.Node) bool {
			sel, ok := m.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if ident.Name == "params" && sel.Sel.Name == "profile" {
				t.Errorf("goroutine at %s reads params.profile, which initializeSession "+
					"reassigns after a storage retry — that is a data race. Snapshot the "+
					"values into locals before the go statement.", fset.Position(m.Pos()))
			}
			return true
		})
		return true
	})

	require.Positive(t, goStmts,
		"expected at least one go statement in initializeSession; this guard test is stale")
}

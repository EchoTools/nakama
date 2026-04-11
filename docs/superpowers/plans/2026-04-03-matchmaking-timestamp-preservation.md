# Matchmaking Timestamp Preservation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Preserve matchmaking queue position across re-queues so players don't restart at the back of the line after crashes or cancels.

**Architecture:** A process-global `sync.Map` stores per-user matchmaking credits (mode + original timestamp + expiry). On re-queue, the original timestamp is reused instead of `time.Now()`. Credits are cleared when a player successfully joins a match.

**Tech Stack:** Go, `sync.Map`, existing `evr.Symbol` types, existing `LobbySessionParameters` struct.

**Spec:** `docs/superpowers/specs/2026-04-02-matchmaking-timestamp-preservation-design.md`

---

## File Structure

| File                                    | Action | Responsibility                                                      |
| --------------------------------------- | ------ | ------------------------------------------------------------------- |
| `server/evr_matchmaking_credit.go`      | Create | `matchmakingCredit` type, `sync.Map`, `get`/`set`/`clear` functions |
| `server/evr_matchmaking_credit_test.go` | Create | All unit tests for credit operations                                |
| `server/evr_lobby_parameters.go`        | Modify | Wire credit lookup/creation into `NewLobbyParametersFromRequest`    |
| `server/evr_lobby_joinentrant.go`       | Modify | Wire credit clear into `LobbyJoinEntrants`                          |

---

### Task 1: Write failing tests for matchmaking credit operations

**Files:**

- Create: `server/evr_matchmaking_credit_test.go`

- [ ] **Step 1: Create the test file with all unit tests**

```go
package server

import (
	"sync"
	"testing"
	"time"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

func TestMatchmakingCredit_StoreAndRetrieve(t *testing.T) {
	matchmakingCredits = sync.Map{}
	t.Cleanup(func() { matchmakingCredits = sync.Map{} })

	ts := time.Now().UTC().Add(-30 * time.Second)
	setMatchmakingCredit("user-1", &matchmakingCredit{
		Mode:      evr.ModeArenaPublic,
		Timestamp: ts,
		Expiry:    time.Now().UTC().Add(8 * time.Minute),
	})

	got := getMatchmakingCredit("user-1", evr.ModeArenaPublic)
	if got == nil {
		t.Fatal("expected credit, got nil")
	}
	if !got.Timestamp.Equal(ts) {
		t.Errorf("expected timestamp %v, got %v", ts, got.Timestamp)
	}
}

func TestMatchmakingCredit_ExpiredCredit(t *testing.T) {
	matchmakingCredits = sync.Map{}
	t.Cleanup(func() { matchmakingCredits = sync.Map{} })

	setMatchmakingCredit("user-1", &matchmakingCredit{
		Mode:      evr.ModeArenaPublic,
		Timestamp: time.Now().UTC().Add(-10 * time.Minute),
		Expiry:    time.Now().UTC().Add(-1 * time.Minute), // expired
	})

	got := getMatchmakingCredit("user-1", evr.ModeArenaPublic)
	if got != nil {
		t.Errorf("expected nil for expired credit, got %+v", got)
	}

	// Verify lazy cleanup removed the entry
	if _, loaded := matchmakingCredits.Load("user-1"); loaded {
		t.Error("expected expired credit to be removed from map")
	}
}

func TestMatchmakingCredit_ModeMismatch(t *testing.T) {
	matchmakingCredits = sync.Map{}
	t.Cleanup(func() { matchmakingCredits = sync.Map{} })

	setMatchmakingCredit("user-1", &matchmakingCredit{
		Mode:      evr.ModeArenaPublic,
		Timestamp: time.Now().UTC(),
		Expiry:    time.Now().UTC().Add(8 * time.Minute),
	})

	got := getMatchmakingCredit("user-1", evr.ModeCombatPublic)
	if got != nil {
		t.Errorf("expected nil for mode mismatch, got %+v", got)
	}

	// Original credit should still be in the map
	if _, loaded := matchmakingCredits.Load("user-1"); !loaded {
		t.Error("expected arena credit to still be in map after mode mismatch lookup")
	}
}

func TestMatchmakingCredit_Clear(t *testing.T) {
	matchmakingCredits = sync.Map{}
	t.Cleanup(func() { matchmakingCredits = sync.Map{} })

	setMatchmakingCredit("user-1", &matchmakingCredit{
		Mode:      evr.ModeArenaPublic,
		Timestamp: time.Now().UTC(),
		Expiry:    time.Now().UTC().Add(8 * time.Minute),
	})

	clearMatchmakingCredit("user-1")

	got := getMatchmakingCredit("user-1", evr.ModeArenaPublic)
	if got != nil {
		t.Errorf("expected nil after clear, got %+v", got)
	}
}

func TestMatchmakingCredit_Overwrite(t *testing.T) {
	matchmakingCredits = sync.Map{}
	t.Cleanup(func() { matchmakingCredits = sync.Map{} })

	t0 := time.Now().UTC().Add(-2 * time.Minute)
	t1 := time.Now().UTC().Add(-1 * time.Minute)

	setMatchmakingCredit("user-1", &matchmakingCredit{
		Mode:      evr.ModeArenaPublic,
		Timestamp: t0,
		Expiry:    time.Now().UTC().Add(8 * time.Minute),
	})
	setMatchmakingCredit("user-1", &matchmakingCredit{
		Mode:      evr.ModeCombatPublic,
		Timestamp: t1,
		Expiry:    time.Now().UTC().Add(8 * time.Minute),
	})

	got := getMatchmakingCredit("user-1", evr.ModeCombatPublic)
	if got == nil {
		t.Fatal("expected credit, got nil")
	}
	if !got.Timestamp.Equal(t1) {
		t.Errorf("expected overwritten timestamp %v, got %v", t1, got.Timestamp)
	}

	// Old mode should no longer match
	got = getMatchmakingCredit("user-1", evr.ModeArenaPublic)
	if got != nil {
		t.Errorf("expected nil for old mode after overwrite, got %+v", got)
	}
}

func TestMatchmakingCredit_FutureTimestamp(t *testing.T) {
	matchmakingCredits = sync.Map{}
	t.Cleanup(func() { matchmakingCredits = sync.Map{} })

	setMatchmakingCredit("user-1", &matchmakingCredit{
		Mode:      evr.ModeArenaPublic,
		Timestamp: time.Now().UTC().Add(5 * time.Minute), // future
		Expiry:    time.Now().UTC().Add(8 * time.Minute),
	})

	got := getMatchmakingCredit("user-1", evr.ModeArenaPublic)
	if got != nil {
		t.Errorf("expected nil for future timestamp, got %+v", got)
	}
}

func TestMatchmakingCredit_DifferentUsers(t *testing.T) {
	matchmakingCredits = sync.Map{}
	t.Cleanup(func() { matchmakingCredits = sync.Map{} })

	t1 := time.Now().UTC().Add(-2 * time.Minute)
	t2 := time.Now().UTC().Add(-1 * time.Minute)

	setMatchmakingCredit("user-1", &matchmakingCredit{
		Mode:      evr.ModeArenaPublic,
		Timestamp: t1,
		Expiry:    time.Now().UTC().Add(8 * time.Minute),
	})
	setMatchmakingCredit("user-2", &matchmakingCredit{
		Mode:      evr.ModeArenaPublic,
		Timestamp: t2,
		Expiry:    time.Now().UTC().Add(8 * time.Minute),
	})

	got1 := getMatchmakingCredit("user-1", evr.ModeArenaPublic)
	got2 := getMatchmakingCredit("user-2", evr.ModeArenaPublic)

	if got1 == nil || got2 == nil {
		t.Fatal("expected both credits to exist")
	}
	if !got1.Timestamp.Equal(t1) {
		t.Errorf("user-1: expected %v, got %v", t1, got1.Timestamp)
	}
	if !got2.Timestamp.Equal(t2) {
		t.Errorf("user-2: expected %v, got %v", t2, got2.Timestamp)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./server/ -run TestMatchmakingCredit -count=1 -v 2>&1 | head -30`

Expected: Compilation failure — `matchmakingCredits`, `setMatchmakingCredit`, `getMatchmakingCredit`, `clearMatchmakingCredit` are undefined.

---

### Task 2: Implement matchmaking credit type and map functions

**Files:**

- Create: `server/evr_matchmaking_credit.go`

- [ ] **Step 1: Create the implementation file**

```go
package server

import (
	"sync"
	"time"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

// matchmakingCredit records that a user was recently matchmaking for a
// specific mode. Preserving the original timestamp across re-queues
// prevents players from restarting at the back of the line after
// crashes or cancels.
type matchmakingCredit struct {
	Mode      evr.Symbol
	Timestamp time.Time
	Expiry    time.Time
}

// matchmakingCredits is a process-global map of user ID → *matchmakingCredit.
// It survives across sessions because it is keyed on user ID, not session ID.
var matchmakingCredits sync.Map

// getMatchmakingCredit returns the stored credit for the given user and mode,
// or nil if no valid credit exists. Invalid credits (expired, wrong mode,
// future timestamp) return nil. Expired entries are lazily deleted.
func getMatchmakingCredit(userID string, mode evr.Symbol) *matchmakingCredit {
	val, ok := matchmakingCredits.Load(userID)
	if !ok {
		return nil
	}

	credit := val.(*matchmakingCredit)

	// Expired — lazy cleanup
	if time.Now().After(credit.Expiry) {
		matchmakingCredits.Delete(userID)
		return nil
	}

	// Mode mismatch — don't delete, just skip
	if credit.Mode != mode {
		return nil
	}

	// Future timestamp — clock skew protection
	if credit.Timestamp.After(time.Now()) {
		matchmakingCredits.Delete(userID)
		return nil
	}

	return credit
}

// setMatchmakingCredit stores a matchmaking credit for the given user,
// overwriting any previous credit.
func setMatchmakingCredit(userID string, credit *matchmakingCredit) {
	matchmakingCredits.Store(userID, credit)
}

// clearMatchmakingCredit removes the matchmaking credit for the given user.
func clearMatchmakingCredit(userID string) {
	matchmakingCredits.Delete(userID)
}

// matchmakingCreditModes is the set of modes that use the matchmaker and
// should receive credits. Social lobbies bypass the matchmaker entirely.
var matchmakingCreditModes = map[evr.Symbol]struct{}{
	evr.ModeArenaPublic:   {},
	evr.ModeCombatPublic:  {},
	evr.ModeArenaPublicAI: {},
}

// isMatchmakingCreditMode returns true if the given mode should create/use
// matchmaking credits.
func isMatchmakingCreditMode(mode evr.Symbol) bool {
	_, ok := matchmakingCreditModes[mode]
	return ok
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run: `go test ./server/ -run TestMatchmakingCredit -count=1 -v 2>&1 | tail -20`

Expected: All 7 tests PASS.

- [ ] **Step 3: Commit**

```bash
git add server/evr_matchmaking_credit.go server/evr_matchmaking_credit_test.go
git commit -m "feat: add matchmaking credit type and map functions

Introduce a process-global sync.Map that stores per-user matchmaking
credits. These preserve the original submission timestamp across
re-queues so players don't restart at the back of the line after
crashes or cancels."
```

---

### Task 3: Wire credit lookup into NewLobbyParametersFromRequest

**Files:**

- Modify: `server/evr_lobby_parameters.go:403`

- [ ] **Step 1: Modify `NewLobbyParametersFromRequest` to check for existing credit**

In `server/evr_lobby_parameters.go`, find the struct literal at the end of `NewLobbyParametersFromRequest` (around line 340-408). Replace the `MatchmakingTimestamp` line and the `}, nil` return with credit-aware logic:

Replace this (lines 402-408):

```go
		MaxServerRTT:                 maxServerRTT,
		MatchmakingTimestamp:         time.Now().UTC(),
		MatchmakingTimeout:           time.Duration(globalSettings.MatchmakingTimeoutSecs) * time.Second,
		FailsafeTimeout:              time.Duration(failsafeTimeoutSecs) * time.Second,
		FallbackTimeout:              time.Duration(globalSettings.FallbackTimeoutSecs) * time.Second,
		DisplayName:                  sessionParams.profile.GetGroupIGN(groupIDStr),
	}, nil
}
```

With:

```go
		MaxServerRTT:                 maxServerRTT,
		MatchmakingTimestamp:         time.Now().UTC(),
		MatchmakingTimeout:           time.Duration(globalSettings.MatchmakingTimeoutSecs) * time.Second,
		FailsafeTimeout:              time.Duration(failsafeTimeoutSecs) * time.Second,
		FallbackTimeout:              time.Duration(globalSettings.FallbackTimeoutSecs) * time.Second,
		DisplayName:                  sessionParams.profile.GetGroupIGN(groupIDStr),
	}

	// Check for an existing matchmaking credit to preserve queue position
	// across re-queues (crashes, cancels, party follower failures).
	if isMatchmakingCreditMode(mode) {
		userIDStr := session.UserID().String()
		if credit := getMatchmakingCredit(userIDStr, mode); credit != nil {
			params.MatchmakingTimestamp = credit.Timestamp
		} else {
			setMatchmakingCredit(userIDStr, &matchmakingCredit{
				Mode:      mode,
				Timestamp: params.MatchmakingTimestamp,
				Expiry:    time.Now().UTC().Add(params.MatchmakingTimeout + 2*time.Minute),
			})
		}
	}

	return params, nil
}
```

Note: This requires changing the return from `}, nil` (returning the struct literal directly) to assigning to a `params` variable first. Change the struct literal opening to:

Find (around line 340):

```go
	return &LobbySessionParameters{
```

Replace with:

```go
	params := &LobbySessionParameters{
```

- [ ] **Step 2: Run tests to verify nothing is broken**

Run: `go test ./server/ -run TestMatchmakingCredit -count=1 -v 2>&1 | tail -20`

Expected: All 7 tests still PASS.

Run: `go test ./server/ -run TestMatchmak -count=1 -v 2>&1 | tail -20`

Expected: Existing matchmaker tests PASS.

- [ ] **Step 3: Commit**

```bash
git add server/evr_lobby_parameters.go
git commit -m "feat: wire matchmaking credit into NewLobbyParametersFromRequest

On re-queue for the same mode, reuse the original matchmaking timestamp
instead of time.Now(). This preserves SBMM range expansion, matchmaker
priority, and backfill age filters across crashes and cancels."
```

---

### Task 4: Wire credit clear into LobbyJoinEntrants

**Files:**

- Modify: `server/evr_lobby_joinentrant.go:280-281`

- [ ] **Step 1: Add credit clear for all entrants before the success return**

In `server/evr_lobby_joinentrant.go`, in the function-level `LobbyJoinEntrants` (line 48), find the success path at the end of the function:

Replace (lines 280-281):

```go
	logger.Info("Joined entrant.", zap.String("mid", label.ID.UUID.String()), zap.String("uid", e.UserID.String()), zap.String("sid", e.SessionID.String()), zap.Int("role", e.RoleAlignment))
	return nil
```

With:

```go
	// Clear matchmaking credits for all entrants (primary + reservations).
	// This ensures the next queue after a completed match starts fresh.
	for _, ent := range entrants {
		clearMatchmakingCredit(ent.UserID.String())
	}

	logger.Info("Joined entrant.", zap.String("mid", label.ID.UUID.String()), zap.String("uid", e.UserID.String()), zap.String("sid", e.SessionID.String()), zap.Int("role", e.RoleAlignment))
	return nil
```

- [ ] **Step 2: Run tests**

Run: `go test ./server/ -run TestMatchmakingCredit -count=1 -v 2>&1 | tail -20`

Expected: All tests PASS.

Run: `go test ./server/ -run TestMatchmak -count=1 -v 2>&1 | tail -20`

Expected: Existing matchmaker tests PASS.

- [ ] **Step 3: Commit**

```bash
git add server/evr_lobby_joinentrant.go
git commit -m "feat: clear matchmaking credits on match join

When a player successfully joins a match, clear their matchmaking credit
so the next queue starts with a fresh timestamp. Clears for all entrants
in the party, not just the primary."
```

---

### Task 5: Run full test suite and verify consumer correctness

**Files:**

- Read-only verification of: `server/evr_lobby_parameters.go`, `server/evr_lobby_matchmake.go`, `server/evr_lobby_find.go`

- [ ] **Step 1: Run full matchmaker test suite**

Run: `go test ./server/ -run TestMatchmak -count=1 -v 2>&1 | tail -30`

Expected: All tests PASS.

- [ ] **Step 2: Verify all consumers of MatchmakingTimestamp**

Run: `grep -n 'MatchmakingTimestamp' server/evr_lobby_parameters.go server/evr_lobby_matchmake.go`

Verify that each consumer handles a potentially old timestamp correctly:

| Consumer                       | File:Line                     | Expected behavior with old timestamp |
| ------------------------------ | ----------------------------- | ------------------------------------ |
| `calculateExpandedRatingRange` | `evr_lobby_parameters.go:419` | Wider range — correct                |
| `BackfillSearchQuery`          | `evr_lobby_parameters.go:450` | Earlier minStartTime — acceptable    |
| `MatchmakingParameters`        | `evr_lobby_parameters.go:577` | Older submission_time — desired      |
| Failsafe reduction             | `evr_lobby_matchmake.go:230`  | Faster failsafe — correct            |

None of these assume the timestamp is within seconds of `time.Now()`.

- [ ] **Step 3: Run the broader server tests**

Run: `go test ./server/ -count=1 -timeout 120s 2>&1 | tail -10`

Expected: PASS (or at least no new failures introduced by this change).

- [ ] **Step 4: Commit if any cleanup was needed, otherwise skip**

```bash
# Only if changes were made during verification
git add -A && git commit -m "chore: cleanup from matchmaking credit verification"
```

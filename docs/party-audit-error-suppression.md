# Party System Error Suppression Audit

Base commit: `ba5ee8f12b3b66c347eb2e1081414c4fa2b82be2`
HEAD: current working tree
Audit date: 2026-05-27

---

## Finding 1: lobbyJoin silently swallowed LobbyJoinEntrants error

**File**: `server/evr_lobby_join.go`
**Severity**: [CRITICAL]
**Introduced at base**: yes (present in base commit)
**Fixed by**: `d9d0bec63` (fix(party): resolve infinite matchmaking and silent failure cascade)
**Still present at HEAD**: NO (fixed)

At base, `lobbyJoin()` called `LobbyJoinEntrants()` and on error only
spawned a goroutine to send the error to the client after 3s delay, but
**always returned nil** to the caller:

```go
// BASE VERSION
if err := p.LobbyJoinEntrants(logger, label, presence); err != nil {
    go func() { /* send error to client after delay */ }()
}
return nil  // <-- always nil, caller assumes success
```

Callers (configureParty, TryFollowPartyLeader, pollFollowPartyLeader) treated
a nil return as "join succeeded" and stopped matchmaking. Clients entered an
infinite matchmaking loop because the server reported success while the join
actually failed.

Fixed in `d9d0bec63` by adding `return fmt.Errorf("lobbyJoin: %w", err)`.

---

## Finding 2: 13 logger lines stripped from pollFollowPartyLeader

**File**: `server/evr_lobby_find.go`
**Severity**: [HIGH]
**Introduced by**: `20bea2927` (Infinite Matchmaking fix)
**Fixed by**: `d9d0bec63` (restore 13 logger lines)
**Still present at HEAD**: NO (12 of 13 restored; see Finding 3 for the one that was not)

Commit `20bea2927` removed 13 logger lines from `pollFollowPartyLeader`,
making the polling loop almost entirely silent. Removed lines included:

1. `logger.Debug("Context canceled but follower is in leader's match (placed by matchmaker)")`
2. `logger.Warn("Party leader not found during poll")`
3. `logger.Debug("This player became the leader during poll")`
4. `logger.Debug("Leader left match during poll")`
5. `logger.Debug("Context canceled during settle but follower is in leader's match")`
6. `logger.Debug("Already in leader's match during poll")`
7. `logger.Debug("Leader's match label unavailable during poll, retrying")`
8. `logger.Debug("Leader's non-social match is persistently non-joinable, releasing follower")` with 5 structured fields
9. `logger.Debug("Joining leader's lobby during poll")`
10. `logger.Warn("Failed to join leader's lobby during poll")`
11. `logger.Debug("Leader is in a non-joinable mode during poll")`
12. `logger.Debug("Polling to follow party leader (follower is in a lobby)")`
13. `logger.Info("Follower in social mode, finding social lobby independently...")`

Commit `d9d0bec63` restored 12 of these. See Finding 3 for the one that
remains reduced.

---

## Finding 3: "Non-joinable, releasing follower" structured log permanently removed

**File**: `server/evr_lobby_find.go`, `pollFollowPartyLeader()`
**Severity**: [MEDIUM]
**Introduced by**: `20bea2927`
**Fixed by**: not restored
**Still present at HEAD**: YES

The structured log at the `maxNonJoinableCycles` threshold was:

```go
logger.Debug("Leader's non-social match is persistently non-joinable, releasing follower",
    zap.String("mid", leaderMatchID.String()),
    zap.String("mode", label.Mode.String()),
    zap.Bool("open", label.Open),
    zap.Int("open_slots", label.OpenPlayerSlots()),
    zap.Int("required_slots", requiredSlots))
```

At HEAD, the `return false` at this location has no log line. The five
structured fields (match ID, mode, open status, open slots, required slots)
are permanently lost from the diagnostic trail. When a follower is released
from polling due to a persistently non-joinable match, there is no log
evidence of why.

---

## Finding 4: Leader matchmaking stream check removed from pollFollowPartyLeader

**File**: `server/evr_lobby_find.go`, `pollFollowPartyLeader()`
**Severity**: [HIGH]
**Introduced by**: `20bea2927`
**Fixed by**: not restored
**Still present at HEAD**: YES

At base, the poll loop had:

```go
// Check if the leader is still matchmaking.
if pr := session.pipeline.tracker.GetLocalBySessionIDStreamUserID(
    leaderSessionID, params.MatchmakingStream(), leaderUserID); pr != nil {
    continue  // leader hasn't settled yet, keep polling
}
```

This was removed in `20bea2927` and never restored. Without this check, the
poll loop proceeds to read the leader's service stream and try to join their
match even while the leader is still in the matchmaker. If the leader has a
stale service stream entry pointing to their previous match, the follower may
attempt to join that match prematurely, fail, and waste a poll cycle or worse.

The `isLeaderHeadingToSocial()` function (line 1041) does check the
matchmaking stream, but only for social mode detection -- it does not guard
the general poll loop.

---

## Finding 5: configureParty stream validation removed

**File**: `server/evr_lobby_find.go`, `configureParty()`
**Severity**: [MEDIUM]
**Introduced by**: `d9d0bec63` (present in cumulative diff; removed between base and HEAD)
**Still present at HEAD**: YES

At base, `configureParty()` validated each party member against the matchmaking
stream and kicked members who were not following the leader:

```go
meta, err := p.nk.StreamUserGet(stream.Mode, stream.Subject.String(),
    stream.Subcontext.String(), stream.Label,
    member.Presence.GetUserId(), member.Presence.GetSessionId())
if err != nil {
    return nil, nil, false, fmt.Errorf("failed to get party stream: %w", err)
} else if meta == nil {
    logger.Debug("Party member is not following the leader", ...)
    if err := p.nk.StreamUserKick(...); err != nil {
        return nil, nil, false, fmt.Errorf("failed to kick party member: %w", err)
    }
}
```

This entire block (including two error-returning paths) was removed. At HEAD,
party members who are not following the leader are silently included in the
party size count and the entrant list, potentially leading to phantom party
members.

---

## Finding 6: handleMatchmakingError no longer calls LeavePartyStream on error

**File**: `server/evr_lobby_session.go`
**Severity**: [MEDIUM]
**Introduced by**: `1c28f2f0b` (fix: don't destroy party on matchmaking error)
**Still present at HEAD**: YES (intentional)

At base, the error path in `handleLobbySessionRequest` called
`LeavePartyStream(session)` when a non-timeout matchmaking error occurred.
This was removed in `1c28f2f0b` and the error handling was extracted to
`handleMatchmakingError()`.

This is an **intentional error suppression** that fixes a worse bug: the
party stream teardown was destroying the party on transient errors, causing
party-of-3 separation. However, it means matchmaking errors no longer trigger
any party cleanup. If a party is in a corrupt state when matchmaking errors,
that state persists.

---

## Finding 7: LeavePartyStream removed from lobbyJoin, LobbyJoinSessionRequest, LobbyCreateSessionRequest

**File**: `server/evr_lobby_join.go`, `server/evr_lobby_session.go`
**Severity**: [MEDIUM]
**Introduced by**: `2b2acb961` (fix: preserve party stream across lobby transitions)
**Still present at HEAD**: YES (intentional)

Three `LeavePartyStream(session)` calls removed:

1. `lobbyJoin()`: removed the conditional `if label.Mode != Arena && Mode != Combat { LeavePartyStream }`
2. `handleLobbySessionRequest / LobbyJoinSessionRequest`: `LeavePartyStream` before join
3. `handleLobbySessionRequest / LobbyCreateSessionRequest`: `LeavePartyStream` before create

Intentional: party stream was being torn down too aggressively, causing the
"infinite matchmaking" symptom. But the safety property "non-competitive
lobby transitions clean up party state" is now gone.

---

## Finding 8: Warn -> Debug demotions in non-critical paths

**File**: `server/evr_lobby_joinentrant.go`, `server/evr_lobby_matchmake.go`
**Severity**: [LOW]
**Introduced by**: `6b1e27e87` (chore: demote noisy log messages)
**Still present at HEAD**: YES

Two Warn demotions:

1. `evr_lobby_joinentrant.go:86`: `logger.Warn("duplicate join attempt; no-op")` -> `logger.Debug(...)`.
   Duplicate joins are benign (no-op return nil). Reasonable demotion.

2. `evr_lobby_matchmake.go:284`: `logger.Warn("Failed to load archetype stats, defaulting to rookie")` -> `logger.Debug(...)`.
   Falls back to a safe default. Error object is preserved. Reasonable demotion.

Also in other files (not in audit scope but in the same commit):
- `evr_lobby_prejoin_ping.go`: `logger.Warn("Pre-join ping validation failed")` -> `logger.Info(...)`
- `evr_match.go`: `.Warn("Evicting stale presence for same-user duplicate EVR-ID.")` -> `.Info(...)`
- `evr_pipeline.go`: `logger.Warn("Received unauthenticated message")` -> `logger.Debug(...)`

---

## Finding 9: entrant metadata error converted to fallback path

**File**: `server/evr_lobby_joinentrant.go`
**Severity**: [MEDIUM]
**Introduced by**: `59c0d4c4e` (fix(lobby): fall back to fresh metadata when entrant presence is missing)
**Still present at HEAD**: YES (intentional)

At base:
```go
if entrantMeta == nil {
    return errors.New("failed to get entrant metadata")
}
```

At HEAD:
```go
if entrantMeta == nil {
    logger.Warn("entrant metadata not found, falling back to fresh metadata", ...)
    freshMeta := PresenceMeta{...}
    if success := tracker.Update(...); !success {
        return ErrFailedToTrackEntrantStream
    }
}
```

The hard error return was replaced with a fallback that synthesizes fresh
metadata and logs a Warn. The error is no longer propagated to the caller.
This prevents reconnect/disconnect races from causing permanent join
failures, but means the entrant joins with potentially stale/default
metadata instead of failing cleanly.

---

## Finding 10: Backfill context cancellation log removed

**File**: `server/evr_lobby_backfill.go`
**Severity**: [LOW]
**Introduced by**: cumulative diff (between base and HEAD)
**Still present at HEAD**: YES

At base:
```go
b.logger.Debug("Backfill cancelled during candidate processing", zap.Error(ctx.Err()))
return results, ctx.Err()
```

At HEAD:
```go
return results, ctx.Err()
```

The Debug log was removed. The error is still propagated (ctx.Err() returned),
so this is purely a diagnostic loss, not an error suppression. Low severity.

---

## Finding 11: isFollowerInLeaderMatch tracker-based convergence fallback

**File**: `server/evr_lobby_find.go`, `pollFollowPartyLeader()`
**Severity**: [MEDIUM]
**Introduced by**: `6f7fcb163` (fix(party): resolve Duo Desync)
**Still present at HEAD**: YES

At base (and through most of the history), `isFollowerInLeaderMatch`
required `MatchLabelByID` to succeed and the follower to appear in the
match's player list:

```go
label, err := MatchLabelByID(ctx, p.nk, leaderMatchID)
if err != nil || label == nil { return false }
return label.GetPlayerByUserID(session.userID.String()) != nil
```

At HEAD, if `MatchLabelByID` fails or p.nk is nil, it returns `true`
based solely on tracker stream convergence:

```go
if p.nk != nil {
    label, err := MatchLabelByID(ctx, p.nk, leaderMatchID)
    if err == nil && label != nil {
        return label.GetPlayerByUserID(session.userID.String()) != nil
    }
}
return true  // fallback: trust tracker convergence
```

This weakens the error check: a transient `MatchLabelByID` failure now
produces a **false positive** (follower "in" leader's match when they may not
actually be joined). The comment acknowledges this is a fallback for
test/transient scenarios, but in production a brief registry miss would
cause the poll loop to exit prematurely with a success that is not real.

---

## Finding 12: executeBackfillResultOptimized context parameter discarded

**File**: `server/evr_lobby_backfill.go`
**Severity**: [LOW]
**Introduced by**: `66160727f` (fix(backfill): join entrants sequentially)
**Still present at HEAD**: YES

The `ctx context.Context` parameter was renamed to `_ context.Context`,
making it impossible for the function body to check context cancellation.
The function still calls `LobbyJoinEntrants` which is synchronous and
serialized (the concurrent goroutine approach was replaced with a sequential
loop). Context cancellation during a slow join sequence cannot be detected.

---

## Summary

| # | File | Severity | Status at HEAD |
|---|------|----------|---------------|
| 1 | evr_lobby_join.go: lobbyJoin swallowed error | CRITICAL | FIXED |
| 2 | evr_lobby_find.go: 13 logger lines stripped | HIGH | 12/13 FIXED |
| 3 | evr_lobby_find.go: non-joinable structured log gone | MEDIUM | NOT FIXED |
| 4 | evr_lobby_find.go: matchmaking stream check removed from poll | HIGH | NOT FIXED |
| 5 | evr_lobby_find.go: configureParty stream validation removed | MEDIUM | NOT FIXED |
| 6 | evr_lobby_session.go: LeavePartyStream on error removed | MEDIUM | INTENTIONAL |
| 7 | evr_lobby_join.go + session.go: LeavePartyStream removed | MEDIUM | INTENTIONAL |
| 8 | joinentrant + matchmake: Warn->Debug demotions | LOW | INTENTIONAL |
| 9 | evr_lobby_joinentrant.go: hard error -> fallback | MEDIUM | INTENTIONAL |
| 10 | evr_lobby_backfill.go: context cancel log removed | LOW | NOT FIXED |
| 11 | evr_lobby_find.go: convergence fallback trusts tracker | MEDIUM | INTENTIONAL |
| 12 | evr_lobby_backfill.go: context param discarded | LOW | INTENTIONAL |

### Open risks

1. **Finding 4** (matchmaking stream check removed from poll) is the most
   concerning unfixed suppression. Without it, followers can attempt to join
   stale leader matches during active matchmaking.

2. **Finding 5** (stream validation removed from configureParty) means phantom
   party members can inflate party size counts, leading to incorrect slot
   calculations.

3. **Finding 11** (tracker convergence fallback) can produce false positives
   during transient registry failures, potentially terminating the poll loop
   when the follower is not actually in the leader's match.

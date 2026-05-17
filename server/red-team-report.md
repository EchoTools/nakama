# Red Team Report — EVR Party Matchmaking
**Operator:** Fox Delgado  
**Date:** 2026-05-17  
**Engagement:** Code-only (no production access)

---

## Successful Kill Chains

### KC-1: Follower Forced Into Private Match (ModeSocialPrivate Bypass)

**Severity:** HIGH  
**Entry Point:** `TryFollowPartyLeader` → `lobbyJoin`  

**Steps:**
1. Attacker (leader) joins/creates a `ModeSocialPrivate` lobby.
2. Victim (follower) in the same party queues for `ModeArenaPublic` or any valid public mode.
3. `lobbyFind` validates mode at line 74-78 — passes, because the follower's *requested* mode is valid.
4. `TryFollowPartyLeader` (line 94) runs. It fetches the leader's match label. The mode switch at line 1128-1132 explicitly allows `ModeSocialPrivate`:
   ```
   case evr.ModeSocialPrivate, evr.ModeSocialPublic, evr.ModeCombatPublic, evr.ModeArenaPublic:
   ```
5. `lobbyJoin` is called. At lines 30-31, it overwrites `lobbyParams.GroupID` and `lobbyParams.Mode` with the private match's values:
   ```go
   lobbyParams.GroupID = label.GetGroupID()
   lobbyParams.Mode = label.Mode
   ```
6. `lobbyAuthorize` runs against the *leader's* GroupID. The follower now joins the private social lobby with no prior consent or invite.

**Impact:** Any party follower can be dragged into a private social lobby (e.g., a stream or private event) by a leader who creates one and queues. The follower's mode selection is irrelevant. The follower bypasses whatever invite/access controls a private lobby might enforce via mode gating, because the only mode check that matters is post-authorization — and by that point, `lobbyParams.Mode` has already been overwritten.

**The same path exists in `pollFollowPartyLeader`** (line 1291), so reconnected followers land in the same trap.

---

### KC-2: lobbyJoin Returns nil on Join Failure — Silent Drop

**Severity:** CRITICAL (reliability/state corruption)  
**Entry Point:** `lobbyJoin` line 72  

**Steps:**
1. Any party follower attempts to join a match via `TryFollowPartyLeader` → `lobbyJoin`.
2. `LobbyJoinEntrants` fails (full server, lock error, feature mismatch, tracker failure).
3. `lobbyJoin` swallows the error and returns `nil`:
   ```go
   // line 58-72
   if err := p.LobbyJoinEntrants(logger, label, presence); err != nil {
       go func() { ... SendEVRMessages ... }()  // async, fire-and-forget
   }
   return nil  // always nil
   ```
4. The caller (`TryFollowPartyLeader`) sees `err == nil`, interprets the join as successful, and returns `true`.
5. `lobbyFind` exits with `return nil`. The follower's matchmaking context is torn down.
6. The follower is not in any match. The client eventually receives an error message (3s delayed, goroutine), but the session is already in a broken limbo state.

**Impact:** A follower who hits any transient join error (e.g., server fills between label fetch and join attempt) permanently loses their matchmaking slot with no retry. From the server's perspective, the session completed successfully. From the client's perspective, it receives a delayed failure. The party continues without the follower. In a targeted attack: repeatedly filling a match to N-1 seats (via bots) causes every follower to silently drop.

---

### KC-3: Presence Payload Poisoning — Mode Manipulation via Malformed Status JSON

**Severity:** HIGH  
**Entry Point:** `isLeaderHeadingToSocial` → `json.Unmarshal` at line 900  

**Steps:**
1. The leader's matchmaking presence status is a JSON-serialized `LobbySessionParameters` blob, written at `configureParty` line 330:
   ```go
   statusBytes, _ := json.Marshal(lobbyParams)
   ```
   The error is silently discarded (SMELL annotation at line 329).
2. `isLeaderHeadingToSocial` reads this blob for every follower:
   ```go
   if err := json.Unmarshal([]byte(presence.GetStatus()), &leaderParams); err == nil {
       if leaderParams.Mode == evr.ModeSocialPublic || leaderParams.Mode == evr.ModeSocialNPE {
           return true
       } else {
           return false  // non-social mode → return false immediately
       }
   }
   ```
3. The `GetStatus()` payload is sourced from the Nakama tracker's in-memory presence store. It is set by the leader at queue time. 
4. A client that can control what they write to the tracker (via a malicious reconnect that replaces their presence before the tracker flushes) can set `"mode"` in the JSON to `ModeSocialPublic` while actually queuing for `ModeArenaPublic`.
5. All followers read this and call `lobbyParams.Mode = evr.ModeSocialPublic` (line 63), overriding their own mode selection. They then call `lobbyFindOrCreateSocial` (line 134) instead of arena matchmaking — wasting their queue entry and sending them to a social lobby.

**Simpler variant:** If the leader's JSON is malformed (unmarshal error), the function falls through to step 2 (check match stream). If the leader is also not in a social match at that moment, it returns `false`. This causes followers to *not* redirect to social when they should, leading to mode mismatch between leader and followers — divergent lobby placement.

**Impact:** Leader can herd all followers to social by controlling the mode field in their presence JSON. No match-level access control prevents this. The effect persists until followers time out or reach `lobbyFindOrCreateSocial`.

---

### KC-4: Social Lobby IsSocial() Full-Match Infinite Poll — Server Resource Drain

**Severity:** MEDIUM (DoS amplification)  
**Entry Point:** `pollFollowPartyLeader` loop, lines 1274-1281  

**Steps:**
1. Leader is in a `ModeSocialPrivate` match that is at capacity (full).
2. Follower reaches `pollFollowPartyLeader`. Per loop iteration:
   - Wait 3s (`time.After`)
   - Tracker lookup (leader presence)
   - Wait another 3s ("settle" wait)
   - `MatchLabelByID` RPC (match state fetch)
   - Evaluate slots
3. At line 1274-1281: match is full → `!label.Open || label.OpenPlayerSlots() < requiredSlots`. 
   Branch: `if !label.IsSocial()` → false (it IS social) → `nonJoinableCycles` is **never incremented**.
   The `maxNonJoinableCycles = 1` guard is dead code for social lobbies.
4. `continue` — loop repeats from step 2 indefinitely.
5. Only exit is context timeout (default 360s, max 600s).

**Resource consumption per follower client:**
- 6s cycle minimum (two `time.After(3s)` calls)
- ~100 cycles over a 600s timeout
- Per cycle: 1 tracker read + 1 `MatchLabelByID` call (database/registry hit)
- Total: ~200 DB/registry reads per client over 10 minutes
- Plus: 1 goroutine running `monitorMatchmakingStream` checking every 1s (an additional 600 tracker reads)
- Total server-side work: ~800 tracker/registry operations per client, all blocked on the social-full-lobby case

**Amplification:** One attacker with a full social lobby and 4 party followers burns 4× the resources above simultaneously. The `maxNonJoinableCycles` kill switch exists but is bypassed by the `IsSocial()` branch.

---

### KC-5: Race-Window GroupID/Mode Corruption (CHAOS-1)

**Severity:** HIGH  
**Entry Point:** `lobbyJoin` lines 30-31, concurrent with `lobbyFind` mode switch  

**Steps:**
1. Party of 2: A (leader), B (follower). Both call `lobbyFind`. They share a `*LobbySessionParameters` pointer because `configureParty` stores parameters in the session context and both goroutines read/write the same `lobbyParams` fields.
2. Thread A (leader) executes `lobbyJoin`:
   ```go
   lobbyParams.GroupID = label.GetGroupID()  // write
   lobbyParams.Mode = label.Mode             // write
   ```
3. Simultaneously, Thread B (follower) in `lobbyFind` reads `lobbyParams.Mode` for the mode switch at line 74, or for the `isLeaderHeadingToSocial` check, or at line 111.
4. Go's memory model: no synchronization on `GroupID` (a `uuid.UUID`, 16 bytes) or `Mode` (an `evr.Symbol`, likely `uint64`). Partial write visible to B. Goroutine B sees a torn `GroupID` — first 8 bytes from one match, last 8 from another.
5. B's `lobbyAuthorize` runs against a corrupted `GroupID`. Authorization result is undefined — may pass or fail for the wrong guild.

**Timing requirement:** The race window is narrow (~nanoseconds between the two writes). However, under load (many party joins concurrently) or with a disconnect/reconnect at the moment of join, the window widens. The attacker controls their disconnect timing precisely — reconnecting immediately after the leader submits to `lobbyJoin` maximizes the overlap.

**Observable outcome:** B either joins the wrong guild's lobby (GroupID mismatch) or gets authorization rejected for a lobby they should be able to join. Either outcome is exploitable: wrong-guild join = unauthorized access, wrong rejection = denial of service for the follower.

---

## Attempted Attack Paths (Failed)

### ATTEMPT-1: Race to Become Party Leader via Rapid First-Join

**What was tried:** Join the party group name before the intended leader to become `expectedInitialLeader` and thus leader.

**Why it failed:** `GetOrCreateByGroupName` uses `LoadOrStore` on a `sync.Map` — exactly-once creation. `JoinPartyGroup` passes `userPresence` as the `leader` argument only at party creation time. Only the goroutine that creates the party (`created == true`) sets `expectedInitialLeader`. Subsequent callers hit the `!created` branch and join as members. The `partyRegistry.Join` path compares `presence.GetUserId()` and `presence.GetSessionId()` against `expectedInitialLeader` — the attacker would need to forge the leader's session ID, which they cannot do. **Dead end.**

### ATTEMPT-2: Follower Re-queues to Steal Leadership During Leader Disconnect

**What was tried:** Force leader disconnect (disconnect mid-matchmaking) so the "oldest presence" promotion fires, then be first in the tracker to claim leader slot.

**Why it partially works but is not controlled:** When the leader's presence leaves, `PartyHandler.Leave` promotes `p.members.Oldest()`. The "oldest" member is determined by insertion order in `PartyPresenceList`, not by connection time. An attacker who joined before other legitimate members would get promoted. However, `JoinPartyGroup` (via `GetOrCreateByGroupName`) requires knowing the `groupName` (the `LobbyGroupName` field from user settings, set server-side). If the attacker does not know the group name, they cannot join the party. **Partially exploitable only if groupName is guessable or shared.**

### ATTEMPT-3: Crafting Malicious MatchID in Match Service Stream

**What was tried:** Feed a crafted `MatchID` string into the match service tracker stream so `TryFollowPartyLeader` follows a non-existent or wrong match.

**Why it failed:** The match service stream is written by `LobbyJoinEntrants` server-side via `tracker.Update`. The player's session cannot directly write to `StreamModeService` streams — these are set exclusively by the server after a successful `JoinAttempt` / `matchRegistry.JoinAttempt`. A client cannot inject into this stream. **Dead end.**

### ATTEMPT-4: Exploit `vibinatorsGravity` to Force Mode Redirect

**What was tried:** Trigger `vibinatorsGravityRedirectMode` to force all social-queuing players to `ModeCombatPublic`.

**Why it failed:** The function checks `lobbyParams.Mode != evr.ModeSocialPublic` at line 73 as a precondition. Only social-lobby finders are affected. Additionally, `vibinatorsUserID` is hardcoded — the attacker cannot impersonate vibinator. And the combat match join path (`LobbyJoinEntrants`) still runs authorization. This is a novelty feature with hardcoded targeting, not an attack surface. **Dead end.**

### ATTEMPT-5: Infinite Social Lobby Creation via lobbyFindOrCreateSocial

**What was tried:** Cause `lobbyFindOrCreateSocial` to create unlimited new lobbies by always failing `LobbyJoinEntrants`.

**Why it failed:** `createLobbyMu.TryLock()` in `newLobby` (line 480) is a global mutex. Only one lobby creation can proceed at a time. On failure to acquire lock, the caller backs off exponentially (up to 8s). Additionally, `maxAttempts = 30` caps the loop. The global mutex prevents runaway creation from a single session. **Rate-limited by design.**

---

## Purple Team Brief

### Summary

Five attack paths were confirmed as viable from code review alone. Three are directly exploitable by any party member without elevated privileges:

**P0 — KC-2 (Silent lobbyJoin nil return):** This is the most operationally dangerous bug. A join failure is indistinguishable from a join success at the caller level. The follower is silently evicted from matchmaking with no retry. This is not an adversarial attack — it's a reliability bug that any server-full condition triggers. Fix: `lobbyJoin` must propagate the error.

**P1 — KC-1 (ModeSocialPrivate bypass via TryFollowPartyLeader):** A leader with a private lobby can drag all followers into it without consent. The mode guard in `lobbyFind` does not protect against modes set post-validation by `lobbyJoin`. Fix: strip or validate `label.Mode` against the follower's permitted modes before calling `lobbyJoin` from the follow path. `ModeSocialPrivate` should not be in the allowed set for follower-join unless the follower was explicitly invited.

**P2 — KC-3 (Presence payload poisoning):** The leader's matchmaking status JSON is trusted without validation. A malformed or adversarially crafted payload causes mode corruption for all followers. The marshal error at line 330 is already annotated as SMELL/CRITICAL. Fix: treat unmarshal failure as "leader status unknown" and do not redirect followers; also validate that `leaderParams.Mode` is within expected bounds.

**P3 — KC-5 (CHAOS-1 race, GroupID/Mode):** The write-write race on `lobbyParams.GroupID` and `lobbyParams.Mode` in `lobbyJoin` is the confirmed CHAOS-1 bug. Under load, authorization runs against a torn GroupID. Fix: protect with a mutex or switch fields to atomic types (for `Mode`) and a sync-safe wrapper (for `GroupID`).

**P4 — KC-4 (Social full-lobby infinite poll):** `maxNonJoinableCycles` is effectively bypassed for social lobbies, allowing the poll loop to run for the full timeout (up to 600s). With N followers, this multiplies tracker and registry load. Fix: apply the `nonJoinableCycles` guard unconditionally, or add a separate `maxSocialFullCycles` limit.

### Handoff Checklist for Blue Team

- [ ] `lobbyJoin`: propagate `LobbyJoinEntrants` error instead of returning nil
- [ ] `TryFollowPartyLeader`/`pollFollowPartyLeader`: remove `ModeSocialPrivate` from allowed follower-join modes
- [ ] `isLeaderHeadingToSocial`: treat unmarshal failure as unknown, not as non-social
- [ ] `configureParty` line 330: return error on `json.Marshal` failure
- [ ] `pollFollowPartyLeader`: make `nonJoinableCycles` guard apply to social lobbies too (or add cap)
- [ ] `lobbyJoin` lines 30-31: protect `GroupID`/`Mode` writes under mutex or per-session lock

**Engagement closed.**

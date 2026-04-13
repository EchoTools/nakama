# Match Capacity Settings Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix combat team size enforcement gap and move match capacity limits into configurable global settings, with frontend editor support.

**Architecture:** Add a `MatchCapacity` struct to `ServiceSettingsData` that defines per-mode capacity limits (team size, max size, spectator/mod slots). Replace hardcoded constants in match creation and join enforcement with lookups from these settings. Add a "Match Capacity" section to the ServiceSettings.vue frontend. Two cross-referenced PRs: one for nakama (backend), one for echovrce-web (frontend).

**Tech Stack:** Go (nakama server), Vue 3 + Tailwind (echovrce-web frontend)

---

## File Structure

### nakama (backend PR)

| File                                         | Action | Responsibility                                                                                                     |
| -------------------------------------------- | ------ | ------------------------------------------------------------------------------------------------------------------ |
| `server/evr_global_settings.go`              | Modify | Add `MatchCapacitySettings` struct and field to `ServiceSettingsData`; add defaults in `FixDefaultServiceSettings` |
| `server/evr_match.go`                        | Modify | Generalize team size guard from arena-only to all public modes; use settings for match creation sizes              |
| `server/evr_match_label.go`                  | Modify | Update `roleLimit()` to handle social lobby spectator/mod distinction                                              |
| `server/evr_runtime_rpc_service_settings.go` | Modify | Add validation for match capacity fields                                                                           |
| `server/evr_match_test.go`                   | Modify | Add combat and social team size enforcement tests; regression tests for arena                                      |

### echovrce-web (frontend PR)

| File                            | Action | Responsibility                                             |
| ------------------------------- | ------ | ---------------------------------------------------------- |
| `src/views/ServiceSettings.vue` | Modify | Add "Match Capacity" settings group in the Matchmaking tab |

---

## Task 1: Write failing test — combat team size enforcement gap

This test exposes the actual bug: a combat match with full teams (5v5) still accepts a player.

**Files:**

- Modify: `server/evr_match_test.go` (add test cases after line 1078)

- [ ] **Step 1: Add combat full-teams test case**

Add these test cases inside the `tests` slice in `TestEvrMatch_MatchJoinAttempt`, after the existing "full if both teams are full" arena test (line 1078):

```go
{
    name: "Combat: rejects join when both teams are full (5v5)",
    m:    &EvrMatch{},
    args: args{
        state_: func() *MatchLabel {
            state := testStateFn()
            state.Open = true
            state.LobbyType = PublicLobby
            state.Mode = evr.ModeCombatPublic
            state.MaxSize = 16
            state.PlayerLimit = 10
            state.TeamSize = 5

            state.presenceMap = func() map[string]*EvrMatchPresence {
                m := make(map[string]*EvrMatchPresence)
                for i, p := range presences[1:11] {
                    if i >= 5 {
                        p.RoleAlignment = evr.TeamOrange
                    }
                    m[p.GetSessionId()] = p
                }
                return m
            }()
            state.rebuildCache()
            return state
        }(),
        presence: presences[0],
        metadata: func() map[string]string {
            presences[0].RoleAlignment = evr.TeamOrange
            return NewJoinMetadata(presences[0]).ToMatchMetadata()
        }(),
    },
    wantAllowed: false,
    wantReason:  ErrJoinRejectReasonLobbyFull.Error(),
},
{
    name: "Combat: rejects join when target team is full but other has space",
    m:    &EvrMatch{},
    args: args{
        state_: func() *MatchLabel {
            state := testStateFn()
            state.Open = true
            state.LobbyType = PublicLobby
            state.Mode = evr.ModeCombatPublic
            state.MaxSize = 16
            state.PlayerLimit = 10
            state.TeamSize = 5

            state.presenceMap = func() map[string]*EvrMatchPresence {
                m := make(map[string]*EvrMatchPresence)
                // 5 on blue (full), 3 on orange (has space)
                for i, p := range presences[1:9] {
                    if i >= 5 {
                        p.RoleAlignment = evr.TeamOrange
                    }
                    m[p.GetSessionId()] = p
                }
                return m
            }()
            state.rebuildCache()
            return state
        }(),
        presence: presences[0],
        metadata: func() map[string]string {
            presences[0].RoleAlignment = evr.TeamBlue // trying to join the full team
            return NewJoinMetadata(presences[0]).ToMatchMetadata()
        }(),
    },
    wantAllowed: false,
    wantReason:  ErrJoinRejectReasonLobbyFull.Error(),
},
{
    name: "Arena: rejects join when target team (4) is full",
    m:    &EvrMatch{},
    args: args{
        state_: func() *MatchLabel {
            state := testStateFn()
            state.Open = true
            state.LobbyType = PublicLobby
            state.Mode = evr.ModeArenaPublic
            state.MaxSize = 16
            state.PlayerLimit = 8
            state.TeamSize = 4

            state.presenceMap = func() map[string]*EvrMatchPresence {
                m := make(map[string]*EvrMatchPresence)
                // 4 on blue (full), 2 on orange
                for i, p := range presences[1:7] {
                    if i >= 4 {
                        p.RoleAlignment = evr.TeamOrange
                    }
                    m[p.GetSessionId()] = p
                }
                return m
            }()
            state.rebuildCache()
            return state
        }(),
        presence: presences[0],
        metadata: func() map[string]string {
            presences[0].RoleAlignment = evr.TeamBlue
            return NewJoinMetadata(presences[0]).ToMatchMetadata()
        }(),
    },
    wantAllowed: false,
    wantReason:  ErrJoinRejectReasonLobbyFull.Error(),
},
{
    name: "Social: rejects join when lobby is at 12 players",
    m:    &EvrMatch{},
    args: args{
        state_: func() *MatchLabel {
            state := testStateFn()
            state.Open = true
            state.LobbyType = PublicLobby
            state.Mode = evr.ModeSocialPublic
            state.MaxSize = 12
            state.PlayerLimit = 12
            state.TeamSize = 12

            state.presenceMap = func() map[string]*EvrMatchPresence {
                m := make(map[string]*EvrMatchPresence)
                for _, p := range presences[1:13] {
                    p.RoleAlignment = evr.TeamSocial
                    m[p.GetSessionId()] = p
                }
                return m
            }()
            state.rebuildCache()
            return state
        }(),
        presence: presences[0],
        metadata: func() map[string]string {
            presences[0].RoleAlignment = evr.TeamSocial
            return NewJoinMetadata(presences[0]).ToMatchMetadata()
        }(),
    },
    wantAllowed: false,
    wantReason:  ErrJoinRejectReasonLobbyFull.Error(),
},
```

Note: The test file creates 15+ presences in `presences` (line ~648-669). Verify there are enough by checking the presences slice. If needed, extend it to at least 14 entries.

- [ ] **Step 2: Run tests to verify the combat tests FAIL**

```bash
cd /home/andrew/src/nakama && go test ./server/ -run "TestEvrMatch_MatchJoinAttempt/Combat" -v -count=1
```

Expected: The two combat tests FAIL because `MatchJoinAttempt` lacks a combat team size guard — it only has one for `ModeArenaPublic` at line 483.

- [ ] **Step 3: Commit the failing tests**

```bash
git add server/evr_match_test.go
git commit -m "test: add failing tests for combat/social team size enforcement

Exposes bug where combat 5v5 matches accept players beyond team capacity
because MatchJoinAttempt only enforces team size for ModeArenaPublic,
not ModeCombatPublic. Also adds regression tests for arena and social."
```

---

## Task 2: Fix team size enforcement for all public modes

Generalize the arena-only guard at `evr_match.go:481-501` to cover combat and social.

**Files:**

- Modify: `server/evr_match.go:481-501`

- [ ] **Step 1: Replace the arena-only guard with a mode-general guard**

Replace lines 481–501 in `server/evr_match.go`:

```go
// Enforce team size limits for public arena matches BEFORE adding player to state
// This prevents broadcasting an invalid state that exceeds the team capacity
if state.Mode == evr.ModeArenaPublic {
    maxTeamSize := DefaultPublicArenaTeamSize // Always 4 for public arena
    targetTeam := meta.Presence.RoleAlignment
    currentCount := state.RoleCount(targetTeam)

    // Check if adding this player would exceed the limit
    if currentCount >= maxTeamSize {
        restoreReconnectReservation()
        logger.WithFields(map[string]interface{}{
            "blue":          state.RoleCount(evr.TeamBlue),
            "orange":        state.RoleCount(evr.TeamOrange),
            "target_team":   targetTeam,
            "max_team_size": maxTeamSize,
            "team_size":     state.TeamSize,
        }).Warn("Rejecting join: team size limit would be exceeded for public arena match.")

        return state, false, ErrJoinRejectReasonLobbyFull.Error()
    }
}
```

With:

```go
// Enforce team size limits for all public matches BEFORE adding player to state.
// This prevents broadcasting an invalid state that exceeds the team capacity.
// Uses the match label's TeamSize which is set at match creation from global settings.
if state.LobbyType == PublicLobby {
    targetTeam := meta.Presence.RoleAlignment
    maxTeamSize := state.TeamSize
    currentCount := state.RoleCount(targetTeam)

    if currentCount >= maxTeamSize {
        restoreReconnectReservation()
        logger.WithFields(map[string]interface{}{
            "mode":          state.Mode,
            "target_team":   targetTeam,
            "current_count": currentCount,
            "max_team_size": maxTeamSize,
            "team_size":     state.TeamSize,
        }).Warn("Rejecting join: team size limit would be exceeded.")

        return state, false, ErrJoinRejectReasonLobbyFull.Error()
    }
}
```

This is the minimal fix: use `state.LobbyType == PublicLobby` instead of `state.Mode == evr.ModeArenaPublic`, and use `state.TeamSize` (already set correctly per mode at match creation) instead of hardcoded `DefaultPublicArenaTeamSize`.

- [ ] **Step 2: Run all team size tests**

```bash
cd /home/andrew/src/nakama && go test ./server/ -run "TestEvrMatch_MatchJoinAttempt" -v -count=1
```

Expected: ALL team size tests pass — arena, combat, and social.

- [ ] **Step 3: Run the full server test suite**

```bash
cd /home/andrew/src/nakama && go test ./server/ -count=1 -timeout 120s
```

Expected: No regressions.

- [ ] **Step 4: Commit the fix**

```bash
git add server/evr_match.go
git commit -m "fix: enforce team size limits for combat and social public matches

Generalizes the arena-only team size guard in MatchJoinAttempt to cover
all public lobby types. Uses state.TeamSize (set per-mode at match
creation) instead of the hardcoded DefaultPublicArenaTeamSize constant.

Fixes: combat 5v5 matches accepting 6th player on a team, who then
gets placed as spectator by the game server."
```

---

## Task 3: Add MatchCapacitySettings to global settings

Move the hardcoded capacity constants into configurable settings stored on the system user.

**Files:**

- Modify: `server/evr_global_settings.go` (add struct, field, defaults)
- Modify: `server/evr_runtime_rpc_service_settings.go` (add validation)

- [ ] **Step 1: Define the MatchCapacitySettings types**

Add after the `CGNATSettings` struct (after line 98) in `server/evr_global_settings.go`:

```go
// ModeCapacity defines player and spectator/moderator limits for a match mode.
type ModeCapacity struct {
	TeamSize    int `json:"team_size"`    // Players per team (blue/orange for arena/combat, all for social)
	MaxSize     int `json:"max_size"`     // Total lobby capacity (players + spectators + moderators)
	PlayerLimit int `json:"player_limit"` // Max players (computed as TeamSize*2 for arena/combat, TeamSize for social)
}

// MatchCapacitySettings holds per-mode capacity limits.
// Stored in global settings and read at match creation time.
type MatchCapacitySettings struct {
	ArenaPublic  ModeCapacity `json:"arena_public"`  // 4v4, 8 players + 8 spec/mod = 16 total
	CombatPublic ModeCapacity `json:"combat_public"` // 5v5, 10 players + 6 spec/mod = 16 total
	SocialPublic ModeCapacity `json:"social_public"` // 12 players + 4 mod = 16 total (no spectators)
	Private      ModeCapacity `json:"private"`        // 16 players, no team assignments
}
```

- [ ] **Step 2: Add the field to ServiceSettingsData**

Add after the `CGNAT` field (line 85) in `ServiceSettingsData`:

```go
MatchCapacity MatchCapacitySettings `json:"match_capacity"`
```

- [ ] **Step 3: Add defaults in FixDefaultServiceSettings**

Add at the end of the `FixDefaultServiceSettings` function in `server/evr_global_settings.go`:

```go
// Initialize match capacity defaults
mc := &data.MatchCapacity
if mc.ArenaPublic.TeamSize == 0 {
    mc.ArenaPublic = ModeCapacity{TeamSize: 4, MaxSize: 16, PlayerLimit: 8}
}
if mc.CombatPublic.TeamSize == 0 {
    mc.CombatPublic = ModeCapacity{TeamSize: 5, MaxSize: 16, PlayerLimit: 10}
}
if mc.SocialPublic.TeamSize == 0 {
    mc.SocialPublic = ModeCapacity{TeamSize: 12, MaxSize: 16, PlayerLimit: 12}
}
if mc.Private.TeamSize == 0 {
    mc.Private = ModeCapacity{TeamSize: 16, MaxSize: 16, PlayerLimit: 16}
}
```

- [ ] **Step 4: Add validation in validateServiceSettings**

Add at the end of the validation function in `server/evr_runtime_rpc_service_settings.go`, before the final `return errs`:

```go
// --- Match capacity ---
validateCapacity := func(name string, cap ModeCapacity) {
    if cap.TeamSize < 1 || cap.TeamSize > 16 {
        errs = append(errs, fmt.Sprintf("match_capacity.%s.team_size must be between 1 and 16", name))
    }
    if cap.MaxSize < 1 || cap.MaxSize > 16 {
        errs = append(errs, fmt.Sprintf("match_capacity.%s.max_size must be between 1 and 16", name))
    }
    if cap.PlayerLimit < 1 || cap.PlayerLimit > cap.MaxSize {
        errs = append(errs, fmt.Sprintf("match_capacity.%s.player_limit must be between 1 and max_size (%d)", name, cap.MaxSize))
    }
}
validateCapacity("arena_public", s.MatchCapacity.ArenaPublic)
validateCapacity("combat_public", s.MatchCapacity.CombatPublic)
validateCapacity("social_public", s.MatchCapacity.SocialPublic)
validateCapacity("private", s.MatchCapacity.Private)
```

- [ ] **Step 5: Build to verify compilation**

```bash
cd /home/andrew/src/nakama && go build ./server/...
```

Expected: Compiles without errors.

- [ ] **Step 6: Commit**

```bash
git add server/evr_global_settings.go server/evr_runtime_rpc_service_settings.go
git commit -m "feat: add configurable match capacity settings to global settings

Adds MatchCapacitySettings with per-mode limits (arena: 4v4+8 spec/mod,
combat: 5v5+6 spec/mod, social: 12+4 mod, private: 16). Stored on
system user in Global/settings storage. Includes validation and defaults."
```

---

## Task 4: Use capacity settings in match creation

Replace the hardcoded constants in the match init switch with lookups from global settings.

**Files:**

- Modify: `server/evr_match.go:1645-1682` (match creation switch)

- [ ] **Step 1: Replace hardcoded match creation sizes**

Replace lines 1645–1682 in `server/evr_match.go`:

```go
// Set the lobby and team sizes
switch settings.Mode {

case evr.ModeSocialPublic:
    state.LobbyType = PublicLobby
    state.MaxSize = SocialLobbyMaxSize
    state.TeamSize = SocialLobbyMaxSize
    state.PlayerLimit = SocialLobbyMaxSize

case evr.ModeSocialPrivate:
    state.LobbyType = PrivateLobby
    state.MaxSize = SocialLobbyMaxSize
    state.TeamSize = SocialLobbyMaxSize
    state.PlayerLimit = SocialLobbyMaxSize

case evr.ModeArenaPublic, evr.ModeArenaPublicAI:
    state.LobbyType = PublicLobby
    state.MaxSize = MatchLobbyMaxSize
    state.TeamSize = DefaultPublicArenaTeamSize
    state.PlayerLimit = min(state.TeamSize*2, state.MaxSize)

case evr.ModeCombatPublic:
    state.LobbyType = PublicLobby
    state.MaxSize = MatchLobbyMaxSize
    state.TeamSize = DefaultPublicCombatTeamSize
    state.PlayerLimit = min(state.TeamSize*2, state.MaxSize)

default:
    state.LobbyType = PrivateLobby
    state.MaxSize = MatchLobbyMaxSize
    state.TeamSize = MatchLobbyMaxSize
    state.PlayerLimit = state.MaxSize
}

if settings.TeamSize > 0 && settings.TeamSize <= 5 {
    state.TeamSize = settings.TeamSize
    state.PlayerLimit = min(state.TeamSize*2, state.MaxSize)
}
```

With:

```go
// Set lobby and team sizes from global settings.
// Fall back to hardcoded defaults if settings are not loaded.
gs := ServiceSettings()
var mc MatchCapacitySettings
if gs != nil {
    mc = gs.MatchCapacity
}

switch settings.Mode {

case evr.ModeSocialPublic:
    state.LobbyType = PublicLobby
    cap := mc.SocialPublic
    if cap.TeamSize == 0 {
        cap = ModeCapacity{TeamSize: SocialLobbyMaxSize, MaxSize: SocialLobbyMaxSize, PlayerLimit: SocialLobbyMaxSize}
    }
    state.MaxSize = cap.MaxSize
    state.TeamSize = cap.TeamSize
    state.PlayerLimit = cap.PlayerLimit

case evr.ModeSocialPrivate:
    state.LobbyType = PrivateLobby
    state.MaxSize = SocialLobbyMaxSize
    state.TeamSize = SocialLobbyMaxSize
    state.PlayerLimit = SocialLobbyMaxSize

case evr.ModeArenaPublic, evr.ModeArenaPublicAI:
    state.LobbyType = PublicLobby
    cap := mc.ArenaPublic
    if cap.TeamSize == 0 {
        cap = ModeCapacity{TeamSize: DefaultPublicArenaTeamSize, MaxSize: MatchLobbyMaxSize, PlayerLimit: DefaultPublicArenaTeamSize * 2}
    }
    state.MaxSize = cap.MaxSize
    state.TeamSize = cap.TeamSize
    state.PlayerLimit = cap.PlayerLimit

case evr.ModeCombatPublic:
    state.LobbyType = PublicLobby
    cap := mc.CombatPublic
    if cap.TeamSize == 0 {
        cap = ModeCapacity{TeamSize: DefaultPublicCombatTeamSize, MaxSize: MatchLobbyMaxSize, PlayerLimit: DefaultPublicCombatTeamSize * 2}
    }
    state.MaxSize = cap.MaxSize
    state.TeamSize = cap.TeamSize
    state.PlayerLimit = cap.PlayerLimit

default:
    state.LobbyType = PrivateLobby
    cap := mc.Private
    if cap.TeamSize == 0 {
        cap = ModeCapacity{TeamSize: MatchLobbyMaxSize, MaxSize: MatchLobbyMaxSize, PlayerLimit: MatchLobbyMaxSize}
    }
    state.MaxSize = cap.MaxSize
    state.TeamSize = cap.TeamSize
    state.PlayerLimit = cap.PlayerLimit
}

// Allow per-match override via match settings (e.g. custom games)
if settings.TeamSize > 0 && settings.TeamSize <= 5 {
    state.TeamSize = settings.TeamSize
    state.PlayerLimit = min(state.TeamSize*2, state.MaxSize)
}
```

- [ ] **Step 2: Run the full test suite**

```bash
cd /home/andrew/src/nakama && go test ./server/ -count=1 -timeout 120s
```

Expected: All tests pass. Match creation now reads from global settings with safe fallbacks.

- [ ] **Step 3: Commit**

```bash
git add server/evr_match.go
git commit -m "feat: use global match capacity settings for match creation

Match init now reads team_size, max_size, and player_limit from
MatchCapacitySettings in global settings instead of hardcoded constants.
Falls back to previous defaults if settings are not loaded."
```

---

## Task 5: Frontend — add Match Capacity section to ServiceSettings

**Files:**

- Modify: `src/views/ServiceSettings.vue` (in echovrce-web repo)

- [ ] **Step 1: Add ensureDefaults for match_capacity**

Find the `ensureDefaults` function and add initialization for the new fields. Add inside the function body:

```javascript
if (!s.match_capacity) s.match_capacity = {};
if (!s.match_capacity.arena_public)
  s.match_capacity.arena_public = {
    team_size: 4,
    max_size: 16,
    player_limit: 8,
  };
if (!s.match_capacity.combat_public)
  s.match_capacity.combat_public = {
    team_size: 5,
    max_size: 16,
    player_limit: 10,
  };
if (!s.match_capacity.social_public)
  s.match_capacity.social_public = {
    team_size: 12,
    max_size: 16,
    player_limit: 12,
  };
if (!s.match_capacity.private)
  s.match_capacity.private = { team_size: 16, max_size: 16, player_limit: 16 };
```

- [ ] **Step 2: Add Match Capacity settings group in the Matchmaking tab**

Add a new `SettingsGroup` inside the matchmaking tab `<div>` (after the existing "Timeouts" group, before "Backfill"):

```vue
<SettingsGroup title="Match Capacity">
  <p class="text-xs text-gray-400 mb-3">Per-mode lobby size limits. Changes apply to newly created matches only.</p>

  <p class="text-xs font-medium text-gray-300 mt-2 mb-1">Arena Public (default: 4v4 + 8 spec/mod)</p>
  <div class="grid grid-cols-3 gap-2">
    <SettingsNumber
      v-model="settings.match_capacity.arena_public.team_size" label="Team Size"
      :min="1" :max="8" help="Players per team." />
    <SettingsNumber
      v-model="settings.match_capacity.arena_public.player_limit" label="Player Limit"
      :min="1" :max="16" help="Max players (both teams combined)." />
    <SettingsNumber
      v-model="settings.match_capacity.arena_public.max_size" label="Max Size"
      :min="1" :max="16" help="Total capacity (players + spectators + moderators)." />
  </div>

  <p class="text-xs font-medium text-gray-300 mt-3 mb-1">Combat Public (default: 5v5 + 6 spec/mod)</p>
  <div class="grid grid-cols-3 gap-2">
    <SettingsNumber
      v-model="settings.match_capacity.combat_public.team_size" label="Team Size"
      :min="1" :max="8" help="Players per team." />
    <SettingsNumber
      v-model="settings.match_capacity.combat_public.player_limit" label="Player Limit"
      :min="1" :max="16" help="Max players (both teams combined)." />
    <SettingsNumber
      v-model="settings.match_capacity.combat_public.max_size" label="Max Size"
      :min="1" :max="16" help="Total capacity (players + spectators + moderators)." />
  </div>

  <p class="text-xs font-medium text-gray-300 mt-3 mb-1">Social Public (default: 12 players + 4 mod, no spectators)</p>
  <div class="grid grid-cols-3 gap-2">
    <SettingsNumber
      v-model="settings.match_capacity.social_public.team_size" label="Team Size"
      :min="1" :max="16" help="Max players in social lobby." />
    <SettingsNumber
      v-model="settings.match_capacity.social_public.player_limit" label="Player Limit"
      :min="1" :max="16" help="Max players." />
    <SettingsNumber
      v-model="settings.match_capacity.social_public.max_size" label="Max Size"
      :min="1" :max="16" help="Total capacity (players + moderators)." />
  </div>

  <p class="text-xs font-medium text-gray-300 mt-3 mb-1">Private (default: 16 players, no team enforcement)</p>
  <div class="grid grid-cols-3 gap-2">
    <SettingsNumber
      v-model="settings.match_capacity.private.team_size" label="Team Size"
      :min="1" :max="16" help="Max players per team (not enforced for private)." />
    <SettingsNumber
      v-model="settings.match_capacity.private.player_limit" label="Player Limit"
      :min="1" :max="16" help="Max players." />
    <SettingsNumber
      v-model="settings.match_capacity.private.max_size" label="Max Size"
      :min="1" :max="16" help="Total lobby capacity." />
  </div>
</SettingsGroup>
```

- [ ] **Step 3: Add frontend validation**

Find the computed validation section and add:

```javascript
// Match capacity validation
const modes = ["arena_public", "combat_public", "social_public", "private"];
for (const mode of modes) {
  const cap = settings.value?.match_capacity?.[mode];
  if (cap) {
    if (cap.player_limit > cap.max_size) {
      errors.push(
        `Match Capacity ${mode}: player_limit cannot exceed max_size`,
      );
    }
    if (
      mode !== "social_public" &&
      mode !== "private" &&
      cap.team_size * 2 > cap.player_limit
    ) {
      errors.push(
        `Match Capacity ${mode}: team_size * 2 cannot exceed player_limit`,
      );
    }
  }
}
```

- [ ] **Step 4: Test the frontend builds**

```bash
cd /home/andrew/src/echovrce-web && npm run build
```

Expected: No build errors.

- [ ] **Step 5: Commit**

```bash
git add src/views/ServiceSettings.vue
git commit -m "feat: add Match Capacity section to global settings editor

Adds per-mode capacity controls (team_size, player_limit, max_size) for
arena, combat, social, and private matches. Includes validation that
player_limit <= max_size and team_size * 2 <= player_limit.

Backend PR: echotools/nakama#<PR_NUMBER>"
```

---

## Task 6: Create cross-referenced PRs

- [ ] **Step 1: Create nakama PR**

```bash
cd /home/andrew/src/nakama
gh pr create --title "fix: enforce team size limits for all public match modes" --body "$(cat <<'EOF'
## Summary
- Fixes combat 5v5 matches accepting players beyond team capacity (overflow placed as spectator by game server)
- Generalizes arena-only team size guard to all public modes (combat, social)
- Adds configurable MatchCapacitySettings to global settings (arena: 4v4+8, combat: 5v5+6, social: 12+4, private: 16)
- Match creation now reads capacity from settings instead of hardcoded constants

## Test plan
- [ ] New tests: combat full-team rejection, combat single-team-full rejection
- [ ] Regression tests: arena full-team rejection still works
- [ ] Social lobby capacity enforcement test
- [ ] `go test ./server/ -count=1` passes
- [ ] Verify in production: combat players no longer overflow to spectator

Frontend PR: echotools/echovrce-web#<FRONTEND_PR>

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 2: Create echovrce-web PR**

```bash
cd /home/andrew/src/echovrce-web
gh pr create --title "feat: add match capacity settings to global settings editor" --body "$(cat <<'EOF'
## Summary
- Adds "Match Capacity" section to the Matchmaking tab in Service Settings
- Per-mode controls: team_size, player_limit, max_size for arena, combat, social, private
- Frontend validation: player_limit <= max_size, team_size * 2 <= player_limit

## Test plan
- [ ] `npm run build` succeeds
- [ ] Settings load with correct defaults
- [ ] Changes save and persist
- [ ] Validation prevents invalid combinations

Backend PR: echotools/nakama#<BACKEND_PR>

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 3: Update PR descriptions with cross-references**

After both PRs are created, update each PR description with the other's number using `gh pr edit`.
